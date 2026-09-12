package flow

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"context"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/kv"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/sqlstore"
	"github.com/NodeSpy/conductor/internal/vaults"
)

// TestGuardApproveGate: an approve-listed verb triggers the dry-run + ask
// hand-off; approval runs the plan for real, denial stops it — and the
// dry-run preview stubs everything first either way.
func TestGuardApproveGate(t *testing.T) {
	pol := `
policy:
  agent_authored:
    allow: [ svc.post ]
    approve: [ svc.ask ]
    approve_via: svc
`
	cfg := planCfg(t, pol)
	plan := "```plan\n- id: risky\n  uses: svc.ask\n  options: { prompt: may I }\n- id: after\n  uses: svc.post\n  options: { text: ran }\n```"

	// Approval path (the fake's ask answers approve by default).
	rig, fake := dispatchPlan(t, cfg, plan)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("approved plan failed: %s", errStr)
	}
	var asks, posts int
	for _, c := range fake.snapshot() {
		switch c.Verb {
		case "ask":
			asks++
		case "post":
			posts++
		}
	}
	// One ask = the approval hand-off; the plan's own svc.ask step then ran,
	// so asks=2; the post ran once (the dry-run preview stubs, not invokes).
	if asks != 2 || posts != 1 {
		t.Fatalf("approve flow calls: asks=%d posts=%d %+v", asks, posts, fake.snapshot())
	}
	plans := rig.Store.auditsWithEvent("plan")
	var approved bool
	for _, e := range plans {
		if e["outcome"] == "approved" {
			approved = true
		}
	}
	if !approved {
		t.Fatalf("approval must be audited: %+v", plans)
	}

	// Denial path.
	cfg2 := planCfg(t, pol)
	reg := buildRegistry(t, cfg2)
	fake2 := newFakeState(t, "svc")
	fake2.mu.Lock()
	fake2.outputs = map[string]map[string]any{"ask": {"action": "discard", "text": "", "ref": ""}}
	fake2.mu.Unlock()
	rig2 := newTestRunner(t, cfg2, reg)
	rig2.Agents.dispatchFunc = planDispatch(plan)
	runTrigger(rig2, newTrigger("ping", nil), mustSpec(t, planSpec))
	failed, errStr := rig2.workflowFailed()
	if !failed || !strings.Contains(errStr, "approval denied") {
		t.Fatalf("denied plan must not run: %v %q", failed, errStr)
	}
	for _, c := range fake2.snapshot() {
		if c.Verb == "post" {
			t.Fatal("a denied plan must run nothing real")
		}
	}
}

// TestGuardApproveWithoutChannel: approval needed but no approve_via → the
// dry-run happens, the plan is rejected, and a human is pinged.
func TestGuardApproveWithoutChannel(t *testing.T) {
	cfg := planCfg(t, `
policy:
  agent_authored:
    allow: [ svc.post ]
    approve: [ svc.ask ]
`)
	plan := "```plan\n- uses: svc.ask\n  options: { prompt: p }\n```"
	rig, fake := dispatchPlan(t, cfg, plan)
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "approve_via is not set") {
		t.Fatalf("no-channel approval: %v %q", failed, errStr)
	}
	if len(fake.snapshot()) != 0 {
		t.Fatal("nothing real may run")
	}
	var needs bool
	for _, e := range rig.Notifier.snapshot() {
		if e.Event == "needs_input" {
			needs = true
		}
	}
	if !needs {
		t.Fatal("must escalate to a human")
	}
}

// TestGuardTrustFull: the deliberate opt-in lifts allow/approve/host but the
// limits still bind.
func TestGuardTrustFull(t *testing.T) {
	cfg := planCfg(t, `
policy:
  agent_authored:
    trust: full
    limits: { max_steps: 2 }
`)
	// A verb no allowlist admits + a command step with no sandbox host.
	plan := "```plan\n- uses: svc.post\n  options: { text: trusted }\n```"
	rig, fake := dispatchPlan(t, cfg, plan)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("trust: full must lift the allowlist: %s", errStr)
	}
	if calls := fake.snapshot(); len(calls) != 1 || calls[0].Opts["text"] != "trusted" {
		t.Fatalf("trusted run: %+v", calls)
	}
	plans := rig.Store.auditsWithEvent("plan")
	if plans[0]["gate"] != "trust" {
		t.Fatalf("trust gate audited: %+v", plans)
	}
	// Limits still bind under trust.
	over := "```plan\n- uses: svc.post\n  options: {text: a}\n- uses: svc.post\n  options: {text: b}\n- uses: svc.post\n  options: {text: c}\n```"
	rig2, _ := dispatchPlan(t, cfg, over)
	if failed, errStr := rig2.workflowFailed(); !failed || !strings.Contains(errStr, "max_steps") {
		t.Fatalf("limits under trust: %v %q", failed, errStr)
	}
}

