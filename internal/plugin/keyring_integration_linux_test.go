//go:build linux

package plugin

import (
	"bufio"
	"bytes"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// The keyring tests run a real Secret Service: gnome-keyring-daemon on a
// private session bus the test starts (its own dbus-daemon, with no service
// activation), never the user's. They skip without one, unless
// CONDUCTOR_REQUIRE_KEYRING is set (CI), where they fail instead.

type testKeyring struct {
	addr string
	conn *dbus.Conn
	sess dbus.ObjectPath
	coll dbus.ObjectPath
}

func requireKeyringTools(t *testing.T, tools ...string) {
	t.Helper()
	skip := t.Skip
	if os.Getenv("CONDUCTOR_REQUIRE_KEYRING") != "" {
		skip = t.Fatal
	}
	for _, n := range append([]string{"dbus-daemon", "gnome-keyring-daemon"}, tools...) {
		if _, err := exec.LookPath(n); err != nil {
			skip(n + " not on PATH")
		}
	}
}

// startKeyring starts a private session bus and a gnome-keyring on it, its
// login keyring created and unlocked with a throwaway password.
func startKeyring(t *testing.T) *testKeyring {
	t.Helper()
	dir, err := os.MkdirTemp("", "kr") // short: a socket path is at most 107 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	for _, d := range []string{"run", "data", "home"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := filepath.Join(dir, "bus.conf")
	if err := os.WriteFile(cfg, []byte(`<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN" "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig><type>session</type><listen>unix:path=`+filepath.Join(dir, "bus")+`</listen><auth>EXTERNAL</auth>
<policy context="default"><allow send_destination="*" eavesdrop="true"/><allow eavesdrop="true"/><allow own="*"/></policy></busconfig>`), 0o600); err != nil {
		t.Fatal(err)
	}
	bus := exec.Command("dbus-daemon", "--config-file="+cfg, "--nofork", "--nopidfile", "--print-address")
	out, _ := bus.StdoutPipe()
	if err := bus.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Process.Kill(); _ = bus.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("dbus-daemon: %v", err)
	}
	addr := strings.TrimSpace(line)
	// CONDUCTOR_TEST_SECRET_SERVICE names another Secret Service to run
	// instead (a KWallet one, say), started with the same environment.
	gk := exec.Command("gnome-keyring-daemon", "--foreground", "--unlock", "--components=secrets")
	if other := os.Getenv("CONDUCTOR_TEST_SECRET_SERVICE"); other != "" {
		gk = exec.Command(other)
	}
	gk.Env = []string{"PATH=" + os.Getenv("PATH"), "DBUS_SESSION_BUS_ADDRESS=" + addr,
		"XDG_RUNTIME_DIR=" + filepath.Join(dir, "run"), "XDG_DATA_HOME=" + filepath.Join(dir, "data"), "HOME=" + filepath.Join(dir, "home")}
	gk.Stdin = strings.NewReader("throwaway")
	var gkErr bytes.Buffer
	gk.Stdout, gk.Stderr = &gkErr, &gkErr
	if err := gk.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gk.Process.Kill(); _ = gk.Wait() })

	conn, err := dbus.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.Auth(nil); err != nil {
		t.Fatal(err)
	}
	if err := conn.Hello(); err != nil {
		t.Fatal(err)
	}
	for i := 0; ; i++ {
		var has bool
		if err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.freedesktop.secrets").Store(&has); err == nil && has {
			break
		}
		if i > 100 {
			t.Fatalf("the Secret Service did not come up: %s", gkErr.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	k := &testKeyring{addr: addr, conn: conn}
	svc := conn.Object("org.freedesktop.secrets", "/org/freedesktop/secrets")
	var v dbus.Variant
	if err := svc.Call("org.freedesktop.Secret.Service.OpenSession", 0, "plain", dbus.MakeVariant("")).Store(&v, &k.sess); err != nil {
		t.Fatal(err)
	}
	if err := svc.Call("org.freedesktop.Secret.Service.ReadAlias", 0, "default").Store(&k.coll); err != nil || k.coll == "/" {
		t.Fatalf("no default collection (%v): %s", err, gkErr.String())
	}
	return k
}

func (k *testKeyring) store(t *testing.T, label, value string, attrs map[string]string) {
	t.Helper()
	props := map[string]dbus.Variant{
		"org.freedesktop.Secret.Item.Label":      dbus.MakeVariant(label),
		"org.freedesktop.Secret.Item.Attributes": dbus.MakeVariant(attrs),
	}
	s := struct {
		Session     dbus.ObjectPath
		Parameters  []byte
		Value       []byte
		ContentType string
	}{k.sess, []byte{}, []byte(value), "text/plain"}
	var item, prompt dbus.ObjectPath
	if err := k.conn.Object("org.freedesktop.secrets", k.coll).Call("org.freedesktop.Secret.Collection.CreateItem", 0, props, s, true).Store(&item, &prompt); err != nil {
		t.Fatal(err)
	}
}

func (k *testKeyring) search(t *testing.T, attrs map[string]string) []dbus.ObjectPath {
	t.Helper()
	var u, l []dbus.ObjectPath
	if err := k.conn.Object("org.freedesktop.secrets", "/org/freedesktop/secrets").Call("org.freedesktop.Secret.Service.SearchItems", 0, attrs).Store(&u, &l); err != nil {
		t.Fatal(err)
	}
	return append(u, l...)
}

func (k *testKeyring) lock(t *testing.T) {
	t.Helper()
	var locked []dbus.ObjectPath
	var prompt dbus.ObjectPath
	if err := k.conn.Object("org.freedesktop.secrets", "/org/freedesktop/secrets").Call("org.freedesktop.Secret.Service.Lock", 0, []dbus.ObjectPath{k.coll}).Store(&locked, &prompt); err != nil {
		t.Fatal(err)
	}
}

var probeOnce struct {
	sync.Once
	path string
	err  error
}

// keyringProbe builds the jail probe once, into dir's plugin tree.
func keyringProbe(t *testing.T) string {
	t.Helper()
	probeOnce.Do(func() {
		dir, err := os.MkdirTemp("", "krprobe")
		if err != nil {
			probeOnce.err = err
			return
		}
		bin := filepath.Join(dir, "plugins", "probe", "v1")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			probeOnce.err = err
			return
		}
		probeOnce.path = filepath.Join(bin, "jailprobe")
		if out, err := exec.Command("go", "build", "-o", probeOnce.path, "./testdata/jailprobe").CombinedOutput(); err != nil {
			probeOnce.err = err
			probeOnce.path = string(out)
		}
	})
	if probeOnce.err != nil {
		t.Fatalf("build probe: %v %s", probeOnce.err, probeOnce.path)
	}
	return probeOnce.path
}

