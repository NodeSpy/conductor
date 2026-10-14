package jail

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/hostcmd"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// serve starts d's broker on a unix socket only its jail can reach (the
// socket's directory is bound into that jail alone). Each connection must
// come from the daemon's own uid and carry d's token.
func (m *Manager) serve(d *Dispatch, sock string) (stop func(), err error) {
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("jail: broker listen: %w", err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				m.handleConn(ctx, d, c)
			}()
		}
	}()
	return func() {
		cancel()
		ln.Close()
		_ = os.Remove(sock)
	}, nil
}

func (m *Manager) handleConn(ctx context.Context, d *Dispatch, c net.Conn) {
	fw := newFrameWriter(c)
	fail := func(msg string) { _ = fw.send(Reply{Done: true, Exit: 1, Error: msg}) }
	if uid, ok := peerUID(c); ok && int(uid) != os.Getuid() {
		fail("conductor broker: refused (peer uid)")
		return
	}
	line, err := readFrame(bufio.NewReaderSize(c, 64<<10))
	if err != nil {
		return
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		fail("conductor broker: bad request")
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Token), []byte(d.Token)) != 1 {
		fail("conductor broker: refused (credential)")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, hostCommandTimeout)
	defer cancel()
	switch req.Op {
	case "exec":
		m.handleExec(ctx, d, req, fw)
	case "git_list":
		m.gitList(ctx, d, req, fw)
	case "git_fetch":
		m.gitFetch(ctx, d, req, fw)
	case "git_push":
		m.gitPush(ctx, d, req, fw)
	case "sign":
		m.handleSign(ctx, d, req, fw)
	case "hook":
		m.handleHook(ctx, d, req, fw)
	default:
		fail("conductor broker: unknown op " + req.Op)
	}
}

// jailTmp is where the jail's /tmp lives as a host-command argument sees it:
// the Linux host command runs in a mount namespace whose /tmp IS the
// dispatch's scratch dir; on macOS the jail's TMPDIR is that dir itself.
func (d *Dispatch) jailTmp() string {
	if runtime.GOOS == "linux" {
		return "/tmp"
	}
	return d.TmpDir
}

// hostPath maps a path the agent named (as seen in the jail) to the host.
func (d *Dispatch) hostPath(p string) string {
	if runtime.GOOS == "linux" && within(p, "/tmp") {
		return filepath.Join(d.TmpDir, strings.TrimPrefix(p, "/tmp"))
	}
	return p
}

func (m *Manager) hostContext(ctx context.Context, d *Dispatch, cwd string) hostcmd.Context {
	hc := hostcmd.Context{
		Repo: d.Repo, Number: d.Number, IsPR: d.IsPR, HeadBranch: d.HeadBranch,
		Workspace: d.Workspace, TmpDir: d.jailTmp(), Home: m.Home, Sensitive: m.Sensitive, Cwd: cwd,
	}
	// RuleFor lets a credential-injecting launcher's profile (vop) judge the
	// command it is about to run as its own child with THAT tool's own
	// configured rule — the same layers, resolved for a different tool.
	hc.RuleFor = func(tool string) hostcmd.Rule { return hostcmd.Resolve(tool, d.Layers, d.StepLayer) }
	hc.ReadFile = func(p string) ([]byte, error) {
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		p = filepath.Clean(p)
		if !within(p, d.Workspace) && !within(p, d.jailTmp()) {
			return nil, fmt.Errorf("%s is outside the workspace", p)
		}
		f, err := os.Open(d.hostPath(p))
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(io.LimitReader(f, 256<<10))
	}
	if m.ThreadTarget != nil {
		hc.ThreadTarget = func(id string) (string, int, error) { return m.ThreadTarget(ctx, d, id) }
	}
	hc.LocalImage = func(ref string) bool { return m.localImage(ctx, ref) }
	hc.ReadOnly = d.ReadOnly
	if m.TargetClosed != nil {
		hc.TargetClosed = func() string { return m.TargetClosed(d) }
	}
	return hc
}

// localImage reports whether a docker image exists on this machine with no
// registry digest — built here (perhaps from workspace content), never
// pulled. An unreadable answer counts as local: the safe side.
func (m *Manager) localImage(ctx context.Context, ref string) bool {
	if strings.HasPrefix(ref, "-") {
		return true
	}
	bin, err := m.LookPath("docker")
	if err != nil {
		return false
	}
	ictx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ictx, bin, "image", "inspect", "--format", "{{len .RepoDigests}}", ref)
	cmd.Env = sandbox.MinimalEnv()
	out, err := cmd.Output()
	if err != nil {
		return false // not on this machine: it would be pulled
	}
	return strings.TrimSpace(string(out)) == "0"
}

