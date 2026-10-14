package hostcmd

import (
	"strings"
	"testing"
)

// vopCtx is testCtx with RuleFor wired to rules, so vop's bind can judge an
// inner command the way the broker does in production.
func vopCtx(rules map[string]Rule) Context {
	ctx := testCtx()
	ctx.RuleFor = func(tool string) Rule {
		if r, ok := rules[tool]; ok {
			return r
		}
		return Rule{Tool: tool, Profiled: profileFor(tool) != nil}
	}
	return ctx
}

func decideVop(rule Rule, inner map[string]Rule, args ...string) Decision {
	if rule.Tool == "" {
		rule = Rule{Tool: "vop", Profiled: true}
	}
	return Decide(Request{Tool: "vop", Args: args, Cwd: "/state/worktrees/d1"}, rule, vopCtx(inner))
}

func TestVopBuiltinRefusals(t *testing.T) {
	refused := [][]string{
		{"dump"}, {"dump", "json"}, {"cred-process", "tap"}, {"cat"},
		{"add", "tap"}, {"edit", "tap"}, {"rm", "tap"},
		{"rotate", "tap"}, {"migrate"}, {"auth"}, {"serve"},
		{"shell", "tap"}, {"unlock", "tap"}, {"refresh", "tap"},
		{"bogus-command"},
	}
	for _, args := range refused {
		d := decideVop(Rule{}, nil, args...)
		if d.Allow {
			t.Errorf("vop %v: must be refused, got allowed", args)
		}
		if !strings.Contains(d.Reason, "built-in") {
			t.Errorf("vop %v: reason %q must cite the built-in refusal", args, d.Reason)
		}
	}
}

func TestVopReadOnlySubcommandsAllowed(t *testing.T) {
	reads := [][]string{
		{"ls"}, {"profile"}, {"show", "tap"}, {"version"}, {"check"},
		{"skill"}, {"test", "tap"}, {"completion", "bash"}, {"help"}, {},
	}
	for _, args := range reads {
		d := decideVop(Rule{}, nil, args...)
		if !d.Allow {
			t.Errorf("vop %v: a read-only subcommand must be allowed, got %q", args, d.Reason)
		}
	}
}

func TestVopExecRequiresExplicitProfile(t *testing.T) {
	for _, args := range [][]string{
		{"exec"},
		{"exec", "--"},
		{"exec", "--", "aws", "s3", "ls"},
	} {
		d := decideVop(Rule{}, nil, args...)
		if d.Allow || !strings.Contains(d.Reason, "explicit profile") {
			t.Errorf("vop %v: an implicit profile must be refused, got %+v", args, d)
		}
	}
}

func TestVopExecInnerToolMustBeInTheAllowedSet(t *testing.T) {
	rule := Rule{Tool: "vop", Profiled: true, OperatorAllow: []string{"exec tap *"}}
	for _, args := range [][]string{
		{"exec", "tap", "--", "bash", "-c", "x"},
		{"exec", "tap", "--", "env", "aws", "s3", "ls"},
		{"exec", "tap", "--", "sh"},
		{"exec", "tap", "--", "vop", "exec", "tap2", "--", "aws", "s3", "ls"},
		{"exec", "tap", "--"}, // no command at all after --
	} {
		d := decideVop(rule, nil, args...)
		if d.Allow {
			t.Errorf("vop %v: a non-aws/terraform/kubectl inner command must be refused", args)
		}
	}
}

func TestVopExecNeedsTheProfileNamedLiterally(t *testing.T) {
	cases := []struct {
		allow []string
		want  bool
	}{
		{nil, false},
		{[]string{"*"}, false},              // a blanket * never opens exec
		{[]string{"exec *"}, false},         // profile still a glob
		{[]string{"exec * *"}, false},       // profile still a glob
		{[]string{"exec other *"}, false},   // names a DIFFERENT profile
		{[]string{"exec tap *"}, true},      // names this profile literally
		{[]string{"*", "exec tap *"}, true}, // alongside a blanket allow
	}
	for _, tc := range cases {
		rule := Rule{Tool: "vop", Profiled: true, OperatorAllow: tc.allow}
		if len(tc.allow) > 0 {
			rule.Allow = [][]string{tc.allow}
		}
		d := decideVop(rule, nil, "exec", "tap", "--", "aws", "s3", "ls")
		if d.Allow != tc.want {
			t.Errorf("allow=%v: got allow=%v reason=%q, want allow=%v", tc.allow, d.Allow, d.Reason, tc.want)
		}
	}
}

func TestVopExecDelegatesToTheInnerToolsOwnRule(t *testing.T) {
	rule := Rule{Tool: "vop", Profiled: true, OperatorAllow: []string{"exec tap *"}, Allow: [][]string{{"exec tap *"}}}

	// aws's own built-in guardrail refuses `aws configure` regardless of vop.
	d := decideVop(rule, nil, "exec", "tap", "--", "aws", "configure", "set", "x", "y")
	if d.Allow || !strings.Contains(d.Reason, "built-in: aws configure") {
		t.Fatalf("aws configure under vop must be refused by aws's own built-in: %+v", d)
	}

	// The inner tool's own configured deny rule applies too.
	innerDeny := map[string]Rule{"aws": {Tool: "aws", Profiled: true, Deny: []string{"s3 rb *"}}}
	d = decideVop(rule, innerDeny, "exec", "tap", "--", "aws", "s3", "rb", "s3://bucket")
	if d.Allow || !strings.Contains(d.Reason, "denied by aws.deny") {
		t.Fatalf("the inner tool's own deny rule must apply: %+v", d)
	}

	// An ordinary read reaches the inner tool fine.
	d = decideVop(rule, nil, "exec", "tap", "--", "aws", "s3", "ls")
	if !d.Allow {
		t.Fatalf("an ordinary aws read under vop must be allowed: %+v", d)
	}
	if d.Confine {
		t.Fatalf("a non-content-executing inner command must not run confined: %+v", d)
	}
}

