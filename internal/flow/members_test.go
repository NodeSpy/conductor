package flow

import (
	"strings"
	"testing"
)

const membersCfg = "connectors:\n  a: { use: fake }\n  b: { use: fake }\n  c: { use: fake }\n"

// over: runs the verb once per member, against THAT member, with
// {{.member}} in scope and outputs keyed by member.
func TestOverRunsOncePerMember(t *testing.T) {
	cfg := loadConfig(t, membersCfg)
	reg := buildRegistry(t, cfg)
	sa, sb, sc := newFakeState(t, "a"), newFakeState(t, "b"), newFakeState(t, "c")
	sa.outputs["post"] = map[string]any{"n": 1}
	sb.outputs["post"] = map[string]any{"n": 2}
	spec := mustSpec(t, `
on: a.ping
steps:
  - { id: fan, uses: a.post, over: true, members: [a, b], options: { text: "to {{.member}}" } }
  - { id: after, uses: c.post, options: { text: "{{.fan.count}} {{.fan.by_member.b.n}}" } }
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", nil), spec)
	if failed, e := rig.workflowFailed(); failed {
		t.Fatal(e)
	}
	if ca, cb := sa.snapshot(), sb.snapshot(); len(ca) != 1 || len(cb) != 1 || ca[0].Opts["text"] != "to a" || cb[0].Opts["text"] != "to b" {
		t.Fatalf("a=%+v b=%+v", ca, cb)
	}
	if got := sc.snapshot(); len(got) != 1 || got[0].Opts["text"] != "2 2" {
		t.Fatalf("after: %+v", got)
	}
}

// member: dispatches to the named member, and refuses any name outside the
// list — a template cannot reach a connector the operator did not list.
func TestMemberPicksAndIsClosed(t *testing.T) {
	cfg := loadConfig(t, membersCfg)
	reg := buildRegistry(t, cfg)
	sa, sb, sc := newFakeState(t, "a"), newFakeState(t, "b"), newFakeState(t, "c")
	spec := mustSpec(t, `
on: a.ping
steps:
  - { id: one, uses: a.post, for_each: "{{.targets}}", member: "{{.item}}", members: [a, b], options: { text: x } }
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"targets": []any{"b", "b", "a"}}), spec)
	if failed, e := rig.workflowFailed(); failed {
		t.Fatal(e)
	}
	if len(sa.snapshot()) != 1 || len(sb.snapshot()) != 2 {
		t.Fatalf("a=%d b=%d", len(sa.snapshot()), len(sb.snapshot()))
	}

	rig = newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"targets": []any{"c"}}), spec)
	failed, e := rig.workflowFailed()
	if !failed || !strings.Contains(e, `"c" is not one of this step's connectors`) {
		t.Fatalf("member outside the list must fail, got failed=%v %q", failed, e)
	}
	if len(sc.snapshot()) != 0 {
		t.Fatal("c must never be called")
	}
}

// The event's connector instance is in scope as {{.connector}}.
func TestConnectorInScope(t *testing.T) {
	cfg := loadConfig(t, membersCfg)
	reg := buildRegistry(t, cfg)
	sa := newFakeState(t, "a")
	spec := mustSpec(t, `
on: a.ping
steps:
  - { id: s, uses: a.post, options: { text: "from {{.connector}}" } }
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", nil), spec)
	if got := sa.snapshot(); len(got) != 1 || got[0].Opts["text"] != "from fake" {
		t.Fatalf("%+v", got)
	}
}

func TestValidateMemberSteps(t *testing.T) {
	ok := membersCfg + `
triggers:
  - on: a.ping
    steps:
      - { id: fan, uses: a.post, over: true, members: [a, b], options: { text: "{{.member}}" } }
      - { id: next, uses: c.post, options: { text: "{{.fan.count}} {{.connector}}" } }
`
	cfg := loadConfig(t, ok)
	if err := Validate(cfg, buildRegistry(t, cfg)); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	bad := map[string]string{
		"uses outside members": "      - { id: x, uses: c.post, over: true, members: [a, b], options: { text: y } }\n",
		"members without mode": "      - { id: x, uses: a.post, members: [a, b], options: { text: y } }\n",
		"unknown member":       "      - { id: x, uses: a.post, over: true, members: [a, zz], options: { text: y } }\n",
	}
	for name, step := range bad {
		cfg := loadConfig(t, membersCfg+"triggers:\n  - on: a.ping\n    steps:\n"+step)
		if err := Validate(cfg, buildRegistry(t, cfg)); err == nil {
			t.Errorf("%s: want a validate error", name)
		}
	}
}
