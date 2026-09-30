// Package jail is the daemon side of the agent workspace jail (#154): it
// lays out what a jailed agent sees, runs the per-dispatch broker the jail's
// shims talk to, and runs the host commands, brokered git, and signatures
// that broker approves.
//
// What a jailed agent sees (the allow-list; everything else is absent):
//
//	read-write  the workspace — the dispatch's own clone, .git included (its
//	            refs, config, hooks, index, new objects); the tool's own state
//	            (~/.claude, ~/.claude.json, ~/.codex, …)
//	read-only   /usr /etc /opt, ~/.gitconfig, the agent CLI's own install
//	            (e.g. ~/.local/share/claude), the conductor binary, the base
//	            clone's object store (what the clone borrows; the rest of the
//	            base's .git is an empty read-only dir)
//	scratch     a tmpfs $HOME; a per-dispatch /tmp (host commands see it too)
//	shims       /run/conductor/bin first on PATH, and conductor's binary bound
//	            over every host-set tool's real path, so `gh`, `/usr/bin/gh`
//	            and `aws` all reach the broker
//
// Nothing credentialed is in the jail: no GH_TOKEN, no ~/.ssh, no cloud
// config. Credentialed work happens only through the broker — host commands
// (internal/hostcmd decides), git's network side (the git-remote-conductor
// helper, push policy), and commit signing (the signing shim) — which is what
// makes those policies binding.
package jail

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/hostcmd"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// In-jail paths.
const (
	RunDir    = "/run/conductor"
	BinDir    = RunDir + "/bin"
	SelfPath  = RunDir + "/conductor"
	BrokerDir = RunDir + "/broker"
	SockPath  = BrokerDir + "/broker.sock"
)

// Shim names that are not host commands.
const (
	ShimRemoteHelper = "git-remote-conductor"
	ShimSSHSign      = "conductor-ssh-sign"
	ShimGPGSign      = "conductor-gpg-sign"
	ShimHook         = "conductor-hook"
)

// Event is one thing that happened at the jail boundary: a host command, a
// push or fetch, a signature, a tool call, a refusal. The daemon records each
// as an audit row and a live `conductor watch` event.
type Event struct {
	Type     string // host_command | git_push | git_fetch | sign | tool_call | egress | jail
	Dispatch string
	Label    string // "fix acme/app#43" — the dispatch as the operator reads it
	Repo     string
	Number   int
	Step     string
	Status   string // ok | refused | exit N | degraded | …
	Detail   string // the command line, refs, reason
	Reason   string // why it was refused ("" = not refused)
	Fields   map[string]any
}

// Manager is the daemon-lifetime jail registry.
type Manager struct {
	// Root holds per-dispatch dirs (<root>/<id>/{broker,tmp,cow}).
	Root string
	// SelfExe resolves conductor's own binary (bound into the jail, run as
	// the shims).
	SelfExe func() (string, error)
	// LookPath resolves host binaries (exec.LookPath).
	LookPath func(string) (string, error)
	// Home is the operator's home (os.UserHomeDir).
	Home string
	// Sensitive are the daemon's own state/config dirs — never visible to a
	// jail or a host command, never a valid host-command argument.
	Sensitive []string
	// Sockets are daemon sockets a jailed agent may connect to (the memory
	// tool socket its MCP server dials), bound in read-only at their own
	// paths — the one exception to "nothing of the state dir".
	Sockets []string
	// Emit records an Event (audit + watch). nil → dropped.
	Emit func(Event)
	// TargetClosed reports why the dispatch's own target takes no more
	// writes ("" = open): the gh and git profiles refuse every write for a
	// closed target. nil → never closed.
	TargetClosed func(d *Dispatch) string
	// ThreadTarget resolves a review-thread node id to its PR.
	ThreadTarget func(ctx context.Context, d *Dispatch, nodeID string) (repo string, number int, err error)
	// HostEgress mints an enforced egress endpoint for a host command whose
	// rule carries a network block (unix socket + credential).
	HostEgress func(allow []string, label string) (sock, cred string, revoke func(), err error)
	// HostEgressTCP is the loopback (TCP) endpoint, macOS's route.
	HostEgressTCP func(allow []string, label string) (addr, cred string, revoke func(), err error)
	// Signer overrides how a commit payload is signed (tests); nil → the
	// operator's configured git signing (see sign.go).
	Signer func(ctx context.Context, d *Dispatch, format string, payload []byte) (sig, status []byte, err error)

	mu   sync.Mutex
	live map[string]*Dispatch // broker token → dispatch
}

