// Prototype of `conductor sandbox exec` — compares the builtin internal/sandbox
// engine with srt behind one wrapper. Evidence only (.scratch).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

type Policy struct {
	Identity      string   `json:"identity"`
	Workspace     string   `json:"workspace"`
	ToolState     string   `json:"toolState"`     // rw bind (per-identity ~/.claude etc.)
	Socket        string   `json:"socket"`        // conductor unix socket (rw bind)
	DenyRead      []string `json:"denyRead"`      // srt only (builtin hides by absence)
	Egress        []string `json:"egress"`        // allowlisted hosts
	Home          string   `json:"home"`          // sandbox HOME
	SelfExe       string   `json:"selfExe"`       // conductor binary (sandbox-net forwarder)
	SrtBin        string   `json:"srtBin"`
	ConductorBin  string   `json:"conductorBin"`
	ExtraReadOnly []string `json:"extraRO"`       // extra ro binds (e.g. /usr already covered)
}

func main() {
	engine := os.Getenv("SBX_ENGINE")
	polPath := os.Getenv("SBX_POLICY")
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: SBX_ENGINE=builtin|srt SBX_POLICY=pol.json sandboxexec -- cmd...")
		os.Exit(2)
	}
	var p Policy
	b, err := os.ReadFile(polPath)
	must(err)
	must(json.Unmarshal(b, &p))

	// Clean env: minimal + HOME + model/proxy passthrough.
	env := append(sandbox.MinimalEnv(), "HOME="+p.Home)
	for _, k := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "OPENAI_API_KEY"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}

	switch engine {
	case "srt":
		execSrt(p, args, env)
	case "builtin":
		execBuiltin(p, args, env)
	default:
		fmt.Fprintln(os.Stderr, "SBX_ENGINE must be builtin|srt")
		os.Exit(2)
	}
}

func execBuiltin(p Policy, args, env []string) {
	spec := &sandbox.Spec{Mode: "namespace", Deny: true, HasEgress: true, Egress: p.Egress}
	pm := sandbox.NewProxyManager(nil)
	binds := []sandbox.BindMount{}
	add := func(path string, ro bool) {
		if path == "" {
			return
		}
		binds = append(binds, sandbox.BindMount{Path: path, RO: ro})
	}
	add(p.ToolState, false)
	add(p.Home, false)
	add(p.ConductorBin, true)
	if p.Socket != "" {
		add(p.Socket, false) // bind the socket FILE only, not its dir (which holds state secrets)
	}
	for _, r := range p.ExtraReadOnly {
		add(r, true)
	}
	deps := sandbox.LocalWrapDeps{
		SelfExe:    func() (string, error) { return p.SelfExe, nil },
		Confine:    true,
		ExtraBinds: binds,
		EgressUnix: pm.UnixEndpoint,
		EgressAddr: pm.Endpoint,
	}
	wrapped, wenv, cleanup, err := sandbox.WrapLocalCommand(spec, args, p.Workspace, env, deps)
	must(err)
	defer cleanup()
	run(wrapped, p.Workspace, wenv)
}

func execSrt(p Policy, args, env []string) {
	settings := map[string]any{
		"filesystem": map[string]any{
			"allowRead":  []string{"/"},
			"denyRead":   p.DenyRead,
			"allowWrite": []string{p.Workspace, "/tmp", p.ToolState},
			"denyWrite":  []string{},
		},
		"network": map[string]any{
			"allowedDomains":      p.Egress,
			"deniedDomains":       []string{},
			"allowAllUnixSockets": true,
		},
	}
	f, _ := os.CreateTemp("", "srt-*.json")
	b, _ := json.Marshal(settings)
	f.Write(b)
	f.Close()
	defer os.Remove(f.Name())
	srt := p.SrtBin
	full := append([]string{srt, "--settings", f.Name(), "--"}, args...)
	run(full, p.Workspace, env)
}

func run(argv []string, dir string, env []string) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
			os.Exit(ws.ExitStatus())
		}
		os.Exit(1)
	}
	must(err)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "sandboxexec:", err)
		os.Exit(1)
	}
}
