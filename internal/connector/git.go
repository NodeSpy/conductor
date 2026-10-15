package connector

import (
	"context"
	"fmt"
	"sync"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// The git connector is how a sandboxed agent's work leaves its workspace:
// the agent holds no push credential, so `git.push` asks conductor to push
// it (docs/design/agent-sandbox.md §6). conductor pushes the workspace's
// committed HEAD to the dispatch's OWN branch — the agent names no ref —
// fast-forward only, re-signing the new commits with the operator's key.
// The dispatch is resolved from the caller's token, never from an option.
var gitDecl = &TypeDecl{
	Type: "git",
	Desc: "Your sandboxed workspace's way out: git.push asks conductor to push your committed HEAD to this dispatch's own branch (fast-forward only, signed by conductor). Auto-granted to every sandboxed dispatch that is not a review; conductor also pushes at the end of the step.",
	Verbs: []VerbDecl{
		{
			Name: "push", Desc: "push your committed work: conductor pushes the workspace HEAD to this dispatch's own branch, fast-forward only, signing each new commit. Commit first; uncommitted changes are not pushed. Takes no branch or ref — the branch is the dispatch's.",
			Options: Schema{},
			Outputs: Schema{
				"branch":     {Type: TString, Desc: "the dispatch branch pushed to"},
				"pushed":     {Type: TBool, Desc: "commits were pushed (false when already up to date)"},
				"commits":    {Type: TInt, Desc: "how many new commits"},
				"head":       {Type: TString, Desc: "the branch's tip after the push"},
				"up_to_date": {Type: TBool},
			},
		},
	},
}

func init() { RegisterType(gitDecl, newGitImpl) }

// GitOps is the daemon-side surface git.push acts through, wired at boot.
type GitOps struct {
	// Push pushes the calling dispatch's workspace (dispatchID is the
	// caller's token-bound dispatch, daemon-injected).
	Push func(ctx context.Context, dispatchID string) (map[string]any, error)
}

var (
	gitOpsMu sync.RWMutex
	gitOpsFn func() *GitOps
)

// SetGitOps wires the daemon's push operation (boot).
func SetGitOps(fn func() *GitOps) {
	gitOpsMu.Lock()
	gitOpsFn = fn
	gitOpsMu.Unlock()
}

func gitOps() *GitOps {
	gitOpsMu.RLock()
	fn := gitOpsFn
	gitOpsMu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn()
}

type gitImpl struct{}

func newGitImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	return gitImpl{}, nil
}

func (gitImpl) Validate() error          { return nil }
func (gitImpl) DeclaredEvents() []string { return nil }
func (gitImpl) Source([]CompiledTrigger) (core.Integration, error) {
	return nil, nil
}

func (gitImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	if verb != "push" {
		return nil, fmt.Errorf("git: unknown verb %q", verb)
	}
	ops := gitOps()
	if ops == nil || ops.Push == nil {
		return nil, fmt.Errorf("git.push: only runs inside a live daemon with the agent sandbox on")
	}
	// Injected by the skill path from the caller's token; an agent cannot
	// set a leading-underscore option.
	dispatchID, _ := opts["__dispatch"].(string)
	if dispatchID == "" {
		return nil, fmt.Errorf("git.push: no dispatch for this caller — only a conductor-launched agent pushes")
	}
	return ops.Push(ctx, dispatchID)
}
