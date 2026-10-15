package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/confdir"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// sandboxRefused is `conductor sandbox exec`'s exit status when it refuses
// to launch (the agent never ran): distinct from any status the agent itself
// returns through it.
const sandboxRefused = 125

// cmdSandbox is `conductor sandbox <sub>`.
func cmdSandbox(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(os.Stderr, sandboxUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	switch args[0] {
	case "exec":
		return cmdSandboxExec(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "conductor sandbox: unknown subcommand %q\n%s", args[0], sandboxUsage)
		return 2
	}
}

const sandboxUsage = `usage: conductor sandbox exec [--tool NAME] [--bin NAME]... [--config PATH | --socket PATH] -- COMMAND [ARGS...]

Runs COMMAND — an agent CLI — under conductor's agent sandbox, with the
policy the running conductor daemon gives this directory: the workspace
read-write, the identity's own tool state as HOME, the conductor socket,
egress only to the model endpoints (and sandbox.egress), no credentials.
Each launch gets a fresh conductor token; a relaunch (resume) finds the same
tool state.

It refuses to launch — exit 125, the agent never runs — when the daemon is
unreachable or has no policy for this directory. Never unsandboxed.

  --tool NAME      the agent CLI (claude, codex, pi, …); default: COMMAND's name
  --bin NAME       a program COMMAND runs by name (the agent CLI a shell command
                   starts): shown in the sandbox like COMMAND itself; repeatable
  --agent-cwd      this directory is the agent's own workspace: refuse it rather
                   than substitute a scratch workspace when it may not be one
  --config PATH    the conductor config whose daemon to ask (default: the usual one)
  --socket PATH    the daemon's control socket, directly
  --state-dir DIR  conductor's state dir, when it is not the default

Wired into paseo by 'conductor paseo setup' (and by the daemon itself at
start), and into agent-deck sessions by conductor's agent-deck runtime.
`

// sandboxExecOpts are `conductor sandbox exec`'s flags.
type sandboxExecOpts struct {
	tool, config, socket, stateDir string
	bins                           []string
	// agentCwd: the working directory is the agent's own workspace (an
	// agent-deck session's) — refused, never replaced by a scratch one.
	agentCwd bool
}

// parseSandboxExec splits flags from the command after "--".
func parseSandboxExec(args []string) (sandboxExecOpts, []string, error) {
	var o sandboxExecOpts
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			if i+1 >= len(args) {
				return o, nil, errors.New("no command after --")
			}
			return o, args[i+1:], nil
		}
		val := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", a)
			}
			i++
			return args[i], nil
		}
		var err error
		switch a {
		case "--tool":
			o.tool, err = val()
		case "--agent-cwd":
			o.agentCwd = true
		case "--bin":
			var b string
			if b, err = val(); err == nil {
				o.bins = append(o.bins, b)
			}
		case "--config":
			o.config, err = val()
		case "--socket":
			o.socket, err = val()
		case "--state-dir":
			o.stateDir, err = val()
		default:
			return o, nil, fmt.Errorf("unknown flag %q (the command goes after --)", a)
		}
		if err != nil {
			return o, nil, err
		}
	}
	return o, nil, errors.New("missing -- COMMAND")
}

func cmdSandboxExec(args []string) int {
	refuse := func(format string, a ...any) int {
		fmt.Fprintf(os.Stderr, "conductor sandbox exec: refusing to launch: "+format+"\n", a...)
		return sandboxRefused
	}
	o, argv, err := parseSandboxExec(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "conductor sandbox exec: %v\n%s", err, sandboxUsage)
		return 2
	}
	cwd, agentCwd, err := sandboxExecCwd()
	if err != nil {
		return refuse("no working directory: %v", err)
	}
	agentCwd = agentCwd || o.agentCwd
	tool := o.tool
	if tool == "" {
		tool = argv[0]
	}
	sock := sandboxControlSocket(o)
	pol, err := fetchSandboxPolicy(sock, controlRequest{Cmd: "sandbox-policy", Cwd: cwd, Tool: tool, Env: modelRouteEnv(os.Environ()), Scratch: !agentCwd})
	if err != nil {
		return refuse("%v — an agent is never launched unsandboxed", err)
	}
	if pol.Note != "" {
		fmt.Fprintf(os.Stderr, "conductor sandbox exec: %s\n", pol.Note)
	}
	launch, cleanup, err := wrapSandboxExec(pol, argv, o.bins)
	if err != nil {
		return refuse("%v", err)
	}
	defer cleanup()
	return runSandboxed(launch)
}

