package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/skill"
)

// cliSkillSetup publishes a daemon socket and an active broker, as boot does.
func cliSkillSetup(t *testing.T) {
	t.Helper()
	memory.Reset()
	t.Cleanup(memory.Reset)
	memory.SetToolCommand([]string{"/usr/local/bin/conductor", "mcp", "memory", "--socket", "/data/memory.sock", "--no-memory"})
	skill.SetActive(skill.NewBroker(func(string) (string, bool) { return "", false }, nil))
	t.Cleanup(func() { skill.SetActive(nil) })
}

func envValue(env []string, key string) (string, bool) {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, key+"="); ok {
			return v, true
		}
	}
	return "", false
}

// A cli-runtime agent is told to finish with `conductor call step.done`, so it
// must get the endpoint and a session token in its environment, as a paseo
// agent does. It used to get neither: the agent reported CONDUCTOR_ENDPOINT
// unset and the call never ran.
func TestCLIAgentGetsSkillEnv(t *testing.T) {
	cliSkillSetup(t)
	l := &fakeLauncher{}
	c := newCLIController("cx", config.ControllerConfig{Transport: "cli", Tool: "codex"}, nil)
	c.launch = l.launch

	sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("changes_requested", "address it"), Cwd: "/wt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)
	env := l.call(0).env
	if ep, _ := envValue(env, "CONDUCTOR_ENDPOINT"); ep != "unix:///data/memory.sock" {
		t.Fatalf("CONDUCTOR_ENDPOINT = %q, want the daemon socket; env: %v", ep, env)
	}
	if tok, _ := envValue(env, "CONDUCTOR_SKILL_TOKEN"); tok == "" {
		t.Fatalf("no CONDUCTOR_SKILL_TOKEN on a cli agent's env: %v", env)
	}
	if strings.Contains(strings.Join(l.call(0).argv, " "), "CONDUCTOR_SKILL_TOKEN") {
		t.Fatal("the token must ride the environment, never argv")
	}
}

// An agent-authored dispatch never holds creds, and a launch that cannot reach
// the socket (a remote host, a namespace sandbox masking the state dir) is not
// handed a token it cannot use.
func TestCLISkillEnvWithheld(t *testing.T) {
	cliSkillSetup(t)
	cases := map[string]func(*cliController, *Spec){
		"agent-authored": func(_ *cliController, s *Spec) { s.Request.AgentAuthored = true },
		"remote host":    func(c *cliController, _ *Spec) { c.host = "box" },
		"namespace":      func(c *cliController, _ *Spec) { c.iso = &config.IsolationConfig{Mode: "namespace"} },
		"user mode":      func(c *cliController, _ *Spec) { c.iso = &config.IsolationConfig{Mode: "user", User: "agents"} },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			c := newCLIController("cx", config.ControllerConfig{Transport: "cli", Tool: "codex"}, nil)
			spec := Spec{Request: makeReq("changes_requested", "address it"), Cwd: "/wt"}
			mut(c, &spec)
			opt := launchOptsFor(c.iso, spec.Request)
			reach := skillSocketReachable(c.host, opt)
			if spec.Request.AgentAuthored {
				// Reachable, but SkillEnv refuses to mint for it.
				if !reach {
					t.Fatal("a plain local launch can reach the socket")
				}
				if env := dispatch.LocalSkillEnv(spec.Request); len(env) != 0 {
					t.Fatalf("agent-authored dispatch got creds: %v", env)
				}
				return
			}
			if reach {
				t.Fatalf("%s: the socket is not reachable from this launch", name)
			}
		})
	}
	priv := launchOpts{iso: &config.IsolationConfig{Mode: "namespace", Privileged: true}}
	if !skillSocketReachable("", priv) {
		t.Fatal("a privileged namespace sandbox keeps the state dir visible")
	}
}