// LaunchSpec describes one jailed launch.
type LaunchSpec struct {
	DispatchID string
	Tool       string // the recipe's tool: claude-code | codex | gemini | …
	Argv0      string // the binary the launch execs (resolved on the host)
	Workspace  string
	Repo       string
	Number     int
	IsPR       bool
	HeadBranch string
	BaseRef    string
	Step       string
	Label      string
	// ReadOnly: a review step — its gh and git write nothing unless the
	// operator's allow list names the write (hostcmd's gh/git binding).
	ReadOnly bool
	// Layers are the isolation blocks in resolution order: global, runtime,
	// step (nil entries skipped). StepLayer reports the last is a step's.
	Layers    []*config.IsolationConfig
	StepLayer bool
	Intent    *config.IntentRules
	// UserToken is the operator's GitHub token, for conductor's own lookups
	// on the dispatch's behalf (PR state, review threads) — never the jail's,
	// never a host command's.
	UserToken string
	// Keychain (macOS) opts the jail into the Keychain's Security services.
	Keychain bool
	// Git is the worktree's git layout resolved on a previous turn (resume
	// turns must not re-read the agent-writable .git pointer).
	Git *GitLayout
}

// GitLayout is a checkout's git layout, resolved on the host when the
// checkout is still conductor's own.
type GitLayout struct {
	GitDir    string // the checkout's own gitdir (<ws>/.git, or <common>/worktrees/<name>)
	CommonDir string // its .git (the dispatch clone's own; a linked worktree's shared one)
	OriginURL string // remote.origin.url
	// Base is the base clone's .git when the checkout is a per-dispatch
	// clone borrowing its objects (alternates): conductor's own repository,
	// never writable from the jail. conductor's git on the dispatch's
	// behalf — push, fetch, the signing check and signing config — runs
	// there, with a conductor-owned copy of the clone's own objects added
	// (snapshotObjects), so nothing the agent can write (the clone's config,
	// hooks, alternates, symlinks in its object store) steers it. "" = a
	// checkout of another shape (a linked worktree, a go-git clone), handled
	// as before.
	Base string
}

// trustedGitDir is where conductor runs git for the dispatch: the base clone
// when there is one, else the checkout's common dir.
func (g *GitLayout) trustedGitDir() string {
	if g.Base != "" {
		return g.Base
	}
	return g.CommonDir
}

// Dispatch is one jailed dispatch's broker-side record.
type Dispatch struct {
	LaunchSpec
	Token   string
	Dir     string // <root>/<id>
	TmpDir  string // host path of the jail's /tmp
	HostSet []string
	Denied  []string // host-set tools disabled by config
	m       *Manager

	mu       sync.Mutex
	closed   bool
	cleanups []func()
	seenNet  map[string]bool
	confined map[string]*sync.Mutex // per tool: its confined runs share a copy-on-write layer
}

// lockConfined serializes one tool's confined runs within the dispatch.
func (d *Dispatch) lockConfined(tool string) func() {
	d.mu.Lock()
	if d.confined == nil {
		d.confined = map[string]*sync.Mutex{}
	}
	mu := d.confined[tool]
	if mu == nil {
		mu = &sync.Mutex{}
		d.confined[tool] = mu
	}
	d.mu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// confinedLayer is the dispatch's copy-on-write layer over the workspace for
// tool's confined runs (an overlay's upper and work dirs on Linux; the upper
// alone on macOS): what `terraform init` writes (.terraform) is there for
// the dispatch's next `terraform plan`, and never in the workspace. It goes
// with the dispatch dir.
func (d *Dispatch) confinedLayer(tool string) (upper, work string, err error) {
	base := filepath.Join(d.Dir, "cow-ws", tool)
	upper, work = filepath.Join(base, "up"), filepath.Join(base, "wk")
	for _, p := range []string{upper, work} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			return "", "", err
		}
	}
	return upper, work, nil
}

