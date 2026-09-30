//go:build darwin

package jail

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/hostcmd"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// On a real Mac: a host command reads its cloned config (copy-on-write
// home), its writes to it are discarded, the real home's other files stay
// unreadable, and conductor's own state is unreadable even though the
// scratch dir lives inside it.
func TestHostRunDarwinCOW(t *testing.T) {
	home, _ := os.UserHomeDir()
	root, err := os.MkdirTemp(filepath.Join(home, "cjl"), "hostrun-")
	// The fake home is INSIDE the real one, as the scratch dir is in real use.
	if err != nil {
		t.Skip("needs ~/cjl")
	}
	defer os.RemoveAll(root)
	fake := filepath.Join(root, "home")
	state := filepath.Join(fake, "state")
	os.MkdirAll(filepath.Join(fake, ".config", "tool"), 0o700)
	os.MkdirAll(filepath.Join(fake, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(fake, ".config", "tool", "cfg"), []byte("login\n"), 0o600)
	os.WriteFile(filepath.Join(fake, ".ssh", "id"), []byte("secret\n"), 0o600)
	os.MkdirAll(filepath.Join(state, "jails"), 0o700)
	os.WriteFile(filepath.Join(state, "audit.jsonl"), []byte("audit\n"), 0o600)
	ws := filepath.Join(state, "worktrees", "w")
	os.MkdirAll(ws, 0o700)
	tmp := filepath.Join(state, "jails", "tmp")
	os.MkdirAll(tmp, 0o700)
	m := &Manager{Root: filepath.Join(state, "jails")}
	hr := hostRun{
		Tool: "sh", Bin: "/bin/sh", Cwd: ws, Home: fake,
		Args:      []string{"-c", `cat "$HOME/.config/tool/cfg"; echo changed >> "$HOME/.config/tool/cfg"; cat ` + fake + `/.ssh/id 2>&1 | head -1; cat ` + state + `/audit.jsonl 2>&1 | head -1; echo out > ` + ws + `/o; echo ws=$?`},
		HomePaths: []string{".config/tool"}, Workspace: ws, TmpDir: tmp, Sensitive: []string{state},
		Env: []string{"PATH=/usr/bin:/bin"},
	}
	var out bytes.Buffer
	res, err := runHost(context.Background(), m, hr, &out, &out)
	t.Logf("exit=%d discarded=%v\n%s", res.Exit, res.Discarded, out.String())
	if err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "login") {
		t.Error("the tool must read its (cloned) config")
	}
	if strings.Contains(s, "secret") || strings.Contains(s, "audit\n") {
		t.Error("other home files and conductor's state must be unreadable")
	}
	if !strings.Contains(s, "ws=0") {
		t.Error("the workspace must be writable")
	}
	if b, _ := os.ReadFile(filepath.Join(fake, ".config", "tool", "cfg")); strings.Contains(string(b), "changed") {
		t.Error("the write to the config must be discarded")
	}
	if len(res.Discarded) == 0 {
		t.Error("the discarded write must be reported")
	}
}

// TestHostRunDarwinRealGH (CONDUCTOR_REAL_GH_PROBE=1) runs the machine's
// own gh read-only (`gh --version`, `gh config list`) the way a host command
// would, against the real home.
func TestHostRunDarwinRealGH(t *testing.T) {
	if os.Getenv("CONDUCTOR_REAL_GH_PROBE") == "" {
		t.Skip("probe")
	}
	home, _ := os.UserHomeDir()
	root, _ := os.MkdirTemp(filepath.Join(home, "cjl"), "ghprobe-")
	defer os.RemoveAll(root)
	state := filepath.Join(root, "state")
	ws := filepath.Join(state, "worktrees", "w")
	tmp := filepath.Join(state, "jails", "tmp")
	os.MkdirAll(ws, 0o700)
	os.MkdirAll(tmp, 0o700)
	m := &Manager{Root: filepath.Join(state, "jails")}
	paths, persist, env, full := homeViewFor("gh")
	hr := hostRun{Tool: "gh", Bin: "/opt/homebrew/bin/gh", Args: []string{"config", "list"}, Cwd: ws, Home: home,
		HomePaths: paths, FullHome: full, Persist: persist, Workspace: ws, TmpDir: tmp, Sensitive: []string{state},
		Env: append([]string{"PATH=/opt/homebrew/bin:/usr/bin:/bin"}, env...)}
	var out bytes.Buffer
	res, err := runHost(context.Background(), m, hr, &out, &out)
	t.Logf("exit=%d err=%v discarded=%v\n%s\nprofile:\n%s", res.Exit, err, res.Discarded, out.String(), hostSeatbelt(hr, filepath.Join(m.Root, "cow-X"), homeEntries(hr)))
}

