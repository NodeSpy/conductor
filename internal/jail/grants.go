package jail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/grant"
	"github.com/NodeSpy/conductor/internal/hostcmd"
)

// Approval grants (internal/grant). A host command the broker would refuse
// because the operator's allow list does not name it — `terraform apply
// plan.tfplan` from an agent, say — runs when the operator approved that
// exact command: a live grant for this agent (its identity, or its dispatch),
// this tool, this parsed argv, run in this directory. The broker:
//
//  1. finds the grant (grant.Store.Find), and decides the command again as
//     if the operator's allow list named it (hostcmd.Rule.Granted) — so a
//     guardrail, a deny rule, the path guard or a closed target still
//     refuses it, and a content-executing command still runs confined. When
//     none applies, the refusal names why from the grants near it
//     (grant.Store.Near, grant.Explain): used, expired, revoked, or another
//     command;
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

// presented is what the broker knows about a host command, for grant
// matching: its agent, its tool and argv, and the directory it runs in,
// relative to the workspace. A command run outside the workspace (from the
// jail's /tmp) gets a cwd that escapes it, which no grant matches.
func presented(d *Dispatch, tool string, args []string, cwd string) grant.Presented {
	return grant.Presented{AgentID: d.AgentID, Dispatch: d.DispatchID, Tool: tool, Argv: grant.ParseArgv(args), Cwd: d.wsRel(cwd)}
}

// wsRel is dir relative to d's workspace, "/"-separated ("." for the
// workspace itself; ".."-led when dir is outside it).
func (d *Dispatch) wsRel(dir string) string {
	rel, err := filepath.Rel(d.Workspace, dir)
	if err != nil || d.Workspace == "" {
		return ".."
	}
	return filepath.ToSlash(rel)
}

// useGrant looks for a grant that lifts a refusal of tool args. It returns
// the redecided command and the grant to run it under; or, when no grant
// applies, "" with nil (the original refusal stands) or a reason naming why a
// grant did not hold.
func (m *Manager) useGrant(ctx context.Context, d *Dispatch, tool string, args []string, cwd string, rule hostcmd.Rule, hc hostcmd.Context) (hostcmd.Decision, *grantUse, string) {
	p := presented(d, tool, args, cwd)
	cands, err := m.Grants.Find(ctx, p)
	if err != nil {
		return hostcmd.Decision{}, nil, "grant lookup: " + err.Error()
	}
	if len(cands) == 0 {
		return hostcmd.Decision{}, nil, m.whyNoGrant(ctx, p)
	}
	g := cands[0]
	rule.Granted = true
	dec := hostcmd.Decide(hostcmd.Request{Tool: tool, Args: args, Cwd: cwd}, rule, hc)
	if !dec.Allow || dec.Native {
		return dec, nil, ""
	}
	use, err := m.stageGrantFiles(d, g, args, p.Cwd)
	if err != nil {
		return hostcmd.Decision{}, nil, fmt.Sprintf("%v (grant %s)", err, g.ID)
	}
	if _, err := m.Grants.Consume(ctx, g.ID, p); err != nil {
		use.cleanup()
		return hostcmd.Decision{}, nil, err.Error()
	}
	use.spec = g
	return dec, use, ""
}

// whyNoGrant names why no grant lifted a refusal, from this agent's grants
// for this tool whatever their argv or state (grant.Store.Near):
//   - a grant for this exact command is used, expired or revoked
//     (grant.Explain) — the newest such approval's, the operator's latest
//     word on it;
//   - else a live one approves another command (grant.ErrMismatch).
//
// "" when none is near: the refusal stands as it is.
func (m *Manager) whyNoGrant(ctx context.Context, p grant.Presented) string {
	near, err := m.Grants.Near(ctx, p)
	if err != nil {
		return "grant lookup: " + err.Error()
	}
	now := time.Now()
	same, other := -1, -1
	for i, g := range near {
		switch {
		case g.Matches(p):
			if grant.Explain(g, p, now) != nil && (same < 0 || !g.Issued.Before(near[same].Issued)) {
				same = i
			}
		case other < 0 && g.State == grant.StateLive:
			other = i
		}
	}
	switch {
	case same >= 0:
		g := near[same]
		return fmt.Sprintf("%v (grant %s, approved by %s)", grant.Explain(g, p, now), g.ID, g.ApprovedBy)
	case other >= 0:
		g := near[other]
		dir, _ := grant.CleanCwd(g.Cwd)
		return fmt.Sprintf("%v (grant %s approves `%s %s` in %s)", grant.ErrMismatch, g.ID, g.Tool, g.Argv, dir)
	}
	return ""
}