// Launch is what Prepare hands the launcher.
type Launch struct {
	Binds []sandbox.BindMount
	Env   []string // added to the jailed process's environment
	// ClaudeSettings is the --settings JSON wiring claude-code's tool-call
	// hooks to the broker ("" for other tools).
	ClaudeSettings string
	// Seatbelt is the macOS profile extras (nil on Linux).
	Seatbelt *sandbox.AgentProfile
	Dispatch *Dispatch
	// Close tears the broker down; idempotent.
	Close func()
}

// toolState are the per-tool state paths bound read-write into the scratch
// home (home-relative). A missing one is skipped.
var toolState = map[string][]string{
	"claude-code": {".claude", ".claude.json", ".config/claude"},
	"claude":      {".claude", ".claude.json", ".config/claude"},
	"codex":       {".codex"},
	"gemini":      {".gemini"},
	"qwen":        {".qwen"},
	"goose":       {".config/goose", ".local/share/goose"},
}

// readOnlyHome are home-relative paths every jail sees read-only.
var readOnlyHome = []string{".gitconfig", ".config/git"}

// gitGlobalConfig is the operator's global git config when it lives where
// GIT_CONFIG_GLOBAL says rather than in ~/.gitconfig: the jail passes that
// variable through, so it must see the file too (read-only) — else the
// agent's git runs with no identity and no commit.gpgsign. "" when unset,
// missing, or inside conductor's own state/config.
func (m *Manager) gitGlobalConfig() string {
	p := os.Getenv("GIT_CONFIG_GLOBAL")
	if p == "" || !filepath.IsAbs(p) || p == os.DevNull {
		return ""
	}
	if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	for _, s := range m.Sensitive {
		if within(p, s) {
			return ""
		}
	}
	return p
}

// Prepare lays out a jailed launch and starts its broker. The caller wraps
// the launch with the returned binds (sandbox.LocalWrapDeps{Confine: true,
// ExtraBinds: …}) and appends Env.
func (m *Manager) Prepare(ctx context.Context, spec LaunchSpec) (*Launch, error) {
	if m.SelfExe == nil || m.LookPath == nil {
		return nil, fmt.Errorf("jail: manager not wired")
	}
	self, err := m.SelfExe()
	if err != nil {
		return nil, fmt.Errorf("jail: resolve conductor binary: %w", err)
	}
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	tok := make([]byte, 24)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	d := &Dispatch{LaunchSpec: spec, Token: hex.EncodeToString(tok), m: m, seenNet: map[string]bool{}}
	if err := os.MkdirAll(m.Root, 0o700); err != nil {
		return nil, fmt.Errorf("jail: state dir: %w", err)
	}
	d.Dir, err = os.MkdirTemp(m.Root, "j-")
	if err != nil {
		return nil, fmt.Errorf("jail: dispatch dir: %w", err)
	}
	d.TmpDir = filepath.Join(d.Dir, "tmp")
	brokerDir := filepath.Join(d.Dir, "broker")
	for _, p := range []string{d.TmpDir, brokerDir} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			d.cleanup()
			return nil, err
		}
	}
	_ = os.Chmod(d.TmpDir, 0o1777)

	if spec.Workspace != "" && spec.Git == nil {
		d.Git = ResolveGit(spec.Workspace)
	}
	d.HostSet, d.Denied = hostcmd.HostSet(spec.Layers, spec.StepLayer, m.LookPath)

	binds, prof, homeEnv, err := m.platformLayout(d, self)
	if err != nil {
		d.cleanup()
		return nil, err
	}
	stop, err := m.serve(d, filepath.Join(brokerDir, "broker.sock"))
	if err != nil {
		d.cleanup()
		return nil, err
	}
	d.addCleanup(stop)
	m.register(d)
	d.addCleanup(func() { m.unregister(d) })

	env := []string{
		"HOME=" + homeEnv,
		"TMPDIR=" + d.jailTmp(),
		EnvSock + "=" + d.sockPath(),
		EnvToken + "=" + d.Token,
		"PATH=" + d.binDir() + ":" + os.Getenv("PATH"),
		"CONDUCTOR_JAILED=1",
	}
	env = append(env, gitEnv(d)...)
	l := &Launch{Binds: binds, Seatbelt: prof, Env: env, Dispatch: d, Close: d.Close}
	if spec.Tool == "claude-code" || spec.Tool == "claude" {
		l.ClaudeSettings = HookSettings(d.binDir())
		// claude-code writes its scratch under /tmp/claude-<uid> unless told
		// otherwise; keep it in the dispatch's own temp dir.
		l.Env = append(l.Env, "CLAUDE_CODE_TMPDIR="+d.jailTmp())
	}
	m.emit(d, Event{Type: "jail", Status: "ok", Detail: fmt.Sprintf("jail up: host commands %s", strings.Join(d.HostSet, ","))})
	return l, nil
}

