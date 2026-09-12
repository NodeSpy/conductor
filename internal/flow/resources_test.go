package flow

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/kv"
	"github.com/NodeSpy/conductor/internal/memory"
)

// The agent-authored resource allowlists (#124). The trigger in every case
// targets repo "o/r" (newTrigger), which is implicitly allowed.

// resourcePlan runs one agent-emitted plan under the given policy YAML and
// returns the rig + fake connector state.
func resourcePlan(t *testing.T, policyYAML, plan string) (*testRig, *fakeState) {
	t.Helper()
	cfg := planCfg(t, policyYAML)
	return dispatchPlan(t, cfg, "```plan\n"+plan+"\n```")
}

// Deny by default, per kind: with no allowlist, an agent-authored plan may
// not reference a secret, a store, or a foreign target at all.
func TestResourceAllowlistsDenyByDefault(t *testing.T) {
	base := `
policy:
  agent_authored:
    allow: [ svc.post, kv.* ]
vaults:
  house: { type: file, dir: /tmp/none }
stores:
  main: { type: boltdb }
`
	kv.SetDataDir(t.TempDir())
	kv.ResetStores()
	t.Cleanup(func() { kv.ResetStores(); kv.SetDataDir("") })
	cases := []struct{ name, plan, want string }{
		{"secret handle", `- uses: svc.post
  options: { text: '{{secret "house/k"}}' }`, "allow_secrets"},
		{"vault call", `- uses: svc.post
  options: { text: '{{ vault "house" "k" }}' }`, "allow_secrets"},
		{"vault field", `- uses: svc.post
  options: { text: "{{.vaults.house.k}}" }`, "allow_secrets"},
		{"secrets field", `- uses: svc.post
  options: { text: "{{.secrets.tok}}" }`, "allow_secrets"},
		{"store", `- uses: kv.get
  options: { store: main, key: k }`, "allow_stores"},
		{"foreign target", `- uses: svc.post
  options: { text: hi, repo: other/repo }`, "allow_targets"},
	}
	for _, c := range cases {
		rig, fake := resourcePlan(t, base, c.plan)
		failed, errStr := rig.workflowFailed()
		if !failed || !strings.Contains(errStr, c.want) {
			t.Fatalf("%s: must be denied by default (%s), got %v %q", c.name, c.want, failed, errStr)
		}
		if len(fake.snapshot()) != 0 {
			t.Fatalf("%s: denied plan must not run", c.name)
		}
	}
}

// The allowlists admit exactly what they name — exact entries and per-kind
// globs — and the triggering target is always in scope.
func TestResourceAllowlistsAdmit(t *testing.T) {
	pol := `
policy:
  agent_authored:
    allow: [ svc.post, kv.* ]
    allow_secrets: [ house/k ]
    allow_stores: [ main ]
    allow_targets: [ friendly/* ]
vaults:
  house: { type: file, dir: /tmp/none }
stores:
  main: { type: boltdb }
`
	kv.SetDataDir(t.TempDir())
	kv.ResetStores()
	t.Cleanup(func() { kv.ResetStores(); kv.SetDataDir("") })

	// Listed store + listed glob target + the TRIGGERING target: all pass
	// the resource gate (the plan proceeds to ordinary execution).
	rig, fake := resourcePlan(t, pol, `- id: a
  uses: kv.get
  options: { store: main, key: k }
- id: b
  uses: svc.post
  options: { text: hi, repo: friendly/box }
- id: c
  uses: svc.post
  options: { text: hi, repo: o/r }`)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("allowlisted plan failed: %s", errStr)
	}
	if calls := fake.snapshot(); len(calls) != 2 {
		t.Fatalf("calls: %+v", calls)
	}

	// An unlisted sibling secret still fails.
	rig, _ = resourcePlan(t, pol, `- uses: svc.post
  options: { text: '{{secret "house/other"}}' }`)
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, "allow_secrets") {
		t.Fatalf("unlisted secret must be denied: %v %q", failed, errStr)
	}
	// A vault glob admits the whole vault.
	globPol := strings.Replace(pol, "allow_secrets: [ house/k ]", `allow_secrets: [ "house/*" ]`, 1)
	rig, _ = resourcePlan(t, globPol, `- uses: svc.post
  options: { text: '{{secret "house/anything"}}' }`)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("vault glob must admit: %s", errStr)
	}
}

