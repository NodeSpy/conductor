package agentsandbox

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Detection. Containment decides what an agent can reach; detection notices
// an agent trying what containment exists for — a tunnel tool, a shell
// wired to a socket, a read of a credential it was never given, a rewrite
// of the git files the operator's own git trusts — and responds:
//
//   - kill (the default): the launch's processes are killed, the event
//     audited and reported;
//   - alert: audited and reported only;
//   - off: no watcher, no tripwires, no git guard.
//
// A git rewrite is undone whatever the action (except off): it is aimed at
// whoever runs git in the workspace next — the operator.
//
// The watcher runs host-side, in whichever process wrapped the launch (the
// daemon, or `conductor sandbox exec`), for the launch's lifetime: it finds
// the launch's processes by a marker in their environment (the sandbox's
// root process is started with it) and their descendants.

// Detection actions (sandbox.detect.action).
const (
	DetectKill  = "kill"
	DetectAlert = "alert"
	DetectOff   = "off"
)

// markerEnv carries a launch's random marker: the watcher's handle on the
// processes it started.
const markerEnv = "CONDUCTOR_SANDBOX_LAUNCH"

// DefaultDetectInterval is how often the process watcher scans.
const DefaultDetectInterval = 3 * time.Second

// procInfo is one process the watcher sees.
type procInfo struct {
	pid, ppid int
	pgid      int
	start     uint64 // a start time, to never mistake a reused pid
	comm      string
	args      []string
	exe       string
	marked    bool
	// Linux: what fds 0-2 are, and whether the process holds a pty master
	// and an inet socket.
	stdio      [3]string
	ptyMaster  bool
	inetSocket bool
}

// tunnelTools are programs whose job is a channel out: none is part of
// checking out, building, testing or committing code, and the egress proxy
// carries only TLS to allowed hosts — what one of these is for is getting
// around it.
var tunnelTools = map[string]bool{
	"ssh": true, "sshd": true, "dropbear": true, "dbclient": true, "autossh": true, "sshuttle": true, "sshpass": true,
	"socat": true, "nc": true, "ncat": true, "netcat": true, "nc.openbsd": true, "nc.traditional": true, "telnet": true,
	"websocat": true, "wstunnel": true, "chisel": true, "cloudflared": true, "ngrok": true, "frpc": true, "frps": true,
	"bore": true, "gost": true, "stunnel": true, "stunnel4": true, "proxytunnel": true, "corkscrew": true,
	"tailscale": true, "tailscaled": true, "iodine": true, "ptunnel": true, "tor": true, "rathole": true,
	"pwncat": true, "pwncat-cs": true, "ligolo": true, "ligolo-ng": true, "sliver": true, "mosh": true, "mosh-client": true,
	"openvpn": true, "wireguard-go": true, "wg-quick": true, "boringtun": true, "inlets": true, "localtunnel": true, "lt": true,
}

// shells are interpreters a reverse shell runs: one with an inet socket
// for stdin or stdout is one.
var shells = map[string]bool{
	"sh": true, "bash": true, "dash": true, "zsh": true, "ash": true, "ksh": true, "mksh": true, "fish": true,
	"csh": true, "tcsh": true, "busybox": true, "python": true, "python3": true, "perl": true, "ruby": true,
	"php": true, "lua": true, "node": true, "pwsh": true, "irb": true,
}

// verdict is a detection.
type verdict struct {
	kind   string // "tunnel_tool", "reverse_shell", "pty_relay"
	reason string
}

// progName is the program a process runs, by its argv[0] or comm.
func (p procInfo) progName() string {
	n := p.comm
	if len(p.args) > 0 && p.args[0] != "" {
		n = p.args[0]
	}
	n = strings.ToLower(filepath.Base(n))
	// python3.12 → python3, a versioned interpreter
	if i := strings.IndexFunc(n, func(r rune) bool { return r == '.' || (r >= '0' && r <= '9') }); i > 0 {
		if base := n[:i]; shells[base] || shells[base+"3"] {
			return base
		}
	}
	return n
}

