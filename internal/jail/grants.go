package jail

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/NodeSpy/conductor/internal/grant"
	"github.com/NodeSpy/conductor/internal/hostcmd"
)

// Approval grants (internal/grant). A host command the broker would refuse
// because the operator's allow list does not name it — `terraform apply
// plan.tfplan` from an agent, say — runs when the operator approved that
// exact command: a live grant for this agent (its identity, or its dispatch),
// this tool and this parsed argv. The broker:
//
//  1. finds the grant (grant.Store.Find), and decides the command again as
//     if the operator's allow list named it (hostcmd.Rule.Granted) — so a
//     guardrail, a deny rule, the path guard or a closed target still
//     refuses it, and a content-executing command still runs confined;
//  2. copies every file the grant pins into a private scratch dir only
//     conductor can write, opening each path component without following a
//     symlink and hashing the bytes as they are copied, and checks them
//     against the approved digests (grant.ErrDigest; nothing consumed);
//  3. consumes the grant (grant.ErrConsumed on a second use of a single-use
//     one), and runs the command with those files named by their private
//     copies — the bytes checked are the bytes run.
type grantUse struct {
	spec    grant.GrantSpec
	args    []string // argv with pinned files rewritten to their private copies
	files   []string // the private copies (bound read-only into the run)
	cleanup func()
}

// grantMemo remembers the grants this broker consumed, to name ErrConsumed
// on a second use (the store's Find returns live grants only).
type grantMemo struct {
	mu   sync.Mutex
	used []grant.GrantSpec
}

func (g *grantMemo) add(s grant.GrantSpec) {
	g.mu.Lock()
	g.used = append(g.used, s)
	if len(g.used) > 256 {
		g.used = g.used[len(g.used)-256:]
	}
	g.mu.Unlock()
}

func (g *grantMemo) matching(p grant.Presented) (grant.GrantSpec, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i := len(g.used) - 1; i >= 0; i-- {
		if g.used[i].SingleUse && g.used[i].Matches(p) {
			return g.used[i], true
		}
	}
	return grant.GrantSpec{}, false
}

// presented is what the broker knows about a host command, for grant matching.
func presented(d *Dispatch, tool string, args []string) grant.Presented {
	return grant.Presented{AgentID: d.AgentID, Dispatch: d.DispatchID, Tool: tool, Argv: grant.ParseArgv(args)}
}

// useGrant looks for a grant that lifts a refusal of tool args. It returns
// the redecided command and the grant to run it under; or, when no grant
// applies, "" with nil (the original refusal stands) or a reason naming why a
// grant did not hold.
func (m *Manager) useGrant(ctx context.Context, d *Dispatch, tool string, args []string, cwd string, rule hostcmd.Rule, hc hostcmd.Context) (hostcmd.Decision, *grantUse, string) {
	p := presented(d, tool, args)
	cands, err := m.Grants.Find(ctx, p)
	if err != nil {
		return hostcmd.Decision{}, nil, "grant lookup: " + err.Error()
	}
	if len(cands) == 0 {
		if g, ok := m.grantsUsed.matching(p); ok {
			return hostcmd.Decision{}, nil, fmt.Sprintf("%v (grant %s, approved by %s)", grant.ErrConsumed, g.ID, g.ApprovedBy)
		}
		return hostcmd.Decision{}, nil, ""
	}
	g := cands[0]
	rule.Granted = true
	dec := hostcmd.Decide(hostcmd.Request{Tool: tool, Args: args, Cwd: cwd}, rule, hc)
	if !dec.Allow || dec.Native {
		return dec, nil, ""
	}
	use, err := m.stageGrantFiles(d, g, args, cwd)
	if err != nil {
		return hostcmd.Decision{}, nil, fmt.Sprintf("%v (grant %s)", err, g.ID)
	}
	if _, err := m.Grants.Consume(ctx, g.ID, p); err != nil {
		use.cleanup()
		return hostcmd.Decision{}, nil, err.Error()
	}
	if g.SingleUse {
		m.grantsUsed.add(g)
	}
	use.spec = g
	return dec, use, ""
}

// stageGrantFiles copies the files g pins into a private scratch dir,
// verifies them against g's digests, and rewrites args to name the copies.
func (m *Manager) stageGrantFiles(d *Dispatch, g grant.GrantSpec, args []string, cwd string) (*grantUse, error) {
	use := &grantUse{args: append([]string(nil), args...), cleanup: func() {}}
	if len(g.FileDigests) == 0 {
		return use, nil
	}
	dir, err := m.grantScratch()
	if err != nil {
		return nil, fmt.Errorf("grant scratch: %w", err)
	}
	use.cleanup = func() { _ = os.RemoveAll(dir) }
	actual := map[string]string{}
	for i, fd := range g.FileDigests {
		abs := fd.Path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(cwd, abs)
		}
		abs = filepath.Clean(abs)
		// The jail's /tmp is the dispatch's own dir on the host, never the
		// host's /tmp.
		if jt := d.jailTmp(); jt != d.TmpDir && within(abs, jt) && !within(abs, d.Workspace) {
			rel, _ := filepath.Rel(jt, abs)
			abs = filepath.Join(d.TmpDir, rel)
		}
		root := ""
		for _, r := range []string{d.TmpDir, d.Workspace} { // the workspace wins
			if r != "" && within(abs, r) {
				root = r
			}
		}
		if root == "" {
			use.cleanup()
			return nil, fmt.Errorf("%w: %s is outside the workspace", grant.ErrDigest, fd.Path)
		}
		rel, _ := filepath.Rel(root, abs)
		dst := filepath.Join(dir, fmt.Sprintf("%d-%s", i, filepath.Base(abs)))
		sum, err := copyNoFollow(root, rel, dst)
		if err != nil {
			use.cleanup()
			return nil, fmt.Errorf("%w: %s: %v", grant.ErrDigest, fd.Path, err)
		}
		actual[fd.Path] = sum
		use.files = append(use.files, dst)
		for j, a := range use.args {
			switch {
			case a == fd.Path:
				use.args[j] = dst
			case strings.HasPrefix(a, "-") && strings.HasSuffix(a, "="+fd.Path):
				use.args[j] = strings.TrimSuffix(a, fd.Path) + dst
			}
		}
	}
	if err := g.CheckDigests(actual); err != nil {
		use.cleanup()
		return nil, err
	}
	return use, nil
}

// grantScratch makes a fresh private dir for one grant's file copies. It
// lives under the host's /tmp (or GrantScratch), in a per-uid 0700 dir the
// jail never shows — the jail and every host command see the dispatch's own
// /tmp there instead — and is bound read-only into the granted run alone.
func (m *Manager) grantScratch() (string, error) {
	base := m.GrantScratch
	if base == "" {
		base = filepath.Join(os.TempDir(), fmt.Sprintf("conductor-grants-%d", os.Getuid()))
	}
	if err := os.Mkdir(base, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	if err := ownPrivateDir(base); err != nil {
		return "", err
	}
	return os.MkdirTemp(base, "g-")
}
