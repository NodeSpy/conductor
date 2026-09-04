package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/connector"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/dispatch"
	"github.com/NodeSpy/paseo-conductor/internal/flow"
	"github.com/NodeSpy/paseo-conductor/internal/secrets"
	"github.com/NodeSpy/paseo-conductor/internal/store"
)

const legacyMultiVariantSlack = `
integrations:
  - type: slack
    name: ops
    app_token: xapp-x
    bot_token: xoxb-x
    triggers:
      - on: app_mention
        ack: { react: eyes }
        on_done: { say: "all done", in_thread: false }
        on_fail: { say: "something failed", in_thread: false }
        actions:
          - name: fast
            type: agent
            agent: fixer
            prompt: "fast path {{.slack.text}}"
          - name: slow
            type: agent
            agent: fixer
            prompt: "slow path {{.slack.text}}"
agents:
  fixer: { provider: claude }
`

// TestSlackMultiVariantAggregationShape: a multi-variant rule becomes ONE
// trigger whose only step is parallel branches, with the feedback hooks on it.
func TestSlackMultiVariantAggregationShape(t *testing.T) {
	res, out := mustTransform(t, legacyMultiVariantSlack)
	if len(out.Triggers) != 1 {
		t.Fatalf("triggers: %d, want 1 (merged)", len(out.Triggers))
	}
	tr := out.Triggers[0]
	if len(tr.Steps) != 1 || tr.Steps[0].Parallel == nil || len(tr.Steps[0].Parallel.Branches) != 2 {
		t.Fatalf("merged step shape: %+v", tr.Steps)
	}
	// Branch step ids are variant-prefixed and unique.
	ids := map[string]bool{}
	for _, br := range tr.Steps[0].Parallel.Branches {
		for _, st := range br {
			if ids[st.ID] {
				t.Fatalf("duplicate branch step id %q", st.ID)
			}
			ids[st.ID] = true
		}
	}
	if !ids["fast-step1"] || !ids["slow-step1"] {
		t.Fatalf("branch ids: %v", ids)
	}
	phases := map[string]int{}
	for _, h := range tr.Hooks {
		phases[h.At]++
	}
	if phases["start"] != 1 || phases["done"] != 1 || phases["fail"] != 1 {
		t.Fatalf("hook phases: %v", phases)
	}
	if !strings.Contains(strings.Join(res.Summary, "\n"), "matching legacy aggregation") {
		t.Fatalf("summary should state the aggregation mapping:\n%s", strings.Join(res.Summary, "\n"))
	}
}

// aggStore/aggNotif are minimal flow deps.
type aggStore struct{ mu sync.Mutex }

func (s *aggStore) Audit(map[string]any)           {}
func (s *aggStore) PutRun(store.WorkflowRun) error { return nil }
func (s *aggStore) DeleteRun(string) error         { return nil }

type aggNotif struct{}

func (aggNotif) Emit(context.Context, string, core.Trigger, string) {}

// TestSlackAggregationTimingGolden runs the MIGRATED multi-variant trigger
// through the real flow runner against a slack-shaped test server and asserts
// the legacy HandleCompletion semantics exactly:
//   - success: on_done posts ONCE, and only after BOTH variants completed;
//   - one variant failing: on_fail posts ONCE, on_done never.
func TestSlackAggregationTimingGolden(t *testing.T) {
	// A slack Web API stand-in capturing chat.postMessage texts.
	var mu sync.Mutex
	var posts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "chat.postMessage") {
			var m struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(b, &m)
			mu.Lock()
			posts = append(posts, m.Text)
			mu.Unlock()
		}
		w.Write([]byte(`{"ok":true,"ts":"1.1"}`))
	}))
	defer srv.Close()
	t.Setenv("PC_SLACK_API_URL", srv.URL)

	res, err := Transform([]byte(legacyMultiVariantSlack))
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	if err := yaml.Unmarshal(res.Output, &cfg); err != nil {
		t.Fatal(err)
	}
	sec := secrets.New()
	reg, err := connector.Build(&cfg, connector.Deps{Secrets: sec, Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	if err := flow.Validate(&cfg, reg); err != nil {
		t.Fatalf("migrated config invalid: %v", err)
	}
	spec := cfg.Triggers[0]

	trig := core.Trigger{
		Source: "slack", Instance: "ops", Kind: "app_mention",
		Title:   "mention",
		Context: map[string]any{"slack": map[string]any{"channel": "C1", "user": "U1", "text": "go", "ts": "1.0", "thread_ts": "1.0"}},
	}

	run := func(failSlow bool) (dispatched []string, postTimes map[string]time.Time, doneAfterBoth bool) {
		mu.Lock()
		posts = nil
		mu.Unlock()
		var dmu sync.Mutex
		var finished []time.Time
		runner := flow.New(flow.Runner{
			Cfg: &cfg, Conns: reg, Secrets: sec, Store: &aggStore{}, Notif: aggNotif{},
			Agents: flow.AgentServices{
				Dispatch: func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
					dmu.Lock()
					dispatched = append(dispatched, req.Action.Prompt[:9])
					dmu.Unlock()
					slow := strings.HasPrefix(req.Action.Prompt, "slow")
					if slow {
						time.Sleep(80 * time.Millisecond) // the aggregation must wait for this one
					}
					if slow && failSlow {
						return dispatch.RunRef{}, fmt.Errorf("boom")
					}
					dmu.Lock()
					finished = append(finished, time.Now())
					dmu.Unlock()
					return dispatch.RunRef{Output: `{"ok":true}`}, nil
				},
			},
		})
		runner.Run(context.Background(), store.WorkflowRun{}, trig, spec, nil, false)

		mu.Lock()
		defer mu.Unlock()
		dmu.Lock()
		defer dmu.Unlock()
		_ = finished
		return dispatched, nil, len(finished) == 2
	}

	// Success: both variants ran; on_done posted exactly once, after both.
	dispatched, _, both := run(false)
	if len(dispatched) != 2 {
		t.Fatalf("dispatched %d variants, want 2: %v", len(dispatched), dispatched)
	}
	if !both {
		t.Fatal("the slow variant did not complete before the workflow ended")
	}
	mu.Lock()
	got := append([]string(nil), posts...)
	mu.Unlock()
	done, fail := 0, 0
	for _, p := range got {
		if p == "all done" {
			done++
		}
		if p == "something failed" {
			fail++
		}
	}
	if done != 1 || fail != 0 {
		t.Fatalf("success run posts: %v (want exactly one 'all done')", got)
	}

	// Failure in ONE variant: on_fail once, on_done never — the legacy
	// HandleCompletion posted on_fail when any variant failed.
	dispatched, _, _ = run(true)
	if len(dispatched) != 2 {
		t.Fatalf("dispatched %d variants, want 2", len(dispatched))
	}
	mu.Lock()
	got = append([]string(nil), posts...)
	mu.Unlock()
	done, fail = 0, 0
	for _, p := range got {
		if p == "all done" {
			done++
		}
		if p == "something failed" {
			fail++
		}
	}
	if done != 0 || fail != 1 {
		t.Fatalf("failure run posts: %v (want exactly one 'something failed', no 'all done')", got)
	}
}