// classify judges one process. root: the agent's own top process (its CLI
// holds sockets and may hold ptys for the commands it runs); self: a
// conductor process (the forwarder, `conductor call`).
func classify(p procInfo, allow map[string]bool, root, self bool) *verdict {
	if self {
		return nil
	}
	name := p.progName()
	if allow[name] {
		return nil
	}
	if tunnelTools[name] {
		return &verdict{"tunnel_tool", fmt.Sprintf("%s runs (%s)", name, argString(p.args))}
	}
	if name == "openssl" {
		for _, a := range p.args[1:] {
			if a == "s_client" || a == "s_server" {
				return &verdict{"tunnel_tool", "openssl " + a + " (" + argString(p.args) + ")"}
			}
		}
	}
	if shells[strings.TrimRight(name, "0123456789.")] || shells[name] {
		for fd := 0; fd < 2; fd++ {
			if p.stdio[fd] == "inet" {
				return &verdict{"reverse_shell", fmt.Sprintf("%s with an inet socket on fd %d (%s)", name, fd, argString(p.args))}
			}
		}
	}
	if !root && p.ptyMaster && p.inetSocket {
		return &verdict{"pty_relay", fmt.Sprintf("%s holds a terminal and a network socket (%s)", name, argString(p.args))}
	}
	return nil
}

