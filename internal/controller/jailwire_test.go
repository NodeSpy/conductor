package controller

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/jail"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

func TestScrubJailEnvDropsCredentials(t *testing.T) {
	env := []string{
		"PATH=/usr/bin", "HOME=/home/op", "LANG=C.UTF-8",
		"GH_TOKEN=x", "GITHUB_TOKEN=x", "PC_GH_WRITE_TOKEN=x", "PC_GH_APP_TOKEN=x",
		"SSH_AUTH_SOCK=/run/agent", "AWS_ACCESS_KEY_ID=x", "AWS_SECRET_ACCESS_KEY=x", "AWS_PROFILE=prod",
		"KUBECONFIG=/k", "GOOGLE_APPLICATION_CREDENTIALS=/g", "VAULT_TOKEN=x", "NPM_TOKEN=x",
		"MY_SERVICE_PASSWORD=x", "SLACK_BOT_TOKEN=x", "CONDUCTOR_SKILL_TOKEN=x", "PC_WEBHOOK_SECRET=x",
		"OPENAI_API_KEY=keep-only-for-codex", "DOCKER_HOST=tcp://x", "XDG_RUNTIME_DIR=/run/user/1000",
		"GIT_AUTHOR_NAME=Op", "GIT_AUTHOR_EMAIL=op@x", "GIT_COMMITTER_NAME=Op", "GIT_COMMITTER_EMAIL=op@x",
		"ANTHROPIC_API_KEY=the-model-credential", "CLAUDE_CODE_OAUTH_TOKEN=t", "GOPATH=/go",
	}
	got := strings.Join(scrubJailEnv(env, "claude-code"), "\n")
	for _, gone := range []string{"GH_TOKEN", "GITHUB_TOKEN", "PC_GH_WRITE_TOKEN", "PC_GH_APP_TOKEN", "SSH_AUTH_SOCK",
		"AWS_", "KUBECONFIG", "GOOGLE_APPLICATION", "VAULT_TOKEN", "NPM_TOKEN", "PASSWORD", "SLACK_BOT_TOKEN",
		"CONDUCTOR_SKILL_TOKEN", "PC_WEBHOOK_SECRET", "OPENAI_API_KEY", "DOCKER_HOST", "XDG_RUNTIME_DIR"} {
		if strings.Contains(got, gone) {
			t.Errorf("%s must not reach the jail:\n%s", gone, got)
		}
	}
	for _, kept := range []string{"PATH=", "HOME=", "LANG=", "GIT_AUTHOR_NAME=Op", "GIT_AUTHOR_EMAIL=op@x",
		"GIT_COMMITTER_NAME=Op", "ANTHROPIC_API_KEY=", "CLAUDE_CODE_OAUTH_TOKEN=", "GOPATH="} {
		if !strings.Contains(got, kept) {
			t.Errorf("%s must be kept:\n%s", kept, got)
		}
	}
	// codex keeps its own model credential, not claude's.
	cx := strings.Join(scrubJailEnv(env, "codex"), "\n")
	if !strings.Contains(cx, "OPENAI_API_KEY=") || strings.Contains(cx, "ANTHROPIC_API_KEY") {
		t.Fatalf("codex model credential: %s", cx)
	}
	// claude-code on Bedrock: the AWS credentials ARE its model credential.
	bed := strings.Join(scrubJailEnv(append(env, "CLAUDE_CODE_USE_BEDROCK=1"), "claude-code"), "\n")
	if !strings.Contains(bed, "AWS_SECRET_ACCESS_KEY") {
		t.Fatal("bedrock needs its AWS credentials")
	}
}

func TestJailNetworkModes(t *testing.T) {
	model := []string{"api.anthropic.com"}
	cases := []struct {
		name     string
		n        *config.IsolationNetwork
		authored bool
		deny     bool
		egress   string
	}{
		{"absent = open", nil, false, false, ""},
		{"open", &config.IsolationNetwork{Mode: config.NetOpen}, false, false, ""},
		{"audit = everything via the proxy", &config.IsolationNetwork{Mode: config.NetAudit}, false, true, "*"},
		{"deny = the model endpoint only", &config.IsolationNetwork{Mode: config.NetDeny}, false, true, "api.anthropic.com"},
		{"egress list + model, enforced", &config.IsolationNetwork{Egress: []string{"proxy.golang.org"}}, false, true, "proxy.golang.org,api.anthropic.com"},
		{"legacy full cut", &config.IsolationNetwork{Deny: true}, false, true, ""},
		{"agent-authored default = deny", nil, true, true, "api.anthropic.com"},
	}
	for _, tc := range cases {
		s := &sandbox.Spec{Mode: "namespace"}
		jailNetwork(s, tc.n, model, tc.authored)
		if s.Deny != tc.deny || strings.Join(s.Egress, ",") != tc.egress {
			t.Errorf("%s: deny=%v egress=%v", tc.name, s.Deny, s.Egress)
		}
		if tc.egress != "" && !s.EnforcedEgress() {
			t.Errorf("%s: an allowlist in the jail must be ENFORCED", tc.name)
		}
	}
}

