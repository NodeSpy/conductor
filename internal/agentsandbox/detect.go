package agentsandbox

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// Detection. Containment decides what an agent can reach; detection notices
// an agent trying what containment exists for — a tunnel tool, a shell
// wired to a socket, a read of a credential it was never given, a rewrite
// of the git files the operator's own git trusts — and responds:
//
//   - kill (the default): the launch's processes are killed, the event
//     audited and reported;
//   - terminate: only the flagged process and its descendants are killed,
//     the event audited and reported, and the launch runs on (a tripwire
//     read or git tamper names no process to stop, so it still kills);
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
	DetectKill      = "kill"
	DetectTerminate = "terminate"
	DetectAlert     = "alert"
	DetectOff       = "off"
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
	// rss (bytes) and threads: what the process holds, for the launch's
	// usage where no cgroup is read.
	rss     uint64
	threads int
	// What fds 0-2 are, every fd holding an inet socket, and (Linux)
	// whether the process holds a pty master.
	stdio      [3]string
	inetFDs    []int
	ptyMaster  bool
	inetSocket bool
	// id is what the process's binary is, when it is one of the host's
	// known shells, interpreters or tunnel tools (see identity.go).
	id identity
}

// toolName is the program a process runs: its binary's identity when known
// (a renamed copy is what it is a copy of), else its name.
func (p procInfo) toolName() string {
	if p.id.name != "" {
		return p.id.name
	}
	return p.progName()
}

// class is the process's program class: its binary's, else its name's.
func (p procInfo) class() string {
	if p.id.class != "" {
		return p.id.class
	}
	name := p.progName()
	if c := classOf(name); c != "" {
		return c
	}
	return classOf(strings.TrimRight(name, "0123456789."))
}

