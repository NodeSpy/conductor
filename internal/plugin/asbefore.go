package plugin

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/sandbox"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// A plugin that ran before the plugin jail existed, on a host that cannot
// jail plugins (HostJailStatus), runs as it did then: an upgrade never
// breaks a working setup. That is the launch below — the one conductor
// used before the jail: the environment scrubbed to what the plugin
// declares and allow_env grants, HTTP(S)_PROXY at conductor's egress proxy
// with what it declares (resolved, so a config: destination is its
// configured host), PATH narrowed to the declared commands, and an
// operator's isolation: block applied as it was.

// runsAsBefore reports whether s runs unjailed as before the jail: the host
// cannot jail plugins, and s was installed before the jail (or is the
// operator's own local build, which never had an install record).
func runsAsBefore(s Spec) (HostJail, bool) {
	h := HostJailStatus()
	return h, !h.OK && !s.TrustFull && (s.PreJail || s.Local)
}

func buildAsBefore(s Spec, sd SandboxDeps, argv []string) (cmd *exec.Cmd, cleanup func(), sandboxed bool, err error) {
	env := withDeclaredEnv(spawnBaseEnv(), grantedEnvBefore(s))
	cleanup = func() {}

	spec := sandbox.FromConfig(s.Isolation)
	if spec == nil {
		env, cleanup, err = confineToManifest(s, env, sd)
		if err != nil {
			return nil, nil, false, err
		}
		c := exec.Command(argv[0], argv[1:]...) //nolint:gosec // path is verified (verify.go); confinement is the declared manifest
		c.Env = env
		return c, cleanup, false, nil
	}

	// An operator-written isolation: block fails closed, as it did.
	stage := ""
	if sd.StagingRoot != "" {
		stage = filepath.Join(sd.StagingRoot, s.Name)
		if err := os.MkdirAll(stage, 0o700); err != nil {
			return nil, nil, false, fmt.Errorf("plugin %s: staging dir: %w", s.Name, err)
		}
		spec.FS = append(spec.FS, stage)
	}
	if err := spec.Check(spawnGOOS, os.Geteuid(), spawnLookPath); err != nil {
		if s.IsolationDefaulted {
			log.Printf("plugin %s: default sandbox unavailable (%v) — running WITHOUT OS confinement", s.Name, err)
			env, cleanup, cerr := confineToManifest(s, env, sd)
			if cerr != nil {
				return nil, nil, false, cerr
			}
			c := exec.Command(argv[0], argv[1:]...) //nolint:gosec // path is verified (verify.go); confinement is the declared manifest
			c.Env = env
			return c, cleanup, false, nil
		}
		return nil, nil, false, fmt.Errorf("plugin %s: sandbox preflight: %w", s.Name, err)
	}
	masks := masksExcludingBinaryDir(sd.MaskPaths, s.BinPath)
	if s.IsolationDefaulted {
		masks = nil
	}
	var nf *sandbox.NetForward
	switch {
	case spawnGOOS == "darwin" && spec.Mode == "namespace":
		if s.BinPath != "" {
			nf = &sandbox.NetForward{Binds: []sandbox.BindMount{{Path: filepath.Dir(s.BinPath), RO: true}}}
		}
		if stage != "" {
			if nf == nil {
				nf = &sandbox.NetForward{}
			}
			nf.Binds = append(nf.Binds, sandbox.BindMount{Path: stage})
		}
	case len(masks) > 0 || spec.EnforcedEgress():
		nf = &sandbox.NetForward{Self: sd.Self, Masks: masks}
	}
	if spec.EnforcedEgress() {
		if sd.EgressUnix == nil {
			return nil, nil, false, fmt.Errorf("plugin %s: isolation requests an egress allowlist but no egress proxy is wired", s.Name)
		}
		sock, cred, revoke, err := sd.EgressUnix(spec.Egress)
		if err != nil {
			return nil, nil, false, fmt.Errorf("plugin %s: egress proxy: %w", s.Name, err)
		}
		nf.UnixSocket = sock
		env = append(env, sandbox.ProxyEnv(sandbox.ForwardAddr, cred)...)
		cleanup = revoke
	}
	wrapped, err := spec.WrapLocal(argv, "", envKeys(env), nf)
	if err != nil {
		cleanup()
		return nil, nil, false, fmt.Errorf("plugin %s: sandbox wrap: %w", s.Name, err)
	}
	c := exec.Command(wrapped[0], wrapped[1:]...) //nolint:gosec // verified binary, sandbox-wrapped
	c.Env = env
	return c, cleanup, true, nil
}