func TestAgentLaunchOptsSynthesizesTheJail(t *testing.T) {
	old := JailManager
	JailManager = &jail.Manager{}
	defer func() { JailManager = old }()
	req := dispatch.Request{Trigger: core.Trigger{TargetTrusted: true, Target: core.Target{Repo: "acme/app", PR: 42}, Context: map[string]any{"head_ref": "fix/42"}}}

	opt := agentLaunchOpts(true, "claude-code", nil, req)
	if opt.jail == nil || !opt.jailDefault {
		t.Fatalf("no isolation: → the synthesized (defaulted) jail: %+v", opt)
	}
	if opt.jail.Repo != "acme/app" || opt.jail.Number != 42 || opt.jail.HeadBranch != "fix/42" || opt.jail.ReadOnly {
		t.Fatalf("jail spec: %+v", opt.jail)
	}
	// An explicit block fails closed (not defaulted).
	opt = agentLaunchOpts(true, "claude-code", &config.IsolationConfig{FS: []string{"/opt/x"}}, req)
	if opt.jail == nil || opt.jailDefault {
		t.Fatalf("explicit block → jail, fail closed: %+v", opt)
	}
	// mode: none opts out entirely.
	if opt = agentLaunchOpts(true, "claude-code", &config.IsolationConfig{Mode: "none"}, req); opt.jail != nil || opt.iso != nil {
		t.Fatalf("mode none: %+v", opt)
	}
	// privileged namespace keeps the older full-view wrapper.
	if opt = agentLaunchOpts(true, "claude-code", &config.IsolationConfig{Mode: "namespace", Privileged: true}, req); opt.jail != nil {
		t.Fatal("privileged: no jail")
	}
	// Not eligible (paseo/agent-deck/opencode/host): no jail.
	if opt = agentLaunchOpts(false, "claude-code", nil, req); opt.jail != nil {
		t.Fatal("ineligible runtime: no jail")
	}
	// A sender-chosen (untrusted) target binds nothing.
	req.Trigger.TargetTrusted = false
	if opt = agentLaunchOpts(true, "claude-code", nil, req); opt.jail.Repo != "" {
		t.Fatalf("untrusted target must bind no repo: %q", opt.jail.Repo)
	}
}

func TestEffectiveWritesRoles(t *testing.T) {
	fix := dispatch.Request{Trigger: core.Trigger{Kind: "merge_conflict"}}
	if ro, _ := dispatch.EffectiveWrites(fix, nil); ro {
		t.Fatal("a fixer writes its own target by default")
	}
	for name, req := range map[string]dispatch.Request{
		"output schema":  {Step: config.Step{OutputSchema: map[string]any{"type": "object"}}},
		"checkout none":  {Step: config.Step{Checkout: "none"}},
		"review trigger": {Trigger: core.Trigger{Kind: "review_requested"}},
		"decide":         {Step: config.Step{DecisionLaunch: &config.DecisionLaunch{}}},
	} {
		if ro, _ := dispatch.EffectiveWrites(req, nil); !ro {
			t.Errorf("%s: a review step is read-only by default", name)
		}
	}
	// expect_push flips a review-shaped step to a writer.
	if ro, _ := dispatch.EffectiveWrites(dispatch.Request{Step: config.Step{Checkout: "none", ExpectPush: true}}, nil); ro {
		t.Fatal("expect_push is a writer")
	}
	// writes: target opts a review-shaped step in; writes: read_only a fixer out.
	st := dispatch.Request{Step: config.Step{OutputSchema: map[string]any{}, Isolation: &config.IsolationConfig{Writes: &config.WritesPolicy{Target: true}}}}
	if ro, _ := dispatch.EffectiveWrites(st, nil); ro {
		t.Fatal("writes: target")
	}
	st = dispatch.Request{Step: config.Step{Isolation: &config.IsolationConfig{Writes: &config.WritesPolicy{ReadOnly: true}}}}
	if ro, _ := dispatch.EffectiveWrites(st, nil); !ro {
		t.Fatal("writes: read_only")
	}
	// A pack step cannot widen past the operator's own block.
	pack := dispatch.Request{Step: config.Step{FromPack: true, Isolation: &config.IsolationConfig{Writes: &config.WritesPolicy{CreatePR: true}}}}
	if p := dispatch.WritePolicyFor(pack, nil); p.CreatePR {
		t.Fatal("a pack cannot open PRs unless the operator allows it")
	}
	if p := dispatch.WritePolicyFor(pack, &config.IsolationConfig{Writes: &config.WritesPolicy{CreatePR: true}}); !p.CreatePR {
		t.Fatal("…but can when the operator's runtime block allows it")
	}
	own := dispatch.Request{Step: config.Step{Isolation: &config.IsolationConfig{Writes: &config.WritesPolicy{CreatePR: true}}}}
	if p := dispatch.WritePolicyFor(own, nil); !p.CreatePR {
		t.Fatal("an operator's own step may widen")
	}
}