// runJailedProbe runs the probe jailed to m with args; it returns each
// check's result and what conductor logged meanwhile.
func runJailedProbe(t *testing.T, m Manifest, args ...string) (map[string]string, string) {
	t.Helper()
	return runJailedProbeFor(t, nil, m, args...)
}

// runJailedProbeFor is runJailedProbe for instances configured as conns.
func runJailedProbeFor(t *testing.T, conns []map[string]any, m Manifest, args ...string) (map[string]string, string) {
	t.Helper()
	return runJailedProbeAllowing(t, conns, nil, m, args...)
}

// runJailedProbeAllowing is runJailedProbeFor with the user's allow_keyring.
func runJailedProbeAllowing(t *testing.T, conns []map[string]any, allow []string, m Manifest, args ...string) (map[string]string, string) {
	t.Helper()
	sd := testJailDeps(t)
	var logs syncBuf
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })
	s := Spec{Name: "probe", Kind: KindConnector, BinPath: keyringProbe(t), Manifest: m, Args: args, Conns: conns, AllowKeyring: allow}
	cmd, cleanup, sandboxed, err := buildCommand(s, sd)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !sandboxed {
		t.Fatal("the launch is not jailed")
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(120 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("probe did not finish; stderr:\n%s", stderr.String())
	}
	got := map[string]string{}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if k, v, ok := strings.Cut(line, "\t"); ok {
			got[k] = v
		}
	}
	if os.Getenv("JAIL_VERBOSE") != "" {
		t.Logf("results: %q\nstderr:\n%s\nlogs:\n%s", got, stderr.String(), logs.String())
	}
	return got, logs.String()
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func wantResults(t *testing.T, got map[string]string, want map[string]string, logs string) {
	t.Helper()
	for k, w := range want {
		g, ok := got[k]
		switch {
		case !ok:
			t.Errorf("%s: no result", k)
		case !strings.HasPrefix(g, w):
			t.Errorf("%s: got %q, want %s", k, g, w)
		}
	}
	if t.Failed() {
		t.Logf("all results: %q\nconductor logged:\n%s", got, logs)
	}
}

