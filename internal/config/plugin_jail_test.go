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
    kubeconfig: ~/.kube/prod
    ssh: { known_hosts: /etc/hook/known_hosts }
`)
	if err != nil {
		t.Fatal(err)
	}
	ref := c.PluginRefs()["connectors/webhooky"]
	wantPaths := []string{filepath.Join(home, "hook.key"), "/etc/hook/cert.pem", filepath.Join(home, ".kube/prod"), "/etc/hook/known_hosts"}
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

// The jail learns the host:port destinations the connection config points
// the plugin at: URLs under url-named keys (port from the scheme when it has
// none) and hosts under host-named keys (port from a sibling port key, else
// any port). Secret refs, variables and wildcards are not destinations.
func TestPluginRefConnectionTargets(t *testing.T) {
	c, err := loadYAML(t, `
  lan:
    use: homey
    url: http://ha.lan:8123/api
    api_base: https://ghe.example.com/api/v3
    mqtt:
      broker_url: mqtts://broker.lan
      host: 10.0.0.9
      port: 1884
    db_host: db.lan
    db_port: "5433"
    smtp_server: mail.lan
    webhook_url: env:HOOK_URL
    any_url: https://*.example.com
    ws_endpoint: wss://stream.example.com
    custom_url: gopher://g.lan
    address: 192.168.1.5:9000
    listen: ":8088"
    homeserver: https://matrix.lan:8448
    webhook: { smee: "https://smee.io/abc", secret: "s" }
    note: "see http in the docs"
`)
	if err != nil {
		t.Fatal(err)
	}
	ref := c.PluginRefs()["connectors/homey"]
	want := []string{
		"ha.lan:8123", "ghe.example.com:443", "broker.lan:8883", "10.0.0.9:1884",
		"db.lan:5433", "mail.lan:*", "stream.example.com:443", "g.lan:*", "192.168.1.5:9000",
		"matrix.lan:8448", "smee.io:443",
	}
	for _, w := range want {
		if !contains(ref.Targets, w) {
			t.Errorf("targets %v lack %s", ref.Targets, w)
		}
	}
	if len(ref.Targets) != len(want) {
		t.Errorf("targets = %v, want exactly %v", ref.Targets, want)
	}
	if g := ref.Instances["lan"]; !reflect.DeepEqual(g.Targets, ref.Targets) {
		t.Errorf("instance targets = %v, want %v", g.Targets, ref.Targets)
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

// network: [host] is the explicit host-network opt-out and must stand
// alone; it is not an egress host.
func TestNetworkHostSentinel(t *testing.T) {
	c, err := loadYAML(t, "  mq: { use: mqtt, broker: mqtt.lan, network: [host] }\n")
	if err != nil {
		t.Fatal(err)
	}
	if !IsHostNetwork(c.PluginRefs()["connectors/mqtt"].Network) {
		t.Fatalf("network: [host] is not the host network: %v", c.PluginRefs()["connectors/mqtt"].Network)
	}
	if _, err := loadYAML(t, "  mq: { use: mqtt, network: [host, mqtt.lan] }\n"); err == nil || !strings.Contains(err.Error(), "only entry") {
		t.Fatalf("network: [host, …] must be refused, got %v", err)
	}
	if IsHostNetwork([]string{"host.lan"}) || IsHostNetwork(nil) {
		t.Fatal("only [host] is the host network")
	}
}