func TestClaudeStreamAndCodexJailArgs(t *testing.T) {
	got := strings.Join(claudeStream([]string{"claude", "-p", "--output-format", "json", "--dangerously-skip-permissions"}), " ")
	if got != "claude -p --output-format stream-json --dangerously-skip-permissions --verbose" {
		t.Fatalf("stream argv: %s", got)
	}
	cx := strings.Join(insertAfter([]string{"codex", "exec", "-"}, 1, codexJailArgs(true)), " ")
	if cx != `codex exec -c approval_policy="never" --sandbox read-only -` {
		t.Fatalf("codex review argv: %s", cx)
	}
	if !strings.Contains(strings.Join(codexJailArgs(false), " "), "workspace-write") {
		t.Fatal("codex fixer sandbox")
	}
}

func TestStreamCaptureKeepsOnlyTheResult(t *testing.T) {
	var c streamCapture
	c.noise.max = 1 << 10
	big := strings.Repeat(`{"type":"assistant","message":"`+strings.Repeat("x", 1000)+`"}`+"\n", 3000)
	_, _ = c.Write([]byte("[claude-code:warn] something on stderr\n"))
	_, _ = c.Write([]byte(big))
	_, _ = c.Write([]byte(`{"type":"result","subtype":"success","result":"the answer","session_id":"s1"}`))
	out := c.String()
	if parseClaudeResult(out) != "the answer" || parseClaudeSessionID(out) != "s1" {
		t.Fatalf("the result survives a 3 MB transcript: %q", out[:min(200, len(out))])
	}
	if len(out) > 4096 {
		t.Fatalf("the transcript is not kept in memory: %d bytes", len(out))
	}
}

func TestModelRouteLoopbackRelay(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:3456")
	hosts, loop := modelRoute("claude-code", t.TempDir())
	if len(loop) != 1 || loop[0] != "127.0.0.1:3456" {
		t.Fatalf("loopback endpoint must be relayed: %v", loop)
	}
	if !strings.Contains(strings.Join(hosts, ","), "api.anthropic.com") {
		t.Fatalf("model hosts: %v", hosts)
	}
	t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.example.test")
	hosts, loop = modelRoute("claude-code", t.TempDir())
	if len(loop) != 0 || !strings.Contains(strings.Join(hosts, ","), "gateway.example.test:443") {
		t.Fatalf("a remote endpoint joins the allowlist: %v %v", hosts, loop)
	}
}

func TestPolicyOnlyBlocksDoNotShapeTheSandbox(t *testing.T) {
	old := JailManager
	JailManager = &jail.Manager{}
	defer func() { JailManager = old }()
	ro := &config.IsolationConfig{Writes: &config.WritesPolicy{ReadOnly: true}}
	req := dispatch.Request{Step: config.Step{Isolation: ro}, Trigger: core.Trigger{TargetTrusted: true, Target: core.Target{Repo: "acme/app", PR: 1}}}
	// On a default runtime: still the synthesized (degradable) jail, read-only.
	opt := agentLaunchOpts(true, "claude-code", nil, req)
	if opt.jail == nil || !opt.jailDefault || !opt.jail.ReadOnly {
		t.Fatalf("policy-only step on a default runtime: %+v", opt)
	}
	// Under a runtime that opted out, a policy-only step does not re-jail it.
	if opt = agentLaunchOpts(true, "claude-code", &config.IsolationConfig{Mode: "none"}, req); opt.jail != nil {
		t.Fatal("a policy-only step must not override the runtime's mode: none")
	}
	// Under an explicit runtime block, that block still governs (fail closed).
	if opt = agentLaunchOpts(true, "claude-code", &config.IsolationConfig{FS: []string{"/x"}}, req); opt.jail == nil || opt.jailDefault {
		t.Fatalf("explicit runtime block governs: %+v", opt)
	}
	// A non-jail runtime (opencode/agent-deck) is not wrapped by a policy-only step.
	if o := launchOptsFor(nil, req); o.iso != nil {
		t.Fatalf("policy-only step must not wrap a non-jail runtime: %+v", o.iso)
	}
}
