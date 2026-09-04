package connector

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/hosts"
)

// The command connector: remote (or local) command execution as a VERB, per
// the issue's model — running a command on another machine is a transport
// option on a connector, not a connector type of its own. A `hosts:` entry
// referenced by `host:` (or an inline `ssh:`) makes `uses: <name>.run`
// execute there over SSH; with neither, commands run where conductor runs.
//
//	connectors:
//	  build-box: { type: command, host: build-box, cwd: /srv/build }
//	triggers:
//	  - on: gh.release
//	    steps:
//	      - { id: deploy, uses: build-box.run, options: { command: [make, deploy] } }
var commandDecl = &TypeDecl{
	Type: "command",
	Desc: "Run commands, locally or on an SSH host; run returns stdout/stderr/exit_code.",
	Connection: Schema{
		"host": {Type: TString, Desc: "a hosts: entry the commands run on (empty = local)"},
		"ssh":  {Type: TMap, Desc: "an inline SSH target instead of a named hosts: entry"},
		"cwd":  {Type: TString, Desc: "default working directory"},
		"env":  {Type: TMap, Desc: "default environment for every run"},
	},
	Verbs: []VerbDecl{{
		Name: "run", Desc: "run one command",
		Options: Schema{
			"command": {Type: TAny, Required: true, Desc: "argv list, or a string executed through sh -c"},
			"cwd":     {Type: TString, Desc: "working directory (default: the connection's)"},
			"env":     {Type: TMap, Desc: "extra environment (merges over the connection's)"},
			"timeout": {Type: TDuration, Desc: "bound the command (default 10m)"},
		},
		Outputs: Schema{
			"stdout":    {Type: TString},
			"stderr":    {Type: TString},
			"exit_code": {Type: TInt},
		},
	}},
}

func init() { RegisterType(commandDecl, newCommandImpl) }

type commandConn struct {
	Host string             `yaml:"host"`
	SSH  *config.HostConfig `yaml:"ssh"`
	Cwd  string             `yaml:"cwd"`
	Env  map[string]string  `yaml:"env"`
}

type commandImpl struct {
	name string
	conn commandConn
	deps Deps
	ssh  *hosts.Client
}

func newCommandImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	var conn commandConn
	if err := ref.Decode(&conn); err != nil {
		return nil, fmt.Errorf("connector %q: decode command connection: %w", name, err)
	}
	return &commandImpl{name: name, conn: conn, deps: deps, ssh: &hosts.Client{}}, nil
}

func (c *commandImpl) Validate() error {
	if c.conn.Host != "" && c.conn.SSH != nil {
		return fmt.Errorf("connector %q: set host: or inline ssh:, not both", c.name)
	}
	if c.conn.Host != "" && c.deps.Config != nil {
		if _, ok := c.deps.Config.Hosts[c.conn.Host]; !ok {
			return fmt.Errorf("connector %q: unknown host %q", c.name, c.conn.Host)
		}
	}
	if c.conn.SSH != nil && c.conn.SSH.Host == "" {
		return fmt.Errorf("connector %q: inline ssh: needs host: (the address)", c.name)
	}
	return nil
}

func (c *commandImpl) DeclaredEvents() []string { return nil }
func (c *commandImpl) Source([]CompiledTrigger) (core.Integration, error) {
	return nil, nil
}

// target resolves the SSH target (nil = local).
func (c *commandImpl) target() (*hosts.Target, error) {
	if c.conn.SSH != nil {
		return &hosts.Target{Name: "(inline)", Cfg: *c.conn.SSH}, nil
	}
	if c.conn.Host == "" {
		return nil, nil
	}
	hc, ok := c.deps.Config.Hosts[c.conn.Host]
	if !ok {
		return nil, fmt.Errorf("unknown host %q", c.conn.Host)
	}
	return &hosts.Target{Name: c.conn.Host, Cfg: hc}, nil
}

func (c *commandImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	if verb != "run" {
		return nil, fmt.Errorf("command: unknown verb %q", verb)
	}
	script, err := commandScript(opts["command"])
	if err != nil {
		return nil, fmt.Errorf("command.run: %w", err)
	}
	cwd, _ := opts["cwd"].(string)
	if cwd == "" {
		cwd = c.conn.Cwd
	}
	env := map[string]string{}
	for k, v := range c.conn.Env {
		env[k] = v
	}
	if m, ok := opts["env"].(map[string]any); ok {
		for k, v := range m {
			env[k] = fmt.Sprintf("%v", v)
		}
	}
	timeout := 10 * time.Minute
	if d, err := toDuration(opts["timeout"]); err != nil {
		return nil, fmt.Errorf("command.run: options.timeout: %w", err)
	} else if d > 0 {
		timeout = d
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	target, err := c.target()
	if err != nil {
		return nil, fmt.Errorf("command.run: %w", err)
	}
	if target != nil {
		res, err := c.ssh.Script(ctx, *target, script, nil, env, cwd)
		if err != nil {
			return nil, fmt.Errorf("command.run on %s: %w", target.Name, err)
		}
		return map[string]any{"stdout": res.Stdout, "stderr": res.Stderr, "exit_code": res.ExitCode}, nil
	}
	return runLocalScript(ctx, script, cwd, env)
}

// commandScript renders the command option as one sh script: an argv list is
// quoted element-by-element, a string runs as written.
func commandScript(v any) (string, error) {
	switch x := v.(type) {
	case string:
		if x == "" {
			return "", fmt.Errorf("empty command")
		}
		return x, nil
	case []any:
		if len(x) == 0 {
			return "", fmt.Errorf("empty command")
		}
		argv := make([]string, len(x))
		for i, e := range x {
			argv[i] = fmt.Sprintf("%v", e)
		}
		return hosts.ShellJoin(argv), nil
	case []string:
		if len(x) == 0 {
			return "", fmt.Errorf("empty command")
		}
		return hosts.ShellJoin(x), nil
	}
	return "", fmt.Errorf("command must be a string or an argv list, got %T", v)
}

// runLocalScript executes the script through the local sh — the same
// semantics the remote path has, so a connector is portable between local
// and host: by changing one key.
func runLocalScript(ctx context.Context, script, cwd string, env map[string]string) (map[string]any, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			return nil, fmt.Errorf("command.run: %w", err)
		}
	}
	return map[string]any{
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"exit_code": exit,
	}, nil
}
