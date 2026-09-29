package hostcmd

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// operatorAllows is tool's rule when the operator's global config allows pats.
func operatorAllows(tool string, pats ...string) Rule {
	return Resolve(tool, []*config.IsolationConfig{{Host: map[string]*config.HostCommand{tool: {Allow: pats}}}}, false)
}

// Every profile's content-executing subcommands are refused by default,
// with the reason and the exact knob that allows them; the tools' safe
// subcommands stay available.
func TestContentExecutingSubcommandsAreRefusedByDefault(t *testing.T) {
	ctx := testCtx()
	ctx.LocalImage = func(ref string) bool { return ref == "app:dev" }
	refused := []struct {
		argv []string
		knob string
	}{
		{[]string{"terraform", "init"}, "init *"}, {[]string{"terraform", "plan"}, "plan *"},
		{[]string{"terraform", "apply", "-auto-approve"}, "apply *"}, {[]string{"terraform", "destroy"}, "destroy *"},
		{[]string{"terraform", "console"}, "console *"}, {[]string{"terraform", "import", "a.b", "id"}, "import *"},
		{[]string{"terraform", "refresh"}, "refresh *"}, {[]string{"terraform", "test"}, "test *"},
		{[]string{"terraform", "-chdir=infra", "plan", "-out=tf.plan"}, "plan *"},
		{[]string{"terraform", "show"}, "show *"}, {[]string{"terraform", "validate"}, "validate *"},
		{[]string{"terraform", "get"}, "get *"}, {[]string{"terraform", "providers", "schema", "-json"}, "providers schema *"},
		{[]string{"docker", "build", "-t", "app:dev", "."}, "build *"}, {[]string{"docker", "buildx", "build", "."}, "buildx build *"},
		{[]string{"docker", "image", "build", "."}, "image build *"}, {[]string{"docker", "buildx", "bake"}, "buildx bake *"},
		{[]string{"docker", "run", "--rm", "app:dev"}, "run *"}, {[]string{"docker", "container", "run", "app:dev", "sh"}, "container run *"},
		{[]string{"kubectl", "apply", "-f", "k8s/"}, "apply *"}, {[]string{"kubectl", "-n", "x", "create", "--filename=a.yaml"}, "create *"},
		{[]string{"kubectl", "replace", "-f", "a.yaml"}, "replace *"}, {[]string{"kubectl", "apply", "-k", "overlays/prod"}, "apply *"},
		{[]string{"kubectl", "kustomize", "overlays/prod"}, "kustomize *"},
		{[]string{"aws", "cloudformation", "deploy", "--template-file", "t.yaml", "--stack-name", "s"}, "cloudformation deploy *"},
		{[]string{"aws", "--region", "eu-west-1", "cloudformation", "create-stack", "--stack-name", "s"}, "cloudformation create-stack *"},
		{[]string{"aws", "lambda", "update-function-code", "--function-name", "f", "--zip-file", "fileb://f.zip"}, "lambda update-function-code *"},
		{[]string{"gcloud", "builds", "submit", "."}, "builds submit *"}, {[]string{"az", "deployment", "group", "create"}, "deployment *"},
		{[]string{"npm", "publish"}, "publish *"}, {[]string{"pnpm", "publish", "--access", "public"}, "publish *"},
	}
	for _, c := range refused {
		d := Decide(Request{Tool: c.argv[0], Args: c.argv[1:], Cwd: "/state/worktrees/d1"},
			Rule{Tool: c.argv[0], Profiled: true}, ctx, ownTarget)
		if d.Allow || d.Confine {
			t.Errorf("%v: want refused by default, got allowed", c.argv)
			continue
		}
		want := "isolation.host." + c.argv[0] + `.allow: ["` + c.knob + `"]`
		if !strings.Contains(d.Reason, "executes workspace content") || !strings.Contains(d.Reason, want) {
			t.Errorf("%v: the refusal must say why and name %s:\n%s", c.argv, want, d.Reason)
		}
	}
	safe := [][]string{
		{"terraform", "fmt", "-check"}, {"terraform", "version"}, {"terraform", "output", "-json"},
		{"terraform", "workspace", "list"}, {"docker", "ps"}, {"docker", "run", "--rm", "alpine:3", "true"},
		{"docker", "images"}, {"kubectl", "get", "pods"}, {"kubectl", "apply", "--help"},
		{"aws", "cloudformation", "describe-stacks"}, {"aws", "lambda", "list-functions"},
		{"gcloud", "builds", "list"}, {"az", "group", "list"}, {"npm", "whoami"},
	}
	for _, argv := range safe {
		d := Decide(Request{Tool: argv[0], Args: argv[1:], Cwd: "/state/worktrees/d1"},
			Rule{Tool: argv[0], Profiled: true}, ctx, ownTarget)
		if !d.Allow || d.Confine {
			t.Errorf("%v: a safe subcommand stays available unconfined, got allow=%v confine=%v %q", argv, d.Allow, d.Confine, d.Reason)
		}
	}
}

