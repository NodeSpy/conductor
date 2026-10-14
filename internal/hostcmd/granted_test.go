package hostcmd

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// A grant stands in for the operator's allow list naming the exact command:
// it lifts an allow-list miss, an unallowed content-executing command (which
// still runs confined), the gh write binding's naming and the keyword guard
// — and nothing else: a built-in guardrail, a deny rule, the path guard, a
// closed target and a step's own narrowing all still refuse.
func TestGrantedLiftsOnlyWhatAnAllowEntryWould(t *testing.T) {
	decide := func(tool string, rule Rule, ctx Context, args ...string) Decision {
		return Decide(Request{Tool: tool, Args: args, Cwd: ctx.Workspace}, rule, ctx)
	}
	granted := func(r Rule) Rule { r.Granted = true; return r }
	ctx := testCtx()
	tf := Resolve("terraform", nil, false)

	if d := decide("terraform", tf, ctx, "apply", "plan.tfplan"); d.Allow {
		t.Fatal("setup: terraform apply is refused without a grant")
	}
	d := decide("terraform", granted(tf), ctx, "apply", "plan.tfplan")
	if !d.Allow || !d.Confine {
		t.Fatalf("a grant lifts the content-exec refusal, and the run stays confined: %+v", d)
	}

	gh := Resolve("gh", nil, false)
	if d := decide("gh", granted(gh), ctx, "pr", "create", "-t", "x", "-b", "y"); !d.Allow {
		t.Fatalf("a grant names a gh write the binding refuses: %s", d.Reason)
	}
	if d := decide("gh", granted(gh), ctx, "auth", "token"); d.Allow {
		t.Fatal("a built-in guardrail is never lifted")
	}
	closed := ctx
	closed.TargetClosed = func() string { return "target: acme/app#42 is merged" }
	if d := decide("gh", granted(gh), closed, "pr", "comment", "42", "-b", "x"); d.Allow {
		t.Fatal("a closed target is never lifted")
	}
	if d := decide("gh", granted(gh), ctx, "pr", "comment", "42", "--body-file", "/home/op/.ssh/id_rsa"); d.Allow {
		t.Fatal("the path guard is never lifted")
	}

	deny := Resolve("terraform", []*config.IsolationConfig{{Host: map[string]*config.HostCommand{"terraform": {Deny: []string{"apply *"}}}}}, false)
	if d := decide("terraform", granted(deny), ctx, "apply", "plan.tfplan"); d.Allow || !strings.Contains(d.Reason, "deny") {
		t.Fatalf("a deny rule is never lifted: %+v", d)
	}
	step := Resolve("terraform", []*config.IsolationConfig{nil, nil, {Host: map[string]*config.HostCommand{"terraform": {Allow: []string{"plan *"}}}}}, true)
	if d := decide("terraform", granted(step), ctx, "apply", "plan.tfplan"); d.Allow {
		t.Fatal("a step's own narrowing still applies to a granted command")
	}
	op := Resolve("docker", []*config.IsolationConfig{{Host: map[string]*config.HostCommand{"docker": {Allow: []string{"ps"}}}}}, false)
	if d := decide("docker", op, ctx, "image", "ls"); d.Allow {
		t.Fatal("setup: an operator allow list refuses what it doesn't name")
	}
	if d := decide("docker", granted(op), ctx, "image", "ls"); !d.Allow {
		t.Fatalf("a grant stands in for the operator's allow list: %s", d.Reason)
	}
}

// A grant for a whole `vop exec <profile> -- <cmd>` is the operator naming
// that exact command, its inner write included; without it the write stays
// refused.
func TestGrantedVopCoversTheInnerWrite(t *testing.T) {
	orgA := Rule{Tool: "vop", Profiled: true, Allow: [][]string{{"exec orgA *"}}, OperatorAllow: []string{"exec orgA *"}}
	args := []string{"exec", "orgA", "--", "aws", "s3", "cp", "a", "s3://b/a"}
	if d := decideVop(orgA, nil, args...); d.Allow {
		t.Fatal("setup: the inner write is refused without a grant")
	}
	orgA.Granted = true
	if d := decideVop(orgA, nil, args...); !d.Allow {
		t.Fatalf("the granted vop command runs its inner write: %s", d.Reason)
	}
}
