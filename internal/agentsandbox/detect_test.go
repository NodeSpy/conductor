package agentsandbox

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

func TestClassify(t *testing.T) {
	p := func(args ...string) procInfo { return procInfo{args: args, comm: filepath.Base(args[0])} }
	for _, c := range []struct {
		p          procInfo
		root, self bool
		want       string
	}{
		{p("/usr/bin/ssh", "-p", "443", "user@host"), false, false, "tunnel_tool"},
		{p("ssh"), true, false, "tunnel_tool"}, // even as the agent's top process
		{p("/opt/x/chisel", "client"), false, false, "tunnel_tool"},
		{p("websocat", "ws://x"), false, false, "tunnel_tool"},
		{p("openssl", "s_client", "-connect", "x:443"), false, false, "tunnel_tool"},
		{p("openssl", "sha256", "f"), false, false, ""},
		{p("/usr/bin/git", "status"), false, false, ""},
		{p("go", "test", "./..."), false, false, ""},
		{procInfo{args: []string{"bash", "-i"}, stdio: [3]string{"inet", "inet", "inet"}}, false, false, "reverse_shell"},
		{procInfo{args: []string{"/usr/bin/python3.12", "-c", "x"}, stdio: [3]string{"other", "inet"}}, false, false, "reverse_shell"},
		{procInfo{args: []string{"bash", "-c", "make"}, stdio: [3]string{"unix", "unix", "unix"}}, false, false, ""}, // libuv socketpair stdio
		{procInfo{args: []string{"python3", "x.py"}, ptyMaster: true, inetSocket: true}, false, false, "pty_relay"},
		{procInfo{args: []string{"codex"}, ptyMaster: true, inetSocket: true}, true, false, ""}, // the agent runs its commands in ptys
		{procInfo{args: []string{"conductor", "sandbox-net"}, inetSocket: true, ptyMaster: true}, false, true, ""},
	} {
		v := classify(c.p, nil, c.root, c.self)
		got := ""
		if v != nil {
			got = v.kind
		}
		if got != c.want {
			t.Errorf("%v: got %q want %q", c.p.args, got, c.want)
		}
	}
	host := p("ssh")
	host.id = identity{"ssh", classTunnel}
	if v := classify(host, map[string]bool{"ssh": true}, false, false); v != nil {
		t.Error("sandbox.detect.allow exempts a tool")
	}
	if v := classify(p("ssh"), map[string]bool{"ssh": true}, false, false); v == nil || v.kind != "tunnel_tool" {
		t.Errorf("sandbox.detect.allow exempted a binary by its name alone: %+v", v)
	}
}

func TestLaunchProcsFollowsTheTree(t *testing.T) {
	all := []procInfo{
		{pid: 1, ppid: 0},
		{pid: 10, ppid: 1},                // the daemon
		{pid: 20, ppid: 10, marked: true}, // unshare
		{pid: 21, ppid: 20, marked: true}, // forwarder
		{pid: 22, ppid: 21},               // the agent, env dropped
		{pid: 23, ppid: 22},               // its child
		{pid: 30, ppid: 1, marked: true},  // a daemonized child (macOS: reparented)
		{pid: 40, ppid: 10},               // unrelated
	}
	var got []int
	for _, p := range launchProcs(all) {
		got = append(got, p.pid)
	}
	want := map[int]bool{20: true, 21: true, 22: true, 23: true, 30: true}
	if len(got) != len(want) {
		t.Fatalf("launch procs %v", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("launch procs %v", got)
		}
	}
	// A marked pid 1 never takes the whole machine with it.
	if ps := launchProcs([]procInfo{{pid: 1, marked: true}, {pid: 5, ppid: 1}}); len(ps) != 0 {
		t.Fatalf("pid 1 followed: %v", ps)
	}
}

