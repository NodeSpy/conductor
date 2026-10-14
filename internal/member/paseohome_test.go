package member

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
func (f *fakePaseo) AgentStatus(context.Context, string) (string, error) { return "idle", nil }
func (f *fakePaseo) Send(context.Context, string, string) error          { return nil }
func (f *fakePaseo) ReleaseWorkspace(_ context.Context, id string) error {
	f.released = append(f.released, id)
	return nil
}
func (f *fakePaseo) Archive(_ context.Context, id string) error {
	f.archived = append(f.archived, id)
	return nil
}

type recJail struct {
	active   bool
	archived []string
}

func (j *recJail) Active() bool { return j.active }
func (j *recJail) ArchiveCwd(cwd string) int {
	j.archived = append(j.archived, cwd)
	return 1
}

func TestPaseoHomeLaunchAndJailSwitch(t *testing.T) {
	cfg := &config.Config{Members: map[string]config.MemberConfig{"sre": {
		Skill: &config.SkillPolicy{Verbs: []string{"gh.comment"}},
		Home:  config.MemberHome{Mode: "plan"},
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
		Hold: func(id string) { held = append(held, id) }, Jail: jail}
	id, err := h.Launch(context.Background(), HomeSpec{Member: "sre", Session: "s1", Dispatch: "d1", Cwd: "/x/s1", Prompt: "{{.secrets}} literal"})
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
	if r.Step.BoundMember != "sre" || r.Step.BoundMemberRole != config.MemberRoleHome || r.Action.Checkout != "none" {
		t.Fatalf("binding: %+v", r.Step)
	}
	if r.Provider != "claude" || r.Model != "opus" || resolved.BoundMember != "sre" {
		t.Fatalf("home model must come from the resolver: %q %q (resolved %+v)", r.Provider, r.Model, resolved)
	}
	jail.active = true
	_, _ = h.Launch(context.Background(), HomeSpec{Member: "sre", Session: "s2", Dispatch: "d2", Cwd: "/x/s2"})
	if p := fp.reqs[1].Provider; p != JailedProvider {
		t.Fatalf("jailed launch provider = %q, want %q", p, JailedProvider)
	}
	if err := h.Retire(context.Background(), "sre", "pa-1", "d1", "/x/s1"); err != nil {
		t.Fatal(err)
	}
	if len(jail.archived) != 1 || jail.archived[0] != "/x/s1" || len(fp.archived) != 1 {
		t.Fatalf("retire: jail %v paseo %v", jail.archived, fp.archived)
	}
}

// A failed launch leaves no workspace behind.
func TestPaseoHomeFailedLaunchReleasesWorkspace(t *testing.T) {
	cfg := &config.Config{Members: map[string]config.MemberConfig{"sre": {}}}
	fp := &fakePaseo{fail: errors.New("no model")}
	h := &PaseoHome{Cfg: func() *config.Config { return cfg }, For: func(string) (PaseoDispatcher, error) { return fp, nil }}
	if _, err := h.Launch(context.Background(), HomeSpec{Member: "sre", Session: "s1", Dispatch: "d1", Cwd: "/x"}); err == nil {
		t.Fatal("launch error swallowed")
	}
	if len(fp.released) != 1 || fp.released[0] != "ws-1" {
		t.Fatalf("workspace not released: %v", fp.released)
	}
}
