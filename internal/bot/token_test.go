package bot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/skill"
)

// mintingPaseo is a paseo dispatcher that mints the session's REAL durable
// conductor token on launch — what SkillEnv does for a bot session — so a
// test can present it to the broker afterwards.
type mintingPaseo struct {
	fakePaseo
	b      *skill.Broker
	tokens map[string]string // dispatch id → token
}

func (m *mintingPaseo) Dispatch(ctx context.Context, r dispatch.Request) (dispatch.RunRef, error) {
	ref, err := m.fakePaseo.Dispatch(ctx, r)
	if err != nil {
		return ref, err
	}
	tok, err := m.b.MintHandoffSession(skill.Identity{Agent: "bot:sre", Dispatch: r.DispatchID, Bot: r.Step.BoundBot, BotRole: r.Step.BoundBotRole}, uint32(os.Getuid()))
	if err != nil {
		return ref, err
	}
	m.tokens[r.DispatchID] = tok
	ref.AgentID = "agent-" + r.DispatchID
	return ref, nil
}

func peer() skill.Peer { return skill.Peer{UID: uint32(os.Getuid()), Valid: true} }

// Rotation retires the old home session's token AT ONCE: right after
// rotating, the old token is refused and the new session's works.
func TestRotationRevokesTheRetiredSessionsToken(t *testing.T) {
	f := newSvc(t)
	b := skill.NewBroker(nil, nil)
	mp := &mintingPaseo{b: b, tokens: map[string]string{}}
	f.svc.Home = &PaseoHome{Cfg: f.svc.Cfg, For: func(string) (PaseoDispatcher, error) { return mp, nil }, Revoke: b.RevokeDispatch}
	ctx := context.Background()
	if err := f.svc.Rotate(ctx, "sre", "first"); err != nil {
		t.Fatal(err)
	}
	old := f.st.Bot("sre").Home
	oldTok := mp.tokens[old.Dispatch]
	if _, err := b.Authorize(oldTok, peer()); err != nil {
		t.Fatalf("the live session's token does not work: %v", err)
	}
	if err := f.svc.Rotate(ctx, "sre", "test"); err != nil {
		t.Fatal(err)
	}
	cur := f.st.Bot("sre").Home
	if cur.Dispatch == old.Dispatch {
		t.Fatal("no new session")
	}
	if _, err := b.Authorize(oldTok, peer()); err == nil {
		t.Fatal("the retired session's token still works after rotation")
	}
	id, err := b.Authorize(mp.tokens[cur.Dispatch], peer())
	if err != nil || id.Bot != "sre" || id.BotRole != config.BotRoleHome {
		t.Fatalf("the new session's token: %+v %v", id, err)
	}
}