// renamed is a note for a process whose name is not its binary's.
func (p procInfo) renamed() string {
	if p.id.name != "" && p.id.name != p.progName() {
		return ", which is " + p.id.name + " run as " + p.progName()
	}
	return ""
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

// classify judges one process: its first verdict, or nil.
func classify(p procInfo, allow map[string]bool, root, self bool) *verdict {
	if vs := verdicts(p, allow, root, self); len(vs) > 0 {
		return &vs[0]
	}
	return nil
}

// verdicts judges one process, most telling first: what it does with a
// socket (a shell's wiring, a pty relay) before what it is named (a tunnel
// tool), so an allowance for a tunnel tool's name never covers a shell
// wired to a socket. root: the agent's own top process (its CLI holds
// sockets and may hold ptys for the commands it runs); self: a conductor
// process (the forwarder, `conductor call`). allow exempts programs by
// name (sandbox.detect.allow), and only a binary identified as that
// program: one known by name alone is judged like any other.
func verdicts(p procInfo, allow map[string]bool, root, self bool) []verdict {
	if self {
		return nil
	}
	name := p.toolName()
	if allow[name] && p.id.name != "" {
		return nil
	}
	var out []verdict
	class := p.class()
	stdioFD := -1
	for fd := 0; fd < 2; fd++ {
		if p.stdio[fd] == "inet" {
			stdioFD = fd
			break
		}
	}
	switch class {
	case classShell:
		// A shell holds no network socket in a build, on any descriptor.
		if len(p.inetFDs) > 0 {
			out = append(out, verdict{"reverse_shell", fmt.Sprintf("%s with an inet socket on fd %d (%s%s)", name, p.inetFDs[0], argString(p.args), p.renamed())})
		} else if stdioFD >= 0 {
			out = append(out, verdict{"reverse_shell", fmt.Sprintf("%s with an inet socket on fd %d (%s%s)", name, stdioFD, argString(p.args), p.renamed())})
		}
	case classInterp:
		// An interpreter's own sockets are its work (pip, npm); its stdin
		// or stdout being one is a shell's.
		if stdioFD >= 0 {
			out = append(out, verdict{"reverse_shell", fmt.Sprintf("%s with an inet socket on fd %d (%s%s)", name, stdioFD, argString(p.args), p.renamed())})
		}
	case classTunnel:
		// A tunnel tool dials its socket; a socket for its stdin or stdout
		// is a shell's wiring, whatever the program is called.
		if stdioFD >= 0 {
			out = append(out, verdict{"reverse_shell", fmt.Sprintf("%s with an inet socket on fd %d, a shell's wiring (%s%s)", name, stdioFD, argString(p.args), p.renamed())})
		}
	}
	if !root && p.ptyMaster && p.inetSocket {
		out = append(out, verdict{"pty_relay", fmt.Sprintf("%s holds a terminal and a network socket (%s)", name, argString(p.args))})
	}
	if class == classTunnel {
		out = append(out, verdict{"tunnel_tool", fmt.Sprintf("%s runs (%s%s)", name, argString(p.args), p.renamed())})
	} else if name == "openssl" {
		for _, a := range p.args[1:] {
			if a == "s_client" || a == "s_server" {
				out = append(out, verdict{"tunnel_tool", "openssl " + a + " (" + argString(p.args) + ")"})
				break
			}
		}
	}
	return out
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
	// Action is DetectKill ("" too), DetectTerminate, DetectAlert or DetectOff.
	Action   string        `json:"action,omitempty"`
	Interval time.Duration `json:"interval,omitempty"`
	// NoTripwires: plant no decoy credentials (sandbox.detect.tripwires:
	// false).
	NoTripwires bool `json:"no_tripwires,omitempty"`
	// Allow are programs exempt from every rule (sandbox.detect.allow): a
	// binary identified as one of them, never a name alone.
	Allow []string `json:"allow,omitempty"`
	// AllowTunnels, AllowReverseShell and AllowPty are the launch's scoped
	// allowances (see Allowance): what they cover is audited as allowed
	// (by AllowedBy) and left running.
	AllowTunnels      []string `json:"allow_tunnels,omitempty"`
	AllowReverseShell bool     `json:"allow_reverse_shell,omitempty"`
	AllowPty          bool     `json:"allow_pty,omitempty"`
	AllowedBy         string   `json:"allowed_by,omitempty"`
	// Connections is what an egress tunnel of a suspicious shape gets
	// (sandbox.ShapeAlert, ShapeClose, ShapeOff; "" = alert).
	Connections string `json:"connections,omitempty"`
	// ShapeAllowed / ShapeAllowAll: tunnels the allowance covers, whose
	// shape is audited as allowed (see sandbox.SessionOpts).
	ShapeAllowed  []string `json:"shape_allowed,omitempty"`
	ShapeAllowAll bool     `json:"shape_allow_all,omitempty"`
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

// covers reports the launch's allowance covering one verdict on p. A
// tunnel allowance names a program, and covers only a binary identified as
// that program (the host's, or a copy of it): a binary the host does not
// know is not covered by taking the name.
func (d *detector) covers(kind string, p procInfo) bool {
	if kind == "tunnel_tool" && p.id.name == "" {
		return false
	}
	return d.spec.allows(kind, p.toolName())
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
	// usage: the launch's peaks against its caps (nil: not measured);
	// identity names the agent on its usage event.
	usage    *launchUsage
	identity string
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
				if d.hasKilled() {
					// The launch is killed: nothing of it is left to
					// watch (close still sweeps for leftovers).
					return
				}
			}
		}
	}()
	if d.usage != nil && d.usage.unit != "" {
		d.done.Add(1)
		go func() {
			defer d.done.Done()
			t := time.NewTicker(usageSampleInterval)
			defer t.Stop()
			for {
				select {
				case <-d.stop:
					return
				case <-t.C:
					d.usage.sampleCgroup(d.rootPids())
				}
			}
		}()
	}
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
	d.noteGroups(procs, all)
	if d.usage != nil {
		d.usage.sampleProcs(procs)
	}
	root := agentRoot(procs, d.self)
	for _, p := range procs {
		vs := verdicts(p, d.allow, p.pid == root, d.self != "" && p.exe == d.self)
		if len(vs) == 0 {
			continue
		}
		// Allowed only when the launch's allowance covers every verdict:
		// a tunnel tool's allowance never covers a shell wired to a socket,
		// and such a process is never audited as an allowed tunnel.
		v := vs[0]
		covered := true
		for _, w := range vs {
			if !d.covers(w.kind, p) {
				v, covered = w, false
				break
			}
		}
		reason := fmt.Sprintf("pid %d: %s", p.pid, v.reason)
		if covered {
			d.allowed(v.kind, reason)
			continue
		}
		if d.spec.action() == DetectTerminate && p.pid != root {
			d.report(v.kind, reason, false)
			d.terminate(p, procs)
			continue
		}
		d.detected(v.kind, reason, false)
	}
}

