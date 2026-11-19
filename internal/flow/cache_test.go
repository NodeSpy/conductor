package flow

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

const cacheCfg = `
connectors:
  svc: { use: fake }
sandbox:
  caches: { enabled: true }
workflows:
  inner:
    cache: { key: "wf-{{.repo}}" }
    steps:
      - { id: plain, type: agent, name: a, prompt: p }
      - { id: own, type: agent, name: a, prompt: p, cache: { key: "own-{{.repo}}" } }
`

// finishes records the step ends dispatch.FinishCache hands the caches.
func finishes(t *testing.T) func() map[string]bool {
	t.Helper()
	var mu sync.Mutex
	got := map[string]bool{}
	old := dispatch.CacheFinisher
	dispatch.CacheFinisher = func(_ context.Context, id string, ok bool) {
		mu.Lock()
		got[id] = ok
		mu.Unlock()
	}
	t.Cleanup(func() { dispatch.CacheFinisher = old })
	return func() map[string]bool {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]bool{}
		for k, v := range got {
			out[k] = v
		}
		return out
	}
}

// An agent step's cache keys render over its facts: its own cache: wins,
// else its workflow's; restore_from keys come along read-only, the one
// equal to its own key dropped; a key with no rendered value means no
// cache. Each keyed dispatch's step end reaches the cache (FinishCache).
func TestStepCacheKeys(t *testing.T) {
	cfg := loadConfig(t, cacheCfg)
	reg := buildRegistry(t, cfg)
	got := finishes(t)
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: call, workflow: inner }
  - id: top
    type: agent
    name: a
    prompt: p
    cache: { key: "pr-{{.repo}}-{{.number}}", restore_from: ["{{.repo}}", "pr-{{.repo}}-{{.number}}", "{{.missing}}"] }
  - { id: none, type: agent, name: a, prompt: p }
  - { id: unrendered, type: agent, name: a, prompt: p, cache: { key: "{{.missing}}" } }
  - { id: badchar, type: agent, name: a, prompt: p, cache: { key: "{{.title}}" } }
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	by := map[string]dispatch.Request{}
	for _, r := range rig.Agents.requests() {
		by[r.Step.ID] = r
	}
	key := func(step string) string {
		c := by[step].Cache
		if c == nil {
			return ""
		}
		return c.Key.Template + "=" + c.Key.Value
	}
	for step, want := range map[string]string{
		"plain": "wf-{{.repo}}=wf-o/r", "own": "own-{{.repo}}=own-o/r",
		"top": "pr-{{.repo}}-{{.number}}=pr-o/r-7", "none": "", "unrendered": "", "badchar": "",
	} {
		if _, ok := by[step]; !ok {
			t.Fatalf("step %s did not dispatch: %v", step, by)
		}
		if k := key(step); k != want {
			t.Errorf("%s: key %q, want %q", step, k, want)
		}
	}
	top := by["top"].Cache
	if len(top.RestoreFrom) != 1 || top.RestoreFrom[0].Value != "o/r" || top.RestoreFrom[0].Template != "{{.repo}}" {
		t.Errorf("restore_from %+v", top.RestoreFrom)
	}
	if top.Teach || by["own"].Cache.Teach {
		t.Error("a step that did not declare teach: teaches")
	}
	f := got()
	for _, step := range []string{"plain", "own", "top"} {
		if ok, seen := f[by[step].DispatchID]; !seen || !ok {
			t.Errorf("%s: step end not handed to the cache (%v)", step, f)
		}
	}
	for _, step := range []string{"none", "unrendered", "badchar"} {
		if _, seen := f[by[step].DispatchID]; seen {
			t.Errorf("%s: an uncached step reached the cache", step)
		}
	}
}