// The per-kind wildcard grants all of ONE kind only.
func TestResourceAllowlistWildcardPerKind(t *testing.T) {
	pol := `
policy:
  agent_authored:
    allow: [ svc.post ]
    allow_secrets: ["*"]
    no_secret_egress: false
vaults:
  house: { type: file, dir: /tmp/none }
stores:
  main: { type: boltdb }
`
	kv.SetDataDir(t.TempDir())
	kv.ResetStores()
	t.Cleanup(func() { kv.ResetStores(); kv.SetDataDir("") })
	rig, _ := resourcePlan(t, pol, `- uses: svc.post
  options: { text: '{{secret "house/k"}} {{.secrets.tok}}' }`)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("wildcard secrets must admit: %s", errStr)
	}
	// …but stores stay denied.
	rig, _ = resourcePlan(t, strings.Replace(pol, "allow: [ svc.post ]", "allow: [ svc.post, kv.* ]", 1),
		"- uses: kv.get\n  options: { store: main, key: k }")
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, "allow_stores") {
		t.Fatalf("wildcard secrets must not lift stores: %v %q", failed, errStr)
	}
}

// trust: full lifts all three lists (the explicit full-access escape).
func TestResourceAllowlistsTrustFull(t *testing.T) {
	pol := `
policy:
  agent_authored:
    trust: full
vaults:
  house: { type: file, dir: /tmp/none }
stores:
  main: { type: boltdb }
`
	kv.SetDataDir(t.TempDir())
	kv.ResetStores()
	t.Cleanup(func() { kv.ResetStores(); kv.SetDataDir("") })
	rig, fake := resourcePlan(t, pol, `- id: a
  uses: kv.set
  options: { store: main, key: k, value: v }
- id: b
  uses: svc.post
  options: { text: '{{secret "house/k"}}', repo: any/where }`)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("trust: full must lift the allowlists: %s", errStr)
	}
	if calls := fake.snapshot(); len(calls) != 1 {
		t.Fatalf("calls: %+v", calls)
	}
}

// The runtime belt: a TEMPLATED store/repo the static scan can't judge is
// refused when it renders outside the allowlists — and audited.
func TestResourceAllowlistRuntimeBelt(t *testing.T) {
	pol := `
policy:
  agent_authored:
    allow: [ svc.post, kv.* ]
    allow_stores: [ main ]
    allow_targets: [ friendly/* ]
stores:
  main: { type: boltdb }
`
	kv.SetDataDir(t.TempDir())
	kv.ResetStores()
	t.Cleanup(func() { kv.ResetStores(); kv.SetDataDir("") })

	// The store name arrives through trigger context — static sees only the
	// template; the rendered value is outside the list.
	cfg := planCfg(t, pol)
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	rig.Agents.dispatchFunc = planDispatch("```plan\n- uses: svc.post\n  options: { text: hi, repo: \"{{.msg}}\" }\n```")
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "evil/repo"}), mustSpec(t, planSpec))
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, "allow_targets") {
		t.Fatalf("templated foreign target must be refused at runtime: %v %q", failed, errStr)
	}
	if len(fake.snapshot()) != 0 {
		t.Fatal("refused call must not dispatch")
	}
	blocked := false
	for _, e := range rig.Store.auditsWithEvent("verb") {
		if e["outcome"] == "blocked" {
			if opts, _ := e["options"].(map[string]any); opts != nil && opts["barrier"] == "resource_allowlist" {
				blocked = true
			}
		}
	}
	if !blocked {
		t.Fatal("runtime refusal must audit with the resource_allowlist barrier")
	}
}

// The code-step belt: ctx.store(name) with an unlisted name is refused by
// the DataGuard, even one level down inside a workflow the plan calls; the
// listed store works. (An inline run: js plan step is sandbox-rejected, so
// the plan reaches code the same way the laundering path does — through a
// config workflow.)
func TestResourceAllowlistCodeStores(t *testing.T) {
	pol := `
policy:
  agent_authored:
    allow: [ workflow ]
    allow_stores: [ main ]
stores:
  main: { type: boltdb }
  other: { type: boltdb }
workflows:
  touch:
    inputs: { which: { type: string, required: true } }
    steps:
      - id: w
        run: js
        code: 'return ctx.store(ctx.inputs.which).get("ns", "k");'
`
	kv.SetDataDir(t.TempDir())
	kv.ResetStores()
	t.Cleanup(func() { kv.ResetStores(); kv.SetDataDir("") })
	rig, _ := resourcePlan(t, pol, `- id: c
  workflow: touch
  with: { which: other }`)
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "allow_stores") {
		t.Fatalf("code-step touch of an unlisted store must be refused: %v %q", failed, errStr)
	}
	rig, _ = resourcePlan(t, pol, `- id: c
  workflow: touch
  with: { which: main }`)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("listed store must work in code: %s", errStr)
	}
}