// ghHome writes a gh config dir that knows a login for github.com and one
// for other.example, both kept in the keyring (no token in the file), and
// returns the manifest entries gh needs to read it.
func ghHome(t *testing.T, fileToken string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "gh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	hosts := "github.com:\n    git_protocol: https\n    users:\n        testuser:\n    user: testuser\n" +
		"other.example:\n    git_protocol: https\n    users:\n        testuser:\n    user: testuser\n" +
		"ghe.example.test:\n    git_protocol: https\n    users:\n        testuser:\n    user: testuser\n"
	if fileToken != "" {
		hosts = "github.com:\n    git_protocol: https\n    oauth_token: " + fileToken + "\n    user: testuser\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte(hosts), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_CONFIG_DIR", dir)
	t.Setenv("GH_NO_UPDATE_NOTIFIER", "1")
	return dir
}

// probeItem is the probe's declared item: what the gh CLI it runs stores
// its github.com login under.
var probeItem = KeyringItem{Label: "probe login", Attributes: map[string]string{"service": "gh:github.com"}}

func ghManifest(ghDir string) Manifest {
	return Manifest{
		Read:     []string{ghDir},
		Commands: []string{"gh", "secret-tool"},
		Env:      []string{"GH_CONFIG_DIR", "GH_NO_UPDATE_NOTIFIER"},
		Keyring:  []KeyringItem{probeItem},
	}
}

// TestJailedKeyringServesOnlyDeclaredItems: a jailed plugin's gh reads the
// login it declares from the user's keyring (a real gnome-keyring), with
// gh's own lookup (go-keyring, a plain session) and libsecret's
// (secret-tool, an encrypted session); it is refused every item it does
// not declare, cannot write to the keyring, and cannot reach the bus.
func TestJailedKeyringServesOnlyDeclaredItems(t *testing.T) {
	requireKeyringTools(t, "gh", "secret-tool")
	k := startKeyring(t)
	k.store(t, "gh github.com", "gho_declared", map[string]string{"service": "gh:github.com", "username": "testuser"})
	k.store(t, "gh other.example", "gho_undeclared", map[string]string{"service": "gh:other.example", "username": "testuser"})
	k.store(t, "unrelated", "undeclared-secret", map[string]string{"service": "unrelated", "username": "x"})
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", k.addr)
	gh := ghHome(t, "")

	got, logs := runJailedProbe(t, ghManifest(gh),
		"out=gh auth token",
		"out=gh auth token --hostname other.example",
		"out=secret-tool lookup service gh:github.com",
		"out=secret-tool lookup service gh:other.example",
		"out=secret-tool lookup service unrelated",
		"out=secret-tool lookup username x",
		"out=secret-tool search --all username testuser",
		"out=secret-tool store --label=planted service gh:github.com username planted",
		"env=DBUS_SESSION_BUS_ADDRESS",
		"dialunix="+strings.TrimPrefix(strings.SplitN(k.addr, ",", 2)[0], "unix:path="),
	)
	wantResults(t, got, map[string]string{
		"out=gh auth token":                                                               "ok:gho_declared",
		"out=gh auth token --hostname other.example":                                      "denied",
		"out=secret-tool lookup service gh:github.com":                                    "ok:gho_declared",
		"out=secret-tool lookup service gh:other.example":                                 "denied",
		"out=secret-tool lookup service unrelated":                                        "denied",
		"out=secret-tool lookup username x":                                               "denied",
		"out=secret-tool store --label=planted service gh:github.com username planted":    "denied",
		"dialunix=" + strings.TrimPrefix(strings.SplitN(k.addr, ",", 2)[0], "unix:path="): "denied",
	}, logs)
	// A search across every user finds only the declared item.
	if s := got["out=secret-tool search --all username testuser"]; strings.Contains(s, "other.example") || strings.Contains(s, "gho_undeclared") || !strings.Contains(s, "secret = gho_declared") {
		t.Errorf("search: got %q, want the declared item alone", s)
	}
	if e := got["env=DBUS_SESSION_BUS_ADDRESS"]; strings.Contains(e, k.addr) || !strings.HasPrefix(e, "set:unix:path=") {
		t.Errorf("the jail's session bus: got %q, want the broker's socket, never the session's", e)
	}
	if n := len(k.search(t, map[string]string{"username": "planted"})); n != 0 {
		t.Errorf("a jailed plugin wrote %d item(s) to the keyring", n)
	}
}

// TestJailedKeyringUndeclaredIsRefused: the same plugin, declaring nothing
// from the keyring, has no session bus at all.
func TestJailedKeyringUndeclaredIsRefused(t *testing.T) {
	requireKeyringTools(t, "gh")
	k := startKeyring(t)
	k.store(t, "gh github.com", "gho_declared", map[string]string{"service": "gh:github.com", "username": "testuser"})
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", k.addr)
	gh := ghHome(t, "")
	m := ghManifest(gh)
	m.Keyring = nil
	m.Env = append(m.Env, "DBUS_SESSION_BUS_ADDRESS") // even passed through, the bus is not in the jail
	got, logs := runJailedProbe(t, m, "out=gh auth token")
	wantResults(t, got, map[string]string{"out=gh auth token": "denied"}, logs)
}

