//go:build darwin

package jail

import (
	"os"
	"path/filepath"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// layoutDarwin is the macOS jail layout (#154 §10), for the Seatbelt
// backend. Where Linux mounts, macOS points: HOME is a per-dispatch scratch
// dir with the tool's state paths linked into it (the real home stays
// unreadable except those paths); the shims are a per-dispatch dir first on
// PATH, and every host-set tool's real binary is exec-denied, so running it
// by absolute path is refused; TMPDIR is the dispatch's own.
func (m *Manager) layoutDarwin(d *Dispatch, self string) ([]sandbox.BindMount, *sandbox.AgentProfile, string, error) {
	home := filepath.Join(d.Dir, "home")
	bin := d.binDir()
	for _, p := range []string{home, bin} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			return nil, nil, "", err
		}
	}
	prof := &sandbox.AgentProfile{Home: m.Home, Keychain: d.Keychain}
	link := func(rel string) string {
		real := filepath.Join(m.Home, rel)
		if _, err := os.Lstat(real); err != nil {
			return ""
		}
		dst := filepath.Join(home, rel)
		_ = os.MkdirAll(filepath.Dir(dst), 0o700)
		_ = os.Symlink(real, dst)
		return real
	}
	for _, rel := range toolState[d.Tool] {
		if real := link(rel); real != "" {
			prof.ReadWrite = append(prof.ReadWrite, real)
		}
	}
	for _, rel := range readOnlyHome {
		if real := link(rel); real != "" {
			prof.ReadOnly = append(prof.ReadOnly, real)
		}
	}
	if p := m.gitGlobalConfig(); p != "" {
		prof.ReadOnly = append(prof.ReadOnly, p)
	}
	prof.ReadWrite = append(prof.ReadWrite, home, d.TmpDir, filepath.Join(d.Dir, "broker"))
	prof.ReadOnly = append(prof.ReadOnly, bin, self)
	if raw, err := m.SelfExe(); err == nil {
		prof.ReadOnly = append(prof.ReadOnly, raw)
	}
	if d.Argv0 != "" {
		prof.ReadOnly = append(prof.ReadOnly, binaryRoots(d.Argv0, m.LookPath)...)
	}
	shims := []string{ShimRemoteHelper, ShimSSHSign, ShimGPGSign, ShimHook}
	shims = append(shims, d.HostSet...)
	shims = append(shims, d.Denied...)
	for _, s := range shims {
		_ = os.Symlink(self, filepath.Join(bin, s))
	}
	// conductor itself (`conductor call step.done`, the skill CLI).
	_ = os.Symlink(self, filepath.Join(bin, "conductor"))
	for _, tool := range append(append([]string(nil), d.HostSet...), d.Denied...) {
		if nativeCapable[tool] {
			continue
		}
		prof.ExecDeny = append(prof.ExecDeny, realPaths(tool, m.LookPath)...)
		if p, err := m.LookPath(tool); err == nil {
			prof.ExecDeny = append(prof.ExecDeny, p)
		}
	}
	prof.UnixSockets = append(prof.UnixSockets, filepath.Join(d.Dir, "broker", "broker.sock"))
	for _, s := range m.Sockets {
		prof.UnixSockets = append(prof.UnixSockets, s)
		prof.ReadOnly = append(prof.ReadOnly, s)
	}
	var binds []sandbox.BindMount
	if g := d.Git; g != nil && g.Base != "" {
		// A per-dispatch clone (see layout): the base's object store
		// readable, nothing else of it (the profile allows no other path
		// of the base at all).
		prof.ReadOnly = append(prof.ReadOnly, filepath.Join(g.Base, "objects"))
	} else if g := d.Git; g != nil && g.CommonDir != "" {
		for _, sub := range []string{"hooks", filepath.Join("objects", "info")} {
			_ = os.MkdirAll(filepath.Join(g.CommonDir, sub), 0o755)
		}
		if !within(g.CommonDir, d.Workspace) {
			binds = append(binds, sandbox.BindMount{Path: g.CommonDir})
		}
		prof.WriteDeny = append(prof.WriteDeny, filepath.Join(g.CommonDir, "config"),
			filepath.Join(g.CommonDir, "hooks"), filepath.Join(g.CommonDir, "objects", "info"))
		if g.GitDir != g.CommonDir {
			prof.Hide = append(prof.Hide, filepath.Join(g.CommonDir, "worktrees"))
			prof.Unhide = append(prof.Unhide, g.GitDir)
		}
	}
	for _, l := range d.Layers {
		if l == nil {
			continue
		}
		for _, p := range l.FS {
			binds = append(binds, sandbox.BindMount{Path: config.ExpandHome(p)})
		}
	}
	return binds, prof, home, nil
}