// Config-authored steps are untouched: the same references run without any
// allowlist under an identical policy block.
func TestResourceAllowlistsConfigStepsUnaffected(t *testing.T) {
	kv.SetDataDir(t.TempDir())
	kv.ResetStores()
	t.Cleanup(func() { kv.ResetStores(); kv.SetDataDir("") })
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
stores:
  main: { type: boltdb }
policy:
  agent_authored:
    allow: [ svc.post ]
`)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: a
    uses: kv.set
    options: { store: main, key: k, value: v }
  - id: b
    uses: svc.post
    options: { text: hi, repo: any/where }
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "m"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("config-authored steps must be unaffected: %s", errStr)
	}
	if calls := st.snapshot(); len(calls) != 1 {
		t.Fatalf("calls: %+v", calls)
	}
}

// ROUND-6 C. planDataGuard built its resource policy from core.Trigger{} — an
// EMPTY trigger — so rp.trigger was "" and the dispatch's own scope was not
// implicitly allowed on the code face. A `run: js` step touching its own
// repo's memory scope, or its own target, was refused unless the operator had
// listed it: the opposite of the rule the verb surfaces apply, and a refusal
// that reads like a bug to whoever hits it.
func TestCodeStepReachesItsOwnScopeWithNoAllowlist(t *testing.T) {
	pol := `
policy:
  agent_authored:
    allow: [ workflow, code ]
stores:
  main: { type: boltdb }
workflows:
  touch:
    inputs: { scope: { type: string, required: true } }
    steps:
      - id: w
        run: js
        code: 'return ctx.memory.remember("note", [], ctx.inputs.scope);'
`
	kv.SetDataDir(t.TempDir())
	kv.ResetStores()
	t.Cleanup(func() { kv.ResetStores(); kv.SetDataDir("") })
	mgr, err := memory.Build(memory.Options{Type: "memory"})
	if err != nil {
		t.Fatal(err)
	}
	memory.Configure(mgr)
	t.Cleanup(func() { memory.Configure(nil) })

	// newTrigger targets o/r, so the run's own scope is repo:o/r.
	rig, _ := resourcePlan(t, pol, `- id: c
  workflow: touch
  with: { scope: "repo:o/r" }`)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("a code step must reach the dispatch's OWN memory scope with no allowlist: %s", errStr)
	}

	// …and another tenant's scope is still refused.
	rig, _ = resourcePlan(t, pol, `- id: c
  workflow: touch
  with: { scope: "repo:victim/other" }`)
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "allow_memory_scopes") {
		t.Fatalf("another tenant's scope must still be refused: %v %q", failed, errStr)
	}
}

// The same for a STORE: a code step reaching the store its own dispatch
// targets is not what allow_stores exists to stop.
func TestCodeStepOwnTargetStoreStillNeedsListing(t *testing.T) {
	pol := `
policy:
  agent_authored:
    allow: [ workflow ]
    allow_stores: [ main ]
stores:
  main: { type: boltdb }
  other: { type: boltdb }
workflows:
  touch:
    inputs: { which: { type: string, required: true } }
    steps:
      - id: w
        run: js
        code: 'return ctx.store(ctx.inputs.which).get("ns", "k");'
`
	kv.SetDataDir(t.TempDir())
	kv.ResetStores()
	t.Cleanup(func() { kv.ResetStores(); kv.SetDataDir("") })

	// A store is not a per-dispatch resource — there is no "own store" — so
	// the listed one works and an unlisted one does not, unchanged by C.
	rig, _ := resourcePlan(t, pol, `- id: c
  workflow: touch
  with: { which: main }`)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("the listed store must work: %s", errStr)
	}
	rig, _ = resourcePlan(t, pol, `- id: c
  workflow: touch
  with: { which: other }`)
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, "allow_stores") {
		t.Fatalf("an unlisted store must still be refused: %v %q", failed, errStr)
	}
}