// TestGuardSandboxHost: agent code/cli steps are FORCED onto the policy
// host; with no host configured they are rejected outright.
func TestGuardSandboxHost(t *testing.T) {
	// No host → rejected, never the main box.
	cfg := planCfg(t, `
policy:
  agent_authored:
    allow: [ code, cli ]
`)
	rig, _ := dispatchPlan(t, cfg, "```plan\n- run: js\n  code: \"return 1;\"\n```")
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, "refusing to run on the main box") {
		t.Fatalf("hostless code: %v %q", failed, errStr)
	}
	rig, _ = dispatchPlan(t, cfg, "```plan\n- type: command\n  command: [echo, hi]\n```")
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, "refusing to run on the main box") {
		t.Fatalf("hostless cli: %v %q", failed, errStr)
	}

	// With a host: the guard rewrites the step onto it (observable via the
	// unknown-host error naming OUR host, since the rig defines none live).
	cfg2 := planCfg(t, `
policy:
  agent_authored:
    allow: [ code ]
    host: sandbox
hosts:
  sandbox: { addr: sandbox.example }
`)
	rig2, _ := dispatchPlan(t, cfg2, "```plan\n- run: sh\n  code: \"echo hi\"\n  host: main-box\n```")
	// The agent tried host: main-box; the guard forced sandbox — the step
	// then fails only at SSH time (no real host in tests), proving the
	// rewrite happened.
	failed, errStr := rig2.workflowFailed()
	if !failed || !strings.Contains(errStr, "sandbox") || strings.Contains(errStr, "main-box") {
		t.Fatalf("sandbox rewrite: %v %q", failed, errStr)
	}
}

// TestGuardIdentityInjection: emitted verbs that take as: get the policy
// identity — the call can't override it back.
func TestGuardIdentityInjection(t *testing.T) {
	cfg := planCfg(t, `
policy:
  agent_authored:
    allow: [ svc.post ]
    identity: bot
`)
	plan := "```plan\n- uses: svc.post\n  options: { text: t, as: me }\n```"
	rig, fake := dispatchPlan(t, cfg, plan)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("failed: %s", errStr)
	}
	calls := fake.snapshot()
	if len(calls) != 1 || calls[0].Opts["as"] != "bot" {
		t.Fatalf("identity must be forced to bot: %+v", calls)
	}
	if len(rig.Store.auditsWithEvent("plan")) == 0 {
		t.Fatal("audited")
	}
}

// TestGuardSecretEgress: reading secret material + touching the outside
// world in one plan is approval-gated (the exfil combo); either alone with
// no_secret_egress passes; disabling the gate lifts it.
func TestGuardSecretEgress(t *testing.T) {
	base := `
policy:
  agent_authored:
    allow: [ svc.post, kv.*, "*.read" ]
    allow_secrets: ["*"]
    allow_stores: ["*"]
%s
vaults:
  housevault: { type: file, dir: /tmp/none }
`
	run := func(t *testing.T, extra, plan string) (*testRig, *fakeState) {
		t.Helper()
		cfg := planCfg(t, fmt.Sprintf(base, extra))
		return dispatchPlan(t, cfg, "```plan\n"+plan+"\n```")
	}
	// Vault read + external write → gated (no approve_via → rejected).
	combo := "- id: r\n  uses: housevault.read\n  options: { key: k }\n- id: w\n  uses: svc.post\n  options: { text: \"{{.r.value}}\" }"
	rig, fake := run(t, "", combo)
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, "no_secret_egress") {
		t.Fatalf("egress combo must gate: %v %q", failed, errStr)
	}
	if len(fake.snapshot()) != 0 {
		t.Fatal("gated plan must not run")
	}
	// A template vault call counts as secret access too.
	tmplRead := "- uses: svc.post\n  options: { text: '{{ vault \"housevault\" \"k\" }}' }"
	rig, _ = run(t, "", tmplRead)
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, "no_secret_egress") {
		t.Fatalf("template vault call must gate: %v %q", failed, errStr)
	}
	// External touch alone: fine.
	rig, fake = run(t, "", "- uses: svc.post\n  options: { text: hello }")
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("plain external: %s", errStr)
	}
	if len(fake.snapshot()) != 1 {
		t.Fatal("plain external must run")
	}
	// Secret access alone (internal-only): fine.
	rig, _ = run(t, "", "- id: r\n  uses: housevault.read\n  options: { key: k }\n- uses: kv.set\n  options: { store: none }")
	if _, errStr := rig.workflowFailed(); strings.Contains(errStr, "no_secret_egress") {
		t.Fatalf("internal-only secret use must not gate on egress: %q", errStr)
	}
	// Gate off → the combo passes the guard.
	rig, _ = run(t, "    no_secret_egress: false", combo)
	if _, errStr := rig.workflowFailed(); strings.Contains(errStr, "no_secret_egress") {
		t.Fatalf("disabled gate must lift: %q", errStr)
	}
}

