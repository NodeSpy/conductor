package agentsandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeOpencodeConfig(t *testing.T, home, name, body string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "opencode")
	os.MkdirAll(dir, 0o700)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// opencode's allowlist follows the configured provider/model: the model's
// provider, never every provider; the catalog it reads its models from.
func TestModelRouteOpencodeFollowsTheModel(t *testing.T) {
	hosts, lb := ModelRouteFor("opencode", "anthropic/claude-sonnet-4-5", "", nil)
	if len(lb) != 0 || !contains(hosts, "api.anthropic.com") || !contains(hosts, "models.dev") || contains(hosts, "api.openai.com") {
		t.Fatalf("anthropic model: %v %v", hosts, lb)
	}
	hosts, _ = ModelRouteFor("opencode", "openrouter/qwen/qwen3", "", nil)
	if !contains(hosts, "openrouter.ai") || contains(hosts, "api.anthropic.com") {
		t.Fatalf("openrouter model: %v", hosts)
	}
	hosts, _ = ModelRouteFor("opencode", "unheard-of/m", "", nil)
	if len(hosts) != 2 || hosts[0] != "models.dev" || hosts[1] != "models.opencode.ai" {
		t.Fatalf("an unknown provider reaches nothing but the catalog: %v", hosts)
	}
	// The model name alone, without a provider, routes nothing extra.
	if hosts, _ = ModelRouteFor("opencode", "gpt-5", "", nil); len(hosts) != 2 {
		t.Fatalf("a bare model: %v", hosts)
	}
	// Another tool ignores the model.
	if hosts, _ = ModelRouteFor("claude-code", "openai/gpt-5", "", nil); contains(hosts, "api.openai.com") {
		t.Fatalf("claude-code with a model: %v", hosts)
	}
}

// A provider the operator pointed at a local server (Ollama) is a loopback
// endpoint, relayed; at a remote one, that host — not the provider's own.
func TestModelRouteOpencodeConfiguredBaseURL(t *testing.T) {
	home := t.TempDir()
	writeOpencodeConfig(t, home, "opencode.jsonc", `{
  // a local model
  "model": "ollama/qwen3", /* the default */
  "provider": {
    "ollama": {"options": {"baseURL": "http://localhost:11434/v1"}},
    "anthropic": {"options": {"baseURL": "https://llm-gw.example:8443/anthropic"}},
  },
}`)
	hosts, lb := ModelRouteFor("opencode", "", home, nil)
	if len(lb) != 1 || lb[0] != "127.0.0.1:11434" {
		t.Fatalf("the config's default model on a local server is relayed: %v", lb)
	}
	if contains(hosts, "api.anthropic.com") || contains(hosts, "llm-gw.example:8443") {
		t.Fatalf("only the model's provider: %v", hosts)
	}
	hosts, lb = ModelRouteFor("opencode", "anthropic/claude", home, nil)
	if !contains(hosts, "llm-gw.example:8443") || contains(hosts, "api.anthropic.com") {
		t.Fatalf("a provider's configured baseURL replaces its public host: %v", hosts)
	}
	if len(lb) != 1 {
		t.Fatalf("the config's default model is still routed: %v", lb)
	}
}

// With no model anywhere, opencode picks among the providers it can sign
// in to: those in auth.json, those with a key in the environment, and those
// the config defines.
func TestModelRouteOpencodeSignedInProviders(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".local", "share", "opencode"), 0o700)
	os.WriteFile(filepath.Join(home, ".local", "share", "opencode", "auth.json"), []byte(`{"openai":{"type":"oauth"}}`), 0o600)
	hosts, _ := ModelRouteFor("opencode", "", home, []string{"GROQ_API_KEY=k", "MISTRAL_API_KEY="})
	if !contains(hosts, "api.openai.com") || !contains(hosts, "api.groq.com") || contains(hosts, "api.mistral.ai") || contains(hosts, "api.anthropic.com") {
		t.Fatalf("signed-in providers: %v", hosts)
	}
	writeOpencodeConfig(t, home, "opencode.json", `{"provider":{"lmstudio":{"options":{"baseURL":"http://127.0.0.1:1234/v1"}}}}`)
	if _, lb := ModelRouteFor("opencode", "", home, nil); len(lb) != 1 || lb[0] != "127.0.0.1:1234" {
		t.Fatalf("a configured provider is a candidate: %v", lb)
	}
	// A model names one: the sign-ins no longer widen it.
	if hosts, _ := ModelRouteFor("opencode", "groq/llama", home, nil); contains(hosts, "api.openai.com") {
		t.Fatalf("a named model narrows to its provider: %v", hosts)
	}
}