// Matching is on the parsed command, so flag order and spelling cannot
// dodge it; an explicit operator allow confines instead of refusing; a
// blanket `*` or a step's own allow is not an explicit allow.
func TestContentExecutingAllowIsExplicitAndParsed(t *testing.T) {
	allowed := operatorAllows("terraform", "plan *", "init *")
	for _, argv := range [][]string{
		{"plan"}, {"plan", "-out=x"}, {"-chdir=infra", "plan"}, {"plan", "-var", "a=b", "-lock=false"},
		{"-chdir=infra", "init", "-upgrade"},
	} {
		d := Decide(Request{Tool: "terraform", Args: argv, Cwd: "/state/worktrees/d1"}, allowed, testCtx(), ownTarget)
		if !d.Allow || !d.Confine {
			t.Errorf("terraform %v with plan/init allowed: want a confined run, got allow=%v confine=%v %q", argv, d.Allow, d.Confine, d.Reason)
		}
	}
	// apply is not in the operator's list: refused (by the content rule).
	if d := Decide(Request{Tool: "terraform", Args: []string{"apply"}}, allowed, testCtx(), ownTarget); d.Allow {
		t.Error("terraform apply was not allowed explicitly")
	}
	// A blanket `*` does not name a content-executing command.
	if d := Decide(Request{Tool: "terraform", Args: []string{"plan"}}, operatorAllows("terraform", "*"), testCtx(), ownTarget); d.Allow {
		t.Error("allow: [\"*\"] must not permit terraform plan")
	}
	// Only a step allows it: a step can narrow, never widen.
	step := Resolve("terraform", []*config.IsolationConfig{nil, nil, {Host: map[string]*config.HostCommand{"terraform": {Allow: []string{"plan *"}}}}}, true)
	if d := Decide(Request{Tool: "terraform", Args: []string{"plan"}}, step, testCtx(), ownTarget); d.Allow {
		t.Error("a step's own allow list must not permit a content-executing command")
	}
	// The operator allows it and the step narrows to the same: confined.
	both := Resolve("terraform", []*config.IsolationConfig{
		{Host: map[string]*config.HostCommand{"terraform": {Allow: []string{"plan *", "fmt *"}}}},
		{Host: map[string]*config.HostCommand{"terraform": {Allow: []string{"plan *"}}}},
	}, true)
	if d := Decide(Request{Tool: "terraform", Args: []string{"plan"}}, both, testCtx(), ownTarget); !d.Allow || !d.Confine {
		t.Errorf("operator allow + step narrowing: %+v", d)
	}
	// A deny still wins.
	deny := operatorAllows("terraform", "plan *")
	deny.Deny = []string{"plan -destroy"}
	if d := Decide(Request{Tool: "terraform", Args: []string{"plan", "-destroy"}}, deny, testCtx(), ownTarget); d.Allow {
		t.Error("a deny rule still applies to an allowed content-executing command")
	}
	// Pulled images run unconfined; only a locally built one is content.
	ctx := testCtx()
	ctx.LocalImage = func(ref string) bool { return false }
	if d := Decide(Request{Tool: "docker", Args: []string{"run", "--rm", "alpine:3"}}, Rule{Tool: "docker", Profiled: true}, ctx, ownTarget); !d.Allow || d.Confine {
		t.Errorf("a pulled image: %+v", d)
	}
}

func TestServiceEndpointsCoverEachContentTool(t *testing.T) {
	for _, tool := range []string{"terraform", "docker", "kubectl", "aws", "gcloud", "az", "npm", "pnpm"} {
		if len(ServiceEndpoints(tool)) == 0 {
			t.Errorf("%s has no default service endpoints for its confined runs", tool)
		}
	}
}