// TestJailedKeyringLocked: a locked keyring is not unlocked for a plugin;
// gh fails as it would, and conductor says what to do.
func TestJailedKeyringLocked(t *testing.T) {
	requireKeyringTools(t, "gh")
	k := startKeyring(t)
	k.store(t, "gh github.com", "gho_declared", map[string]string{"service": "gh:github.com", "username": "testuser"})
	k.lock(t)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", k.addr)
	gh := ghHome(t, "")
	got, logs := runJailedProbe(t, ghManifest(gh), "out=gh auth token")
	wantResults(t, got, map[string]string{"out=gh auth token": "denied"}, logs)
	if !strings.Contains(logs, `plugin probe: its keyring item "probe login" is locked: unlock your keyring`) {
		t.Errorf("no clear message for a locked keyring; logged:\n%s", logs)
	}
}

// TestJailedKeyringNoSessionBus: on a box with no session bus, a CLI that
// keeps its login in a file still works, and conductor says why the
// keyring could not be read.
func TestJailedKeyringNoSessionBus(t *testing.T) {
	requireKeyringTools(t, "gh", "secret-tool")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	os.Unsetenv("DBUS_SESSION_BUS_ADDRESS")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	gh := ghHome(t, "gho_from_file")
	got, logs := runJailedProbe(t, ghManifest(gh), "out=gh auth token", "out=secret-tool lookup service gh:github.com")
	wantResults(t, got, map[string]string{
		"out=gh auth token":                            "ok:gho_from_file",
		"out=secret-tool lookup service gh:github.com": "denied",
	}, logs)
	if !strings.Contains(logs, "plugin probe: its keyring items (probe login) cannot be read: no session bus: DBUS_SESSION_BUS_ADDRESS is unset") {
		t.Errorf("no clear message for a box without a session bus; logged:\n%s", logs)
	}
}

// TestJailedKeyringConfigReference: an item named by the instance's config
// (an enterprise host from its API base URL) is served; the static one it
// does not declare is not.
func TestJailedKeyringConfigReference(t *testing.T) {
	requireKeyringTools(t, "gh")
	k := startKeyring(t)
	k.store(t, "gh enterprise", "ghe_declared", map[string]string{"service": "gh:ghe.example.test", "username": "testuser"})
	k.store(t, "gh github.com", "gho_undeclared", map[string]string{"service": "gh:github.com", "username": "testuser"})
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", k.addr)
	gh := ghHome(t, "")
	m := ghManifest(gh)
	m.Keyring = []KeyringItem{{Label: "probe enterprise login", Attributes: map[string]string{"service": "gh:${host:config:api_base}"}}}
	conns := []map[string]any{{"api_base": "https://ghe.example.test/api/v3"}}
	got, logs := runJailedProbeFor(t, conns, m, "out=gh auth token --hostname ghe.example.test", "out=gh auth token")
	wantResults(t, got, map[string]string{
		"out=gh auth token --hostname ghe.example.test": "ok:ghe_declared",
		"out=gh auth token":                             "denied",
	}, logs)
	// Unconfigured, the item is not granted at all.
	got, logs = runJailedProbeFor(t, []map[string]any{{}}, m, "out=gh auth token --hostname ghe.example.test")
	wantResults(t, got, map[string]string{"out=gh auth token --hostname ghe.example.test": "denied"}, logs)
}