// Later config files win, in opencode's order.
func TestReadOpencodeConfigMergesInOrder(t *testing.T) {
	home := t.TempDir()
	writeOpencodeConfig(t, home, "config.json", `{"model":"a/one","small_model":"a/small","provider":{"x":{"options":{"baseURL":"http://x"}}}}`)
	writeOpencodeConfig(t, home, "opencode.json", `{"model":"b/two"}`)
	c := readOpencodeConfig(home)
	if c.Model != "b/two" || c.SmallModel != "a/small" || c.Provider["x"].Options.BaseURL != "http://x" {
		t.Fatalf("merged config: %+v", c)
	}
	writeOpencodeConfig(t, home, "opencode.jsonc", `not json`)
	if readOpencodeConfig(home).Model != "b/two" {
		t.Fatal("an unreadable file is skipped")
	}
	if c := readOpencodeConfig(""); c.Model != "" {
		t.Fatal("no home, no config")
	}
}

func TestStripJSONC(t *testing.T) {
	for in, want := range map[string]string{
		`{"a": "x // not a comment", "b": 1, // gone` + "\n}": `{"a": "x // not a comment", "b": 1 ` + "\n}",
		`{"u": "http://h/*x*/"} /* c */`:                      `{"u": "http://h/*x*/"} `,
		`[1, 2, ]`:                                            `[1, 2 ]`,
		`{"q": "a \"quoted\" // b"}`:                          `{"q": "a \"quoted\" // b"}`,
		`{"a":1}// end`:                                       `{"a":1}`,
		`,]`:                                                  `]`, ` ]`: ` ]`, `{"a":1} /* unterminated`: `{"a":1} `, `{"a":1} /* x*`: `{"a":1} `, `{"a":1} /* x */`: `{"a":1} `,
	} {
		if got := string(stripJSONC([]byte(in))); got != want {
			t.Errorf("stripJSONC(%q) = %q, want %q", in, got, want)
		}
	}
}

