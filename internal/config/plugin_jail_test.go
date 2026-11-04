package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// trust: full and fs: reach the plugin ref; trust: full is the opt-out, so
// the ref carries an isolation of mode none.
func TestPluginRefTrustAndFS(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	c, err := loadYAML(t, `
  mon:
    use: slack
    trust: full
  mail:
    use: imap
    fs: [~/certs, "/srv/spool:rw"]
`)
	if err != nil {
		t.Fatal(err)
	}
	mon := c.PluginRefs()["connectors/slack"]
	if !mon.TrustFull || mon.Isolation == nil || !mon.Isolation.OptsOut() {
		t.Fatalf("trust: full must mark the ref and opt it out: %+v", mon)
	}
	mail := c.PluginRefs()["connectors/imap"]
	if mail.TrustFull || mail.Isolation != nil {
		t.Fatalf("an entry without trust: is jailed by default: %+v", mail)
	}
	want := []string{filepath.Join(home, "certs"), "/srv/spool:rw"}
	if !reflect.DeepEqual(mail.FS, want) {
		t.Fatalf("fs = %v, want %v", mail.FS, want)
	}
}

// The jail learns, from the connection config, the files it is pointed at
// (path-named keys, read-only in the jail) and the addresses it serves
// (listen / *_listen). Header keys and relative paths are not facts.
func TestPluginRefConnectionJailFacts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	c, err := loadYAML(t, `
  hook:
    use: webhooky
    listen: ":8088"
    admin_listen: 127.0.0.1:9000
    key_file: ~/hook.key
    tls: { cert_file: /etc/hook/cert.pem }
    dir: relative/dir
`)
	if err != nil {
		t.Fatal(err)
	}
	ref := c.PluginRefs()["connectors/webhooky"]
	wantPaths := []string{filepath.Join(home, "hook.key"), "/etc/hook/cert.pem"}
	for _, p := range wantPaths {
		if !contains(ref.Paths, p) {
			t.Errorf("paths %v lack %s", ref.Paths, p)
		}
	}
	if len(ref.Paths) != len(wantPaths) {
		t.Errorf("paths = %v, want exactly %v", ref.Paths, wantPaths)
	}
	for _, l := range []string{":8088", "127.0.0.1:9000"} {
		if !contains(ref.Listen, l) {
			t.Errorf("listen %v lacks %s", ref.Listen, l)
		}
	}
}

// A shared process gets the union of its instances' fs:, paths and listen
// addresses; an isolated instance's own are not folded in.
func TestPluginRefJailFactsUnion(t *testing.T) {
	c, err := loadYAML(t, `
  a: { use: imap, fs: [/srv/a], key_file: /k/a }
  b: { use: imap, fs: [/srv/b], key_file: /k/b }
  c: { use: imap, fs: [/srv/c], key_file: /k/c, isolate: true }
`)
	if err != nil {
		t.Fatal(err)
	}
	ref := c.PluginRefs()["connectors/imap"]
	if !reflect.DeepEqual(ref.FS, []string{"/srv/a", "/srv/b"}) {
		t.Fatalf("shared fs = %v", ref.FS)
	}
	if !reflect.DeepEqual(ref.Paths, []string{"/k/a", "/k/b"}) {
		t.Fatalf("shared paths = %v", ref.Paths)
	}
	g := ref.Instances["c"]
	if !reflect.DeepEqual(g.FS, []string{"/srv/c"}) || !reflect.DeepEqual(g.Paths, []string{"/k/c"}) {
		t.Fatalf("isolated instance grant = %+v", g)
	}
}

// One instance trusting the plugin while another shares its process jailed
// is a conflict: the shared process cannot be both.
func TestPluginRefTrustMixRefused(t *testing.T) {
	_, err := loadYAML(t, `
  a: { use: imap, trust: full }
  b: { use: imap }
`)
	if err == nil || !strings.Contains(err.Error(), "trust: full") {
		t.Fatalf("a trusted and a jailed instance sharing one process must be refused naming trust: full, got %v", err)
	}
	if _, err := loadYAML(t, `
  a: { use: imap, trust: full }
  b: { use: imap, isolate: true }
`); err != nil {
		t.Fatalf("isolate: true resolves it: %v", err)
	}
}

func TestPluginTrustAndFSValidation(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"bad trust", "  a: { use: imap, trust: sorta }\n", "trust must be"},
		{"trust and isolation", "  a: { use: imap, trust: full, isolation: { mode: namespace } }\n", "not both"},
		{"relative fs", "  a: { use: imap, fs: [certs] }\n", "absolute path"},
		{"runtime relative fs", "runtimes: { r: { use: paseo, fs: [x] } }\n", "absolute path"},
		{"engine relative fs", "engines: { wasmtime: { fs: [x] } }\n", "absolute path"},
		{"sandbox.plugins", "sandbox: { plugins: sometimes }\n", "sandbox.plugins"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadYAML(t, tc.body)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestSandboxPluginsDefault(t *testing.T) {
	var nilCfg *SandboxConfig
	if got := nilCfg.PluginJail(); got != "on" {
		t.Fatalf("no sandbox: block: %q, want on", got)
	}
	for _, v := range []string{"on", "off", "required"} {
		c, err := loadYAML(t, "sandbox: { plugins: "+v+" }\n")
		if err != nil {
			t.Fatalf("%s: %v", v, err)
		}
		if got := c.Sandbox.PluginJail(); got != v {
			t.Fatalf("sandbox.plugins %s read back as %q", v, got)
		}
	}
}

func TestParsePluginFS(t *testing.T) {
	for in, want := range map[string]struct {
		p  string
		rw bool
	}{"/a": {"/a", false}, "/a:rw": {"/a", true}, "/a:ro": {"/a", false}} {
		p, rw := ParsePluginFS(in)
		if p != want.p || rw != want.rw {
			t.Errorf("%s: got %s %v", in, p, rw)
		}
	}
}