// dockerSockets are the Docker daemon's sockets a confined docker command
// reaches (the only daemon socket in its view).
func dockerSockets() []string {
	var out []string
	for _, p := range []string{"/var/run/docker.sock", "/run/docker.sock"} {
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// dropSessionEnv removes the operator's session plumbing from a confined
// run's environment: agent and bus sockets, the runtime dir.
func dropSessionEnv(env []string) []string {
	out := env[:0]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "XDG_RUNTIME_DIR", "SSH_AUTH_SOCK", "GPG_AGENT_INFO", "DBUS_SESSION_BUS_ADDRESS", "DOCKER_HOST":
			continue
		}
		out = append(out, kv)
	}
	return out
}

func (m *Manager) handleExec(ctx context.Context, d *Dispatch, req Request, fw *frameWriter) {
	tool := filepath.Base(req.Tool)
	cmdline := shortArgs(tool, req.Args)
	refuse := func(reason string) {
		m.emit(d, Event{Type: "host_command", Status: "refused", Detail: cmdline, Reason: reason})
		_ = fw.send(Reply{Done: true, Exit: 126, Refused: reason})
	}
	if !contains(d.HostSet, tool) && !contains(d.Denied, tool) {
		refuse(tool + " is not a host command in this jail")
		return
	}
	cwd := req.Cwd
	if cwd == "" || !(within(cwd, d.Workspace) || within(cwd, d.jailTmp())) {
		cwd = d.Workspace
	}
	rule := hostcmd.Resolve(tool, d.Layers, d.StepLayer)
	hc := m.hostContext(ctx, d, cwd)
	dec := hostcmd.Decide(hostcmd.Request{Tool: tool, Args: req.Args, Cwd: cwd}, rule, hc)
	var granted *grantUse
	if !dec.Allow && !dec.Native && m.Grants != nil {
		// An operator's approval of this exact command (grants.go).
		gdec, use, why := m.useGrant(ctx, d, tool, req.Args, cwd, rule, hc)
		switch {
		case use != nil:
			dec, granted = gdec, use
			defer use.cleanup()
		case why != "":
			refuse(dec.Reason + " — " + why)
			return
		case gdec.Reason != "":
			refuse(gdec.Reason) // a grant matched, but it cannot lift this
			return
		}
	}
	if dec.Native {
		if !dec.Allow {
			refuse(dec.Reason)
			return
		}
		_ = fw.send(Reply{Done: true, Native: true})
		return
	}
	if !dec.Allow {
		refuse(dec.Reason)
		return
	}
	bin, err := m.LookPath(tool)
	if err != nil {
		refuse(tool + " is not installed on the host")
		return
	}
	paths, persist, penv, full := hostcmd.HomeView(tool, rule.Persist, rule.Paths)
	envTools := []string{tool}
	binRoots := binaryRoots(bin, m.LookPath)
	if inner := dec.Parsed.InnerTool; inner != "" {
		// A credential-injecting launcher (vop) runs the inner tool as its
		// own child: the inner tool's own view (e.g. aws's ~/.aws) must be
		// visible too, unioned with the launcher's — never narrower than
		// either, since both processes share the one copy-on-write home this
		// run gets.
		innerRule := hostcmd.Resolve(inner, d.Layers, d.StepLayer)
		ip, iper, ienv, ifull := hostcmd.HomeView(inner, innerRule.Persist, innerRule.Paths)
		paths = unionStrings(paths, ip)
		persist = unionStrings(persist, iper)
		full = full || ifull
		if penv == nil {
			penv = map[string]string{}
		}
		for k, v := range ienv {
			if _, ok := penv[k]; !ok {
				penv[k] = v
			}
		}
		// The inner tool's own operator env (isolation.host.<inner>.env — an
		// org's AWS_CONFIG_FILE, say) applies to it under vop as it would
		// run alone; the launcher's own rule env (applied after) still wins.
		for k, v := range innerRule.Env {
			penv[k] = config.ExpandHome(v)
		}
		envTools = append(envTools, inner)
		if ibin, err := m.LookPath(inner); err == nil {
			binRoots = append(binRoots, binaryRoots(ibin, m.LookPath)...)
		}
	}
	hr := hostRun{
		Tool: tool, Bin: bin, Args: append(append([]string(nil), runArgs(req.Args, granted)...), dec.Parsed.ExtraArgs...),
		Cwd: cwd, Stdin: req.Stdin, Home: m.Home, HomePaths: paths, FullHome: full, Persist: persist,
		Workspace: d.Workspace, TmpDir: d.TmpDir, Sensitive: m.Sensitive,
		BinRoots: append(binRoots, grantFiles(granted)...),
	}
	hr.Env = m.hostEnv(d, envTools, req.Env, penv, rule, full)
	restrict, allow := rule.Network != nil && rule.Network.Mode != config.NetOpen, networkAllow(rule.Network, nil)
	if dec.Confine {
		// A content-executing command (hostcmd/content.go): the host-side
		// jail, one confined run of this tool at a time per dispatch (they
		// share the workspace's copy-on-write layer).
		unlock := d.lockConfined(tool)
		defer unlock()
		hr.Confine, hr.Stdin = true, nil
		if hr.WsUpper, hr.WsWork, err = d.confinedLayer(tool); err != nil {
			refuse("confined run: " + err.Error())
			return
		}
		if tool == "docker" {
			hr.Sockets = dockerSockets()
		}
		// Its network is always restricted: the tool's service endpoints,
		// or the binary's own network block (open → audited, not open).
		restrict, allow = true, hostcmd.ServiceEndpoints(tool)
		if n := rule.Network; n != nil {
			allow = networkAllow(n, nil)
			if n.Mode == config.NetOpen {
				allow = []string{"*"}
			}
		}
		hr.Env = dropSessionEnv(hr.Env)
	}
	if restrict {
		if isDarwin {
			// macOS: Seatbelt confines the command to the proxy's loopback
			// port (no network namespace to forward into).
			if m.HostEgressTCP == nil {
				refuse("host command network policy set but no egress proxy is wired")
				return
			}
			addr, cred, revoke, err := m.HostEgressTCP(allow, d.DispatchID)
			if err != nil {
				refuse("egress proxy: " + err.Error())
				return
			}
			defer revoke()
			_, port, _ := strings.Cut(addr, ":")
			hr.EgressSock = "port:" + port
			hr.Env = append(hr.Env, sandbox.ProxyEnv(addr, cred)...)
		} else {
			if m.HostEgress == nil {
				refuse("host command network policy set but no egress proxy is wired")
				return
			}
			sock, cred, revoke, err := m.HostEgress(allow, d.DispatchID)
			if err != nil {
				refuse("egress proxy: " + err.Error())
				return
			}
			defer revoke()
			hr.EgressSock = sock
			hr.Env = append(hr.Env, sandbox.ProxyEnv(sandbox.ForwardAddr, cred)...)
		}
	}
	res, err := runHost(ctx, m, hr, streamWriter{fw: fw}, streamWriter{fw: fw, err: true})
	if err != nil {
		m.emit(d, Event{Type: "host_command", Status: "error", Detail: cmdline, Reason: err.Error()})
		_ = fw.send(Reply{Done: true, Exit: 125, Error: "conductor: host command could not run: " + err.Error()})
		return
	}
	status := fmt.Sprintf("exit %d", res.Exit)
	ev := Event{Type: "host_command", Status: status, Detail: cmdline}
	if len(res.Discarded) > 0 || hr.Confine || granted != nil {
		ev.Fields = map[string]any{}
		if granted != nil {
			ev.Fields["grant"] = granted.spec.ID
			ev.Fields["approved_by"] = granted.spec.ApprovedBy
			if granted.spec.CaseID != "" {
				ev.Fields["case_id"] = granted.spec.CaseID
			}
		}
		if len(res.Discarded) > 0 {
			ev.Fields["discarded"] = res.Discarded
		}
		if hr.Confine {
			ev.Fields["confined"] = true
			ev.Fields["egress"] = allow
		}
	}
	m.emit(d, ev)
	_ = fw.send(Reply{Done: true, Exit: res.Exit})
}