// opencode's identity home has its XDG state, its login shared, its
// settings seeded once — never its sessions.
func TestPrepareHomeOpencode(t *testing.T) {
	real := t.TempDir()
	writeOpencodeConfig(t, real, "opencode.json", `{"model":"anthropic/claude"}`)
	share := filepath.Join(real, ".local", "share", "opencode")
	os.MkdirAll(filepath.Join(share, "storage", "session"), 0o700)
	os.WriteFile(filepath.Join(share, "auth.json"), []byte(`{}`), 0o600)
	os.WriteFile(filepath.Join(share, "storage", "session", "s1.json"), []byte(`{}`), 0o600)
	home, logins, err := PrepareHome(HomeSpec{Root: t.TempDir(), Key: "agent:oc", Tool: "opencode", RealHome: real, GOOS: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{".local/share/opencode", ".config/opencode", ".local/state/opencode", ".cache/opencode"} {
		if fi, err := os.Stat(filepath.Join(home, d)); err != nil || !fi.IsDir() {
			t.Errorf("state dir %s: %v", d, err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "opencode.json")); err != nil || !strings.Contains(string(b), "anthropic/claude") {
		t.Errorf("settings seeded: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "opencode", "storage", "session", "s1.json")); err == nil {
		t.Error("the operator's sessions are never copied")
	}
	if len(logins) != 1 || logins[0].Src != filepath.Join(share, "auth.json") {
		t.Errorf("login shared: %+v", logins)
	}
}

// opencode keeps its provider keys; never a forge or cloud credential.
func TestAgentEnvOpencode(t *testing.T) {
	env := envMap(AgentEnv([]string{"OPENROUTER_API_KEY=or", "ANTHROPIC_API_KEY=a", "GH_TOKEN=g", "AWS_SECRET_ACCESS_KEY=s", "PATH=/usr/bin"},
		Policy{Tool: "opencode", Home: "/h"}))
	if env["OPENROUTER_API_KEY"] != "or" || env["ANTHROPIC_API_KEY"] != "a" || env["GH_TOKEN"] != "" || env["AWS_SECRET_ACCESS_KEY"] != "" {
		t.Fatalf("opencode env: %v", env)
	}
}

// Linux: an exposed address becomes a forwarder socket in a private dir the
// jail never shows; macOS: the profile lets the agent listen on exactly
// that port, reached directly.
func TestWrapExpose(t *testing.T) {
	withPlatform(t, "linux")
	f := newPolicyFixture(t)
	f.pol.Expose = []string{"127.0.0.1:4096"}
	l, err := Wrap(f.pol, []string{"claude"}, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	sock := l.Exposed["127.0.0.1:4096"]
	if sock == "" || !strings.Contains(strings.Join(l.Argv, " "), "--expose "+sock+"=127.0.0.1:4096") {
		t.Fatalf("expose: %q in %q", sock, l.Argv)
	}
	for _, b := range jailOf(t, l.Argv) {
		if within(sock, b.Path) {
			t.Fatalf("the expose socket's dir is shown in the jail: %+v", b)
		}
	}
	if fi, err := os.Stat(filepath.Dir(sock)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("expose dir: %v %v", fi, err)
	}
	l.Cleanup()
	if _, err := os.Stat(filepath.Dir(sock)); !os.IsNotExist(err) {
		t.Fatal("cleanup removes the expose dir")
	}

	// The highest port is a port.
	f = newPolicyFixture(t)
	f.pol.Expose = []string{"127.0.0.1:65535"}
	if l, err := Wrap(f.pol, []string{"claude"}, f.deps); err != nil {
		t.Fatalf("port 65535: %v", err)
	} else {
		l.Cleanup()
	}
	// Nothing exposed: no socket dir is made.
	// (A short dir: SocketDir passes over a long TMPDIR for /tmp.)
	tmp, err := os.MkdirTemp("/tmp", "cx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	f = newPolicyFixture(t)
	t.Setenv("TMPDIR", tmp)
	l, err = Wrap(f.pol, []string{"claude"}, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Exposed) != 0 {
		t.Fatalf("exposed %v", l.Exposed)
	}
	if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
		t.Fatalf("a launch exposing nothing made %v", ents)
	}
	l.Cleanup()

	withPlatform(t, "darwin")
	f = newPolicyFixture(t)
	f.pol.Expose = []string{"127.0.0.1:50123"}
	l, err = Wrap(f.pol, []string{"claude"}, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := l.Exposed["127.0.0.1:50123"]; !ok || s != "" {
		t.Fatalf("macOS reaches the port directly: %v", l.Exposed)
	}
	if !strings.Contains(l.Argv[2], `(allow network-bind network-inbound (local ip "localhost:50123"))`) {
		t.Fatalf("profile lets the agent listen on its port:\n%s", l.Argv[2])
	}
	if strings.Contains(l.Argv[2], "network-bind network-inbound (local ip \"localhost:4096\")") {
		t.Fatal("only the exposed port")
	}
}

// Deps.Bins shows a further program the launch runs by name, like the
// launch binary.
func TestWrapBins(t *testing.T) {
	withPlatform(t, "linux")
	f := newPolicyFixture(t)
	dir := t.TempDir()
	tool := filepath.Join(dir, "claude")
	os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755)
	look := f.deps.LookPath
	f.deps.LookPath = func(n string) (string, error) {
		if n == "claude" {
			return tool, nil
		}
		return look(n)
	}
	f.deps.Bins = []string{"claude"}
	l, err := Wrap(f.pol, []string{"/bin/bash", "-c", "claude"}, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, b := range jailOf(t, l.Argv) {
		if b.Path == dir || b.Path == tool {
			found = b.RO
		}
	}
	if !found {
		t.Fatalf("the tool is shown read-only: %+v", jailOf(t, l.Argv))
	}
}