// sandboxExecCwd is the agent's working directory: paseo names it in
// PASEO_AGENT_CWD — for a provider whose process serves its sessions from
// another directory (codex app-server inherits paseo's own cwd and gets the
// session's over its protocol) that, not our own cwd, is the workspace. The
// launcher sets it outside any sandbox; it is the operator's paseo speaking.
//
// agent reports that the launcher named the agent's directory; without it
// (paseo spawning a provider for a utility call: listing its models) the
// daemon may answer with a scratch workspace.
func sandboxExecCwd() (cwd string, agent bool, err error) {
	if v := os.Getenv("PASEO_AGENT_CWD"); v != "" && filepath.IsAbs(v) {
		if fi, err := os.Stat(v); err == nil && fi.IsDir() {
			return filepath.Clean(v), true, nil
		}
	}
	cwd, err = os.Getwd()
	return cwd, false, err
}

// wrapSandboxExec realizes pol around argv with this process as the host
// side: its own allowlist proxy and model relays live as long as the agent.
func wrapSandboxExec(pol agentsandbox.Policy, argv, bins []string) (*agentsandbox.Launch, func(), error) {
	pm := sandbox.NewProxyManager(func(_, hostport string) {
		fmt.Fprintf(os.Stderr, "conductor sandbox: egress to %s denied (not in this agent's allowlist)\n", hostport)
	})
	rel, err := agentsandbox.NewRelayer()
	if err != nil {
		pm.Close()
		return nil, nil, err
	}
	home, _ := os.UserHomeDir()
	launch, err := agentsandbox.Wrap(pol, argv, agentsandbox.Deps{
		SelfExe:    os.Executable,
		EgressUnix: pm.UnixEndpoint,
		EgressAddr: pm.Endpoint,
		Relay:      rel.Relay,
		LookPath:   exec.LookPath,
		RealHome:   home,
		Inherited:  os.Environ(),
		Bins:       bins,
	})
	if err != nil {
		rel.Close()
		pm.Close()
		return nil, nil, err
	}
	return launch, func() { launch.Cleanup(); rel.Close(); pm.Close() }, nil
}

// runSandboxed runs the wrapped launch in the foreground — its stdio is
// ours (paseo speaks to the agent through it) — forwarding signals, and
// returns its exit status.
func runSandboxed(l *agentsandbox.Launch) int {
	cmd := exec.Command(l.Argv[0], l.Argv[1:]...)
	cmd.Dir, cmd.Env = l.Dir, l.Env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	setParentDeath(cmd)
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(sigs)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "conductor sandbox exec: refusing to launch: start sandbox: %v\n", err)
		return sandboxRefused
	}
	go func() {
		for s := range sigs {
			_ = cmd.Process.Signal(s)
		}
	}()
	err := cmd.Wait()
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	fmt.Fprintf(os.Stderr, "conductor sandbox exec: %v\n", err)
	return 1
}

// modelRouteEnv is the part of the launcher's environment the daemon needs
// to route the agent's model traffic: its endpoint overrides, never a
// credential.
func modelRouteEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if strings.HasSuffix(k, "_BASE_URL") && (strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://")) {
			out = append(out, kv)
		}
	}
	return out
}

// sandboxControlSocket finds the daemon's control socket: --socket, else
// the state file's directory as the config names it (or the default), read
// without loading the rest of the config (no secrets, no plugins).
func sandboxControlSocket(o sandboxExecOpts) string {
	if o.socket != "" {
		return o.socket
	}
	if o.stateDir != "" {
		config.SetStateDir(o.stateDir)
	}
	path := o.config
	if path == "" {
		path = confdir.File()
	}
	stateFile := filepath.Join(config.StateDir(), "state.json")
	if b, err := os.ReadFile(path); err == nil {
		var c struct {
			Store struct {
				StateFile string `yaml:"state_file"`
			} `yaml:"store"`
		}
		if yaml.Unmarshal(b, &c) == nil && c.Store.StateFile != "" {
			sf := os.ExpandEnv(c.Store.StateFile)
			if strings.HasPrefix(sf, "~/") {
				if h, err := os.UserHomeDir(); err == nil {
					sf = filepath.Join(h, sf[2:])
				}
			}
			stateFile = sf
		}
	}
	return filepath.Join(filepath.Dir(stateFile), "control.sock")
}

// fetchSandboxPolicy asks the daemon for this launch's policy and a fresh
// token. Any failure is a refusal.
func fetchSandboxPolicy(sock string, req controlRequest) (agentsandbox.Policy, error) {
	conn, err := net.DialTimeout("unix", sock, 10*time.Second)
	if err != nil {
		return agentsandbox.Policy{}, fmt.Errorf("the conductor daemon is unreachable at %s (%v)", sock, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return agentsandbox.Policy{}, fmt.Errorf("the conductor daemon at %s: %v", sock, err)
	}
	var resp controlResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return agentsandbox.Policy{}, fmt.Errorf("the conductor daemon at %s gave no policy: %v", sock, err)
	}
	if resp.Error != "" {
		return agentsandbox.Policy{}, fmt.Errorf("the conductor daemon has no sandbox policy for %s: %s", req.Cwd, resp.Error)
	}
	if resp.Policy == nil {
		return agentsandbox.Policy{}, fmt.Errorf("the conductor daemon at %s returned no policy (an older conductor?)", sock)
	}
	return *resp.Policy, nil
}