// agentEnvAllow are the variables a shim may forward from the jail to the
// host-side process: presentation only, never anything that would redirect
// a tool's config or credentials.
var agentEnvAllow = map[string]bool{
	"NO_COLOR": true, "CLICOLOR": true, "CLICOLOR_FORCE": true, "COLUMNS": true, "LINES": true,
	"TZ": true, "LANG": true, "AWS_REGION": true, "AWS_DEFAULT_REGION": true,
}

// AgentEnvAllowed reports a variable the shim forwards.
func AgentEnvAllowed(k string) bool { return agentEnvAllow[k] || strings.HasPrefix(k, "LC_") }

// hostEnvPrefixes are, per profiled tool, the daemon-environment variables
// that are the machine's own setup for it (AWS_PROFILE, KUBECONFIG, …) —
// used in place, host-side only.
var hostEnvPrefixes = map[string][]string{
	"aws":       {"AWS_"},
	"gcloud":    {"CLOUDSDK_", "GOOGLE_", "GCLOUD_"},
	"az":        {"AZURE_"},
	"kubectl":   {"KUBECONFIG", "AWS_", "CLOUDSDK_", "GOOGLE_", "AZURE_"},
	"docker":    {"DOCKER_"},
	"terraform": {"TF_", "AWS_", "CLOUDSDK_", "GOOGLE_", "AZURE_", "ARM_", "KUBECONFIG"},
	"gh":        {"GH_HOST"},
	"npm":       {"NPM_", "NODE_AUTH_TOKEN"},
	"pnpm":      {"NPM_", "NODE_AUTH_TOKEN", "PNPM_"},
	"ssh":       {"SSH_AUTH_SOCK"},
	"scp":       {"SSH_AUTH_SOCK"},
	// vop deliberately carries none: VOP_PROFILE and AGENT_DECK_VOP_PROFILE
	// must never reach a host run of it — the profile `vop exec` uses comes
	// only from its explicit argument (hostcmd's vop profile refuses a bare
	// `vop exec -- …` that would fall back to env/.vop resolution), not from
	// whatever the daemon's own environment happens to have pinned.
}