func TestDetectorRespondsByAction(t *testing.T) {
	// Pids no test process has (safeToSignal refuses its own and its parent's).
	procs := []procInfo{{pid: 900050, ppid: 1, marked: true, start: 1, args: []string{"unshare"}}, {pid: 900051, ppid: 900050, start: 1, args: []string{"claude"}},
		{pid: 900052, ppid: 900051, start: 1, args: []string{"/usr/bin/ssh", "x"}}}
	for _, action := range []string{DetectKill, DetectAlert} {
		var mu sync.Mutex
		var events []map[string]any
		var signalled []int
		d := &detector{spec: DetectSpec{Action: action}, run: "r1", allow: map[string]bool{},
			list:   func() ([]procInfo, error) { return procs, nil },
			signal: func(pid int, _ uint64) error { mu.Lock(); signalled = append(signalled, pid); mu.Unlock(); return nil },
			event: func(k string, f map[string]any) {
				mu.Lock()
				events = append(events, f)
				mu.Unlock()
			},
			reported: map[string]bool{}}
		d.scan()
		d.scan() // reported once
		if len(events) != 1 || events[0]["kind"] != "tunnel_tool" || events[0]["run"] != "r1" {
			t.Fatalf("%s: events %v", action, events)
		}
		if action == DetectKill && len(signalled) != 3 {
			t.Fatalf("kill: signalled %v", signalled)
		}
		if action == DetectAlert && len(signalled) != 0 {
			t.Fatalf("alert: signalled %v", signalled)
		}
	}
}

// terminate kills only the process a rule flagged, and what it started: the
// agent keeps running, and a later offender is terminated too. A tripwire
// read names no process to stop, so it still ends the launch.
func TestDetectorTerminateStopsOnlyTheOffender(t *testing.T) {
	agent := []procInfo{{pid: 900060, ppid: 1, marked: true, start: 1, args: []string{"unshare"}}, {pid: 900061, ppid: 900060, start: 1, args: []string{"claude"}},
		{pid: 900062, ppid: 900061, start: 1, args: []string{"git", "fetch", "origin", "main"}}}
	ssh := []procInfo{{pid: 900063, ppid: 900062, start: 1, args: []string{"/usr/bin/ssh", "git@github.com"}}, {pid: 900064, ppid: 900063, start: 1, args: []string{"sh", "-c", "proxy"}}}
	procs := append(append([]procInfo{}, agent...), ssh...)
	var events []map[string]any
	var signalled []int
	d := &detector{spec: DetectSpec{Action: DetectTerminate}, run: "r1", allow: map[string]bool{},
		list:     func() ([]procInfo, error) { return procs, nil },
		signal:   func(pid int, _ uint64) error { signalled = append(signalled, pid); return nil },
		event:    func(k string, f map[string]any) { events = append(events, f) },
		reported: map[string]bool{}}
	d.scan()
	slices.Sort(signalled)
	if fmt.Sprint(signalled) != "[900063 900064]" {
		t.Fatalf("terminate stops the tunnel tool and its child, not the agent or git: %v", signalled)
	}
	if len(events) != 1 || events[0]["kind"] != "tunnel_tool" || events[0]["action"] != DetectTerminate {
		t.Fatalf("events %v", events)
	}
	// A second offender later in the run is stopped as well.
	signalled = nil
	procs = append(append([]procInfo{}, agent...), procInfo{pid: 900065, ppid: 900062, start: 1, args: []string{"/usr/bin/ssh", "git@github.com"}})
	d.scan()
	if fmt.Sprint(signalled) != "[900065]" {
		t.Fatalf("a later offender is terminated too: %v", signalled)
	}
	signalled = nil
	d.detected("tripwire", "decoy credential .ssh/id_ed25519 read", true)
	if len(signalled) != 4 {
		t.Fatalf("a tripwire read still kills the launch: %v", signalled)
	}
}

func TestTripwireCatchesEveryRead(t *testing.T) {
	home := t.TempDir()
	ws, err := plantTripwires(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		w.poll = 10 * time.Millisecond
	}
	stop := make(chan struct{})
	defer close(stop)
	hits := make(chan string, 8)
	for _, w := range ws {
		w := w
		go w.watch(stop, func(string) { hits <- w.rel })
	}
	// A read gets a believable credential, and is seen.
	b, err := os.ReadFile(filepath.Join(home, ".ssh", "id_ed25519"))
	if err != nil || !strings.Contains(string(b), "OPENSSH PRIVATE KEY") {
		t.Fatalf("decoy read %q %v", b, err)
	}
	select {
	case h := <-hits:
		if h != ".ssh/id_ed25519" {
			t.Fatalf("hit %s", h)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the read was not seen")
	}
	// Replaced by the agent with a plain file: planted again, not written.
	p := filepath.Join(home, ".vault-token")
	os.Remove(p)
	os.WriteFile(p, []byte("mine"), 0o600)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeNamedPipe != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not re-planted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case h := <-hits:
		t.Fatalf("a replaced decoy counted as read: %s", h)
	default:
	}
	// Off: removed, so no reader ever hangs.
	removeTripwires(home)
	if _, err := os.Lstat(filepath.Join(home, ".ssh", "id_rsa")); !os.IsNotExist(err) {
		t.Fatal("decoy left with detection off")
	}
}