// detected responds to one detection: reported once per kind and reason,
// the launch killed when the action is kill or terminate (which reaches here
// only for a detection with no process of its own to stop).
func (d *detector) detected(kind, reason string, always bool) {
	d.mu.Lock()
	a := d.spec.action()
	kill := (a == DetectKill || a == DetectTerminate) && !d.killed
	if kill {
		d.killed = true
	}
	d.mu.Unlock()
	d.report(kind, reason, always)
	if kill {
		d.killAll()
	}
}

// hasKilled reports the watcher killed its launch.
func (d *detector) hasKilled() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.killed
}

// report audits a detection once per kind and reason (every time when
// always).
func (d *detector) report(kind, reason string, always bool) {
	d.mu.Lock()
	key := kind + "\x00" + reason
	first := !d.reported[key]
	d.reported[key] = true
	d.mu.Unlock()
	if (first || always) && d.event != nil {
		d.event("detection", map[string]any{"run": d.run, "kind": kind, "reason": reason, "action": d.spec.action()})
	}
}

// terminate kills p and its descendants among procs (the launch's), bound
// to the start times this listing saw, never this process, its parent or
// pid 1. The rest of the launch runs on.
func (d *detector) terminate(p procInfo, procs []procInfo) {
	byParent := map[int][]procInfo{}
	for _, c := range procs {
		byParent[c.ppid] = append(byParent[c.ppid], c)
	}
	seen := map[int]bool{}
	for queue := []procInfo{p}; len(queue) > 0; queue = queue[1:] {
		c := queue[0]
		if seen[c.pid] {
			continue
		}
		seen[c.pid] = true
		queue = append(queue, byParent[c.pid]...)
		if !safeToSignal(c.pid) {
			continue
		}
		if c.start == 0 {
			st, ok := procStart(c.pid)
			if !ok {
				continue
			}
			c.start = st
		}
		_ = d.signal(c.pid, c.start)
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
	d.noteGroups(launch, all)
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
		if p.start == 0 {
			// Every kill is bound to the process this listing saw: its
			// start time now, or no signal at all.
			st, ok := procStart(p.pid)
			if !ok {
				continue
			}
			p.start = st
		}
		if d.signal(p.pid, p.start) == nil {
			n++
		}
	}
	return n
}

// startDetection arms p's detection around a launch and returns the
// marker its root process must carry and the closer (nil, nil: off). unit
// is the launch's scope unit ("" when it has no caps), capped at memory
// and pids: its usage is measured.
func startDetection(p Policy, self string, caps *sandbox.Spec, event func(string, map[string]any)) (string, func(), error) {
	unit, capMem, capPids := "", "", 0
	if caps != nil {
		unit, capMem, capPids = caps.Unit, caps.Memory, caps.Pids
	}
	off := p.Detect.action() == DetectOff
	diskOn := !p.Limits.Off && !p.Limits.DiskOff
	if off {
		removeTripwires(p.Home)
		if !diskOn && unit == "" {
			return "", nil, nil
		}
	}
	d := &detector{
		spec: p.Detect, run: p.EgressSession().Run, marker: newMarker(), self: self,
		allow: map[string]bool{}, event: event, signal: signalProc, off: off,
		usage: &launchUsage{unit: unit, capMemory: capMem, capPids: capPids}, identity: p.Identity,
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

// rootPids are the launch's live roots.
func (d *detector) rootPids() []int {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]int, 0, len(d.roots))
	for pid := range d.roots {
		out = append(out, pid)
	}
	return out
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
func (d *detector) noteGroups(procs, all []procInfo) {
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
	// A group with no member left is forgotten: while it has one its id
	// cannot be reused, once empty it may be — and must not make another
	// run's process a leftover of this launch.
	live := map[int]bool{}
	for _, p := range all {
		live[p.pgid] = true
	}
	for g := range d.groups {
		if !live[g] {
			delete(d.groups, g)
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
	d.reportUsage()
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

// reportUsage records the launch's peaks against its caps at its end: one
// usage event, and a limit event for a cap that killed or refused it.
func (d *detector) reportUsage() {
	if d.usage == nil {
		return
	}
	if d.usage.unit != "" {
		d.usage.sampleCgroup(d.rootPids())
		res := scopeResult(d.usage.unit)
		d.usage.mu.Lock()
		d.usage.result = res
		d.usage.mu.Unlock()
	}
	f, _, limits := d.usage.report(d.run, d.identity)
	if f == nil || d.event == nil {
		return
	}
	d.event("usage", f)
	for _, l := range limits {
		d.event("limit", l)
	}
}
