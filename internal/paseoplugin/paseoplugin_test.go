package paseoplugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakePaseo is a paseo CLI stand-in: `plugin ls --json` prints the state
// file (a JSON list), install/reload append to a log.
func fakePaseo(t *testing.T) (bin, state, log string) {
	t.Helper()
	dir := t.TempDir()
	bin, state, log = filepath.Join(dir, "paseo"), filepath.Join(dir, "state.json"), filepath.Join(dir, "calls.log")
	if err := os.WriteFile(state, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
echo "$*" >> ` + log + `
case "$1 $2" in
"plugin ls") cat ` + state + ` ;;
"plugin install") printf '[{"id":"conductor","path":"%s"}]' "$3" > ` + state + `; echo '{}' ;;
"plugin reload") echo '{}' ;;
*) echo "unexpected: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, state, log
}

func calls(t *testing.T, log string) []string {
	t.Helper()
	b, _ := os.ReadFile(log)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// The embedded plugin is written with the socket it dials, and its version
// is the content: the same socket, the same version; another socket, another.
func TestContentsCarryTheSocket(t *testing.T) {
	a, err := Contents("/s/one.sock")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"paseo-plugin.json", "index.server.ts", "server/endpoint.json"} {
		if len(a[f]) == 0 {
			t.Fatalf("missing %s", f)
		}
	}
	if !strings.Contains(string(a["server/endpoint.json"]), "/s/one.sock") {
		t.Fatalf("endpoint: %s", a["server/endpoint.json"])
	}
	if !strings.Contains(string(a["index.server.ts"]), `id: PROVIDER_ID`) || !strings.Contains(string(a["index.server.ts"]), `"session.archive"`) {
		t.Fatal("the plugin registers the provider and serves archive")
	}
	b, _ := Contents("/s/one.sock")
	c, _ := Contents("/s/two.sock")
	if Version(a) != Version(b) || Version(a) == Version(c) {
		t.Fatal("the version is the content")
	}
}

// Install is idempotent: install once, then nothing while unchanged, a
// reload when the plugin changed — and never over someone else's plugin.
func TestInstallIsIdempotent(t *testing.T) {
	bin, state, log := fakePaseo(t)
	dir := filepath.Join(t.TempDir(), "paseo-plugin")
	opt := Options{Dir: dir, Socket: "/s/p.sock", PaseoBin: bin, PaseoHome: "/tmp/ph"}
	ctx := context.Background()

	res, err := Install(ctx, opt)
	if err != nil || res.Action != "installed" {
		t.Fatalf("first install: %+v %v", res, err)
	}
	if got := calls(t, log); len(got) != 2 || got[1] != "plugin install "+dir+" --json --home /tmp/ph" {
		t.Fatalf("calls: %q", got)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "server", "endpoint.json")); !strings.Contains(string(b), "/s/p.sock") {
		t.Fatal("the plugin is written under Dir")
	}
	if res, err = Install(ctx, opt); err != nil || res.Action != "unchanged" {
		t.Fatalf("second install: %+v %v", res, err)
	}
	// A conductor whose plugin differs (another socket here): a reload.
	opt.Socket = "/s/other.sock"
	if res, err = Install(ctx, opt); err != nil || res.Action != "reloaded" {
		t.Fatalf("changed plugin: %+v %v", res, err)
	}
	// A stray file from an older version is removed.
	stray := filepath.Join(dir, "old.ts")
	_ = os.WriteFile(stray, []byte("x"), 0o600)
	if res, err = Install(ctx, opt); err != nil || res.Action != "reloaded" {
		t.Fatalf("stray file: %+v %v", res, err)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatal("a file the plugin no longer has is removed")
	}
	// A `conductor` plugin configured from elsewhere is the operator's.
	_ = os.WriteFile(state, []byte(`[{"id":"conductor","path":"/home/x/my-plugin"}]`), 0o600)
	if _, err := Install(ctx, opt); err == nil || !strings.Contains(err.Error(), "/home/x/my-plugin") {
		t.Fatalf("another plugin with the id must not be replaced: %v", err)
	}
}