// TestJailedKeyringGitCredentialHelper: git's own keyring helper
// (git-credential-libsecret, libsecret's encrypted session) reads, in the
// jail, the org.git.Password login of a host the instance's config names,
// and not another host's.
func TestJailedKeyringGitCredentialHelper(t *testing.T) {
	requireKeyringTools(t, "git")
	helper := ""
	for _, d := range []string{"/usr/lib/git-core", "/usr/libexec/git-core"} {
		if _, err := os.Stat(filepath.Join(d, "git-credential-libsecret")); err == nil {
			helper = d
		}
	}
	if helper == "" {
		if os.Getenv("CONDUCTOR_REQUIRE_KEYRING") != "" && os.Getenv("CONDUCTOR_REQUIRE_GIT_LIBSECRET") != "" {
			t.Fatal("git-credential-libsecret is not installed")
		}
		t.Skip("git-credential-libsecret is not installed")
	}
	k := startKeyring(t)
	git := func(host string) map[string]string {
		return map[string]string{"xdg:schema": "org.git.Password", "protocol": "https", "server": host, "user": "me"}
	}
	k.store(t, "Git: https://git.example.test/", "git_declared", git("git.example.test"))
	k.store(t, "Git: https://other.example.test/", "git_undeclared", git("other.example.test"))
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", k.addr)
	in := t.TempDir()
	for host, f := range map[string]string{"git.example.test": "declared", "other.example.test": "other"} {
		if err := os.WriteFile(filepath.Join(in, f), []byte("protocol=https\nhost="+host+"\nusername=me\n\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := Manifest{
		Read:     []string{in},
		Commands: []string{"git"},
		Exec:     []string{"command:git/../../lib/git-core", "command:git/../../libexec/git-core"},
		Keyring: []KeyringItem{{Label: "git login for a host in its credential_hosts",
			Attributes: map[string]string{"xdg:schema": "org.git.Password", "protocol": "https", "server": "${host:config:credential_hosts}"}}},
	}
	conns := []map[string]any{{"credential_hosts": []any{"https://git.example.test"}}}
	get := "git -c credential.helper=libsecret credential-libsecret get"
	got, logs := runJailedProbeFor(t, conns, m,
		"outin="+filepath.Join(in, "declared")+" "+get,
		"outin="+filepath.Join(in, "other")+" "+get)
	declared := got["outin="+filepath.Join(in, "declared")+" "+get]
	other := got["outin="+filepath.Join(in, "other")+" "+get]
	t.Logf("declared host: %s", declared)
	t.Logf("other host:    %s", other)
	if !strings.Contains(declared, "password=git_declared") {
		t.Errorf("the declared host's login was not read: %s\n%s", declared, logs)
	}
	if strings.Contains(other, "git_undeclared") {
		t.Errorf("another host's login was read: %s", other)
	}
}

// TestJailedKeyringSafeStorageWithheld: an app's safe-storage key (Chromium's
// schema, the app's name) is the app's master key. A declaration naming the
// app alone matches it; the jail reads the app's own login and never the key,
// whatever it searches for, unless the user's allow_keyring names it. Naming
// a browser outright, or through config, withholds the item at the grant.
func TestJailedKeyringSafeStorageWithheld(t *testing.T) {
	requireKeyringTools(t, "secret-tool")
	k := startKeyring(t)
	const cs = "chrome_libsecret_os_crypt_password_v2"
	k.store(t, "Slack Safe Storage", "SLACK-MASTER-KEY", map[string]string{"xdg:schema": cs, "application": "Slack"})
	k.store(t, "Chrome Safe Storage", "CHROME-MASTER-KEY", map[string]string{"xdg:schema": cs, "application": "chrome"})
	k.store(t, "Slack CLI", "slack-cli-token", map[string]string{"application": "Slack", "service": "slack-cli"})
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", k.addr)
	search := "out=secret-tool search --all xdg:schema " + cs
	lookup := "out=secret-tool lookup application Slack"
	for _, it := range []KeyringItem{
		{Label: "Slack CLI login", Attributes: map[string]string{"application": "Slack"}},
		{Label: "browser", Attributes: map[string]string{"application": "chrome"}},
		{Label: "browser", Attributes: map[string]string{"application": "${config:app}"}},
	} {
		m := Manifest{Commands: []string{"secret-tool"}, Keyring: []KeyringItem{it}}
		got, logs := runJailedProbeFor(t, []map[string]any{{"app": "chrome"}}, m, search, lookup)
		if strings.Contains(got[search], "MASTER-KEY") || strings.Contains(got[lookup], "MASTER-KEY") {
			t.Errorf("%v read a master key: %q", it.Attributes, got)
		}
		if !strings.Contains(logs, "withheld") {
			t.Errorf("%v: nothing logged as withheld:\n%s", it.Attributes, logs)
		}
		if it.Attributes["application"] == "Slack" && got[lookup] != "ok:slack-cli-token" {
			t.Errorf("the app's own login: %q\n%s", got[lookup], logs)
		}
	}
	m := Manifest{Commands: []string{"secret-tool"}, Keyring: []KeyringItem{{Label: "Slack CLI login", Attributes: map[string]string{"application": "Slack"}}}}
	got, logs := runJailedProbeAllowing(t, nil, []string{"application=Slack"}, m, search)
	if !strings.Contains(got[search], "SLACK-MASTER-KEY") || strings.Contains(got[search], "CHROME-MASTER-KEY") {
		t.Errorf("allowed by allow_keyring: %q\n%s", got[search], logs)
	}
}
