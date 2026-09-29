package jail

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/hostcmd"
)

const commitPayload = "tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n" +
	"parent 5f1d2a3b4c5d6e7f8091a2b3c4d5e6f708192a3b\n" +
	"author Op <op@example.test> 1790000000 +0000\n" +
	"committer Op <op@example.test> 1790000000 +0000\n\nfix: the thing\n"

func TestParseCommitPayloadAcceptsOnlyCommits(t *testing.T) {
	h, err := parseCommitPayload([]byte(commitPayload))
	if err != nil || h.Tree != "4b825dc642cb6eb9a060e54bf8d69288fbee4904" || len(h.Parents) != 1 || h.Subject != "fix: the thing" {
		t.Fatalf("commit: %+v %v", h, err)
	}
	for name, p := range map[string]string{
		"a tag":          "object 5f1d2a3b4c5d6e7f8091a2b3c4d5e6f708192a3b\ntype commit\ntag v1\ntagger Op <op@x> 1 +0000\n\nv1\n",
		"a blob":         "just some text the agent wants signed\n",
		"already signed": strings.Replace(commitPayload, "\n\n", "\ngpgsig -----BEGIN SSH SIGNATURE-----\n -----END SSH SIGNATURE-----\n\n", 1),
		"no committer":   "tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\nauthor Op <op@x> 1 +0000\n\nm\n",
		"odd header":     strings.Replace(commitPayload, "author", "x-evil 1\nauthor", 1),
		"push cert":      "certificate version 0.1\npusher Op <op@x> 1 +0000\n\npush\n",
		"bad tree":       strings.Replace(commitPayload, "tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904", "tree zz", 1),
	} {
		if _, err := parseCommitPayload([]byte(p)); err == nil {
			t.Errorf("%s must be refused for signing", name)
		}
	}
}

func TestGitEnvRewritesRemotesAndSigning(t *testing.T) {
	d := &Dispatch{LaunchSpec: LaunchSpec{Repo: "acme/app", Git: &GitLayout{OriginURL: "git://forge/acme/app.git"}}}
	env := strings.Join(gitEnv(d), "\n")
	for _, want := range []string{
		"GIT_CONFIG_KEY_0=url.conductor::.insteadOf", "GIT_CONFIG_VALUE_0=git@github.com:",
		"=https://github.com/", "=ssh://git@github.com/",
		"url.conductor::acme/app.insteadOf", "=git://forge/acme/app.git",
		"gpg.ssh.program", "/" + ShimSSHSign, "gpg.program", "/" + ShimGPGSign,
		"credential.helper", "gc.auto", "GIT_TERMINAL_PROMPT=0",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("gitEnv missing %q:\n%s", want, env)
		}
	}
	if !strings.HasPrefix(env, "GIT_CONFIG_COUNT=") {
		t.Fatal("GIT_CONFIG_COUNT first")
	}
}

func TestNormRemote(t *testing.T) {
	for in, want := range map[string]string{"acme/app.git": "acme/app", "acme/app/": "acme/app", "conductor::acme/app": "acme/app", " acme/app ": "acme/app"} {
		if got := normRemote(in); got != want {
			t.Errorf("%q → %q want %q", in, got, want)
		}
	}
}

func TestPorcelainSummary(t *testing.T) {
	out := "To git://forge/acme/app.git\n \t5d9e6e7:refs/heads/pr-1\ta1b2c3d..5d9e6e7\nDone\n"
	if got := porcelainSummary(out, "refs/heads/pr-1"); got != "a1b2c3d..5d9e6e7" {
		t.Fatalf("summary %q", got)
	}
}

func TestPushOnePolicy(t *testing.T) {
	m := &Manager{CheckPush: func(d *Dispatch, branch string, force, del bool) string {
		if force || del || branch != d.HeadBranch {
			return "target: refused"
		}
		return ""
	}}
	var events []Event
	m.Emit = func(e Event) { events = append(events, e) }
	d := &Dispatch{LaunchSpec: LaunchSpec{Repo: "acme/app", HeadBranch: "fix/42", Git: &GitLayout{CommonDir: t.TempDir()}}}
	for _, p := range []GitPush{
		{SHA: "0123456789abcdef0123456789abcdef01234567", Dst: "refs/tags/v1"},
		{SHA: "0123456789abcdef0123456789abcdef01234567", Dst: "refs/heads/main"},
		{SHA: "0123456789abcdef0123456789abcdef01234567", Dst: "refs/heads/fix/42", Force: true},
		{Dst: "refs/heads/fix/42"},
	} {
		if r := m.pushOne(t.Context(), d, p, false); !strings.HasPrefix(r, "error ") {
			t.Errorf("%+v must be refused, got %q", p, r)
		}
	}
	// Only branches are brokered at all — even with a binding that would allow
	// anything, a tag (or any non-branch ref) is refused by the helper itself.
	open := &Manager{CheckPush: func(*Dispatch, string, bool, bool) string { return "" }}
	for _, ref := range []string{"refs/tags/v1", "refs/notes/commits", "refs/pull/1/head", "HEAD"} {
		if r := open.pushOne(t.Context(), d, GitPush{SHA: "0123456789abcdef0123456789abcdef01234567", Dst: ref}, false); !strings.Contains(r, "only branch pushes") {
			t.Errorf("%s must be refused as a non-branch ref: %q", ref, r)
		}
	}
	d.ReadOnly = true
	if r := m.pushOne(t.Context(), d, GitPush{SHA: "0123456789abcdef0123456789abcdef01234567", Dst: "refs/heads/fix/42"}, false); !strings.Contains(r, "review step") {
		t.Fatalf("a reviewer never pushes: %q", r)
	}
	if len(events) != 5 {
		t.Fatalf("every refusal is audited: %d events", len(events))
	}
	// Another repository's remote is refused before any policy.
	if r := m.checkRemote(&Dispatch{LaunchSpec: LaunchSpec{Repo: "acme/app", Git: &GitLayout{CommonDir: "/x"}}}, Request{Remote: "other/repo.git"}); !strings.Contains(r, "not the dispatch's repository") {
		t.Fatalf("remote binding: %q", r)
	}
}