// REGRESSION (audit finding #2): step hooks are verb calls — an allowlisted
// step must NOT smuggle a non-allowed verb out through hooks:. The guard
// classifies each hook's uses: like a step, approve-listed hook verbs trip
// the approval gate, and admitted hooks actually FIRE on plan steps (with
// the policy identity injected) instead of being silently dropped.
func TestGuardPlanStepHooks(t *testing.T) {
	pol := `
policy:
  agent_authored:
    allow: [ svc.post ]
    approve: [ svc.ask ]
`
	cfg := planCfg(t, pol)

	// A non-allowed hook verb rejects the whole plan pre-run.
	smuggle := "```plan\n- id: ok\n  uses: svc.post\n  options: { text: fine }\n  hooks:\n    - { at: done, uses: svc.fail, options: {} }\n```"
	rig, fake := dispatchPlan(t, cfg, smuggle)
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, `hook[0]: "svc.fail" is not in policy.agent_authored.allow`) {
		t.Fatalf("hook smuggling must be rejected: %v %q", failed, errStr)
	}
	if len(fake.snapshot()) != 0 {
		t.Fatal("nothing may run when a hook is rejected")
	}

	// An approve-listed hook verb trips the approval gate (no approve_via →
	// dry-run + reject), same as a step.
	gated := "```plan\n- id: ok\n  uses: svc.post\n  options: { text: fine }\n  hooks:\n    - { at: done, uses: svc.ask, options: { prompt: p } }\n```"
	rig, _ = dispatchPlan(t, cfg, gated)
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, "approve_via is not set") {
		t.Fatalf("approve-listed hook must gate: %v %q", failed, errStr)
	}

	// Hook options validate against the verb schema at emit time.
	badOpt := "```plan\n- id: ok\n  uses: svc.post\n  options: { text: fine }\n  hooks:\n    - { at: done, uses: svc.post, options: { bogus: 1 } }\n```"
	rig, _ = dispatchPlan(t, cfg, badOpt)
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, `"bogus"`) {
		t.Fatalf("hook option validation: %v %q", failed, errStr)
	}
	badAt := "```plan\n- id: ok\n  uses: svc.post\n  options: { text: fine }\n  hooks:\n    - { at: sometime, uses: svc.post, options: { text: t } }\n```"
	rig, _ = dispatchPlan(t, cfg, badAt)
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, "start|done|fail") {
		t.Fatalf("hook at validation: %v %q", failed, errStr)
	}

	// Hooks count toward max_steps — no 1-step plan with 50 hook verbs.
	cfgLim := planCfg(t, `
policy:
  agent_authored:
    allow: [ svc.post ]
    limits: { max_steps: 2 }
`)
	many := "```plan\n- id: ok\n  uses: svc.post\n  options: { text: fine }\n  hooks:\n    - { at: done, uses: svc.post, options: { text: a } }\n    - { at: done, uses: svc.post, options: { text: b } }\n```"
	rig, _ = dispatchPlan(t, cfgLim, many)
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, "max_steps") {
		t.Fatalf("hooks must count against max_steps: %v %q", failed, errStr)
	}

	// Admitted hooks FIRE on plan steps (consistent with workflow steps) and
	// carry the policy identity.
	cfgID := planCfg(t, `
policy:
  agent_authored:
    allow: [ svc.post ]
    identity: bot
`)
	firing := "```plan\n- id: main\n  uses: svc.post\n  options: { text: step }\n  hooks:\n    - { at: done, uses: svc.post, options: { text: hooked } }\n```"
	rig, fake = dispatchPlan(t, cfgID, firing)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("admitted hooks: %s", errStr)
	}
	calls := fake.snapshot()
	if len(calls) != 2 || calls[1].Opts["text"] != "hooked" || calls[1].Opts["as"] != "bot" {
		t.Fatalf("plan-step hooks must fire with the policy identity: %+v", calls)
	}

	// A hook reading a vault + any external verb = the egress combo.
	cfgEg := planCfg(t, `
policy:
  agent_authored:
    allow: [ svc.post, "*.read" ]
    allow_secrets: [ housevault/k ]
vaults:
  housevault: { type: file, dir: /tmp/none }
`)
	eg := "```plan\n- id: ok\n  uses: svc.post\n  options: { text: out }\n  hooks:\n    - { at: done, uses: housevault.read, options: { key: k } }\n```"
	rig, _ = dispatchPlan(t, cfgEg, eg)
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, "no_secret_egress") {
		t.Fatalf("hook vault read must count for egress: %v %q", failed, errStr)
	}
}