func TestVopExecRefusesAContentExecutingInnerCommand(t *testing.T) {
	rule := Rule{Tool: "vop", Profiled: true, OperatorAllow: []string{"exec tap *"}, Allow: [][]string{{"exec tap *"}}}
	// Even when the operator's own terraform config would let `apply` run
	// confined as a direct host command, it must not run at all under vop:
	// vop's confined-run support does not exist in this version.
	innerAllowed := map[string]Rule{"terraform": {
		Tool: "terraform", Profiled: true,
		OperatorAllow: []string{"apply *"}, Allow: [][]string{{"apply *"}},
	}}
	// Sanity: run directly (no vop), terraform apply there DOES run confined.
	direct := Decide(Request{Tool: "terraform", Args: []string{"apply", "plan.tfplan"}}, innerAllowed["terraform"], testCtx())
	if !direct.Allow || !direct.Confine {
		t.Fatalf("sanity: a directly-allowed terraform apply should run confined: %+v", direct)
	}
	d := decideVop(rule, innerAllowed, "exec", "tap", "--", "terraform", "apply", "plan.tfplan")
	if d.Allow {
		t.Fatalf("a content-executing inner command must be refused under vop, not run confined: %+v", d)
	}
	if !strings.Contains(d.Reason, "confined runs are not supported under vop") {
		t.Errorf("reason should explain why: %q", d.Reason)
	}

	// An unallowed content-executing inner command is refused too (by the
	// inner tool's own content-exec gate, same as running it directly).
	d = decideVop(rule, nil, "exec", "tap", "--", "terraform", "apply", "plan.tfplan")
	if d.Allow {
		t.Fatalf("an unallowed content-executing inner command must be refused: %+v", d)
	}
}

func TestVopHomeViewIsItsOwnConfigAndOnePassword(t *testing.T) {
	paths, persist, _, full := HomeView("vop", nil, nil)
	if full {
		t.Fatal("vop is profiled: it must not see the whole home")
	}
	for _, want := range []string{".config/vop", ".config/op"} {
		if !contains(paths, want) {
			t.Errorf("vop's view must include %s: %v", want, paths)
		}
	}
	if !contains(persist, ".config/vop") {
		t.Errorf("vop's own state (its rate-limit cooldown bookkeeping) must persist: %v", persist)
	}
}

// Per-member pinning, read-only by default: a member's runtime names the vop
// profile(s) it may use; under it, the inner command may read, and writes
// only what the operator's own allow list for that tool names literally.
func TestVopPinnedAndReadOnlyByDefault(t *testing.T) {
	orgA := Rule{Tool: "vop", Profiled: true, Allow: [][]string{{"exec orgA *"}}, OperatorAllow: []string{"exec orgA *"}}
	if d := decideVop(orgA, nil, "exec", "orgB", "--", "aws", "sts", "get-caller-identity"); d.Allow {
		t.Fatal("a profile the member's config does not pin is refused")
	}
	for _, read := range [][]string{
		{"aws", "sts", "get-caller-identity"}, {"aws", "s3", "ls"}, {"aws", "ec2", "describe-instances"},
		{"terraform", "version"}, {"terraform", "state", "list"}, {"kubectl", "get", "pods"},
	} {
		args := append([]string{"exec", "orgA", "--"}, read...)
		if d := decideVop(orgA, nil, args...); !d.Allow {
			t.Errorf("a read under the pinned profile runs: %v: %s", read, d.Reason)
		}
	}
	for _, write := range [][]string{
		{"aws", "s3", "cp", "a", "s3://b/a"}, {"aws", "s3", "rm", "s3://b/a"}, {"aws", "ec2", "terminate-instances", "--instance-ids", "i-1"},
		{"terraform", "state", "rm", "x"}, {"kubectl", "delete", "pod", "p"},
	} {
		args := append([]string{"exec", "orgA", "--"}, write...)
		d := decideVop(orgA, nil, args...)
		if d.Allow || !strings.Contains(d.Reason, "is a write") {
			t.Errorf("a write is refused by default: %v: allow=%v %s", write, d.Allow, d.Reason)
		}
	}
	// terraform apply executes workspace content: refused under vop
	// whatever the allow lists say (confined runs under vop are unsupported).
	if d := decideVop(orgA, nil, "exec", "orgA", "--", "terraform", "apply", "plan.tfplan"); d.Allow {
		t.Fatal("terraform apply under vop is refused")
	}
	// The operator names the write on the inner tool: it runs. A blanket
	// "*" on the inner tool does not open writes.
	blanket := map[string]Rule{"aws": {Tool: "aws", Profiled: true, Allow: [][]string{{"*"}}, OperatorAllow: []string{"*"}}}
	if d := decideVop(orgA, blanket, "exec", "orgA", "--", "aws", "s3", "cp", "a", "s3://b/a"); d.Allow {
		t.Fatal("a blanket inner allow does not open writes")
	}
	named := map[string]Rule{"aws": {Tool: "aws", Profiled: true, Allow: [][]string{{"*", "s3 cp *"}}, OperatorAllow: []string{"*", "s3 cp *"}}}
	if d := decideVop(orgA, named, "exec", "orgA", "--", "aws", "s3", "cp", "a", "s3://b/a"); !d.Allow {
		t.Fatalf("a write the operator names runs: %s", d.Reason)
	}
}