// A failed dispatch ends its cache not-ok: nothing is published.
func TestStepCacheFinishedNotOKOnFailure(t *testing.T) {
	cfg := loadConfig(t, cacheCfg)
	reg := buildRegistry(t, cfg)
	got := finishes(t)
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: top, type: agent, name: a, prompt: p, cache: { key: "k-{{.repo}}" } }
`)
	rig := newTestRunner(t, cfg, reg)
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{}, dispatch.Unrecoverable(context.DeadlineExceeded)
	}
	runTrigger(rig, newTrigger("ping", nil), spec)
	reqs := rig.Agents.requests()
	if len(reqs) == 0 {
		t.Fatal("no dispatch")
	}
	if ok, seen := got()[reqs[0].DispatchID]; !seen || ok {
		t.Fatalf("a failed step's cache end: seen %v ok %v", seen, ok)
	}
}

// No cache where its key would be a write grant someone else chose: caches
// off, a target the event's sender chose.
func TestStepCacheWithheld(t *testing.T) {
	// A key that renders whatever the target (so the gate, not a fact the
	// forged target lacks, is what withholds it).
	spec := `
on: svc.ping
steps:
  - { id: top, type: agent, name: a, prompt: p, cache: { key: "static" } }
`
	for name, tc := range map[string]struct {
		cfg   string
		trust bool
	}{
		"caches off":          {cfg: "connectors:\n  svc: { use: fake }\n", trust: true},
		"sandbox off":         {cfg: "connectors:\n  svc: { use: fake }\nsandbox:\n  agents: off\n  caches: { enabled: true }\n", trust: true},
		"an untrusted target": {cfg: cacheCfg},
		"control: trusted":    {cfg: cacheCfg, trust: true},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := loadConfig(t, tc.cfg)
			reg := buildRegistry(t, cfg)
			rig := newTestRunner(t, cfg, reg)
			trig := newTrigger("ping", nil)
			trig.TargetTrusted = tc.trust
			runTrigger(rig, trig, mustSpec(t, spec))
			reqs := rig.Agents.requests()
			want := name == "control: trusted"
			if len(reqs) != 1 || (reqs[0].Cache != nil) != want {
				t.Fatalf("cache %+v", reqs)
			}
		})
	}
}

// Only a step that declares cache: { teach: true } teaches; restoring from
// a key no longer makes its runs teach (an implicit grant of a credential
// for any repository on the host).
func TestStepCacheTeachIsDeclared(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
sandbox:
  caches: { enabled: true }
workflows:
  warm:
    steps:
      - { id: w, type: agent, name: a, prompt: p, cache: { key: "{{.repo}}" } }
  teacher:
    steps:
      - { id: t, type: agent, name: a, prompt: p, cache: { key: "{{.repo}}", teach: true } }
  fix:
    steps:
      - { id: f, type: agent, name: a, prompt: p, cache: { key: "pr-{{.repo}}", restore_from: ["{{.repo}}"] } }
`)
	reg := buildRegistry(t, cfg)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps:
  - { id: a, workflow: warm }
  - { id: b, workflow: teacher }
  - { id: c, workflow: fix }
`))
	teach := map[string]bool{}
	for _, r := range rig.Agents.requests() {
		if r.Cache != nil {
			teach[r.Step.ID] = r.Cache.Teach
		}
	}
	if teach["w"] || !teach["t"] || teach["f"] {
		t.Fatalf("teach %v, want only t", teach)
	}
}

// An agent-authored step may never declare teach: (nor any cache:).
func TestAgentAuthoredStepCannotTeach(t *testing.T) {
	s := config.Step{ID: "x", Type: "agent", Prompt: "p", Cache: &config.StepCache{Key: "{{.repo}}", Teach: true}}
	err := checkAgentAuthoredFields("plan", &s)
	if err == nil || !strings.Contains(err.Error(), "cache.teach") {
		t.Fatalf("an agent-authored step declared teach: %v", err)
	}
}

// An agent-authored plan may not name a cache: a key is a write grant into
// a store other runs restore from — the operator's to give.
func TestAgentAuthoredStepCannotSetCache(t *testing.T) {
	s := config.Step{ID: "x", Type: "agent", Prompt: "p", Cache: &config.StepCache{Key: "{{.repo}}"}}
	if err := checkAgentAuthoredFields("plan", &s); err == nil {
		t.Fatal("an agent-authored step set cache:")
	}
	sanitizeAgentAuthoredStep(&s)
	if s.Cache != nil {
		t.Fatal("sanitize kept cache:")
	}
}
