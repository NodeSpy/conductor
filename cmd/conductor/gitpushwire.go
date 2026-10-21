package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/gitpush"
)

// workPusher pushes sandboxed agents' work: the git.push verb and the
// end-of-step push. One push per workspace at a time.
type workPusher struct {
	cfg   *config.Config
	audit func(map[string]any)
	logf  func(string, ...any)

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (p *workPusher) lock(ws string) func() {
	p.mu.Lock()
	if p.locks == nil {
		p.locks = map[string]*sync.Mutex{}
	}
	m := p.locks[ws]
	if m == nil {
		m = &sync.Mutex{}
		p.locks[ws] = m
	}
	p.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// push pushes dispatchID's workspace. Not a sandboxed dispatch, or one with
// nowhere to push (a review step, a detached checkout): errNothingToPush.
func (p *workPusher) push(ctx context.Context, dispatchID string) (map[string]any, error) {
	e, ok := agentsandbox.Default.ForDispatch(dispatchID)
	if !ok {
		return nil, fmt.Errorf("%w: this dispatch has no sandboxed workspace (an unsandboxed agent pushes itself)", errNothingToPush)
	}
	if e.Push == nil {
		return nil, fmt.Errorf("%w: conductor has no branch to push this workspace to (a review step, a detached checkout, or no remote)", errNothingToPush)
	}
	signer, err := pushSigner(p.cfg)
	if err != nil {
		return nil, err
	}
	defer p.lock(e.Workspace)()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	res, err := gitpush.Push(ctx, gitpush.Request{
		Workspace: e.Workspace, GitDir: e.Push.GitDir, HeadDir: e.Push.HeadDir,
		Branch: e.Push.Branch, Remote: e.Push.Remote, Signer: signer,
	})
	row := map[string]any{"event": "git_push", "dispatch": dispatchID, "repo": e.Push.Repo, "branch": e.Push.Branch,
		"old": res.Old, "new": res.New, "commits": res.Commits, "signed": res.Signed}
	if err != nil {
		row["outcome"], row["error"] = "refused", err.Error()
		p.audit(row)
		p.logf("git push %s %s: %v", e.Push.Repo, e.Push.Branch, err)
		return nil, err
	}
	row["outcome"] = "ok"
	if res.UpToDate {
		row["outcome"] = "up_to_date"
	}
	p.audit(row)
	if !res.UpToDate {
		p.logf("git push %s %s: %d commit(s) %s..%s (signed=%v)", e.Push.Repo, e.Push.Branch, res.Commits, shortSHA(res.Old), shortSHA(res.New), res.Signed)
	}
	return map[string]any{"branch": res.Branch, "pushed": !res.UpToDate, "commits": res.Commits, "head": res.New, "up_to_date": res.UpToDate}, nil
}

// errNothingToPush is flow's: the end-of-step push skips it silently.
var errNothingToPush = flow.ErrNothingToPush

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	if s == "" {
		return "(new)"
	}
	return s
}

// pushSigner is the key pushed commits are signed with: sandbox.push's,
// else the operator's own git signing key (gpg.format ssh). Signing on and
// no key: an error — conductor does not push unsigned unless told to.
func pushSigner(cfg *config.Config) (*gitpush.Signer, error) {
	if !cfg.Sandbox.PushSign() {
		return nil, nil
	}
	s := &gitpush.Signer{}
	if p := cfg.Sandbox; p != nil && p.Push != nil {
		s.Key, s.Name, s.Email = p.Push.SigningKey, p.Push.Name, p.Push.Email
	}
	git := func(k string) string {
		out, err := exec.Command("git", "config", "--global", "--get", k).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	if s.Key == "" && git("gpg.format") == "ssh" {
		s.Key = git("user.signingkey")
	}
	if s.Key == "" {
		return nil, fmt.Errorf("git.push: commits are signed (sandbox.push.sign) but no signing key is set — set sandbox.push.signing_key, or git's user.signingkey with gpg.format ssh (or sandbox.push.sign: false to push unsigned)")
	}
	if strings.HasPrefix(s.Key, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			s.Key = filepath.Join(h, s.Key[2:])
		}
	}
	if s.Name == "" {
		s.Name = git("user.name")
	}
	if s.Email == "" {
		s.Email = git("user.email")
	}
	return s, nil
}

// wireWorkPusher turns on conductor's pushes of sandboxed agents' work: the
// git.push verb and (sandbox.push.at_step_end, default on) the end-of-step
// push. Returns the unwiring.
func wireWorkPusher(cfg *config.Config, runner *flow.Runner, audit func(map[string]any), logf func(string, ...any)) func() {
	if !cfg.AgentSandboxOn() {
		return func() {}
	}
	p := &workPusher{cfg: cfg, audit: audit, logf: logf}
	connector.SetGitOps(func() *connector.GitOps { return &connector.GitOps{Push: p.push} })
	if runner != nil && cfg.Sandbox.PushAtStepEnd() {
		runner.PushWork = p.push
	}
	return func() {
		connector.SetGitOps(nil)
		if runner != nil {
			runner.PushWork = nil
		}
	}
}
