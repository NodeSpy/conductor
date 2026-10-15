package agentsandbox

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// containerExplicit are the agent variables given on the engine's command
// line rather than passed through from the client's environment: the
// client needs its own value of each (its HOME holds its config), and none
// is a secret.
var containerExplicit = map[string]bool{"HOME": true, "PATH": true, "TMPDIR": true, "USER": true, "LOGNAME": true, "SHELL": true, "PWD": true}

// WrapContainer realizes p — prepared by the daemon (Containers.Prepare:
// p.Container.Session) — around argv: a `<engine> exec` into the identity's
// container. The agent's secrets (its model credential, its token, the
// proxy credential) travel in the engine client's environment, passed
// through by name, never on its command line.
func WrapContainer(p Policy, argv []string, d Deps) (*Launch, error) {
	if p.Container == nil || p.Container.Session == nil {
		return nil, fmt.Errorf("agentsandbox: the container launch was not prepared by the daemon")
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("agentsandbox: nothing to launch")
	}
	s := p.Container.Session
	cp := p
	// The agent sees its HOME and the conductor socket where the container
	// has them; the session's env names the socket.
	cp.Home, cp.Socket = sandbox.ContainerHome, ""
	agentEnv := AgentEnv(d.Inherited, cp)
	agentEnv = dedupEnv(append(append(agentEnv, s.Env...), "TMPDIR=/tmp"))
	var explicit, pass, client []string
	for _, kv := range agentEnv {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "PATH":
			// The container's own (its image's, plus HOME's bin dirs).
		case containerExplicit[k]:
			explicit = append(explicit, kv)
		default:
			pass = append(pass, k)
			client = append(client, kv)
		}
	}
	argv = append([]string(nil), argv...)
	if filepath.IsAbs(argv[0]) && !within(argv[0], p.Workspace) {
		// The host's path to the agent CLI is not the container's: the
		// image has its own, on its PATH.
		argv[0] = filepath.Base(argv[0])
	}
	if files := launcherFiles(argv[1:]); len(files) > 0 {
		return nil, fmt.Errorf("agentsandbox: %s hands the agent files in the host's temp dir (%s), which a container does not show", argv[0], strings.Join(files, ", "))
	}
	cwd := p.Cwd
	if cwd == "" {
		cwd = p.Workspace
	}
	inner := append([]string{s.Helper, "sandbox-net", "--run", s.LaunchID, "--"}, argv...)
	args, err := sandbox.ContainerExecArgs(s.Engine, s.Name, s.User, cwd, explicit, pass, inner)
	if err != nil {
		return nil, err
	}
	env := append(EngineClientEnv(d.Inherited), client...)
	signal := func(sig string) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = runQuiet(ctx, s.Engine, EngineClientEnv(d.Inherited), "exec", "--user", s.User, s.Name, s.Helper, "sandbox-net", "--signal", s.LaunchID, sig)
	}
	return &Launch{
		Argv: args, Env: dedupEnv(env), Dir: p.Workspace,
		Cleanup: func() { signal("KILL") },
		Signal:  signal,
		Exposed: s.Exposed,
	}, nil
}
