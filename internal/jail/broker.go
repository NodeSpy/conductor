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
	"path/filepath"
	"runtime"
	"strings"
	"sync"

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
		Workspace: d.Workspace, TmpDir: d.jailTmp(), Home: m.Home, Sensitive: m.Sensitive,
	}
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
	return hc
}

// writeCheck binds one write: review steps write nothing; everything else
// goes through the target binding.
func (m *Manager) writeCheck(d *Dispatch) hostcmd.WriteCheck {
	return func(w hostcmd.Write) string {
		if d.ReadOnly {
			return "target: this is a review step — writes are refused (isolation.writes to change)"
		}
		if m.CheckWrite == nil {
			return "target: writes are not permitted (no target binding wired)"
		}
		return m.CheckWrite(d, w)
	}
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
	dec := hostcmd.Decide(hostcmd.Request{Tool: tool, Args: req.Args, Cwd: cwd}, rule, m.hostContext(ctx, d, cwd), m.writeCheck(d))
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
	paths, persist, penv, full := hostcmd.HomeView(tool, rule.Persist)
	hr := hostRun{
		Tool: tool, Bin: bin, Args: append(append([]string(nil), req.Args...), dec.Parsed.ExtraArgs...),
		Cwd: cwd, Stdin: req.Stdin, Home: m.Home, HomePaths: paths, FullHome: full, Persist: persist,
		Workspace: d.Workspace, TmpDir: d.TmpDir, Sensitive: m.Sensitive,
		BinRoots: binaryRoots(bin, m.LookPath),
	}
	hr.Env = m.hostEnv(d, tool, req.Env, penv, rule, full)
	if rule.Network != nil && rule.Network.Mode != config.NetOpen {
		allow := networkAllow(rule.Network, nil)
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
	res, err := runHost(ctx, m, hr, streamWriter{fw: fw}, streamWriter{fw: fw, err: true})
	if err != nil {
		m.emit(d, Event{Type: "host_command", Status: "error", Detail: cmdline, Reason: err.Error()})
		_ = fw.send(Reply{Done: true, Exit: 125, Error: "conductor: host command could not run: " + err.Error()})
		return
	}
	status := fmt.Sprintf("exit %d", res.Exit)
	ev := Event{Type: "host_command", Status: status, Detail: cmdline}
	if len(res.Discarded) > 0 {
		ev.Fields = map[string]any{"discarded": res.Discarded}
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
}

func (m *Manager) hostEnv(d *Dispatch, tool string, agent map[string]string, profileEnv map[string]string, rule hostcmd.Rule, full bool) []string {
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
		for _, pre := range hostEnvPrefixes[tool] {
			if strings.HasPrefix(k, pre) {
				env[k] = v
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
	if tool == "gh" {
		env["GH_REPO"] = d.Repo
		if d.UserToken != "" {
			env["GH_TOKEN"] = d.UserToken
		}
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