// A finished worker's token is revoked at once on every way a worker
// finishes: its step.done, a run that ends without one, and a daemon restart
// that ended it.
func TestFinishedWorkerTokensAreRevoked(t *testing.T) {
	f := newSvc(t)
	b := skill.NewBroker(nil, nil)
	f.svc.RevokeToken = b.RevokeDispatch
	ctx := context.Background()
	f.svc.Tick(ctx, "sre")
	mint := func(w string) string {
		tok, err := b.MintHandoffSession(skill.Identity{Dispatch: w, Bot: "sre", BotRole: config.BotRoleWorker}, uint32(os.Getuid()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := b.Authorize(tok, peer()); err != nil {
			t.Fatal(err)
		}
		return tok
	}
	refused := func(tok, how string) {
		t.Helper()
		if _, err := b.Authorize(tok, peer()); err == nil {
			t.Errorf("%s: the finished worker's token still works", how)
		}
	}
	w1, _ := f.svc.Spawn(ctx, homeCaller(f), SpawnRequest{Brief: "a"})
	t1 := mint(w1.Dispatch)
	f.svc.WorkerDone(ctx, w1.Dispatch, "done", map[string]any{"summary": "ok"})
	refused(t1, "step.done")

	w2, _ := f.svc.Spawn(ctx, homeCaller(f), SpawnRequest{Brief: "b"})
	t2 := mint(w2.Dispatch)
	f.svc.OnRunEvent(ctx, "run_done", w2.HistoryID, "ok", "")
	refused(t2, "run ended without step.done")

	w3, _ := f.svc.Spawn(ctx, homeCaller(f), SpawnRequest{Brief: "c"})
	t3 := mint(w3.Dispatch)
	f.svc.recoverWorkers("sre")
	refused(t3, "daemon restart")

	// A live worker's token is untouched by another's finish.
	w4, _ := f.svc.Spawn(ctx, homeCaller(f), SpawnRequest{Brief: "d"})
	t4 := mint(w4.Dispatch)
	w5, _ := f.svc.Spawn(ctx, homeCaller(f), SpawnRequest{Brief: "e"})
	f.svc.WorkerDone(ctx, w5.Dispatch, "done", nil)
	if _, err := b.Authorize(t4, peer()); err != nil {
		t.Fatalf("a live worker lost its token when another finished: %v", err)
	}
}

// The home seam's pre-launch hook runs before anything is created, gets the
// session's own cwd, and aborts the launch on error — the place a
// per-session .claude/settings.json would be written.
func TestBeforeLaunchHook(t *testing.T) {
	cfg := &config.Config{Bots: map[string]config.BotConfig{"sre": {}}}
	fp := &fakePaseo{}
	cwd := t.TempDir()
	var seen HomeSpec
	h := &PaseoHome{Cfg: func() *config.Config { return cfg }, For: func(string) (PaseoDispatcher, error) { return fp, nil },
		BeforeLaunch: func(_ context.Context, s HomeSpec) error {
			seen = s
			return os.WriteFile(filepath.Join(s.Cwd, "prepared"), []byte("x"), 0o600)
		}}
	if _, err := h.Launch(context.Background(), HomeSpec{Bot: "sre", Session: "s1", Dispatch: "d1", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	if seen.Cwd != cwd {
		t.Fatalf("hook saw cwd %q", seen.Cwd)
	}
	if _, err := os.Stat(filepath.Join(cwd, "prepared")); err != nil {
		t.Fatal("the hook's file is not in the session cwd")
	}
	h.BeforeLaunch = func(context.Context, HomeSpec) error { return errors.New("nope") }
	n := len(fp.reqs)
	if _, err := h.Launch(context.Background(), HomeSpec{Bot: "sre", Session: "s2", Dispatch: "d2", Cwd: cwd}); err == nil {
		t.Fatal("a failing hook did not abort the launch")
	}
	if len(fp.reqs) != n || len(fp.released) != 0 {
		t.Fatal("a launch went ahead (or created a workspace) after its hook failed")
	}
}

// Under the sandbox wrapper, each launch's token is minted from the daemon's
// cwd lookup. Across a rotation: the retired session's cwd resolves to
// nothing (no new token can be minted for it), a token minted for it is
// revoked, and the new session's cwd resolves to the NEW dispatch, whose
// freshly minted token works.
func TestWrapperTokensAcrossRotation(t *testing.T) {
	f := newSvc(t)
	b := skill.NewBroker(nil, nil)
	mp := &mintingPaseo{b: b, tokens: map[string]string{}}
	f.svc.Home = &PaseoHome{Cfg: f.svc.Cfg, For: func(string) (PaseoDispatcher, error) { return mp, nil }, Revoke: b.RevokeDispatch}
	ctx := context.Background()
	if err := f.svc.Rotate(ctx, "sre", "first"); err != nil {
		t.Fatal(err)
	}
	old := f.st.Bot("sre").Home
	// What the wrapper's endpoint does for a launch in the session's cwd.
	wrapperMint := func(cwd string) (string, SessionBinding, bool) {
		sb, ok := f.svc.SessionForCwd(cwd)
		if !ok {
			return "", sb, false
		}
		tok, err := b.MintHandoffSession(skill.Identity{Dispatch: sb.Dispatch, Bot: sb.Bot, BotRole: sb.Role, CaseID: sb.CaseID}, uint32(os.Getuid()))
		if err != nil {
			t.Fatal(err)
		}
		return tok, sb, true
	}
	oldTok, sb, ok := wrapperMint(old.Cwd)
	if !ok || sb.Dispatch != old.Dispatch || sb.Role != config.BotRoleHome || sb.Identity != SessionIdentity("sre", old.ID) {
		t.Fatalf("live session binding: %+v %v", sb, ok)
	}
	if err := f.svc.Rotate(ctx, "sre", "test"); err != nil {
		t.Fatal(err)
	}
	cur := f.st.Bot("sre").Home
	if _, err := b.Authorize(oldTok, peer()); err == nil {
		t.Fatal("a wrapper-minted token for the retired session still works")
	}
	if _, _, ok := wrapperMint(old.Cwd); ok {
		t.Fatal("the retired session's cwd still resolves — a new token could be minted for it")
	}
	newTok, sb2, ok := wrapperMint(cur.Cwd)
	if !ok || sb2.Dispatch != cur.Dispatch || sb2.Dispatch == old.Dispatch {
		t.Fatalf("new session binding: %+v %v", sb2, ok)
	}
	if id, err := b.Authorize(newTok, peer()); err != nil || id.Dispatch != cur.Dispatch {
		t.Fatalf("the new session's fresh token: %+v %v", id, err)
	}
}
