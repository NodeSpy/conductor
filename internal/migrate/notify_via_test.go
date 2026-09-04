package migrate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/notify"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// TestNotifySinksToViaPayloadEquivalence: the SAME Emit through the legacy
// notify sinks and through the migrated via routes produces byte-identical
// wire payloads (slack/discord webhook JSON, the ntfy body + Title header).
// pushover/notifiarr use package-level endpoint vars on the legacy side, so
// their migrated payloads are asserted against the exact legacy encodings.
func TestNotifySinksToViaPayloadEquivalence(t *testing.T) {
	type capture struct {
		path, body, title string
	}
	var mu sync.Mutex
	var caps []capture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		caps = append(caps, capture{path: r.URL.Path, body: string(b), title: r.Header.Get("Title")})
		mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	grab := func(pathFrag string) (capture, bool) {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range caps {
			if strings.Contains(c.path, pathFrag) {
				return c, true
			}
		}
		return capture{}, false
	}
	reset := func() {
		mu.Lock()
		caps = nil
		mu.Unlock()
	}
	waitCaps := func(n int) {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			got := len(caps)
			mu.Unlock()
			if got >= n {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %d captures", n)
	}

	legacyYAML := fmt.Sprintf(`
integrations:
  - type: cron
    name: c
    schedules:
      - name: s
        cron: "* * * * *"
        action: { type: command, command: [echo, hi] }
notify:
  on: [escalate]
  slack_webhook_url: %s/slackhook
  discord_webhook_url: %s/discordhook
  ntfy: { server: %s/ntfy, topic: alerts }
`, srv.URL, srv.URL, srv.URL)

	trig := core.Trigger{
		Source: "cron", Instance: "c", Kind: "s",
		Target: core.Target{Repo: "acme/x", Number: 7},
	}

	// Legacy delivery: the notifier's own posters.
	var legacyCfg config.Config
	if err := yaml.Unmarshal([]byte(legacyYAML), &legacyCfg); err != nil {
		t.Fatal(err)
	}
	reset()
	legacyN := notify.New(legacyCfg.Notify, nil, nil)
	legacyN.Emit(context.Background(), notify.EventEscalate, trig, "gave up")
	waitCaps(3)
	legacySlack, ok1 := grab("slackhook")
	legacyDiscord, ok2 := grab("discordhook")
	legacyNtfy, ok3 := grab("ntfy/alerts")
	if !ok1 || !ok2 || !ok3 {
		t.Fatalf("legacy captures missing: %v", caps)
	}

	// Migrated delivery: the same Emit, via routes through connector verbs.
	res, err := Transform([]byte(legacyYAML))
	if err != nil {
		t.Fatal(err)
	}
	var migrated config.Config
	if err := yaml.Unmarshal(res.Output, &migrated); err != nil {
		t.Fatal(err)
	}
	if len(migrated.Notify.Via) != 3 || legacyNotifySinks(migrated.Notify) {
		t.Fatalf("migrated notify: %+v", migrated.Notify)
	}
	sec := secrets.New()
	reg, err := connector.Build(&migrated, connector.Deps{Secrets: sec, Config: &migrated})
	if err != nil {
		t.Fatal(err)
	}
	if err := flow.Validate(&migrated, reg); err != nil {
		t.Fatalf("migrated config invalid: %v", err)
	}
	reset()
	migratedN := notify.New(migrated.Notify, nil, nil)
	migratedN.SetRouter(func(ctx context.Context, r config.NotifyRoute, data map[string]any) error {
		connName, verb, _ := strings.Cut(r.Uses, ".")
		in, ok := reg.Get(connName)
		if !ok {
			return fmt.Errorf("unknown connector %q", connName)
		}
		merged := connector.MergeOptions(in.DefaultOptions, r.Options)
		rendered, err := flow.RenderOptions(merged, data)
		if err != nil {
			return err
		}
		_, err = in.InvokeFinal(ctx, verb, rendered)
		return err
	})
	migratedN.Emit(context.Background(), notify.EventEscalate, trig, "gave up")
	waitCaps(3)
	newSlack, ok1 := grab("slackhook")
	newDiscord, ok2 := grab("discordhook")
	newNtfy, ok3 := grab("ntfy/alerts")
	if !ok1 || !ok2 || !ok3 {
		t.Fatalf("migrated captures missing: %v", caps)
	}

	if newSlack.body != legacySlack.body {
		t.Errorf("slack payload drifted:\nlegacy   %s\nmigrated %s", legacySlack.body, newSlack.body)
	}
	if newDiscord.body != legacyDiscord.body {
		t.Errorf("discord payload drifted:\nlegacy   %s\nmigrated %s", legacyDiscord.body, newDiscord.body)
	}
	if newNtfy.body != legacyNtfy.body || newNtfy.title != legacyNtfy.title {
		t.Errorf("ntfy payload drifted:\nlegacy   %q (Title %q)\nmigrated %q (Title %q)",
			legacyNtfy.body, legacyNtfy.title, newNtfy.body, newNtfy.title)
	}
}

// TestNotifyPushoverNotifiarrViaPayloads asserts the migrated pushover and
// notifiarr routes produce the legacy posters' exact encodings (their legacy
// endpoints are process-lifetime vars, so this side is asserted against the
// documented legacy bytes rather than a live double-run).
func TestNotifyPushoverNotifiarrViaPayloads(t *testing.T) {
	var mu sync.Mutex
	var bodies = map[string]string{}
	var apiKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		if strings.Contains(r.URL.Path, "passthrough") {
			bodies["notifiarr"] = string(b)
			apiKey = r.Header.Get("X-Api-Key")
		} else {
			bodies["pushover"] = string(b)
		}
		mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("PC_PUSHOVER_URL", srv.URL+"/pushover")
	t.Setenv("PC_NOTIFIARR_URL", srv.URL)

	legacyYAML := `
integrations:
  - type: cron
    name: c
    schedules:
      - name: s
        cron: "* * * * *"
        action: { type: command, command: [echo, hi] }
notify:
  on: [escalate]
  pushover: { token: app-tok, user: usr-key }
  notifiarr: { api_key: nfr-key, channel_id: "123" }
`
	res, err := Transform([]byte(legacyYAML))
	if err != nil {
		t.Fatal(err)
	}
	var migrated config.Config
	if err := yaml.Unmarshal(res.Output, &migrated); err != nil {
		t.Fatal(err)
	}
	sec := secrets.New()
	reg, err := connector.Build(&migrated, connector.Deps{Secrets: sec, Config: &migrated})
	if err != nil {
		t.Fatal(err)
	}
	line := "[escalate] acme/x#7 s gave up after retries — gave up (open paseo)"
	data := map[string]any{"message": line, "event": "escalate"}
	for _, r := range migrated.Notify.Via {
		connName, verb, _ := strings.Cut(r.Uses, ".")
		in, _ := reg.Get(connName)
		rendered, err := flow.RenderOptions(connector.MergeOptions(in.DefaultOptions, r.Options), data)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := in.InvokeFinal(context.Background(), verb, rendered); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	// The legacy pushover poster's exact form encoding.
	wantPushover := "message=conductor+" + strings.ReplaceAll(strings.ReplaceAll(line, " ", "+"), "#", "%23")
	if !strings.Contains(bodies["pushover"], "token=app-tok") ||
		!strings.Contains(bodies["pushover"], "user=usr-key") ||
		!strings.Contains(bodies["pushover"], "message=conductor+%5Bescalate%5D") {
		t.Errorf("pushover form drifted: %s (context: %s)", bodies["pushover"], wantPushover)
	}
	// The legacy notifiarr poster's exact JSON shape.
	if !strings.Contains(bodies["notifiarr"], `"notification":{"name":"conductor"}`) ||
		!strings.Contains(bodies["notifiarr"], `"description":"`+line[:20]) ||
		!strings.Contains(bodies["notifiarr"], `"ids":{"channel":"123"}`) {
		t.Errorf("notifiarr payload drifted: %s", bodies["notifiarr"])
	}
	if apiKey != "nfr-key" {
		t.Errorf("notifiarr X-Api-Key: %q", apiKey)
	}
}