// On a real Mac: a confined (content-executing) run reads its own config,
// cannot read the rest of the home or conductor's state, cannot write the
// real workspace (its writes land in the clone and are kept for the next
// confined run), gets no stdin, and reaches nothing but the egress proxy,
// which refuses a destination outside the allowlist.
func TestHostRunDarwinConfined(t *testing.T) {
	home, _ := os.UserHomeDir()
	root, err := os.MkdirTemp(filepath.Join(home, "cjl"), "confined-")
	if err != nil {
		t.Skip("needs ~/cjl")
	}
	defer os.RemoveAll(root)
	fake := filepath.Join(root, "home")
	state := filepath.Join(fake, "state")
	os.MkdirAll(filepath.Join(fake, ".config", "tool"), 0o700)
	os.MkdirAll(filepath.Join(fake, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(fake, ".config", "tool", "cfg"), []byte("login\n"), 0o600)
	os.WriteFile(filepath.Join(fake, ".ssh", "id"), []byte("secret\n"), 0o600)
	os.MkdirAll(filepath.Join(state, "jails", "d", "cow-ws", "sh", "up"), 0o700)
	os.WriteFile(filepath.Join(state, "audit.jsonl"), []byte("audit\n"), 0o600)
	ws := filepath.Join(state, "worktrees", "w")
	os.MkdirAll(ws, 0o700)
	os.WriteFile(filepath.Join(ws, "main.tf"), []byte("# config\n"), 0o644)
	tmp := filepath.Join(state, "jails", "tmp")
	os.MkdirAll(tmp, 0o700)
	pm := sandbox.NewProxyManager(nil)
	defer pm.Close()
	addr, cred, revoke, err := pm.EndpointLabeled([]string{"registry.terraform.io"}, "d")
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	_, port, _ := strings.Cut(addr, ":")
	m := &Manager{Root: filepath.Join(state, "jails")}
	script := `cat "$HOME/.config/tool/cfg"
cat ` + fake + `/.ssh/id >/dev/null 2>&1 && echo LEAK-ssh
cat ` + state + `/audit.jsonl >/dev/null 2>&1 && echo LEAK-state
test -e main.tf && echo sees-workspace
test -e rel.txt && echo sees-earlier-write
echo x > ` + ws + `/direct.txt 2>/dev/null && echo WS-WRITABLE
echo x > rel.txt && echo clone-write-ok
echo x > /private/tmp/conductor-confined-probe 2>/dev/null && echo SYS-WRITABLE
curl -s -m 5 -o /dev/null -w 'proxy-code=%{http_connect}\n' https://example.com
curl -s -m 5 --noproxy '*' -o /dev/null https://example.com; echo "direct-rc=$?"
if read -r l; then echo "STDIN=$l"; else echo no-stdin; fi`
	hr := hostRun{
		Tool: "sh", Bin: "/bin/sh", Cwd: ws, Home: fake, Args: []string{"-c", script},
		HomePaths: []string{".config/tool"}, Workspace: ws, TmpDir: tmp, Sensitive: []string{state},
		Env:     append([]string{"PATH=/usr/bin:/bin"}, sandbox.ProxyEnv(addr, cred)...),
		Confine: true, EgressSock: "port:" + port, Stdin: []byte("from-the-agent\n"),
		WsUpper: filepath.Join(state, "jails", "d", "cow-ws", "sh", "up"),
	}
	hr.Stdin = nil // the broker drops stdin for a confined run
	defer os.Remove("/private/tmp/conductor-confined-probe")
	for i := 0; i < 2; i++ {
		var out bytes.Buffer
		res, err := runHost(context.Background(), m, hr, &out, &out)
		s := out.String()
		t.Logf("run %d exit=%d\n%s", i+1, res.Exit, s)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"login", "sees-workspace", "clone-write-ok", "proxy-code=403", "no-stdin"} {
			if !strings.Contains(s, want) {
				t.Errorf("run %d: missing %q", i+1, want)
			}
		}
		for _, bad := range []string{"LEAK-", "WS-WRITABLE", "SYS-WRITABLE", "direct-rc=0", "STDIN="} {
			if strings.Contains(s, bad) {
				t.Errorf("run %d: unexpected %q", i+1, bad)
			}
		}
		if i == 1 && !strings.Contains(s, "sees-earlier-write") {
			t.Error("the second confined run must see the first's write (the dispatch's layer)")
		}
	}
	for _, f := range []string{"rel.txt", "direct.txt"} {
		if _, err := os.Stat(filepath.Join(ws, f)); err == nil {
			t.Errorf("%s reached the real workspace", f)
		}
	}
}

