package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// A connector whose new_comment event declares a default batching window
// (connector.EventDecl.Coalesce), the way github's does — short, so the tests
// don't wait out the production 15s.
var coalesceConnOnce sync.Once

const testCoalesceWindow = 40 * time.Millisecond

func registerCoalesceConn() {
	registerGateConn() // its eg.post verb records runs
	coalesceConnOnce.Do(func() {
		connector.RegisterType(&connector.TypeDecl{
			Type: "enginecoalesce",
			Events: []connector.EventDecl{{
				Name: "new_comment",
				Context: connector.Schema{
					"comment_body": {Type: connector.TString},
					"comment_id":   {Type: connector.TInt},
				},
				Coalesce: testCoalesceWindow,
			}},
		}, func(string, config.ConnectorRef, connector.Deps) (connector.Impl, error) {
			return gateConnImpl{}, nil
		})
	})
}

// promptDispatcher records every agent dispatch's prompt.
type promptDispatcher struct {
	fakeFlowDispatcher
	mu      sync.Mutex
	prompts []string
}

func (d *promptDispatcher) Dispatch(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.prompts = append(d.prompts, req.Action.Prompt)
	return dispatch.RunRef{Backend: "test", Kind: req.Trigger.Kind}, nil
}

func (d *promptDispatcher) seen() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.prompts...)
}

