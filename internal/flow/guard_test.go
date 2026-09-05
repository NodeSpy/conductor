package flow

import (
	"fmt"
	"strings"
	"testing"
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