// grantedEnvBefore is what passed before the jail: the variables allow_env
// names that the plugin also declares.
func grantedEnvBefore(s Spec) []string {
	if s.Probe {
		return nil
	}
	declared := map[string]bool{}
	for _, n := range s.Manifest.Env {
		declared[n] = true
	}
	var out []string
	for _, n := range s.AllowEnv {
		if declared[n] {
			out = append(out, n)
		}
	}
	return out
}

// egressBefore is the allowlist the pre-jail launch points the plugin's
// proxy at: network: when the config narrows it, else what the plugin
// declares, each config:/env: destination resolved to its configured value.
// An entry that resolves to nothing (its field unset) adds nothing; one
// that cannot be read is left out and logged.
func egressBefore(s Spec) []string {
	if s.Probe {
		return nil
	}
	if len(s.Network) > 0 {
		return append([]string(nil), s.Network...)
	}
	return resolvedEgress(s.Name, s.Manifest, s.Conns)
}

// EgressFor is what m declares it connects to for one configured instance:
// each config:/env: destination resolved to the instance's value (an unset
// one adds nothing).
func EgressFor(name string, m Manifest, conn map[string]any) []string {
	return resolvedEgress(name, m, []map[string]any{conn})
}

func resolvedEgress(name string, m Manifest, conns []map[string]any) []string {
	home, _ := jailHome()
	var out []string
	for _, ent := range m.Egress {
		g, err := sdk.Capabilities{Egress: []string{ent}}.Resolve(sdk.ResolveEnv{Home: home, Getenv: os.LookupEnv, Conns: conns})
		if err != nil {
			log.Printf("plugin %s: declared destination %q: %v — left out", name, ent, err)
			continue
		}
		out = append(out, g.Egress...)
	}
	return out
}

// confineToManifest is the pre-jail launch's confinement: the egress proxy
// for what the plugin declares (a connector that resolves to no destination
// is not pointed at it, as before), and PATH narrowed to its declared
// commands.
func confineToManifest(s Spec, env []string, sd SandboxDeps) ([]string, func(), error) {
	cleanup := func() {}
	allow := egressBefore(s)
	confine := len(allow) > 0 || s.Kind == KindStep || s.Probe
	if confine && sd.EgressAddr != nil {
		addr, cred, revoke, err := sd.EgressAddr(allow)
		if err != nil {
			return nil, nil, fmt.Errorf("plugin %s: egress proxy for declared network (%s): %w", s.Name, strings.Join(allow, ", "), err)
		}
		env = append(env, sandbox.ProxyEnv(addr, cred)...)
		cleanup = revoke
	}
	if dir, err := commandPathDir(s); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("plugin %s: confining declared commands: %w", s.Name, err)
	} else if dir != "" {
		env = replaceEnv(env, "PATH", dir)
	}
	return env, cleanup, nil
}

// commandPathDir is the directory, beside the plugin's binary, holding a
// link per declared command: the pre-jail launch's PATH. "" leaves PATH
// alone (no install dir, or a plugin that spawns unnamed children).
func commandPathDir(s Spec) (string, error) {
	if s.BinPath == "" {
		return "", nil
	}
	base := filepath.Dir(s.BinPath)
	if base == "" || base == "." {
		return "", nil
	}
	m := s.Manifest
	if m.Spawns && len(m.Commands) == 0 {
		return "", nil
	}
	dir := filepath.Join(base, ".path")
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	cmds := append([]string(nil), m.Commands...)
	sort.Strings(cmds)
	for _, c := range cmds {
		c = strings.TrimSpace(c)
		if c == "" || strings.ContainsAny(c, "/\\") {
			continue
		}
		real, err := exec.LookPath(c)
		if err != nil {
			continue
		}
		_ = os.Symlink(real, filepath.Join(dir, c))
	}
	return dir, nil
}