func TestIntentRules(t *testing.T) {
	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, "db", "migrations"), 0o755)
	os.WriteFile(filepath.Join(ws, "big.go"), []byte(strings.Repeat("line\n", 500)), 0o644)
	r := &config.IntentRules{AllowPaths: []string{"src/**", "big.go"}, DenyPaths: []string{"**/migrations/**"}, MaxDeleteLines: 100, DenyTools: []string{"WebFetch"}}
	in := func(m map[string]any) json.RawMessage { b, _ := json.Marshal(m); return b }
	cases := []struct {
		tool string
		in   map[string]any
		deny string
	}{
		{"Edit", map[string]any{"file_path": filepath.Join(ws, "src/a/b.go")}, ""},
		{"Edit", map[string]any{"file_path": "README.md"}, "outside the paths"},
		{"Write", map[string]any{"file_path": filepath.Join(ws, "db/migrations/001.sql")}, "outside the paths"},
		{"Edit", map[string]any{"file_path": "/etc/passwd"}, "outside the workspace"},
		{"WebFetch", map[string]any{"url": "https://x"}, "deny_tools"},
		{"Bash", map[string]any{"command": "rm -f big.go"}, "delete limit"},
		{"Bash", map[string]any{"command": "git rm big.go"}, "delete limit"},
		{"Write", map[string]any{"file_path": filepath.Join(ws, "big.go"), "content": ""}, "delete limit"},
		{"Bash", map[string]any{"command": "go test ./..."}, ""},
	}
	for _, tc := range cases {
		got := intentCheck(r, ws, tc.tool, in(tc.in))
		if (tc.deny == "") != (got == "") || (tc.deny != "" && !strings.Contains(got, tc.deny)) {
			t.Errorf("%s %v: got %q want %q", tc.tool, tc.in, got, tc.deny)
		}
	}
	// No rules → nothing refused.
	if intentCheck(nil, ws, "Edit", in(map[string]any{"file_path": "/etc/passwd"})) != "" {
		t.Fatal("no intent rules, no refusal")
	}
	// deny_paths on its own (no allow list in front of it).
	deny := &config.IntentRules{DenyPaths: []string{"**/migrations/**"}}
	if got := intentCheck(deny, ws, "Edit", in(map[string]any{"file_path": filepath.Join(ws, "db/migrations/001.sql")})); !strings.Contains(got, "must not edit") {
		t.Fatalf("deny_paths: %q", got)
	}
	if got := intentCheck(deny, ws, "Edit", in(map[string]any{"file_path": filepath.Join(ws, "src/a.go")})); got != "" {
		t.Fatalf("deny_paths only: %q", got)
	}
	r2 := &config.IntentRules{DenyPaths: []string{"migrations/**"}}
	if intentCheck(r2, ws, "Bash", in(map[string]any{"command": "rm -rf migrations/002.sql"})) == "" {
		t.Fatal("rm of a denied path")
	}
}

func TestHookSettingsShape(t *testing.T) {
	var st struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type, Command string
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(HookSettings(BinDir)), &st); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []string{"PreToolUse", "PostToolUse"} {
		h := st.Hooks[ev]
		if len(h) != 1 || h[0].Matcher != "*" || h[0].Hooks[0].Command != BinDir+"/"+ShimHook+" "+ev {
			t.Fatalf("%s hook: %+v", ev, h)
		}
	}
}

func TestToolSummary(t *testing.T) {
	if got := toolSummary("Bash", json.RawMessage(`{"command":"git push origin HEAD"}`)); got != "Bash: git push origin HEAD" {
		t.Fatal(got)
	}
	if got := toolSummary("Edit", json.RawMessage(`{"file_path":"src/x.go"}`)); got != "Edit: src/x.go" {
		t.Fatal(got)
	}
}