func TestGitGuardUndoesTampering(t *testing.T) {
	ws := t.TempDir()
	gd := filepath.Join(ws, ".git")
	os.MkdirAll(filepath.Join(gd, "hooks"), 0o755)
	os.MkdirAll(filepath.Join(gd, "objects", "info"), 0o755)
	orig := "[core]\n\trepositoryformatversion = 0\n[remote \"origin\"]\n\turl = https://example.test/r\n"
	os.WriteFile(filepath.Join(gd, "config"), []byte(orig), 0o644)
	os.WriteFile(filepath.Join(gd, "hooks", "pre-commit.sample"), []byte("#!/bin/sh\n"), 0o755)
	g := newGitGuard(ws)
	if r := g.check(); len(r) != 0 {
		t.Fatalf("unchanged: %v", r)
	}
	// A harmless change stands.
	harmless := orig + "[user]\n\tname = Agent\n"
	os.WriteFile(filepath.Join(gd, "config"), []byte(harmless), 0o644)
	if r := g.check(); len(r) != 0 {
		t.Fatalf("user.name: %v", r)
	}
	for _, evil := range []string{"[core]\n\tfsmonitor = /tmp/x\n", "[filter \"lfs\"]\n\tsmudge = sh -c id\n", "[alias]\n\tst = !sh\n",
		"[core]\nhooksPath=/tmp\n", "[includeIf \"gitdir:/\"]\n\tpath = /tmp/c\n", "[diff \"x\"]\n\ttextconv = id\n"} {
		os.WriteFile(filepath.Join(gd, "config"), []byte(harmless+evil), 0o644)
		r := g.check()
		if len(r) != 1 || !strings.Contains(r[0], "restored") {
			t.Fatalf("%q: %v", evil, r)
		}
		if b, _ := os.ReadFile(filepath.Join(gd, "config")); string(b) != orig {
			t.Fatalf("%q: config now %q", evil, b)
		}
	}
	// Alternates and a commondir added: quarantined.
	os.WriteFile(filepath.Join(gd, "objects", "info", "alternates"), []byte("/home/op/.ssh\n"), 0o644)
	os.WriteFile(filepath.Join(gd, "commondir"), []byte("/home/op/repo/.git\n"), 0o644)
	if r := g.check(); len(r) != 2 {
		t.Fatalf("added files: %v", r)
	}
	for _, f := range []string{"objects/info/alternates", "commondir"} {
		if _, err := os.Lstat(filepath.Join(gd, f)); !os.IsNotExist(err) {
			t.Fatalf("%s still there", f)
		}
	}
	// A planted hook: quarantined, not executable.
	os.WriteFile(filepath.Join(gd, "hooks", "post-checkout"), []byte("#!/bin/sh\nid\n"), 0o755)
	if r := g.check(); len(r) != 1 || !strings.Contains(r[0], "post-checkout") {
		t.Fatalf("hook: %v", r)
	}
	if _, err := os.Lstat(filepath.Join(gd, "hooks", "post-checkout")); !os.IsNotExist(err) {
		t.Fatal("hook still there")
	}

	// A linked worktree's pointer rewritten: restored.
	ws2 := t.TempDir()
	os.WriteFile(filepath.Join(ws2, ".git"), []byte("gitdir: /repo/.git/worktrees/a\n"), 0o644)
	g2 := newGitGuard(ws2)
	os.WriteFile(filepath.Join(ws2, ".git"), []byte("gitdir: /home/op/.config\n"), 0o644)
	if r := g2.check(); len(r) != 1 {
		t.Fatalf("pointer: %v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(ws2, ".git")); string(b) != "gitdir: /repo/.git/worktrees/a\n" {
		t.Fatalf("pointer now %q", b)
	}
	// Replaced by a symlink: moved aside, the pointer restored.
	os.Remove(filepath.Join(ws2, ".git"))
	os.Symlink("/etc", filepath.Join(ws2, ".git"))
	if r := g2.check(); len(r) != 1 || !strings.Contains(r[0], "restored") {
		t.Fatalf("symlinked pointer: %v", r)
	}
	if fi, err := os.Lstat(filepath.Join(ws2, ".git")); err != nil || !fi.Mode().IsRegular() {
		t.Fatal("pointer not restored over the link")
	}
	_ = io.Discard
}

// A scope's allowance and overrides land on the launch's policy and its
// egress session; with none, the strict defaults stand.
func TestApplyScope(t *testing.T) {
	var p Policy
	applyScope(&p, nil, nil)
	if p.Detect.AllowedBy != "" || p.NonTLS != nil || p.Detect.Action != "" || p.Detect.NoTripwires {
		t.Fatalf("no scope changed the policy: %+v", p)
	}
	off := false
	p = Policy{Egress: []string{"api.example:443"}, EgressUpload: 7}
	applyScope(&p, &Allowance{Tunnels: []string{"Cloudflared"}, By: "sandbox"}, &Scope{
		Allow:        &Allowance{Tunnels: []string{"ssh"}, TunnelHosts: []string{"bastion.example:22"}, NonTLS: []string{"bastion.example:22"}, By: "workflow deploy"},
		DetectAction: DetectAlert, Tripwires: &off, EgressDownload: -1,
	})
	d := p.Detect
	if !d.allows("tunnel_tool", "cloudflared") || !d.allows("tunnel_tool", "ssh") || d.allows("tunnel_tool", "socat") || d.allows("reverse_shell", "sh") {
		t.Fatalf("tunnels %v", d.AllowTunnels)
	}
	if d.AllowedBy != "sandbox + workflow deploy" || d.Action != DetectAlert || !d.NoTripwires {
		t.Fatalf("detect %+v", d)
	}
	if !slices.Contains(p.Egress, "bastion.example:22") || p.EgressDownload != -1 || p.EgressUpload != 7 {
		t.Fatalf("egress %v, limits %d/%d", p.Egress, p.EgressUpload, p.EgressDownload)
	}
	so := p.EgressSession()
	if !so.RequireTLS || !slices.Equal(so.NonTLS, []string{"bastion.example:22"}) || so.NonTLSBy != "sandbox + workflow deploy" {
		t.Fatalf("session %+v", so)
	}
	// Detection off takes the tripwires with it, whatever the scope says.
	on := true
	p = Policy{}
	applyScope(&p, nil, &Scope{DetectAction: DetectOff, Tripwires: &on})
	if !p.Detect.NoTripwires {
		t.Fatal("tripwires planted with detection off")
	}
	// A scope's caps replace sandbox.limits' own, and cap a launch even
	// where sandbox.limits is off; one it leaves unset keeps the global.
	p = Policy{Limits: LimitSpec{Off: true, Pids: 50, DiskFreeFrac: 0.1}}
	applyScope(&p, nil, &Scope{Memory: "16G"})
	if p.Limits.Off || p.Limits.Memory != "16G" || p.Limits.Pids != 50 || p.Limits.DiskFreeFrac != 0.1 {
		t.Fatalf("scope caps: %+v", p.Limits)
	}
	// Every knob, both ways: CPU, disk, detection, the protocol check.
	no := false
	p = Policy{Limits: LimitSpec{DiskFreeFrac: 0.05}, Detect: DetectSpec{Interval: 3 * time.Second}}
	applyScope(&p, nil, &Scope{CPU: "150%", CPUWeight: 50, DiskSet: true, DiskOff: true, DetectInterval: int64(time.Second), Connections: "close", ProtocolCheck: &no})
	if p.Limits.CPU != "150%" || p.Limits.CPUWeight != 50 || !p.Limits.DiskOff || p.Limits.DiskFreeFrac != 0 ||
		p.Detect.Interval != time.Second || p.Detect.Connections != "close" || !p.EgressAnyBytes {
		t.Fatalf("knobs: %+v %+v any=%v", p.Limits, p.Detect, p.EgressAnyBytes)
	}
	yes := true
	p = Policy{EgressAnyBytes: true}
	applyScope(&p, nil, &Scope{ProtocolCheck: &yes})
	if p.EgressAnyBytes {
		t.Fatal("a scope's protocol check did not tighten the global off")
	}
	// A scope's inspect adds to the global list; the same host once, its
	// later ca: winning.
	p = Policy{Inspect: []sandbox.InspectHost{{Host: "npm.lan"}}}
	applyScope(&p, nil, &Scope{Inspect: []sandbox.InspectHost{{Host: "NPM.lan", CA: "/ca.pem"}, {Host: "pypi.lan"}}})
	if len(p.Inspect) != 2 || p.Inspect[0] != (sandbox.InspectHost{Host: "npm.lan", CA: "/ca.pem"}) || p.Inspect[1].Host != "pypi.lan" {
		t.Fatalf("inspect: %+v", p.Inspect)
	}
}

// egress_only: the scope's entries and the model's endpoints, nothing
// inherited — sandbox.egress, an outer network.egress, sandbox.allow's
// tunnel hosts; the scope's own allowance still adds its own. A pack's
// narrows only: an entry the inherited list does not cover is dropped.
func TestEgressOnly(t *testing.T) {
	base := &Allowance{Tunnels: []string{"cloudflared"}, TunnelHosts: []string{"*.trycloudflare.com"}, By: "sandbox"}
	inherited := func() Policy {
		return Policy{ModelRoute: []string{"api.anthropic.com"}, Egress: []string{"api.anthropic.com", "proxy.golang.org", "*.npmjs.org", "registry.lan:443"}}
	}
	p := inherited()
	applyScope(&p, base, &Scope{EgressOnlySet: true, EgressOnly: []string{"registry.lan:443", "http://athens.lan:3000/github.com/myorg/", "new.example"}})
	if got := strings.Join(p.Egress, ","); got != "api.anthropic.com,registry.lan:443,http://athens.lan:3000/github.com/myorg/,new.example" {
		t.Fatalf("operator's egress_only: %s", got)
	}
	p = inherited()
	applyScope(&p, base, &Scope{EgressOnlySet: true, EgressOnly: []string{}, Allow: &Allowance{Tunnels: []string{"ssh"}, TunnelHosts: []string{"bastion.lan:22"}, By: "step x"}})
	if got := strings.Join(p.Egress, ","); got != "api.anthropic.com,bastion.lan:22" {
		t.Fatalf("egress_only: [] with the scope's own tunnel: %s", got)
	}
	p = inherited()
	applyScope(&p, nil, &Scope{EgressOnlySet: true, EgressOnlyNarrow: true, EgressOnly: []string{"registry.npmjs.org", "proxy.golang.org", "evil.example", "*.npmjs.org", "*.evil.example"}})
	if got := strings.Join(p.Egress, ","); got != "api.anthropic.com,registry.npmjs.org,proxy.golang.org,*.npmjs.org" {
		t.Fatalf("a pack's egress_only: %s", got)
	}
}

// A recorded process group is forgotten once a scan finds it empty: a later
// process with that group id (the id reused) is not a leftover of this
// launch. A group that kept a member stays the launch's.
func TestEmptyGroupsAreForgotten(t *testing.T) {
	leader := procInfo{pid: 900001, ppid: 1, pgid: 900001, marked: true, start: 5}
	member := procInfo{pid: 900002, ppid: 900001, pgid: 900001, start: 6}
	stranger := procInfo{pid: 900003, ppid: 1, pgid: 900001, start: 99}
	run := func(scans ...[]procInfo) []int {
		var signalled []int
		i := 0
		d := &detector{allow: map[string]bool{}, reported: map[string]bool{},
			list:   func() ([]procInfo, error) { s := scans[i]; i++; return s, nil },
			signal: func(pid int, _ uint64) error { signalled = append(signalled, pid); return nil }}
		for range scans[:len(scans)-1] {
			d.scan()
		}
		d.killAll()
		return signalled
	}
	if got := run([]procInfo{leader, member}, nil, []procInfo{stranger}); len(got) != 0 {
		t.Fatalf("a process in a reused group id was killed: %v", got)
	}
	orphan := member
	orphan.ppid = 1
	if got := run([]procInfo{leader, member}, []procInfo{orphan}, []procInfo{orphan}); !slices.Contains(got, orphan.pid) {
		t.Fatalf("a daemon left in the launch's group survived: %v", got)
	}
}

// The config keys that run a command, redirect a fetch or push, or weaken
// its transport are all recorded by the git guard.
func TestDangerousGitKeysCoverExecAndRedirects(t *testing.T) {
	cfg := []byte(`[init]
	templateDir = /tmp/t
[core]
	attributesFile = /tmp/a
	sshCommand = x
	editor = x
[url "https://evil.example/"]
	insteadOf = https://github.com/
	pushInsteadOf = git@github.com:
[http]
	proxy = http://evil.example
	sslVerify = false
[http "https://github.com/"]
	extraHeader = x
[remote "origin"]
	url = https://evil.example/r
	pushurl = https://evil.example/r
[hook "pre-commit"]
	command = x
[trailer "sign"]
	command = x
[credential "https://github.com"]
	helper = x
[diff "x"]
	textconv = x
[merge "x"]
	driver = x
[gpg "ssh"]
	program = x
[ssh]
	variant = ssh
[user]
	name = fine
[core]
	autocrlf = false
`)
	got := dangerousGitKeys(cfg)
	for _, k := range []string{"init.templatedir", "core.attributesfile", "core.sshcommand", "core.editor", "url.https://evil.example/.insteadof", "url.https://evil.example/.pushinsteadof",
		"http.proxy", "http.sslverify", "http.https://github.com/.extraheader", "remote.origin.url", "remote.origin.pushurl", "hook.pre-commit.command",
		"trailer.sign.command", "credential.https://github.com.helper", "diff.x.textconv", "merge.x.driver", "gpg.ssh.program", "ssh.variant"} {
		if !strings.Contains(got, k+"=") {
			t.Errorf("%s not recorded", k)
		}
	}
	if strings.Contains(got, "user.name") || strings.Contains(got, "autocrlf") {
		t.Fatalf("a harmless key recorded:\n%s", got)
	}
}

// An allowance for a tunnel tool covers that tool, never a shell wired to
// a socket under its name (the red team's expF): neither a renamed host
// shell nor a binary the host does not know is audited as an allowed
// tunnel, and the launch is killed.
func TestTunnelAllowanceNeverCoversAShell(t *testing.T) {
	spec := DetectSpec{AllowTunnels: []string{"ssh", "cloudflared"}, AllowedBy: "workflow ops"}
	sock := [3]string{"inet", "inet"}
	cases := []struct {
		name    string
		p       procInfo
		allowed bool
		kind    string
	}{
		{"real ssh, its own socket", procInfo{args: []string{"ssh", "bastion"}, id: identity{"ssh", classTunnel}, inetFDs: []int{3}}, true, "tunnel_tool"},
		{"the host's cloudflared", procInfo{args: []string{"cloudflared", "tunnel"}, id: identity{"cloudflared", classTunnel}, inetFDs: []int{7}}, true, "tunnel_tool"},
		{"a binary the host does not know, named cloudflared", procInfo{args: []string{"./cloudflared", "tunnel"}, inetFDs: []int{7}}, false, "tunnel_tool"},
		{"a binary the host does not know, named ssh", procInfo{args: []string{"ssh", "bastion"}, inetFDs: []int{3}}, false, "tunnel_tool"},
		{"plain reverse shell", procInfo{args: []string{"bash"}, id: identity{"bash", classShell}, stdio: sock, inetFDs: []int{0, 1}}, false, "reverse_shell"},
		{"host bash renamed ssh", procInfo{args: []string{"ssh"}, id: identity{"bash", classShell}, stdio: sock, inetFDs: []int{0, 1}}, false, "reverse_shell"},
		{"unknown binary named ssh, socket on stdio", procInfo{args: []string{"ssh"}, stdio: sock, inetFDs: []int{0, 1}}, false, "reverse_shell"},
		{"unknown binary named cloudflared, socket on stdin", procInfo{args: []string{"./cloudflared"}, stdio: [3]string{"inet"}, inetFDs: []int{0}}, false, "reverse_shell"},
		{"unknown binary named ssh relaying a pty", procInfo{args: []string{"ssh"}, ptyMaster: true, inetSocket: true, inetFDs: []int{4}}, false, "pty_relay"},
	}
	for _, c := range cases {
		c.p.pid, c.p.ppid, c.p.start = 900072, 900071, 1
		procs := []procInfo{{pid: 900070, ppid: 1, marked: true, start: 1, args: []string{"unshare"}}, {pid: 900071, ppid: 900070, start: 1, args: []string{"claude"}}, c.p}
		var events []map[string]any
		var signalled []int
		d := &detector{spec: spec, run: "r1", allow: map[string]bool{},
			list:     func() ([]procInfo, error) { return procs, nil },
			signal:   func(pid int, _ uint64) error { signalled = append(signalled, pid); return nil },
			event:    func(_ string, f map[string]any) { events = append(events, f) },
			reported: map[string]bool{}}
		d.scan()
		if len(events) != 1 {
			t.Fatalf("%s: events %v", c.name, events)
		}
		e := events[0]
		if e["kind"] != c.kind {
			t.Errorf("%s: kind %v, want %s (%v)", c.name, e["kind"], c.kind, e["reason"])
		}
		if got := e["action"] == "allowed"; got != c.allowed {
			t.Errorf("%s: audited %v, want allowed=%v", c.name, e["action"], c.allowed)
		}
		if c.allowed != (len(signalled) == 0) {
			t.Errorf("%s: signalled %v", c.name, signalled)
		}
	}
	// Every verdict must be covered, not only the first; a covered process
	// is audited by its most telling verdict.
	for _, c := range []struct {
		name    string
		spec    DetectSpec
		p       procInfo
		allowed bool
		kind    string
	}{
		{"pty allowed, the tunnel not", DetectSpec{AllowPty: true, AllowedBy: "step x"},
			procInfo{args: []string{"socat"}, id: identity{"socat", classTunnel}, ptyMaster: true, inetSocket: true, inetFDs: []int{5}}, false, "tunnel_tool"},
		{"shells and the tool allowed", DetectSpec{AllowReverseShell: true, AllowTunnels: []string{"ssh"}, AllowedBy: "step x"},
			procInfo{args: []string{"ssh"}, id: identity{"ssh", classTunnel}, stdio: sock, inetFDs: []int{0, 1}}, true, "reverse_shell"},
	} {
		c.p.pid, c.p.ppid, c.p.start = 900072, 900071, 1
		procs := []procInfo{{pid: 900070, ppid: 1, marked: true, start: 1, args: []string{"unshare"}}, {pid: 900071, ppid: 900070, start: 1, args: []string{"claude"}}, c.p}
		var events []map[string]any
		var signalled []int
		d := &detector{spec: c.spec, run: "r1", allow: map[string]bool{},
			list:     func() ([]procInfo, error) { return procs, nil },
			signal:   func(pid int, _ uint64) error { signalled = append(signalled, pid); return nil },
			event:    func(_ string, f map[string]any) { events = append(events, f) },
			reported: map[string]bool{}}
		d.scan()
		if len(events) != 1 || events[0]["kind"] != c.kind || (events[0]["action"] == "allowed") != c.allowed || c.allowed != (len(signalled) == 0) {
			t.Errorf("%s: events %v signalled %v", c.name, events, signalled)
		}
	}
	// sandbox.detect.allow exempts the program it names, never a binary
	// taking its name.
	if v := classify(procInfo{args: []string{"ssh"}, stdio: sock}, map[string]bool{"ssh": true}, false, false); v == nil || v.kind != "reverse_shell" {
		t.Fatalf("detect.allow covered a shell's wiring: %+v", v)
	}
	if v := classify(procInfo{args: []string{"ssh"}, inetFDs: []int{3}}, map[string]bool{"ssh": true}, false, false); v == nil || v.kind != "tunnel_tool" {
		t.Fatalf("detect.allow exempted a name: %+v", v)
	}
	if v := classify(procInfo{args: []string{"ssh"}, id: identity{"ssh", classTunnel}, inetFDs: []int{3}}, map[string]bool{"ssh": true}, false, false); v != nil {
		t.Fatalf("detect.allow did not exempt the tool: %+v", v)
	}
}