func argString(args []string) string {
	s := strings.Join(args, " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// DetectSpec configures one launch's detection.
// The zero value is the strict default: kill, tripwires on.
type DetectSpec struct {
	// Action is DetectKill ("" too), DetectAlert or DetectOff.
	Action   string        `json:"action,omitempty"`
	Interval time.Duration `json:"interval,omitempty"`
	// NoTripwires: plant no decoy credentials (sandbox.detect.tripwires:
	// false).
	NoTripwires bool `json:"no_tripwires,omitempty"`
	// Allow are programs exempt from the tunnel-tool rule.
	Allow []string `json:"allow,omitempty"`
	// AllowTunnels, AllowReverseShell and AllowPty are the launch's scoped
	// allowances (see Allowance): what they cover is audited as allowed
	// (by AllowedBy) and left running.
	AllowTunnels      []string `json:"allow_tunnels,omitempty"`
	AllowReverseShell bool     `json:"allow_reverse_shell,omitempty"`
	AllowPty          bool     `json:"allow_pty,omitempty"`
	AllowedBy         string   `json:"allowed_by,omitempty"`
}

// allows reports whether the launch's allowance covers a verdict of kind
// on program name.
func (s DetectSpec) allows(kind, name string) bool {
	switch kind {
	case "tunnel_tool":
		for _, t := range s.AllowTunnels {
			if strings.EqualFold(t, name) {
				return true
			}
		}
	case "reverse_shell":
		return s.AllowReverseShell
	case "pty_relay":
		return s.AllowPty
	}
	return false
}

func (s DetectSpec) action() string {
	if s.Action == "" {
		return DetectKill
	}
	return s.Action
}

// detector is one launch's watcher.
type detector struct {
	spec   DetectSpec
	run    string
	marker string
	self   string
	allow  map[string]bool
	event  func(kind string, fields map[string]any)
	list   func() ([]procInfo, error)
	signal func(pid int, start uint64) error

	git   *gitGuard
	wires []*tripwire
	// off: detection is off — only the disk guard runs.
	off bool
	// diskLow names a filesystem of the launch below the disk guard's
	// minimum ("" = fine; nil = no guard).
	diskLow func() string

	// groups are process groups a launch process leads (it called setsid
	// or setpgid): a member that later leaves the tree — double-forked,
	// its environment dropped — is still the launch's.
	groups map[int]bool
	// roots are the launch's root processes its launcher named
	// (LaunchStarted), by pid and kernel start time: the tree's top even
	// where the marker cannot be read (macOS hides the environment of
	// platform and hardened-runtime binaries — sandbox-exec, ssh, sh).
	roots map[int]uint64

	closeOnce sync.Once
	mu        sync.Mutex
	reported  map[string]bool
	killed    bool
	stop      chan struct{}
	done      sync.WaitGroup
}

func newMarker() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// start runs the watcher loops until close.
func (d *detector) start() {
	d.stop = make(chan struct{})
	d.reported = map[string]bool{}
	iv := d.spec.Interval
	if iv <= 0 {
		iv = DefaultDetectInterval
	}
	d.done.Add(1)
	go func() {
		defer d.done.Done()
		t := time.NewTicker(iv)
		defer t.Stop()
		for {
			select {
			case <-d.stop:
				return
			case <-t.C:
				if !d.off {
					d.scan()
					d.checkGit()
				}
				d.checkDisk()
			}
		}
	}()
	for _, w := range d.wires {
		w := w
		d.done.Add(1)
		go func() {
			defer d.done.Done()
			w.watch(d.stop, func(reader string) {
				d.detected("tripwire", fmt.Sprintf("decoy credential %s read%s", w.rel, reader), true)
			})
		}()
	}
}

// launchProcs are the launch's processes: every one carrying the marker,
// and their descendants (a child that dropped its environment is still
// one).
func launchProcs(all []procInfo) []procInfo {
	byParent := map[int][]procInfo{}
	var out []procInfo
	seen := map[int]bool{}
	var queue []procInfo
	for _, p := range all {
		byParent[p.ppid] = append(byParent[p.ppid], p)
		if p.marked && p.pid > 1 {
			queue = append(queue, p)
		}
	}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if seen[p.pid] {
			continue
		}
		seen[p.pid] = true
		out = append(out, p)
		queue = append(queue, byParent[p.pid]...)
	}
	return out
}

// agentRoot is the agent's own top process: the first descendant of the
// launch that is not conductor's (unshare, the forwarder, sandbox-exec).
func agentRoot(procs []procInfo, self string) int {
	in := map[int]procInfo{}
	for _, p := range procs {
		in[p.pid] = p
	}
	best, depth := 0, 1<<30
	for _, p := range procs {
		if p.exe == self || isLauncher(p) {
			continue
		}
		d := 0
		for q, ok := in[p.ppid]; ok && d < 64; q, ok = in[q.ppid] {
			d++
		}
		if d < depth {
			best, depth = p.pid, d
		}
	}
	return best
}

// isLauncher reports the sandbox's own wrapper processes.
func isLauncher(p procInfo) bool {
	switch p.progName() {
	case "unshare", "sandbox-exec", "systemd-run":
		return true
	}
	return false
}

func (d *detector) scan() {
	all, err := d.list()
	if err != nil {
		return
	}
	procs := launchProcs(all)
	d.noteGroups(procs)
	root := agentRoot(procs, d.self)
	for _, p := range procs {
		if v := classify(p, d.allow, p.pid == root, d.self != "" && p.exe == d.self); v != nil {
			reason := fmt.Sprintf("pid %d: %s", p.pid, v.reason)
			if d.spec.allows(v.kind, p.progName()) {
				d.allowed(v.kind, reason)
				continue
			}
			d.detected(v.kind, reason, false)
		}
	}
}

// detected responds to one detection: reported once per kind and reason,
// the launch killed when the action is kill.
func (d *detector) detected(kind, reason string, always bool) {
	d.mu.Lock()
	key := kind + "\x00" + reason
	first := !d.reported[key]
	d.reported[key] = true
	kill := d.spec.action() == DetectKill && !d.killed
	if kill {
		d.killed = true
	}
	d.mu.Unlock()
	if first || always {
		if d.event != nil {
			d.event("detection", map[string]any{"run": d.run, "kind": kind, "reason": reason, "action": d.spec.action()})
		}
	}
	if kill {
		d.killAll()
	}
}

// allowed audits a detection the launch's allowance covers, once per kind
// and reason; nothing is killed.
func (d *detector) allowed(kind, reason string) {
	d.mu.Lock()
	key := "allowed\x00" + kind + "\x00" + reason
	first := !d.reported[key]
	d.reported[key] = true
	d.mu.Unlock()
	if first && d.event != nil {
		d.event("detection", map[string]any{"run": d.run, "kind": kind, "reason": reason, "action": "allowed", "allowed_by": d.spec.AllowedBy})
	}
}

// killAll kills every process of the launch. Only a pid the watcher has
// just seen carrying the marker, or descending from one, is signalled, and
// never this process, its parent or pid 1.
func (d *detector) killAll() int {
	all, err := d.list()
	if err != nil {
		return 0
	}
	launch := launchProcs(all)
	d.noteGroups(launch)
	d.mu.Lock()
	in := map[int]bool{}
	for _, p := range launch {
		in[p.pid] = true
	}
	for _, p := range all {
		if !in[p.pid] && p.pgid > 1 && d.groups[p.pgid] && p.pgid != ownPgid() {
			launch = append(launch, p)
		}
	}
	d.mu.Unlock()
	n := 0
	for _, p := range launch {
		if !safeToSignal(p.pid) {
			continue
		}
		if d.signal(p.pid, p.start) == nil {
			n++
		}
	}
	return n
}

// startDetection arms p's detection around a launch and returns the
// marker its root process must carry and the closer (nil, nil: off).
func startDetection(p Policy, self string, event func(string, map[string]any)) (string, func(), error) {
	off := p.Detect.action() == DetectOff
	diskOn := !p.Limits.Off && !p.Limits.DiskOff
	if off {
		removeTripwires(p.Home)
		if !diskOn {
			return "", nil, nil
		}
	}
	d := &detector{
		spec: p.Detect, run: p.EgressSession().Run, marker: newMarker(), self: self,
		allow: map[string]bool{}, event: event, signal: signalProc, off: off,
	}
	if !off {
		d.git = newGitGuard(p.Workspace)
	}
	if diskOn {
		lim, ws, home := p.Limits, p.Workspace, p.Home
		d.diskLow = func() string { return lim.diskLow(ws, home) }
	}
	for _, a := range p.Detect.Allow {
		d.allow[strings.ToLower(a)] = true
	}
	d.list = listProcs(d.marker, d.isRoot)
	if p.Detect.NoTripwires {
		removeTripwires(p.Home)
	} else if !off {
		ws, err := plantTripwires(p.Home)
		if err != nil {
			return "", nil, err
		}
		d.wires = ws
	}
	launches.Store(d.marker, d)
	d.start()
	return d.marker, d.close, nil
}

// isRoot reports pid as a root the launcher named, still running as
// itself (a root that is not is forgotten).
func (d *detector) isRoot(pid int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	start, ok := d.roots[pid]
	if !ok {
		return false
	}
	if st, ok := procStart(pid); !ok || st != start {
		delete(d.roots, pid)
		return false
	}
	return true
}

// started records pid as a root of the launch.
func (d *detector) started(pid int) {
	start, ok := procStart(pid)
	if !ok || pid <= 1 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.roots == nil {
		d.roots = map[int]uint64{}
	}
	d.roots[pid] = start
}

// launches are the running detectors, by marker.
var launches sync.Map

// LaunchStarted names pid as the root process of the launch whose
// environment env is (its marker): the launcher calls it right after
// starting the wrapped command. A no-op for a launch with no detection.
func LaunchStarted(env []string, pid int) {
	for _, kv := range env {
		if m, ok := strings.CutPrefix(kv, markerEnv+"="); ok {
			if d, ok := launches.Load(m); ok {
				d.(*detector).started(pid)
			}
			return
		}
	}
}

// noteGroups remembers each process group a launch process leads.
func (d *detector) noteGroups(procs []procInfo) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.groups == nil {
		d.groups = map[int]bool{}
	}
	for _, p := range procs {
		if p.pgid == p.pid && p.pid > 1 {
			d.groups[p.pgid] = true
		}
	}
}