func buildCoalesceEngine(t *testing.T, cfgYAML string, d Dispatcher) *Engine {
	t.Helper()
	registerCoalesceConn()
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(cfgYAML), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.NormalizeTriggers(); err != nil {
		t.Fatal(err)
	}
	reg, err := connector.Build(&cfg, connector.Deps{Secrets: secrets.New(), Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	st := newFlowGateStore()
	notif := &fakeNotif{}
	runner := flow.New(flow.Runner{Cfg: &cfg, Conns: reg, Secrets: secrets.New(), Store: st, Notif: notif})
	return New(Options{Config: &cfg, Store: st, Dispatch: d, Notifier: notif, Flow: runner, Connectors: reg, Log: t.Logf})
}

const coalesceCfg = `
connectors:
  eg: { use: enginegate }
  cc: { use: enginecoalesce }
triggers:
  - on: cc.new_comment
    steps:
      - { id: p, uses: eg.post, options: { text: "{{.group.count}}" } }
`

func coalesceComment(id int, pr int) core.Trigger {
	return core.Trigger{
		Source: "enginecoalesce", Instance: "cc", Kind: "new_comment",
		Target:  core.Target{Repo: "acme/x", PR: pr, Number: pr},
		Title:   "comment",
		Dedup:   fmt.Sprintf("comment:%d", id),
		Context: map[string]any{"comment_body": fmt.Sprintf("comment %d", id), "comment_id": int64(id)},
		Action:  config.Action{FlowRef: "0:cc.new_comment"},
	}
}

// postsSince returns the eg.post texts recorded after the first `before` calls.
func postsSince(before int) []string {
	gateConnMu.Lock()
	defer gateConnMu.Unlock()
	var out []string
	for _, c := range gateConnCalls[before:] {
		out = append(out, fmt.Sprint(c["text"]))
	}
	return out
}

// A burst of plain comments on one PR is ONE run by default — a trigger with
// no group: of its own takes the event's declared batching — carrying the
// whole burst, and a burst on another PR is its own run.
func TestNewCommentBurstCoalescesIntoOneRunByDefault(t *testing.T) {
	eng := buildCoalesceEngine(t, coalesceCfg, fakeFlowDispatcher{})
	before := gateCalls()
	const n = 5
	for i := 0; i < n; i++ {
		eng.process(context.Background(), coalesceComment(100+i, 7))
	}
	eng.process(context.Background(), coalesceComment(200, 8))
	waitCond(t, "both batches", func() bool { return gateCalls()-before >= 2 })
	time.Sleep(4 * testCoalesceWindow) // nothing else may fire
	got := postsSince(before)
	if len(got) != 2 {
		t.Fatalf("%d comments on PR 7 + 1 on PR 8 ran %d flows (%v), want 2 — one per PR", n, len(got), got)
	}
	counts := map[string]bool{got[0]: true, got[1]: true}
	if !counts[fmt.Sprint(n)] || !counts["1"] {
		t.Fatalf("batch sizes %v, want one batch of %d and one of 1", got, n)
	}
}

// `group: { enabled: false }` opts a trigger out of the default batching: one
// run per comment, each dispatched at once, as before.
func TestNewCommentDefaultBatchingOptOut(t *testing.T) {
	cfg := strings.Replace(coalesceCfg, "    steps:", "    group: { enabled: false }\n    steps:", 1)
	cfg = strings.Replace(cfg, `"{{.group.count}}"`, `"{{.comment_body}}"`, 1)
	eng := buildCoalesceEngine(t, cfg, fakeFlowDispatcher{})
	before := gateCalls()
	for i := 0; i < 3; i++ {
		// One at a time: a comment landing while the previous run is still
		// waiting for its slot folds into that wait (coalesceQueued), which
		// is not what this test is about.
		eng.process(context.Background(), coalesceComment(300+i, 7))
		want := i + 1
		waitCond(t, "an unbatched run per comment", func() bool { return gateCalls()-before >= want })
	}
	time.Sleep(4 * testCoalesceWindow)
	got := postsSince(before)
	if len(got) != 3 || got[0] != "comment 300" || got[2] != "comment 302" {
		t.Fatalf("opted-out trigger ran %v, want one run per comment", got)
	}
}

// A forced trigger (`conductor run --force`) runs now, unbatched: its run has
// no {{.group}} (a batch of one would render count 1).
func TestForcedNewCommentIsNotBatched(t *testing.T) {
	eng := buildCoalesceEngine(t, coalesceCfg, fakeFlowDispatcher{})
	before := gateCalls()
	tr := coalesceComment(400, 7)
	tr.Force = true
	eng.process(context.Background(), tr)
	waitCond(t, "forced run", func() bool { return gateCalls() > before })
	time.Sleep(4 * testCoalesceWindow)
	if got := postsSince(before); len(got) != 1 || got[0] == "1" {
		t.Fatalf("forced trigger posted %v, want one run outside any batch", got)
	}
}

// A step that writes its own per-comment prompt, on a run the default batching
// formed, must still be handed EVERY comment of the burst — the prompt's own
// fields are the newest comment's — and the hand-off must stay literal text.
func TestDefaultBatchHandsAPromptedStepTheWholeBurst(t *testing.T) {
	cfg := `
connectors:
  cc: { use: enginecoalesce }
triggers:
  - on: cc.new_comment
    steps:
      - { id: fix, type: agent, agent: fixer, prompt: "Address: {{.comment_body}}" }
`
	d := &promptDispatcher{}
	eng := buildCoalesceEngine(t, cfg, d)
	for i := 0; i < 3; i++ {
		tr := coalesceComment(500+i, 7)
		if i == 0 {
			tr.Context["comment_body"] = "use {{ .Values.x }} here" // template-looking text from a reviewer
		}
		eng.process(context.Background(), tr)
	}
	waitCond(t, "the batched agent dispatch", func() bool { return len(d.seen()) >= 1 })
	time.Sleep(4 * testCoalesceWindow)
	ps := d.seen()
	if len(ps) != 1 {
		t.Fatalf("3 comments dispatched %d agents, want 1", len(ps))
	}
	p := ps[0]
	if !strings.HasPrefix(p, "Address: {{.comment_body}}") || !strings.Contains(p, "batches 3 events") {
		t.Fatalf("prompted step not handed the burst:\n%s", p)
	}
	rendered, err := dispatch.RenderPrompt(dispatch.Request{Action: config.Action{Prompt: p},
		Trigger: coalesceComment(502, 7)})
	if err != nil {
		t.Fatalf("batched prompt does not render: %v", err)
	}
	for _, want := range []string{"Address: comment 502", `"comment_id": 500`, "comment 501", "use {{ .Values.x }} here"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered prompt missing %q:\n%s", want, rendered)
		}
	}
}
