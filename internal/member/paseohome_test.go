package member

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

type fakePaseo struct {
	reqs     []dispatch.Request
	archived []string
}

func (f *fakePaseo) Dispatch(_ context.Context, r dispatch.Request) (dispatch.RunRef, error) {
	f.reqs = append(f.reqs, r)
	return dispatch.RunRef{AgentID: "pa-1"}, nil
}
func (f *fakePaseo) CreateLocalWorkspace(context.Context, string, string) (string, error) {
	return "ws-1", nil
}
func (f *fakePaseo) AgentStatus(context.Context, string) (string, error) { return "idle", nil }
func (f *fakePaseo) Send(context.Context, string, string) error          { return nil }
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
		Home:  config.MemberHome{Provider: "claude", Model: "opus"},
	}}}
	fp := &fakePaseo{}
	jail := &recJail{}
	var held []string
	h := &PaseoHome{Cfg: func() *config.Config { return cfg }, For: func(string) (PaseoDispatcher, error) { return fp, nil },
		Hold: func(id string) { held = append(held, id) }, Jail: jail}
	id, err := h.Launch(context.Background(), HomeSpec{Member: "sre", Session: "s1", Dispatch: "d1", Cwd: "/x/s1", Prompt: "{{.secrets}} literal"})
	if err != nil || id != "pa-1" || len(held) != 1 {
		t.Fatalf("launch: %v %q held=%v", err, id, held)
	}
	r := fp.reqs[0]
	if !r.LiteralPrompt || r.Action.Prompt != "{{.secrets}} literal" || r.Workspace != "ws-1" || r.DispatchID != "d1" {
		t.Fatalf("request: %+v", r)
	}
	if r.Step.BoundMember != "sre" || r.Step.BoundMemberRole != config.MemberRoleHome || r.Action.Checkout != "none" {
		t.Fatalf("binding: %+v", r.Step)
	}
	if r.Provider != "claude" || r.Model != "opus" {
		t.Fatalf("unjailed launch must keep the configured provider: %q %q", r.Provider, r.Model)
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