// stageGrantFiles copies the files g pins into a private scratch dir,
// verifies them against g's digests, and rewrites args to name the copies.
// Each is read by pinnedFile, as the gate hashed it at mint (DigestFile). An
// argv token names a pinned file when it resolves to the pin's path from the
// command's cwd (grant.ResolvePath, as the gate resolved it): a bare token,
// or the value of a `-k=v` flag.
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
		dst := filepath.Join(dir, fmt.Sprintf("%d-%s", i, filepath.Base(fd.Path)))
		sum, err := d.copyPinned(fd.Path, dst)
		if err != nil {
			use.cleanup()
			return nil, fmt.Errorf("%w: %s: %v", grant.ErrDigest, fd.Path, err)
		}
		actual[fd.Path] = sum
		use.files = append(use.files, dst)
		for j, a := range use.args {
			tok, prefix := a, ""
			if k, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(a, "-") {
				tok, prefix = v, k+"="
			}
			if r, err := grant.ResolvePath(cwd, tok); err == nil && r == fd.Path {
				use.args[j] = prefix + dst
			}
		}
	}
	if err := g.CheckDigests(actual); err != nil {
		use.cleanup()
		return nil, err
	}
	return use, nil
}

// copyPinned is pinnedFile into a new file dst (read-only, never an
// existing one).
func (d *Dispatch) copyPinned(path, dst string) (string, error) {
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o400)
	if err != nil {
		return "", err
	}
	sum, err := d.pinnedFile(path, out)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return sum, err
}

// pinnedFile reads the file a grant pins — path, workspace-relative and
// clean: the argv token resolved against the command's cwd
// (grant.ResolvePath) — from where it is for d, into w, and returns the
// sha256 of its bytes. It is the one read both ends of a grant use: the
// gate's hash at mint (DigestFile) and the broker's copy at use
// (stageGrantFiles), so the bytes approved are the bytes checked.
//
//   - A file a confined run wrote is read from the copy-on-write layer it
//     went to (a `terraform plan -out` in envs/prod lands in the dispatch's
//     layer for terraform, at envs/prod/…, never in the workspace); any
//     other, from the workspace. Which tool's layer does not depend on the
//     command — the gate does not know it at mint — so a file two tools'
//     layers both hold is refused as ambiguous.
//   - Every path component is opened without following a symlink, and only a
//     regular file is read (a layer's whiteout — the confined run deleted
//     it — is not one).
func (d *Dispatch) pinnedFile(path string, w io.Writer) (string, error) {
	if p, err := grant.ResolvePath(".", path); err != nil || p != path {
		return "", fmt.Errorf("%q is not a clean workspace-relative path", path)
	}
	if d.Workspace == "" {
		return "", errors.New("no workspace")
	}
	root := d.Workspace
	upper, err := d.layerHolding(path)
	if err != nil {
		return "", err
	}
	if upper != "" {
		root = upper
	}
	return readNoFollow(root, path, w)
}

// layerHolding is the copy-on-write upper dir among d's confined layers
// (cow-ws/<tool>/up) that holds workspace path rel, "" when none does.
func (d *Dispatch) layerHolding(rel string) (string, error) {
	base := filepath.Join(d.Dir, "cow-ws")
	ents, err := os.ReadDir(base)
	if err != nil {
		return "", nil // no confined run yet
	}
	var tools []string
	upper := ""
	for _, e := range ents {
		if up := filepath.Join(base, e.Name(), "up"); e.IsDir() && layerHas(up, rel) {
			tools, upper = append(tools, e.Name()), up
		}
	}
	if len(tools) > 1 {
		return "", fmt.Errorf("%s is in the copy-on-write layers of %s: ambiguous", rel, strings.Join(tools, " and "))
	}
	return upper, nil
}

// DigestFile is the sha256 (lowercase hex) of the file a grant for identity
// would pin as path, read exactly as the broker reads it when the grant is
// used (pinnedFile). The member gate hashes with it when it records a
// host-command proposal, so the operator approves the bytes the command will
// run on.
//
// identity is a live jail's AgentID (a paseo provider session's identity) or
// its DispatchID (a cli worker) — what the grant binds to; path is the file's
// workspace-relative path, as the grant pins it (grant.ResolvePath of the
// argv token from the command's cwd). It is an error when identity has no
// live jail, or its live jails read different bytes.
func (m *Manager) DigestFile(identity, path string) (string, error) {
	if identity == "" {
		return "", errors.New("no identity")
	}
	m.mu.Lock()
	var ds []*Dispatch
	for _, d := range m.live {
		if d.AgentID == identity || d.DispatchID == identity {
			ds = append(ds, d)
		}
	}
	m.mu.Unlock()
	if len(ds) == 0 {
		return "", fmt.Errorf("no live jail for %s", identity)
	}
	sum := ""
	for _, d := range ds {
		s, err := d.pinnedFile(path, io.Discard)
		if err != nil {
			return "", err
		}
		if sum != "" && s != sum {
			return "", fmt.Errorf("%s reads differently in %s's jails", path, identity)
		}
		sum = s
	}
	return sum, nil
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