// binDir is where the jail's shims live, as the jail sees it: a fixed path
// on Linux (a tmpfs in the jail), the dispatch's own dir on macOS.
func (d *Dispatch) binDir() string {
	if isDarwin {
		return filepath.Join(d.Dir, "bin")
	}
	return BinDir
}

// sockPath is the broker socket as the jail sees it.
func (d *Dispatch) sockPath() string {
	if isDarwin {
		return filepath.Join(d.Dir, "broker", "broker.sock")
	}
	return SockPath
}

// platformLayout is the OS's jail layout: Linux mounts (layout); macOS a
// Seatbelt profile plus a per-dispatch scratch home and shim dir.
func (m *Manager) platformLayout(d *Dispatch, self string) ([]sandbox.BindMount, *sandbox.AgentProfile, string, error) {
	if isDarwin {
		return m.layoutDarwin(d, self)
	}
	b, err := m.layout(d, self)
	return b, nil, m.Home, err
}

// layout builds the allow-list for d beyond the workspace itself (which the
// sandbox binds read-write as the launch dir).
func (m *Manager) layout(d *Dispatch, self string) ([]sandbox.BindMount, error) {
	home := m.Home
	var b []sandbox.BindMount
	add := func(bm sandbox.BindMount) { b = append(b, bm) }

	if home != "" {
		add(sandbox.BindMount{Path: home, Tmpfs: true})
		for _, rel := range toolState[d.Tool] {
			add(sandbox.BindMount{Path: filepath.Join(home, rel), Optional: true})
		}
		for _, rel := range readOnlyHome {
			add(sandbox.BindMount{Path: filepath.Join(home, rel), RO: true, Optional: true})
		}
	}
	if p := m.gitGlobalConfig(); p != "" {
		add(sandbox.BindMount{Path: p, RO: true, Optional: true})
	}
	// The agent CLI's own install when it lives outside /usr, /opt (e.g. the
	// native installer's ~/.local/bin/claude → ~/.local/share/claude/…).
	if d.Argv0 != "" {
		for _, p := range binaryRoots(d.Argv0, m.LookPath) {
			if !visibleInBase(p) {
				add(sandbox.BindMount{Path: p, RO: true, Optional: true})
			}
		}
	}
	// The per-dispatch /tmp, visible to host commands at the same host path.
	add(sandbox.BindMount{Path: "/tmp", Src: d.TmpDir})

	// conductor's own surface: the binary, the shims, the broker socket.
	add(sandbox.BindMount{Path: RunDir, Tmpfs: true})
	add(sandbox.BindMount{Path: SelfPath, Src: self, RO: true})
	// …and at its own host path, which tool-server commands name.
	if raw, err := m.SelfExe(); err == nil {
		for _, p := range []string{raw, self} {
			if !visibleInBase(p) {
				add(sandbox.BindMount{Path: p, Src: self, RO: true})
			}
		}
	}
	for _, sock := range m.Sockets {
		add(sandbox.BindMount{Path: sock, RO: true, Optional: true})
	}
	add(sandbox.BindMount{Path: BrokerDir, Src: filepath.Join(d.Dir, "broker")})
	shims := []string{ShimRemoteHelper, ShimSSHSign, ShimGPGSign, ShimHook}
	shims = append(shims, d.HostSet...)
	shims = append(shims, d.Denied...)
	for _, s := range shims {
		add(sandbox.BindMount{Path: filepath.Join(BinDir, s), Link: "../conductor"})
	}
	// conductor itself (`conductor call step.done`, the skill CLI).
	add(sandbox.BindMount{Path: filepath.Join(BinDir, "conductor"), Link: "../conductor"})
	// Bind conductor over every credentialed host-set tool's real path, so an
	// absolute path (`/usr/bin/gh`) reaches the broker too. Native-capable
	// tools (npm, pnpm) keep their real binary: without credentials in the
	// jail a direct run of it can do nothing the jail forbids.
	seen := map[string]bool{}
	for _, tool := range append(append([]string(nil), d.HostSet...), d.Denied...) {
		if nativeCapable[tool] {
			continue
		}
		for _, p := range realPaths(tool, m.LookPath) {
			if seen[p] || !visibleInJail(p, b) {
				continue
			}
			seen[p] = true
			add(sandbox.BindMount{Path: p, Src: self, RO: true})
		}
	}
	// Git, a per-dispatch clone: the clone is the workspace, .git included,
	// all of it read-write — its config, hooks and alternates are the
	// agent's own, and conductor never runs git on them (see
	// GitLayout.Base). What it borrows — the base clone's object store — is
	// read-only; the rest of the base's .git is an empty read-only dir, so
	// a write there fails rather than landing in the scratch home.
	if g := d.Git; g != nil && g.Base != "" {
		shadow := filepath.Join(d.Dir, "base-git")
		if err := os.MkdirAll(filepath.Join(shadow, "objects"), 0o755); err != nil {
			return nil, err
		}
		add(sandbox.BindMount{Path: g.Base, Src: shadow, RO: true})
		add(sandbox.BindMount{Path: filepath.Join(g.Base, "objects"), RO: true})
	} else if g := d.Git; g != nil && g.CommonDir != "" {
		// Another shape (a linked worktree, a go-git clone): the common dir
		// read-write (objects, refs), its code-execution surface read-only,
		// sibling worktrees hidden, this worktree's own gitdir read-write.
		for _, sub := range []string{"hooks", filepath.Join("objects", "info")} {
			_ = os.MkdirAll(filepath.Join(g.CommonDir, sub), 0o755)
		}
		if !within(g.CommonDir, d.Workspace) {
			add(sandbox.BindMount{Path: g.CommonDir})
		}
		add(sandbox.BindMount{Path: filepath.Join(g.CommonDir, "config"), RO: true})
		add(sandbox.BindMount{Path: filepath.Join(g.CommonDir, "hooks"), RO: true})
		add(sandbox.BindMount{Path: filepath.Join(g.CommonDir, "objects", "info"), RO: true})
		if g.GitDir != g.CommonDir {
			add(sandbox.BindMount{Path: filepath.Join(g.CommonDir, "worktrees"), Tmpfs: true})
			add(sandbox.BindMount{Path: g.GitDir})
		}
	}
	// Operator-declared extra paths.
	for _, l := range d.Layers {
		if l == nil {
			continue
		}
		for _, p := range l.FS {
			add(sandbox.BindMount{Path: config.ExpandHome(p)})
		}
	}
	for _, s := range m.Sensitive {
		for _, bm := range b {
			if bm.Tmpfs || bm.Link != "" || bm.Src != "" || contains(m.Sockets, bm.Path) {
				continue
			}
			if within(bm.Path, s) && !within(bm.Path, d.Workspace) && (d.Git == nil || !within(bm.Path, d.Git.CommonDir)) &&
				(d.Git == nil || d.Git.Base == "" || bm.Path != filepath.Join(d.Git.Base, "objects")) {
				return nil, fmt.Errorf("jail: %s would expose conductor's own state/config (%s)", bm.Path, s)
			}
		}
	}
	return b, nil
}