// REGRESSION (audit finding #5): the two-plan kv exfiltration path is
// blocked. Plan A (vault read → kv.set) is approval-gated STATICALLY —
// writing durable shared state with secrets in scope is parking. Values the
// static scan can't see (a secret laundered through the trigger context or a
// step output into kv.set) hit the runtime write barrier. Either way the
// secret never lands in kv, so plan B (kv.get → external post) reads
// nothing.
func TestGuardKvExfilBlocked(t *testing.T) {
	t.Cleanup(vaults.Reset)
	t.Cleanup(func() { kv.ResetStores(); kv.SetDataDir("") })
	kv.SetDataDir(t.TempDir())
	vaultDir := t.TempDir()
	const secretVal = "exfil-me-cleartext"
	if err := os.WriteFile(filepath.Join(vaultDir, "apikey"), []byte(secretVal), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgYAML := `
connectors:
  svc: { use: fake }
stores:
  main: { type: boltdb }
vaults:
  hv: { type: file, dir: ` + vaultDir + ` }
workflows:
  roles:
    steps:
      - { id: planner, type: agent, name: planner, prompt: p, model: x }
policy:
  agent_authored:
    allow: [ svc.post, kv.*, "*.read" ]
    allow_secrets: ["*"]
    allow_stores: [main]
`
	cfg := loadConfig(t, cfgYAML)
	shared := testSecrets(nil)
	reg, err := connector.Build(cfg, connector.Deps{Secrets: shared, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	newRig := func() *testRig {
		rig := newTestRunner(t, cfg, reg)
		rig.Runner.Secrets = shared
		return rig
	}

	// PLAN A, static layer: vault read + kv.set in one plan = parking → the
	// egress gate fires pre-run (no approve_via → rejected).
	rig := newRig()
	planA := "```plan\n" +
		"- id: r\n  uses: hv.read\n  options: { key: apikey }\n" +
		"- id: park\n  uses: kv.set\n  options: { store: main, key: loot, value: \"{{.r.value}}\" }\n" +
		"```"
	rig.Agents.dispatchFunc = planDispatch(planA)
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, planSpec))
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "durable shared state") {
		t.Fatalf("plan A must gate statically: %v %q", failed, errStr)
	}

	// PLAN A, laundered variant the static scan can't see: the secret rides
	// the trigger context into kv.set — the runtime write barrier refuses it.
	shared.Track(secretVal)
	rig2 := newRig()
	planL := "```plan\n- id: park\n  uses: kv.set\n  options: { store: main, key: loot, value: \"{{.leak}}\" }\n```"
	rig2.Agents.dispatchFunc = planDispatch(planL)
	runTrigger(rig2, newTrigger("ping", map[string]any{"leak": secretVal}), mustSpec(t, planSpec))
	failed, errStr = rig2.workflowFailed()
	if !failed || !strings.Contains(errStr, "refusing to write secret material") {
		t.Fatalf("laundered write must hit the barrier: %v %q", failed, errStr)
	}
	blocked := false
	for _, e := range rig2.Store.auditsWithEvent("verb") {
		if e["outcome"] == "blocked" {
			blocked = true
		}
	}
	if !blocked {
		t.Fatal("the barrier refusal must be audited")
	}

	// The secret never landed: PLAN B (individually innocent kv.get →
	// external post) reads nothing.
	st, err := kv.Use("main")
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := st.Get("default", "loot"); found {
		t.Fatal("the secret must never land in kv")
	}
	rig3 := newRig()
	fake := newFakeState(t, "svc")
	planB := "```plan\n- id: g\n  uses: kv.get\n  options: { store: main, key: loot }\n- id: out\n  uses: svc.post\n  options: { text: \"loot={{.g.value}}\" }\n```"
	rig3.Agents.dispatchFunc = planDispatch(planB)
	runTrigger(rig3, newTrigger("ping", nil), mustSpec(t, planSpec))
	if failed, errStr := rig3.workflowFailed(); failed {
		t.Fatalf("plan B is individually innocent and runs: %s", errStr)
	}
	calls := fake.snapshot()
	if len(calls) != 1 || strings.Contains(fmt.Sprint(calls[0].Opts["text"]), secretVal) {
		t.Fatalf("nothing to exfiltrate: %+v", calls)
	}

	// Non-secret kv writes from plans stay unaffected.
	rig4 := newRig()
	planOK := "```plan\n- uses: kv.set\n  options: { store: main, key: note, value: \"plain\" }\n```"
	rig4.Agents.dispatchFunc = planDispatch(planOK)
	runTrigger(rig4, newTrigger("ping", nil), mustSpec(t, planSpec))
	if failed, errStr := rig4.workflowFailed(); failed {
		t.Fatalf("plain kv writes must pass: %s", errStr)
	}
	// memory.remember parking is gated the same way statically.
	memCfg := loadConfig(t, strings.Replace(cfgYAML, "allow: [ svc.post, kv.*, \"*.read\" ]",
		"allow: [ svc.post, kv.*, memory.*, \"*.read\" ]", 1)+"memory: { type: memory }\n")
	regM, err := connector.Build(memCfg, connector.Deps{Secrets: shared, Config: memCfg})
	if err != nil {
		t.Fatal(err)
	}
	tempMemory(t)
	rig5 := newTestRunner(t, memCfg, regM)
	rig5.Runner.Secrets = shared
	planM := "```plan\n- id: r\n  uses: hv.read\n  options: { key: apikey }\n- uses: memory.remember\n  options: { text: \"{{.r.value}}\" }\n```"
	rig5.Agents.dispatchFunc = planDispatch(planM)
	runTrigger(rig5, newTrigger("ping", nil), mustSpec(t, planSpec))
	if failed, errStr := rig5.workflowFailed(); !failed || !strings.Contains(errStr, "durable shared state") {
		t.Fatalf("memory parking must gate: %v %q", failed, errStr)
	}
}

