package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/core/coretest"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/secrets"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// TestFlowAgentStepRetriesRateLimitedMint is finding 1's regression test: a
// flow agent step's credential mint answering rate_limited once, then
// succeeding, must not fail the step.
//
// Flow steps and flow resume run in their OWN per-run goroutine
// (internal/flow/flow.go execAgent, dispatched from processFlow/resumeFlowRun
// goroutines), never on the engine's single shared dispatch loop — unlike
// process()/ResumeWorkflows' legacy path/remediate, which must never block
// that loop in a retry sleep. credentialsForFlow (internal/engine/
// credentials.go) is the mode that retries a rate_limited/not_ready mint
// through connector.RetryContract instead of failing the step on the first
// answer, because blocking here only delays this one run, not any other
// queued trigger. Before that split, mint() never retried at all (a recent
// regression fixing the shared-loop case globally), and noStepRetry
// (internal/flow/contract.go) excludes rate_limited/not_ready from the
// step's own retry: — so a step whose mint was rate_limited even once failed
// outright, with processFlow having already consumed the trigger's dedup (a
// redelivery would just be dropped).
func TestFlowAgentStepRetriesRateLimitedMint(t *testing.T) {
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(`
connectors:
  i: { use: github }
triggers:
  - on: i.new_comment
    steps:
      - { id: p, agent: fixer, prompt: "go" }
`), &cfg); err != nil {
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

	var writeTokenCalls int32
	coretest.Forge.Respond(func(req sdk.InvokeRequest) (sdk.InvokeResult, error) {
		switch req.Verb {
		case "read_token":
			return sdk.InvokeResult{Outputs: map[string]any{"token": "rtok"}}, nil
		case "write_token":
			if atomic.AddInt32(&writeTokenCalls, 1) == 1 {
				// The FIRST write_token mint is rate_limited; the retried
				// call (inside the SAME credentialsForFlow invocation) must
				// succeed.
				return sdk.InvokeResult{}, sdk.Fail(sdk.CodeRateLimited, "slow down", map[string]any{"retry_after": "1ms"})
			}
			return sdk.InvokeResult{Outputs: map[string]any{"token": "wtok"}}, nil
		}
		return sdk.InvokeResult{Outputs: map[string]any{}}, nil
	})
	t.Cleanup(func() { coretest.Forge.Respond(nil) })

	runner := flow.New(flow.Runner{Cfg: &cfg, Conns: reg, Secrets: secrets.New(), Store: st, Notif: notif})
	eng := New(Options{Config: &cfg, Store: st, Dispatch: fakeFlowDispatcher{}, Notifier: notif, Flow: runner, Connectors: reg})

	tr := core.Trigger{
		Source: "github", Instance: "i", Kind: "new_comment", TargetTrusted: true,
		Target: core.Target{Repo: "acme/x", Number: 1},
		Title:  "hi", Dedup: "d1",
		Context: map[string]any{"repo": "acme/x"},
		Action:  config.Action{FlowRef: "0:i.new_comment"},
	}
	eng.process(context.Background(), tr)

	waitCond(t, "write_token retried past its rate_limited answer", func() bool {
		return atomic.LoadInt32(&writeTokenCalls) >= 2
	})
	// Give the step a moment to finish (or fail) after the mint succeeds.
	time.Sleep(50 * time.Millisecond)
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, a := range st.audits {
		if a["event"] == "workflow_failed" {
			t.Fatalf("the flow step failed instead of retrying the rate_limited mint: %v", a)
		}
	}
}