// nativeCapable tools run natively in the jail except for their publish/auth
// verbs (hostcmd's npm profile).
var nativeCapable = map[string]bool{"npm": true, "pnpm": true}

// realPaths are the host paths a tool resolves to (the PATH hit and its
// symlink target).
func realPaths(tool string, lookPath func(string) (string, error)) []string {
	p, err := lookPath(tool)
	if err != nil {
		return nil
	}
	out := []string{}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		out = append(out, r)
	}
	return out
}

// binaryRoots are the host paths that must be readable for bin to run: the
// PATH hit, each symlink hop, the resolved file's directory, and (for a
// script) its interpreter's, recursively and bounded.
func binaryRoots(bin string, lookPath func(string) (string, error)) []string {
	seen := map[string]bool{}
	var out []string
	var walk func(p string, depth int)
	walk = func(p string, depth int) {
		if depth > 4 || p == "" {
			return
		}
		if !filepath.IsAbs(p) {
			lp, err := lookPath(p)
			if err != nil {
				return
			}
			p = lp
		}
		for hop := 0; hop < 8; hop++ {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
			fi, err := os.Lstat(p)
			if err != nil || fi.Mode()&os.ModeSymlink == 0 {
				break
			}
			t, err := os.Readlink(p)
			if err != nil {
				break
			}
			if !filepath.IsAbs(t) {
				t = filepath.Join(filepath.Dir(p), t)
			}
			p = filepath.Clean(t)
		}
		dir := filepath.Dir(p)
		if !seen[dir] {
			seen[dir] = true
			out = append(out, dir)
		}
		if interp := shebang(p); interp != "" {
			fields := strings.Fields(interp)
			if filepath.Base(fields[0]) == "env" && len(fields) > 1 {
				walk(fields[len(fields)-1], depth+1)
			} else {
				walk(fields[0], depth+1)
			}
		}
	}
	walk(bin, 0)
	// Keep only the directories (the files inside them come with them) and
	// anything outside a directory already listed.
	sort.Strings(out)
	var dirs []string
	for _, p := range out {
		fi, err := os.Stat(p)
		if err != nil || !fi.IsDir() {
			continue
		}
		dirs = append(dirs, p)
	}
	var keep []string
	for _, p := range dirs {
		covered := false
		for _, k := range keep {
			if within(p, k) {
				covered = true
				break
			}
		}
		if !covered {
			keep = append(keep, p)
		}
	}
	for _, p := range out {
		fi, err := os.Lstat(p)
		if err != nil || fi.IsDir() {
			continue
		}
		covered := false
		for _, k := range keep {
			if within(p, k) {
				covered = true
				break
			}
		}
		if !covered && fi.Mode()&os.ModeSymlink == 0 {
			keep = append(keep, p)
		}
	}
	return keep
}