// TestHostRunDarwinLiveTerraform (CONDUCTOR_LIVE_TERRAFORM=1): the real
// terraform in the macOS host-side jail, with the real egress proxy and
// network: init downloads the external provider (the tool's endpoints), and
// the plan runs an external data source whose program tries to read ~/.ssh
// and reach an outside host — both fail while the plan runs.
func TestHostRunDarwinLiveTerraform(t *testing.T) {
	if os.Getenv("CONDUCTOR_LIVE_TERRAFORM") == "" {
		t.Skip("live: CONDUCTOR_LIVE_TERRAFORM=1")
	}
	tf, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform missing")
	}
	home, _ := os.UserHomeDir()
	root, err := os.MkdirTemp(filepath.Join(home, "cjl"), "tflive-")
	if err != nil {
		t.Skip("needs ~/cjl")
	}
	defer os.RemoveAll(root)
	fake := filepath.Join(root, "home")
	state := filepath.Join(fake, "state")
	os.MkdirAll(filepath.Join(fake, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(fake, ".ssh", "id_secret"), []byte("NOT-A-REAL-KEY\n"), 0o600)
	os.MkdirAll(filepath.Join(fake, ".terraform.d"), 0o700)
	ws := filepath.Join(state, "worktrees", "w")
	os.MkdirAll(ws, 0o700)
	for _, n := range []string{"main.tf", "probe.sh"} {
		b, _ := os.ReadFile(filepath.Join("testdata", "tfprobe", n))
		os.WriteFile(filepath.Join(ws, n), b, 0o755)
	}
	tmp := filepath.Join(state, "jails", "tmp")
	upper := filepath.Join(state, "jails", "d", "cow-ws", "terraform", "up")
	os.MkdirAll(tmp, 0o700)
	os.MkdirAll(upper, 0o700)
	pm := sandbox.NewProxyManager(nil)
	defer pm.Close()
	addr, cred, revoke, err := pm.EndpointLabeled(hostcmd.ServiceEndpoints("terraform"), "d")
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	_, port, _ := strings.Cut(addr, ":")
	m := &Manager{Root: filepath.Join(state, "jails")}
	paths, persist, penv, _ := hostcmd.HomeView("terraform", nil)
	env := append([]string{"PATH=/opt/homebrew/bin:/usr/bin:/bin"}, sandbox.ProxyEnv(addr, cred)...)
	for k, v := range penv {
		env = append(env, k+"="+v)
	}
	var all bytes.Buffer
	for _, args := range [][]string{{"init", "-input=false", "-no-color"}, {"plan", "-input=false", "-no-color"}} {
		hr := hostRun{Tool: "terraform", Bin: tf, Args: args, Cwd: ws, Home: fake, HomePaths: paths, Persist: persist,
			Workspace: ws, TmpDir: tmp, Sensitive: []string{state}, Env: env, Confine: true, EgressSock: "port:" + port,
			WsUpper: upper, BinRoots: binaryRoots(tf, exec.LookPath)}
		var out bytes.Buffer
		res, err := runHost(context.Background(), m, hr, &out, &out)
		fmt.Fprintf(&all, "$ terraform %s → exit %d\n%s", strings.Join(args, " "), res.Exit, out.String())
		if err != nil || res.Exit != 0 {
			t.Fatalf("terraform %v: exit %d err %v\n%s", args, res.Exit, err, out.String())
		}
	}
	s := all.String()
	t.Logf("\n%s", s)
	val := func(k string) string {
		m := regexp.MustCompile(`"?` + k + `"?\s*=\s*"([^"]*)"`).FindStringSubmatch(s)
		if m == nil {
			return ""
		}
		return m[1]
	}
	if val("ssh") != "blocked" || val("via_proxy") != "403" || val("workspace") != "written-copy-on-write" {
		t.Errorf("probe: ssh=%q via_proxy=%q workspace=%q", val("ssh"), val("via_proxy"), val("workspace"))
	}
	if rc := val("direct_rc"); rc == "" || rc == "0" {
		t.Errorf("the external program reached the network directly (rc %q)", rc)
	}
	for _, f := range []string{"written-by-probe.txt", ".terraform"} {
		if _, err := os.Stat(filepath.Join(ws, f)); err == nil {
			t.Errorf("%s reached the real workspace", f)
		}
	}
}