// REGRESSION: the secret-scope detector matched only the literal ".secrets"/
// ".vaults", so {{index . "secrets" "x"}} (and rebinding tricks) walked the
// same data while evading the egress gate. Any word-boundaried mention of
// secrets/vaults inside a template action now counts as secret access.
func TestGuardSecretEgressIndexEvasion(t *testing.T) {
	unit := []struct {
		s    string
		want bool
	}{
		{`{{index . "secrets" "gh_token"}}`, true},
		{`{{ $c := . }}{{ index $c "vaults" }}`, true},
		{`{{.secrets.gh_token}}`, true},
		{`{{.options.supersecretsummary}}`, false}, // word boundary: no false positive
		{`plain text mentioning secrets outside any action`, false},
	}
	for _, tc := range unit {
		if got := referencesSecretScope(tc.s); got != tc.want {
			t.Errorf("referencesSecretScope(%q) = %v, want %v", tc.s, got, tc.want)
		}
	}

	// End to end: the index-form read + an external write is egress-gated.
	cfg := planCfg(t, `
policy:
  agent_authored:
    allow: [ svc.post, "*.read" ]
vaults:
  housevault: { type: file, dir: /tmp/none }
`)
	plan := "- uses: svc.post\n  options: { text: '{{index . \"secrets\" \"gh_token\"}}' }"
	rig, fake := dispatchPlan(t, cfg, "```plan\n"+plan+"\n```")
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, "no_secret_egress") {
		t.Fatalf("index-form secret read must gate: %v %q", failed, errStr)
	}
	if len(fake.snapshot()) != 0 {
		t.Fatal("gated plan must not run")
	}
}

