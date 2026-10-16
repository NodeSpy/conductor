package bot

import (
	"context"
	"errors"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

type fakePaseo struct {
	reqs     []dispatch.Request
	archived []string
	released []string
	fail     error
	status   string
}

func (f *fakePaseo) Dispatch(_ context.Context, r dispatch.Request) (dispatch.RunRef, error) {
	f.reqs = append(f.reqs, r)
	if f.fail != nil {
		return dispatch.RunRef{}, f.fail
	}
	return dispatch.RunRef{AgentID: "pa-1"}, nil
}
func (f *fakePaseo) CreateLocalWorkspace(context.Context, string, string) (string, error) {
	return "ws-1", nil
}
func (f *fakePaseo) AgentStatus(context.Context, string) (string, error) {
	if f.status != "" {
		return f.status, nil
	}
	return "idle", nil
}
func (f *fakePaseo) Send(context.Context, string, string) error { return nil }
func (f *fakePaseo) ReleaseWorkspace(_ context.Context, id string) error {
	f.released = append(f.released, id)
	return nil
}
func (f *fakePaseo) Archive(_ context.Context, id string) error {
	f.archived = append(f.archived, id)
	return nil
}

type recJail struct {
	active bool
}

func (j *recJail) Active() bool { return j.active }
func (j *recJail) PaseoProvider(base string) (string, bool) {
	if base == "" {
		return "", false
	}
	return base + "-sandboxed", true
}

func TestPaseoHomeLaunchAndJailSwitch(t *testing.T) {
	cfg := &config.Config{Bots: map[string]config.BotConfig{"sre": {
		Skill: &config.SkillPolicy{Verbs: []string{"gh.comment"}},
		Home:  config.BotHome{Mode: "plan"},
	}}}
	fp := &fakePaseo{}
	jail := &recJail{}
	var held []string
	var resolved config.Step
	h := &PaseoHome{Cfg: func() *config.Config { return cfg }, For: func(string) (PaseoDispatcher, error) { return fp, nil },
		Resolve: func(_ context.Context, s config.Step) (string, string, string) {
			resolved = s
			return "opus", "paseo", "claude"
		},
		Hold: func(id string) { held = append(held, id) }, Sandbox: jail}
	id, err := h.Launch(context.Background(), HomeSpec{Bot: "sre", Session: "s1", Dispatch: "d1", Cwd: "/x/s1", Prompt: "{{.secrets}} literal"})
	if err != nil || id != "pa-1" || len(held) != 1 {
		t.Fatalf("launch: %v %q held=%v", err, id, held)
	}
	r := fp.reqs[0]
	if !r.LiteralPrompt || r.Action.Prompt != "{{.secrets}} literal" || r.Workspace != "ws-1" || r.DispatchID != "d1" {
		t.Fatalf("request: %+v", r)
	}
	if r.Step.Mode != "plan" {
		t.Fatalf("home.mode not passed: %q", r.Step.Mode)
	}
	if r.Step.BoundBot != "sre" || r.Step.BoundBotRole != config.BotRoleHome || r.Action.Checkout != "none" {
		t.Fatalf("binding: %+v", r.Step)
	}
	if r.Provider != "claude" || r.Model != "opus" || resolved.BoundBot != "sre" {
		t.Fatalf("home model must come from the resolver: %q %q (resolved %+v)", r.Provider, r.Model, resolved)
	}
	// With the sandbox active, the home session launches on the sandboxed
	// provider entry for its base provider (claude → claude-sandboxed), the
	// model pin unchanged.
	jail.active = true
	_, _ = h.Launch(context.Background(), HomeSpec{Bot: "sre", Session: "s2", Dispatch: "d2", Cwd: "/x/s2"})
	if r := fp.reqs[1]; r.Provider != "claude-sandboxed" || r.Model != "opus" {
		t.Fatalf("sandboxed launch: provider %q model %q", r.Provider, r.Model)
	}
	if err := h.Retire(context.Background(), "sre", "pa-1", "d1", "/x/s1"); err != nil {
		t.Fatal(err)
	}
	if len(fp.archived) != 1 {
		t.Fatalf("retire: paseo %v", fp.archived)
	}
}

// A failed launch leaves no workspace behind.
func TestPaseoHomeFailedLaunchReleasesWorkspace(t *testing.T) {
	cfg := &config.Config{Bots: map[string]config.BotConfig{"sre": {}}}
	fp := &fakePaseo{fail: errors.New("no model")}
	h := &PaseoHome{Cfg: func() *config.Config { return cfg }, For: func(string) (PaseoDispatcher, error) { return fp, nil }}
	if _, err := h.Launch(context.Background(), HomeSpec{Bot: "sre", Session: "s1", Dispatch: "d1", Cwd: "/x"}); err == nil {
		t.Fatal("launch error swallowed")
	}
	if len(fp.released) != 1 || fp.released[0] != "ws-1" {
		t.Fatalf("workspace not released: %v", fp.released)
	}
}

// A closed paseo agent (its process ended, e.g. across a paseo daemon
// restart) would resume on send without its launch env — so without its
// conductor endpoint and token. The home reports it gone, and the bot
// rotates to a fresh session.
func TestPaseoHomeClosedAgentIsGone(t *testing.T) {
	cfg := &config.Config{Bots: map[string]config.BotConfig{"sre": {}}}
	fp := &fakePaseo{}
	h := &PaseoHome{Cfg: func() *config.Config { return cfg }, For: func(string) (PaseoDispatcher, error) { return fp, nil }}
	for _, c := range []struct{ paseo, want string }{{"idle", "idle"}, {"running", "running"}, {"closed", "gone"}} {
		fp.status = c.paseo
		if st, err := h.State(context.Background(), "sre", "pa-1"); err != nil || st != c.want {
			t.Fatalf("paseo %q: state %q %v, want %q", c.paseo, st, err, c.want)
		}
	}
}