func shebang(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 256)
	n, _ := f.Read(buf)
	s := string(buf[:n])
	if !strings.HasPrefix(s, "#!") {
		return ""
	}
	line, _, _ := strings.Cut(s[2:], "\n")
	return strings.TrimSpace(line)
}

// visibleInBase reports a path the jail's base system already shows.
func visibleInBase(p string) bool {
	for _, root := range []string{"/usr", "/etc", "/opt", "/bin", "/sbin", "/lib", "/lib64"} {
		if within(p, root) {
			return true
		}
	}
	return false
}

// visibleInJail reports whether p is inside the base system or a bound path.
func visibleInJail(p string, binds []sandbox.BindMount) bool {
	if visibleInBase(p) {
		return true
	}
	for _, b := range binds {
		if !b.Tmpfs && b.Link == "" && b.Src == "" && within(p, b.Path) {
			return true
		}
	}
	return false
}

func within(p, root string) bool {
	if root == "" || p == "" {
		return false
	}
	p, root = filepath.Clean(p), filepath.Clean(root)
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// ResolveGit reads a worktree's git layout. It is called while the checkout
// is still conductor's own (before the agent runs); later turns reuse it.
func ResolveGit(ws string) *GitLayout {
	dotgit := filepath.Join(ws, ".git")
	fi, err := os.Lstat(dotgit)
	if err != nil {
		return nil
	}
	g := &GitLayout{}
	if fi.IsDir() {
		g.GitDir, g.CommonDir = dotgit, dotgit
	} else {
		raw, err := os.ReadFile(dotgit)
		if err != nil {
			return nil
		}
		line := strings.TrimSpace(string(raw))
		if !strings.HasPrefix(line, "gitdir:") {
			return nil
		}
		gd := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
		if !filepath.IsAbs(gd) {
			gd = filepath.Join(ws, gd)
		}
		g.GitDir = filepath.Clean(gd)
		g.CommonDir = g.GitDir
		if c, err := os.ReadFile(filepath.Join(g.GitDir, "commondir")); err == nil {
			cd := strings.TrimSpace(string(c))
			if !filepath.IsAbs(cd) {
				cd = filepath.Join(g.GitDir, cd)
			}
			g.CommonDir = filepath.Clean(cd)
		}
	}
	g.OriginURL = gitConfigGet(g.CommonDir, "remote.origin.url")
	g.Base = borrowedBase(g)
	return g
}

// borrowedBase is the base clone a per-dispatch clone borrows its objects
// from — the one alternate conductor wrote at provisioning ("<base>/.git/
// objects") — or "" when the checkout is not such a clone.
func borrowedBase(g *GitLayout) string {
	if g.GitDir != g.CommonDir {
		return "" // a linked worktree
	}
	b, err := os.ReadFile(filepath.Join(g.CommonDir, "objects", "info", "alternates"))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 1 || !filepath.IsAbs(lines[0]) || filepath.Base(lines[0]) != "objects" {
		return ""
	}
	base := filepath.Dir(filepath.Clean(lines[0]))
	if _, err := os.Stat(filepath.Join(base, "HEAD")); err != nil {
		return ""
	}
	return base
}

func (m *Manager) register(d *Dispatch) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.live == nil {
		m.live = map[string]*Dispatch{}
	}
	m.live[d.Token] = d
}