// REGRESSION: `allow: ["*"]` (and any glob, "conductor.*" included) admitted
// the conductor.* daemon-control verbs — an agent-authored plan could
// update/restart/reload the daemon or fire triggers under a broad allowlist.
// Those verbs are exact-match only now.
func TestGuardWildcardNeverAdmitsConductorVerbs(t *testing.T) {
	mk := func(allow string) (*config.Config, *connector.Registry) {
		cfg := planCfg(t, `
policy:
  agent_authored:
    allow: [ `+allow+` ]
`)
		return cfg, buildRegistry(t, cfg)
	}
	step := func(uses string) []config.Step {
		return []config.Step{{ID: "s", Uses: uses, Options: map[string]any{}}}
	}

	// The full wildcard: ordinary verbs admit, conductor verbs do not.
	cfg, reg := mk(`"*"`)
	pol := cfg.Policy.AgentAuthored
	if _, err := guardPlan(cfg, reg, pol, step("svc.post")); err != nil {
		t.Fatalf("wildcard must still admit ordinary verbs: %v", err)
	}
	for _, v := range []string{"conductor.update", "conductor.restart", "conductor.reload", "conductor.run"} {
		if _, err := guardPlan(cfg, reg, pol, step(v)); err == nil || !strings.Contains(err.Error(), v) {
			t.Fatalf("allow [*] must not admit %s: %v", v, err)
		}
	}

	// A conductor glob is not explicit naming either.
	cfg2, reg2 := mk(`"conductor.*"`)
	if _, err := guardPlan(cfg2, reg2, cfg2.Policy.AgentAuthored, step("conductor.restart")); err == nil {
		t.Fatal("allow [conductor.*] must not admit conductor.restart")
	}

	// Exact naming admits.
	cfg3, reg3 := mk(`conductor.restart`)
	if _, err := guardPlan(cfg3, reg3, cfg3.Policy.AgentAuthored, step("conductor.restart")); err != nil {
		t.Fatalf("explicitly named conductor.restart must admit: %v", err)
	}
}

