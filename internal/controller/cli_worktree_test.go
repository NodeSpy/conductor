package controller

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// The leak this whole change exists to plug (docs/design/cli-git-worktrees.md):
// every cli dispatch got its own checkout and cliSession.Close never released
// it, so fixers and judges piled checkouts up for the daemon's whole life.
func TestCLICloseReleasesTheProvisionedWorktree(t *testing.T) {
	prov := &fakeProv{id: "/state/worktrees/disp-1", cwd: "/state/worktrees/disp-1"}
	l := &fakeLauncher{}
	c := newCLIController("cx", config.ControllerConfig{Transport: "cli", Tool: "codex"}, prov)
	c.launch = l.launch

	id, cwd, err := prov.ProvisionWorktree(context.Background(), makeReq("merge_conflict", "fix it"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := c.NewSession(context.Background(),
		Spec{Request: makeReq("merge_conflict", "fix it"), Cwd: cwd, WorkspaceID: id}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)

	if len(prov.removed) != 0 {
		t.Fatalf("worktree released before Close: %v", prov.removed)
	}
	if err := sess.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(prov.removed) != 1 || prov.removed[0] != id {
		t.Fatalf("Close released %v, want exactly [%s]", prov.removed, id)
	}
}

// Close after the session's own context is already cancelled (daemon shutdown)
// must still release — the teardown runs on a context of its own.
func TestCLICloseReleasesUnderACancelledContext(t *testing.T) {
	prov := &fakeProv{id: "/state/worktrees/disp-1", cwd: "/state/worktrees/disp-1"}
	l := &fakeLauncher{}
	c := newCLIController("cx", config.ControllerConfig{Transport: "cli", Tool: "codex"}, prov)
	c.launch = l.launch

	sess, err := c.NewSession(context.Background(),
		Spec{Request: makeReq("merge_conflict", "fix it"), Cwd: prov.cwd, WorkspaceID: prov.id}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sess.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(prov.removed) != 1 {
		t.Fatalf("cancelled-context Close released %v, want one removal", prov.removed)
	}
}

// A session opened on ANOTHER live session's checkout — the corrective
// output_schema turn, which controllerRunner.enforceSchema defer-Closes — must
// not tear the worktree down: the first session is still live, and the quality
// gate and proposed-diff read still have to happen in it.
func TestCLIReuseSessionDoesNotReleaseTheWorktree(t *testing.T) {
	prov := &fakeProv{id: "/state/worktrees/disp-1", cwd: "/state/worktrees/disp-1"}
	l := &fakeLauncher{}
	c := newCLIController("cx", config.ControllerConfig{Transport: "cli", Tool: "codex"}, prov)
	c.launch = l.launch

	sess, err := c.NewSession(context.Background(), Spec{
		Request: makeReq("merge_conflict", "fix it"), Cwd: prov.cwd,
		WorkspaceID: prov.id, ReuseWorkspace: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)
	if err := sess.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(prov.removed) != 0 {
		t.Fatalf("a borrowed checkout was released: %v", prov.removed)
	}
}

// Closing twice (Close, then the engine's Archive, then a reaper pass) must
// release exactly once.
func TestCLICloseReleasesOnlyOnce(t *testing.T) {
	prov := &fakeProv{id: "/state/worktrees/disp-1", cwd: "/state/worktrees/disp-1"}
	l := &fakeLauncher{}
	c := newCLIController("cx", config.ControllerConfig{Transport: "cli", Tool: "codex"}, prov)
	c.launch = l.launch

	sess, err := c.NewSession(context.Background(),
		Spec{Request: makeReq("merge_conflict", "fix it"), Cwd: prov.cwd, WorkspaceID: prov.id}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)
	for i := 0; i < 3; i++ {
		if err := sess.Close(context.Background()); err != nil {
			t.Fatalf("Close %d: %v", i, err)
		}
	}
	if len(prov.removed) != 1 {
		t.Fatalf("released %d times, want exactly once: %v", len(prov.removed), prov.removed)
	}
}

// A checkout-less run (checkout:none — the read-only judges) has nothing to
// release, so Close must not call the provisioner at all.
func TestCLICloseWithoutAWorktreeReleasesNothing(t *testing.T) {
	prov := &fakeProv{}
	l := &fakeLauncher{}
	c := newCLIController("cx", config.ControllerConfig{Transport: "cli", Tool: "codex"}, prov)
	c.launch = l.launch

	sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("cron", "triage")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)
	if err := sess.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(prov.removed) != 0 {
		t.Fatalf("a checkout-less session released %v", prov.removed)
	}
}

// ---- routing -------------------------------------------------------------

// The git provisioner is local-box only (phase 1): a cli runtime pinned to a
// `host:` must stay on paseo, or the agent would be handed a path that exists
// on the wrong machine.
func TestCLIProvisionerRoutesRemoteLaunchesToPaseo(t *testing.T) {
	git := &fakeProv{id: "/state/worktrees/disp-1", cwd: "/state/worktrees/disp-1"}
	paseo := &fakeProv{id: "ws-9", cwd: "/remote/ws-9"}

	local := cliProvisioner(config.ControllerConfig{}, paseo, git)
	if id, _, _ := local.ProvisionWorktree(context.Background(), makeReq("merge_conflict", "x")); id != git.id {
		t.Fatalf("local launch provisioned via %q, want the git provisioner", id)
	}

	remote := cliProvisioner(config.ControllerConfig{Host: "box"}, paseo, git)
	if id, _, _ := remote.ProvisionWorktree(context.Background(), makeReq("merge_conflict", "x")); id != paseo.id {
		t.Fatalf("host-pinned launch provisioned via %q, want the paseo provisioner", id)
	}

	// A step's own host overrides a local controller's.
	req := makeReq("merge_conflict", "x")
	req.Step.Host = "box"
	if id, _, _ := local.ProvisionWorktree(context.Background(), req); id != paseo.id {
		t.Fatalf("step-pinned launch provisioned via %q, want the paseo provisioner", id)
	}
}

// Teardown must go back to whoever made the checkout — never hand a paseo
// workspace id to git, or a git path to paseo.
func TestCLIProvisionerRemovesViaTheOwner(t *testing.T) {
	git := &fakeProv{id: "/state/worktrees/disp-1", cwd: "/state/worktrees/disp-1"}
	paseo := &fakeProv{id: "ws-9", cwd: "/remote/ws-9"}
	p := cliProvisioner(config.ControllerConfig{}, paseo, git)
	ctx := context.Background()

	gitID, _, _ := p.ProvisionWorktree(ctx, makeReq("merge_conflict", "x"))
	remoteReq := makeReq("merge_conflict", "x")
	remoteReq.Step.Host = "box"
	paseoID, _, _ := p.ProvisionWorktree(ctx, remoteReq)

	if err := p.RemoveWorktree(ctx, gitID); err != nil {
		t.Fatal(err)
	}
	if err := p.RemoveWorktree(ctx, paseoID); err != nil {
		t.Fatal(err)
	}
	if len(git.removed) != 1 || git.removed[0] != gitID {
		t.Fatalf("git provisioner saw %v, want [%s]", git.removed, gitID)
	}
	if len(paseo.removed) != 1 || paseo.removed[0] != paseoID {
		t.Fatalf("paseo provisioner saw %v, want [%s]", paseo.removed, paseoID)
	}
	// An id from a provisioner we have no record of (a session resumed after a
	// restart) is nobody's to remove here — the reaper reclaims it.
	if err := p.RemoveWorktree(ctx, "/state/worktrees/unknown"); err != nil {
		t.Fatal(err)
	}
	if len(git.removed) != 1 || len(paseo.removed) != 1 {
		t.Fatalf("an unknown id was routed somewhere: git=%v paseo=%v", git.removed, paseo.removed)
	}
}

// With no git provisioner wired the cli path is byte-for-byte what it was: the
// dispatcher-backed provisioner, unwrapped.
func TestCLIProvisionerFallsBackWhenGitIsUnwired(t *testing.T) {
	paseo := &fakeProv{id: "ws-9"}
	if got := cliProvisioner(config.ControllerConfig{}, paseo, nil); got != Provisioner(paseo) {
		t.Fatalf("cliProvisioner wrapped %T when no git provisioner was given", got)
	}
	if got := cliProvisioner(config.ControllerConfig{}, nil, nil); got != nil {
		t.Fatalf("cliProvisioner invented %T out of two nils", got)
	}
}

// The registry hands the git provisioner to cli controllers ONLY — acp,
// opencode and agent-deck stay on paseo in phase 1.
func TestRegistryGivesTheGitProvisionerToCLIOnly(t *testing.T) {
	git := &fakeProv{id: "/state/worktrees/disp-1"}
	disp := &dispatch.Dispatcher{}
	reg := NewRegistry(map[string]config.ControllerConfig{
		"cx":   {Transport: "cli", Tool: "codex"},
		"usec": {Type: "cli", Tool: "claude-code"},
		"acp":  {Transport: "acp", Agent: "gemini"},
		"oc":   {Type: "opencode"},
		"deck": {Type: "agent-deck"},
	}, "", disp, nil, WithCLIProvisioner(git))

	for _, name := range []string{"cx", "usec"} {
		c, err := reg.Resolve(name)
		if err != nil {
			t.Fatal(err)
		}
		cc, ok := c.(*cliController)
		if !ok {
			t.Fatalf("%s resolved to %T, want a cli controller", name, c)
		}
		if _, routed := cc.prov.(*routedProvisioner); !routed {
			t.Fatalf("%s got provisioner %T, want the git-routed one", name, cc.prov)
		}
	}
	acp, err := reg.Resolve("acp")
	if err != nil {
		t.Fatal(err)
	}
	if _, routed := provOf(acp).(*routedProvisioner); routed {
		t.Fatal("the acp controller was given the cli git provisioner")
	}
}

// provOf reads a controller's provisioner for the assertions above.
func provOf(c Controller) Provisioner {
	switch t := c.(type) {
	case *cliController:
		return t.prov
	case *acpController:
		return t.prov
	}
	return nil
}