// close stops the watcher, runs the git guard one last time and kills
// whatever of the launch is left (a daemonized child the launch's own end
// did not take down).
func (d *detector) close() {
	d.closeOnce.Do(d.shutdown)
}

func (d *detector) shutdown() {
	defer launches.Delete(d.marker)
	if d.stop != nil {
		close(d.stop)
		d.done.Wait()
	}
	d.checkGit()
	if n := d.killAll(); n > 0 && d.event != nil {
		d.event("leftover_killed", map[string]any{"run": d.run, "processes": n})
	}
	// No watcher answers the decoys now: a reader (a recursive grep over
	// the state dir, a backup) would block on one forever.
	for _, w := range d.wires {
		w.remove()
	}
}

// checkDisk stops the launch when its disk is nearly full — whatever the
// detection action: it is a limit, not a detection.
func (d *detector) checkDisk() {
	if d.diskLow == nil {
		return
	}
	why := d.diskLow()
	if why == "" {
		return
	}
	d.mu.Lock()
	first := !d.reported["disk"]
	d.reported["disk"] = true
	d.mu.Unlock()
	if !first {
		return
	}
	if d.event != nil {
		d.event("limit", map[string]any{"run": d.run, "kind": "disk_low", "reason": why, "action": "kill"})
	}
	d.killAll()
}

func (d *detector) checkGit() {
	if d.git == nil {
		return
	}
	for _, r := range d.git.check() {
		d.detected("git_tamper", r, true)
	}
}