// REGRESSION (read-and-relay): no_secret_egress gated the WRITE half only —
// a plan doing kv.get of a secret PARKED in a store (by an earlier trusted
// run, an external writer, …) and posting it out via an allowlisted external
// verb relayed it with zero approval. Two runtime layers close it: the relay
// barrier refuses an external verb whose rendered options carry a tracked
// secret, and a read-taint (set when ANY step's outputs carry one — kv.get/
// first/last/index/slice, sql.query, memory.recall alike) refuses every
// LATER external step even when the value was transformed in between.
func TestGuardReadAndRelayBarrier(t *testing.T) {
	const secretVal = "parked-s3cr3t-XYZZY"
	cfgYAML := `
connectors:
  svc: { use: fake }
stores:
  main: { type: boltdb }
  db:   { type: sqlite, path: ":memory:" }
workflows:
  roles:
    steps:
      - { id: planner, type: agent, name: planner, prompt: p, model: x }
policy:
  agent_authored:
    allow: [ svc.post, kv.*, sql.*, "*.read" ]
    allow_stores: [main, db]
`
	kv.SetDataDir(t.TempDir())
	kv.ResetStores()
	sqlstore.ResetStores()
	t.Cleanup(func() { kv.ResetStores(); sqlstore.ResetStores(); kv.SetDataDir("") })
	cfg := loadConfig(t, cfgYAML)
	shared := testSecrets(nil)
	shared.Track(secretVal)
	reg, err := connector.Build(cfg, connector.Deps{Secrets: shared, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	newRig := func() (*testRig, *fakeState) {
		rig := newTestRunner(t, cfg, reg)
		rig.Runner.Secrets = shared
		return rig, newFakeState(t, "svc")
	}

	// Park the secret directly (simulating an earlier trusted writer).
	st, err := kv.Use("main")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Set("default", "loot", secretVal, 0); err != nil {
		t.Fatal(err)
	}

	// kv.get → external post of the raw value: refused, nothing sent.
	rig, fake := newRig()
	rig.Agents.dispatchFunc = planDispatch("```plan\n" +
		"- id: g\n  uses: kv.get\n  options: { store: main, key: loot }\n" +
		"- id: out\n  uses: svc.post\n  options: { text: \"loot={{.g.value}}\" }\n```")
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, planSpec))
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, "no_secret_egress") {
		t.Fatalf("read-and-relay must be refused: %v %q", failed, errStr)
	}
	if len(fake.snapshot()) != 0 {
		t.Fatalf("the secret reached the wire: %+v", fake.snapshot())
	}

	// The read-taint catches a TRANSFORMED relay too: the posted text doesn't
	// contain the tracked value, but the plan read it — later external
	// touches are refused wholesale.
	rig2, fake2 := newRig()
	rig2.Agents.dispatchFunc = planDispatch("```plan\n" +
		"- id: g\n  uses: kv.get\n  options: { store: main, key: loot }\n" +
		"- id: out\n  uses: svc.post\n  options: { text: \"all done, nothing to see\" }\n```")
	runTrigger(rig2, newTrigger("ping", nil), mustSpec(t, planSpec))
	if failed, errStr := rig2.workflowFailed(); !failed || !strings.Contains(errStr, "secret material") {
		t.Fatalf("post-read external touch must be refused: %v %q", failed, errStr)
	}
	if len(fake2.snapshot()) != 0 {
		t.Fatalf("tainted plan still posted: %+v", fake2.snapshot())
	}

	// sql.query is a read path too.
	sqlSt, err := sqlstore.Use("db")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := sqlSt.Exec(context.Background(), "CREATE TABLE loot (v TEXT)", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sqlSt.Exec(context.Background(), "INSERT INTO loot (v) VALUES (?)", []any{secretVal}); err != nil {
		t.Fatal(err)
	}
	rig3, fake3 := newRig()
	rig3.Agents.dispatchFunc = planDispatch("```plan\n" +
		"- id: q\n  uses: sql.query\n  options: { store: db, sql: \"SELECT v FROM loot\" }\n" +
		"- id: out\n  uses: svc.post\n  options: { text: \"rows read\" }\n```")
	runTrigger(rig3, newTrigger("ping", nil), mustSpec(t, planSpec))
	if failed, errStr := rig3.workflowFailed(); !failed || !strings.Contains(errStr, "no_secret_egress") {
		t.Fatalf("sql.query read must taint the plan: %v %q", failed, errStr)
	}
	if len(fake3.snapshot()) != 0 {
		t.Fatalf("sql-tainted plan still posted: %+v", fake3.snapshot())
	}

	// A non-secret read followed by an external post stays unaffected.
	if err := st.Set("default", "plain", "just-a-note", 0); err != nil {
		t.Fatal(err)
	}
	rig4, fake4 := newRig()
	rig4.Agents.dispatchFunc = planDispatch("```plan\n" +
		"- id: g\n  uses: kv.get\n  options: { store: main, key: plain }\n" +
		"- id: out\n  uses: svc.post\n  options: { text: \"note={{.g.value}}\" }\n```")
	runTrigger(rig4, newTrigger("ping", nil), mustSpec(t, planSpec))
	if failed, errStr := rig4.workflowFailed(); failed {
		t.Fatalf("non-secret reads must pass: %s", errStr)
	}
	if calls := fake4.snapshot(); len(calls) != 1 || calls[0].Opts["text"] != "note=just-a-note" {
		t.Fatalf("plain relay: %+v", fake4.snapshot())
	}
}