func TestWantsStdin(t *testing.T) {
	for _, a := range [][]string{{"pr", "comment", "-F", "-"}, {"api", "x", "--input", "-"}, {"apply", "-f", "-"}, {"api", "x", "-F", "body=@-"}, {"-"}} {
		if !wantsStdin(a) {
			t.Errorf("%v reads stdin", a)
		}
	}
	for _, a := range [][]string{{"pr", "view"}, {"pr", "comment", "-b", "x"}} {
		if wantsStdin(a) {
			t.Errorf("%v must not block on stdin", a)
		}
	}
}

func TestShimMainDispatch(t *testing.T) {
	t.Setenv(EnvSock, "")
	if h, _ := ShimMain([]string{"/usr/local/bin/conductor", "run"}); h {
		t.Fatal("conductor's own name runs the CLI")
	}
	if h, _ := ShimMain([]string{"conductor.test"}); h {
		t.Fatal("the test binary is not a shim")
	}
	if h, code := ShimMain([]string{"/usr/bin/gh", "pr", "view"}); !h || code != 127 {
		t.Fatalf("a tool shim outside a jail refuses: %v %d", h, code)
	}
}

func TestScanDiscardedAndWriteBack(t *testing.T) {
	home, scratch := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(home, ".aws", "sso", "cache"), 0o700)
	os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("orig"), 0o600)
	// The overlay upper for entry 0 (.aws) got a discarded write and a persist write.
	up := filepath.Join(scratch, "up", "0")
	os.MkdirAll(filepath.Join(up, "cli", "cache"), 0o700)
	os.MkdirAll(filepath.Join(up, "sso", "cache"), 0o700)
	os.WriteFile(filepath.Join(up, "cli", "cache", "x.json"), []byte("tmp"), 0o600)
	os.WriteFile(filepath.Join(up, "sso", "cache", "tok.json"), []byte("rotated"), 0o600)
	// A top-level file copied into the scratch home and changed.
	os.MkdirAll(filepath.Join(scratch, "home"), 0o700)
	os.WriteFile(filepath.Join(scratch, "home", ".gitconfig"), []byte("changed"), 0o600)
	entries, persist := []string{".aws"}, []string{".aws/sso/cache"}
	d := scanDiscarded(scratch, home, entries, persist)
	if strings.Join(d, ",") != ".aws/cli/cache/x.json,.gitconfig" {
		t.Fatalf("discarded: %v", d)
	}
	writeBack(scratch, home, entries, persist)
	if b, _ := os.ReadFile(filepath.Join(home, ".aws", "sso", "cache", "tok.json")); string(b) != "rotated" {
		t.Fatal("a persist path is written through")
	}
	if _, err := os.Stat(filepath.Join(home, ".aws", "cli", "cache", "x.json")); err == nil {
		t.Fatal("a non-persist write must not reach the real home")
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".gitconfig")); string(b) != "orig" {
		t.Fatal("the real config is untouched")
	}
}

func TestHomeEntries(t *testing.T) {
	home := t.TempDir()
	for _, p := range []string{".config/gh", ".config/git", ".aws/sso/cache"} {
		os.MkdirAll(filepath.Join(home, p), 0o700)
	}
	os.WriteFile(filepath.Join(home, ".gitconfig"), nil, 0o600)
	got := homeEntries(hostRun{Home: home, HomePaths: []string{".gitconfig", ".config/git", ".config/gh", ".ssh"}, Persist: nil})
	if strings.Join(got, ",") != ".gitconfig,.config/git,.config/gh" {
		t.Fatalf("entries (missing ones skipped): %v", got)
	}
	got = homeEntries(hostRun{Home: home, HomePaths: []string{".aws"}, Persist: []string{".aws/sso/cache"}})
	if strings.Join(got, ",") != ".aws" {
		t.Fatalf("persist inside an entry is covered: %v", got)
	}
}

func TestRemoteHelperProtocolCapabilities(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader("capabilities\noption verbosity 1\noption bogus x\n\n")
	if code := RunRemoteHelper([]string{"origin", "acme/app"}, in, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := out.String(); got != "option\nfetch\npush\n\nok\nunsupported\n" {
		t.Fatalf("protocol: %q", got)
	}
}

// hostcmd is exercised end to end by the integration test; here the broker's
// write binding refuses everything for a review step whatever the rule says.
func TestWriteCheckReadOnly(t *testing.T) {
	m := &Manager{CheckWrite: func(*Dispatch, hostcmd.Write) string { return "" }}
	d := &Dispatch{LaunchSpec: LaunchSpec{ReadOnly: true}}
	if r := m.writeCheck(d)(hostcmd.Write{Kind: "comment"}); !strings.Contains(r, "review step") {
		t.Fatalf("read-only: %q", r)
	}
	if r := (&Manager{}).writeCheck(&Dispatch{})(hostcmd.Write{Kind: "comment"}); r == "" {
		t.Fatal("no binding wired → refuse")
	}
}
