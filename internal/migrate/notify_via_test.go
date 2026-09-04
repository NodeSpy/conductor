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

// escalateLine is the notifier's composed escalate message for the fixture
// trigger — what {{.message}} carries into the generated trigger steps.
const escalateLine = "[escalate] acme/x#7 s gave up after retries — gave up (open paseo)"

// runNotifyTriggerSteps renders and invokes every step of the named
// generated trigger with the lifecycle context — the flow runner's verb
// path, driven directly.
func runNotifyTriggerSteps(t *testing.T, cfg *config.Config, reg *connector.Registry, triggerName, msg string) {
	t.Helper()
	data := map[string]any{
		"message": msg, "event": "escalate", "ref": "acme/x#7",
		"repo": "acme/x", "number": 7, "origin_kind": "s",
	}
	found := false
	for _, spec := range cfg.Triggers {
		if spec.Name != triggerName {
			continue
		}
		found = true
		for _, st := range spec.Steps {
			connName, verb, _ := strings.Cut(st.Uses, ".")
			in, ok := reg.Get(connName)
			if !ok {
				t.Fatalf("step connector %q missing", connName)
			}
			rendered, err := flow.RenderOptions(connector.MergeOptions(in.DefaultOptions, st.Options), data)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := in.InvokeFinal(context.Background(), verb, rendered); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !found {
		t.Fatalf("no generated trigger %q (have %v)", triggerName, triggerNames(cfg))
	}
}

func triggerNames(cfg *config.Config) []string {
	var out []string
	for _, tr := range cfg.Triggers {
		out = append(out, tr.Name+" ("+tr.On+")")
	}
	return out
}

// TestNotifyTriggerPayloadEquivalence is the behavioral golden: the SAME
// lifecycle event through the legacy notify sinks and through the migrated
// conductor.* trigger steps produces byte-identical wire payloads
// (slack/discord webhook JSON, the ntfy body + Title header) — and the
// migrated config carries no notify: block at all.
func TestNotifyTriggerPayloadEquivalence(t *testing.T) {
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

	// Migrated delivery: conductor.* triggers whose steps are the sink verbs.
	res, err := Transform([]byte(legacyYAML))
	if err != nil {
		t.Fatal(err)
	}
	var migrated config.Config
	if err := yaml.Unmarshal(res.Output, &migrated); err != nil {
		t.Fatal(err)
	}
	if migrated.Notify.Configured() {
		t.Fatalf("notify block survived migration: %+v", migrated.Notify)
	}
	if err := migrated.NormalizeTriggers(); err != nil {
		t.Fatal(err)
	}
	// escalate maps to BOTH conductor.escalate and conductor.failed (the
	// legacy event covered give-ups and run errors).
	var onEvents []string
	for _, tr := range migrated.Triggers {
		if strings.HasPrefix(tr.On, "conductor.") {
			onEvents = append(onEvents, tr.On)
		}
	}
	if len(onEvents) != 2 || onEvents[0] != "conductor.escalate" || onEvents[1] != "conductor.failed" {
		t.Fatalf("generated triggers: %v", onEvents)
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
	runNotifyTriggerSteps(t, &migrated, reg, "notify-escalate", escalateLine)
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

	// Idempotent: re-running the transform on the migrated output changes
	// nothing (no notify block remains).
	res2, err := Transform(res.Output)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Changed {
		t.Fatalf("second pass changed the output:\n%s", string(res2.Output))
	}
}

// TestNotifyPushoverNotifiarrTriggerPayloads asserts the migrated pushover
// and notifiarr trigger steps produce the legacy posters' exact encodings.
func TestNotifyPushoverNotifiarrTriggerPayloads(t *testing.T) {
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
	if err := migrated.NormalizeTriggers(); err != nil {
		t.Fatal(err)
	}
	sec := secrets.New()
	reg, err := connector.Build(&migrated, connector.Deps{Secrets: sec, Config: &migrated})
	if err != nil {
		t.Fatal(err)
	}
	runNotifyTriggerSteps(t, &migrated, reg, "notify-escalate", escalateLine)

	mu.Lock()
	defer mu.Unlock()
	// The legacy pushover poster's exact form encoding.
	wantPushover := "message=conductor+" + strings.ReplaceAll(strings.ReplaceAll(escalateLine, " ", "+"), "#", "%23")
	if !strings.Contains(bodies["pushover"], "token=app-tok") ||
		!strings.Contains(bodies["pushover"], "user=usr-key") ||
		!strings.Contains(bodies["pushover"], "message=conductor+%5Bescalate%5D") {
		t.Errorf("pushover form drifted: %s (context: %s)", bodies["pushover"], wantPushover)
	}
	// The legacy notifiarr poster's exact JSON shape.
	if !strings.Contains(bodies["notifiarr"], `"notification":{"name":"conductor"}`) ||
		!strings.Contains(bodies["notifiarr"], `"description":"`+escalateLine[:20]) ||
		!strings.Contains(bodies["notifiarr"], `"ids":{"channel":"123"}`) {
		t.Errorf("notifiarr payload drifted: %s", bodies["notifiarr"])
	}
	if apiKey != "nfr-key" {
		t.Errorf("notifiarr X-Api-Key: %q", apiKey)
	}
}

// TestNotifyPassStandalone: a connectors-schema config still carrying a
// notify: block (via routes and/or digest) migrates standalone — the block
// is removed, routes become triggers, digest becomes a grouped trigger.
func TestNotifyPassStandalone(t *testing.T) {
	pre := `
connectors:
  alerts: { type: ntfy, topic: t }
triggers:
  - name: ping
    on: manual
    steps: [ { uses: alerts.publish, options: { message: x } } ]
notify:
  on: [escalate, needs_input]
  digest: 24h
  ntfy: { topic: t2 }
  via:
    - { on: [complete], uses: alerts.publish, options: { message: "done {{.message}}" } }
`
	res, err := Transform([]byte(pre))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatal("the notify pass must fire")
	}
	out := string(res.Output)
	if strings.Contains(out, "notify:") {
		t.Fatalf("notify block survived:\n%s", out)
	}
	// yaml quotes the `on` key (a YAML 1.1 boolean).
	for _, want := range []string{
		`"on": conductor.escalate`,
		`"on": conductor.failed`,
		`"on": conductor.needs_input`,
		`"on": conductor.complete`,
		"notify-ntfy",
		"done {{.message}}",
		"window: 24h",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	var migrated config.Config
	if err := yaml.Unmarshal(res.Output, &migrated); err != nil {
		t.Fatal(err)
	}
	if err := migrated.NormalizeTriggers(); err != nil {
		t.Fatal(err)
	}
	if err := migrated.Validate(); err != nil {
		t.Fatalf("migrated config must validate: %v", err)
	}
	reg, err := connector.Build(&migrated, connector.Deps{Secrets: secrets.New(), Config: &migrated})
	if err != nil {
		t.Fatal(err)
	}
	if err := flow.Validate(&migrated, reg); err != nil {
		t.Fatalf("migrated config semantic pass: %v", err)
	}
	// Idempotent.
	res2, err := Transform(res.Output)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Changed {
		t.Fatalf("second pass changed the output:\n%s", string(res2.Output))
	}
}