// hostEnv builds a host command's environment. tools is the outer tool, plus
// the inner tool when a launcher like vop runs one as its own child — each
// tool's own hostEnvPrefixes (the machine's setup for it) is folded in.
func (m *Manager) hostEnv(d *Dispatch, tools []string, agent map[string]string, profileEnv map[string]string, rule hostcmd.Rule, full bool) []string {
	env := map[string]string{}
	for _, kv := range sandbox.MinimalEnv() {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	env["HOME"] = m.Home
	env["TERM"] = "dumb"
	env["TMPDIR"] = d.jailTmp()
	for _, k := range []string{"XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	if full {
		if v, ok := os.LookupEnv("SSH_AUTH_SOCK"); ok {
			env["SSH_AUTH_SOCK"] = v
		}
	}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		for _, t := range tools {
			for _, pre := range hostEnvPrefixes[t] {
				if strings.HasPrefix(k, pre) {
					env[k] = v
				}
			}
		}
	}
	for k, v := range agent {
		if AgentEnvAllowed(k) {
			env[k] = v
		}
	}
	for k, v := range profileEnv {
		env[k] = v
	}
	for k, v := range rule.Env {
		env[k] = config.ExpandHome(v)
	}
	if tools[0] == "gh" {
		// The dispatch's repository for commands that infer one. No token:
		// the host gh uses the machine's own login, in place (#154 §2).
		env["GH_REPO"] = d.Repo
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// networkAllow is the enforced allowlist for a network block: audit allows
// everything (and records it), deny only the extra (model) endpoints, an
// egress list its entries plus the extra.
func networkAllow(n *config.IsolationNetwork, extra []string) []string {
	switch {
	case n == nil:
		return nil
	case n.Mode == config.NetAudit:
		return []string{"*"}
	case n.Mode == config.NetDeny:
		return append([]string(nil), extra...)
	default:
		return append(append([]string(nil), n.Egress...), extra...)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// unionStrings is a ∪ b, deduplicated, a's order first.
func unionStrings(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range b {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// runArgs is the argv a host command runs with: a grant's, whose pinned
// files name their private copies, or the agent's own.
func runArgs(args []string, g *grantUse) []string {
	if g != nil {
		return g.args
	}
	return args
}

// grantFiles are the private copies a granted run reads (bound read-only).
func grantFiles(g *grantUse) []string {
	if g == nil {
		return nil
	}
	return g.files
}