// REGRESSION (end-to-end): the plan write barrier now reaches IN-PROCESS
// code bindings. A config workflow whose js step writes its input through
// ctx.store, invoked FROM an unapproved plan (the barrier ctx propagates
// through execWorkflowCall), must refuse a tracked-secret value — before
// this, ctx.store bypassed the barrier that `uses: kv.set` enforces.
func TestGuardCodeBindingWriteBarrier(t *testing.T) {
	const secretVal = "code-park-s3cr3t"
	kv.SetDataDir(t.TempDir())
	kv.ResetStores()
	t.Cleanup(func() { kv.ResetStores(); kv.SetDataDir("") })
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
stores:
  main: { type: boltdb }
workflows:
  roles:
    steps:
      - { id: planner, type: agent, name: planner, prompt: p, model: x }
  park:
    inputs: { v: { type: string, required: true } }
    steps:
      - id: w
        run: js
        code: |
          ctx.store("main").set("ns", "loot", ctx.inputs.v);
          return 1;
policy:
  agent_authored:
    allow: [ workflow, svc.post ]
    allow_stores: [main]
`)
	shared := testSecrets(nil)
	shared.Track(secretVal)
	reg, err := connector.Build(cfg, connector.Deps{Secrets: shared, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.Secrets = shared
	rig.Agents.dispatchFunc = planDispatch("```plan\n" +
		"- id: go\n  workflow: park\n  with: { v: \"{{.leak}}\" }\n```")
	runTrigger(rig, newTrigger("ping", map[string]any{"leak": secretVal}), mustSpec(t, planSpec))
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, "no_secret_egress") {
		t.Fatalf("code-binding write of a secret must hit the barrier: %v %q", failed, errStr)
	}
	st, err := kv.Use("main")
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := st.Get("ns", "loot"); found {
		t.Fatal("the secret must never land in kv via a code binding")
	}

	// The same workflow with a plain value passes.
	rig2 := newTestRunner(t, cfg, reg)
	rig2.Runner.Secrets = shared
	rig2.Agents.dispatchFunc = planDispatch("```plan\n" +
		"- id: go\n  workflow: park\n  with: { v: \"plain-note\" }\n```")
	runTrigger(rig2, newTrigger("ping", nil), mustSpec(t, planSpec))
	if failed, errStr := rig2.workflowFailed(); failed {
		t.Fatalf("plain code-binding writes must pass: %s", errStr)
	}
	if v, found, _ := st.Get("ns", "loot"); !found || v != "plain-note" {
		t.Fatalf("plain write must land: %v %v", v, found)
	}
}

// REGRESSION: audit entries redacted options/outputs but wrote err.Error()
// RAW — a REST secret in a URL query rides url.Error verbatim into
// step_error / verb audits (and the fail-hook scope). Error strings now
// redact like their siblings.
func TestAuditRedactsErrorStrings(t *testing.T) {
	const secret = "url-borne-s3cr3t-XYZZY"
	// A rest connector whose base_url points at a dead port: the transport
	// error embeds the full URL — query (with the interpolated secret)
	// included.
	cfg := loadConfig(t, `
connectors:
  api:
    use: rest
    base_url: http://127.0.0.1:1
    verbs:
      ping: { method: GET, path: /x, query: { key: "url-borne-s3cr3t-XYZZY" } }
`)
	shared := testSecrets(nil)
	shared.Track(secret)
	reg, err := connector.Build(cfg, connector.Deps{Secrets: shared, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.Secrets = shared
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps: [ { id: call, uses: api.ping } ]
`))
	if failed, _ := rig.workflowFailed(); !failed {
		t.Fatal("the dead-port call must fail")
	}
	audits := rig.Store.auditsWithEvent("step_error")
	if len(audits) == 0 {
		t.Fatal("expected a step_error audit entry")
	}
	var sawRedacted bool
	for _, e := range audits {
		if strings.Contains(fmt.Sprint(e), secret) {
			t.Fatalf("secret reached an audit entry: %v", e)
		}
		if es, _ := e["error"].(string); strings.Contains(es, secrets.Placeholder) {
			sawRedacted = true
		}
	}
	if !sawRedacted {
		t.Fatalf("the error string must carry the redaction placeholder: %v", audits)
	}
}

// REGRESSION (encoding-aware tracking): a code step base64/hex/url-encoding
// a secret before relaying it defeated the substring-only barriers. Tracking
// now covers the common encodings, so the SAME containsTrackedSecret used by
// the write barrier, relay barrier, and read-taint catches them. (An
// attacker-controlled transform still evades — see trackLocked's scope note.)
func TestBarriersCatchEncodedSecrets(t *testing.T) {
	const secret = "enc-s3cr3t+value/x"
	shared := testSecrets(nil)
	shared.Track(secret)
	r := &Runner{Secrets: shared}
	for name, enc := range map[string]string{
		"base64": base64.StdEncoding.EncodeToString([]byte(secret)),
		"hex":    hex.EncodeToString([]byte(secret)),
		"url":    url.QueryEscape(secret),
	} {
		if !r.containsTrackedSecret(map[string]any{"out": "v=" + enc}) {
			t.Errorf("%s-encoded secret not caught by containsTrackedSecret", name)
		}
	}
	if r.containsTrackedSecret(map[string]any{"out": "plain text"}) {
		t.Error("plain text must not match")
	}
}