func (m *Manager) unregister(d *Dispatch) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.live, d.Token)
}

func (m *Manager) lookup(tok string) *Dispatch {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.live[tok]
}

func (m *Manager) emit(d *Dispatch, e Event) {
	if m.Emit == nil {
		return
	}
	if d != nil {
		e.Dispatch, e.Label, e.Repo, e.Number, e.Step = d.DispatchID, d.Label, d.Repo, d.Number, d.Step
	}
	m.Emit(e)
}

// NetVerdict records one egress-proxy decision for the dispatch whose
// credential carried label (its token prefix): denials always, each allowed
// destination once.
func (m *Manager) NetVerdict(label string, allowed bool, hostport string) {
	m.mu.Lock()
	var d *Dispatch
	for _, x := range m.live {
		if x.DispatchID == label {
			d = x
			break
		}
	}
	m.mu.Unlock()
	if d == nil {
		return
	}
	if allowed {
		d.mu.Lock()
		dup := d.seenNet[hostport]
		d.seenNet[hostport] = true
		d.mu.Unlock()
		if dup {
			return
		}
		m.emit(d, Event{Type: "egress", Status: "ok", Detail: hostport})
		return
	}
	m.emit(d, Event{Type: "egress", Status: "refused", Detail: hostport, Reason: "egress_denied: not in the jail's network allowlist"})
}

func (d *Dispatch) addCleanup(f func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cleanups = append(d.cleanups, f)
}

// Close stops the broker and removes the per-dispatch dir.
func (d *Dispatch) Close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	cs := d.cleanups
	d.mu.Unlock()
	for i := len(cs) - 1; i >= 0; i-- {
		cs[i]()
	}
	d.cleanup()
}

func (d *Dispatch) cleanup() {
	if d.Dir != "" {
		_ = os.RemoveAll(d.Dir)
	}
}

// deadline bounds one host command.
const hostCommandTimeout = 30 * time.Minute
