//go:build darwin

package jail

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
