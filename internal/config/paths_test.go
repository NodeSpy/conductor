package config

import (
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// pathNamed is a field name that reads as a path: TestPathFieldsTagged
// requires every string field so named to say how ~ is handled.
var pathNamed = regexp.MustCompile(`(dir|path|file|known_hosts)$`)

// pathField is one field reachable from Config.
type pathField struct {
	id   string // Type.Field
	yaml string // the field's key
	tag  string // its path: tag
	kind reflect.Kind
}

// configFields walks every exported field reachable from Config — steps
// included — once per declaring type.
func configFields() []pathField {
	var out []pathField
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		switch t.Kind() {
		case reflect.Ptr, reflect.Slice, reflect.Array, reflect.Map:
			walk(t.Elem())
			return
		case reflect.Struct:
		default:
			return
		}
		if seen[t] {
			return
		}
		seen[t] = true
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			if name == "-" {
				continue // not written by the operator: a load-time derivation
			}
			if name == "" {
				name = snake(f.Name)
			}
			out = append(out, pathField{id: t.Name() + "." + f.Name, yaml: name, tag: f.Tag.Get("path"), kind: f.Type.Kind()})
			walk(f.Type)
		}
	}
	walk(reflect.TypeOf(Config{}))
	return out
}

func snake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// A field named like a path (…dir, …path, …file, known_hosts) must carry a
// path: tag — local (expandPaths expands it) or a stated reason it is not —
// so a new path field cannot miss the ~ expansion silently.
func TestPathFieldsTagged(t *testing.T) {
	valid := map[string]bool{pathLocal: true, pathRemote: true, pathStep: true, pathRef: true, pathNone: true}
	n := 0
	for _, f := range configFields() {
		if f.tag != "" && !valid[f.tag] {
			t.Errorf("%s: path:%q is not one of local, remote, step, ref, -", f.id, f.tag)
		}
		if f.tag == pathLocal && f.kind != reflect.String && f.kind != reflect.Slice {
			t.Errorf("%s: path:\"local\" on a %s; it applies to a string or []string", f.id, f.kind)
		}
		if f.kind != reflect.String && f.kind != reflect.Slice {
			continue
		}
		if pathNamed.MatchString(f.yaml) {
			n++
			if f.tag == "" {
				t.Errorf("%s (%s) is named like a path but has no path: tag — add path:\"local\" (and a case to TestPathFieldsExpand), or say why not (see paths.go)", f.id, f.yaml)
			}
		}
	}
	if n < 10 {
		t.Fatalf("only %d path-named fields found — the walk is broken", n)
	}
}

// pathCases is every path:"local" field: a config that sets it to a ~ path,
// and how to read it back. TestPathFieldsExpand fails when a tagged field has
// no case here.
var pathCases = []struct {
	field, yaml string
	get         func(*Config) string
}{
	{"Store.StateFile", "store: { state_file: ~/st/state.json }", func(c *Config) string { return c.Store.StateFile }},
	{"Store.AuditLog", "store: { audit_log: ~/st/audit.jsonl }", func(c *Config) string { return c.Store.AuditLog }},
	{"MemoryConfig.Dir", "memory: { dir: ~/mem }", func(c *Config) string { return c.Memory.Dir }},
	{"SandboxConfig.StateDir", "sandbox: { state_dir: ~/agent-state }", func(c *Config) string { return c.Sandbox.StateDir }},
	{"SandboxPush.SigningKey", "sandbox: { push: { signing_key: ~/.ssh/id_ed25519 } }", func(c *Config) string { return c.Sandbox.Push.SigningKey }},
	{"DashboardBind.CertFile", "dashboard: { bind: [ { cert_file: ~/tls/c.pem } ] }", func(c *Config) string { return c.Dashboard.Bind[0].CertFile }},
	{"DashboardBind.KeyFile", "dashboard: { bind: [ { key_file: ~/tls/k.pem } ] }", func(c *Config) string { return c.Dashboard.Bind[0].KeyFile }},
	{"DashboardCert.CertFile", "dashboard: { tailscale: { cert: { cert_file: ~/ts/c.pem, key_file: ~/ts/k.pem } } }", func(c *Config) string { return c.Dashboard.Tailscale.Cert.CertFile }},
	{"DashboardCert.KeyFile", "dashboard: { tailscale: { cert: { cert_file: ~/ts/c.pem, key_file: ~/ts/k.pem } } }", func(c *Config) string { return c.Dashboard.Tailscale.Cert.KeyFile }},
	{"InspectEntry.CA", "sandbox: { inspect: [{ host: npm.lan, ca: ~/lan-ca.pem }] }", func(c *Config) string { return c.Sandbox.Inspect[0].CA }},
	{"BrowserConfig.Chrome", "browser: { chrome: ~/bin/chromium }", func(c *Config) string { return c.Browser.Chrome }},
	{"BrowserMailbox.CAFile", "browser: { mailboxes: { m: { ca_file: ~/ca.pem } } }", func(c *Config) string { return c.Browser.Mailboxes["m"].CAFile }},
	{"RuntimeConfig.Bin", "runtimes: { r: { use: paseo, bin: ~/bin/paseo } }", func(c *Config) string { return c.Runtimes["r"].Bin }},
	{"RuntimeConfig.Home", "runtimes: { r: { use: paseo, home: ~/.paseo-2 } }", func(c *Config) string { return c.Runtimes["r"].Home }},
	{"HostConfig.Key", "hosts: { h: { host: box, key: ~/.ssh/box } }", func(c *Config) string { return c.Hosts["h"].Key }},
	{"HostConfig.KnownHosts", "hosts: { h: { host: box, known_hosts: ~/.ssh/known_hosts } }", func(c *Config) string { return c.Hosts["h"].KnownHosts }},
	{"IsolationConfig.FS", "engines: { e: { isolation: { mode: namespace, fs: [/srv/media, ~/media] } } }", func(c *Config) string { return c.Engines["e"].Isolation.FS[1] }},
	{"ConnectorRef.FS", "connectors: { github: { fs: [/srv/x, ~/.config/gh] } }", func(c *Config) string { return c.ConnectorsMap["github"].FS[1] }},
	{"RuntimeConfig.FS", "runtimes: { r: { use: paseo, fs: [~/.paseo-2] } }", func(c *Config) string { return c.Runtimes["r"].FS[0] }},
	{"EngineConfig.FS", "engines: { e: { fs: [~/models] } }", func(c *Config) string { return c.Engines["e"].FS[0] }},
	{"BotConfig.CharterFile", "bots: { b: { charter_file: ~/bots/charter.md } }", func(c *Config) string { return c.Bots["b"].CharterFile }},
	{"BotConfig.MemoryRepo", "bots: { b: { memory_repo: ~/bots/memory } }", func(c *Config) string { return c.Bots["b"].MemoryRepo }},
	{"BotConfig.SeedDir", "bots: { b: { seed_dir: ~/bots/seed } }", func(c *Config) string { return c.Bots["b"].SeedDir }},
	{"ProactiveChecklist.File", "bots: { b: { proactive: { checklist: { file: ~/bots/checklist.md } } } }", func(c *Config) string { return c.Bots["b"].Proactive.Checklist.File }},
	{"Collection.From", "bots: { b: { collections: { pm: { from: ~/defs/pm.yaml } } } }", func(c *Config) string { return c.Bots["b"].Collections["pm"].From }},
	{"TeamKnowledge.Repo", "teams: { t: { knowledge: { repo: ~/teams/k } } }", func(c *Config) string { return c.Teams["t"].Knowledge.Repo }},
	{"TeamKnowledge.SeedDir", "teams: { t: { knowledge: { seed_dir: ~/teams/seed } } }", func(c *Config) string { return c.Teams["t"].Knowledge.SeedDir }},
}

// decodeConfig is the config as written, before Load's passes.
func decodeConfig(t *testing.T, body string) *Config {
	t.Helper()
	var c Config
	if err := yaml.Unmarshal([]byte(body), &c); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	return &c
}

// Every path:"local" field expands ~/ to the home directory; a bare ~ is
// the home itself; absolute, relative and ~user paths, and names, are left
// as written — and a second pass changes nothing.
func TestPathFieldsExpand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	covered := map[string]bool{}
	for _, tc := range pathCases {
		covered[tc.field] = true
		c := decodeConfig(t, tc.yaml)
		raw := tc.get(c)
		if !strings.HasPrefix(raw, "~/") {
			t.Fatalf("%s: case does not set a ~/ path (%q)", tc.field, raw)
		}
		c.expandPaths()
		if got, want := tc.get(c), filepath.Join(home, raw[2:]); got != want {
			t.Errorf("%s: %q expanded to %q, want %q", tc.field, raw, got, want)
		}
		c.expandPaths()
		if got := tc.get(c); got != filepath.Join(home, raw[2:]) {
			t.Errorf("%s: a second pass changed it to %q", tc.field, got)
		}
		for in, want := range map[string]string{"~": home, "/srv/x": "/srv/x", "rel/x": "rel/x",
			"~other/x": "~other/x", "chromium": "chromium", "x/~/y": "x/~/y"} {
			c := decodeConfig(t, strings.Replace(tc.yaml, raw, strconvQuote(in), 1))
			c.expandPaths()
			if got := tc.get(c); got != want {
				t.Errorf("%s: %q expanded to %q, want %q", tc.field, in, got, want)
			}
		}
	}
	for _, f := range configFields() {
		if f.tag == pathLocal && !covered[f.id] {
			t.Errorf("%s is path:\"local\" but has no case in pathCases", f.id)
		}
	}
}

func strconvQuote(s string) string { return `"` + s + `"` }

// What is not this box's path is left as written: a host's cwd: and
// isolation:, a runtime that runs on another host, a collection's
// repo-relative dir, and every step field (steps are fingerprinted from
// their definition; their paths expand where they run).
func TestPathsNotExpanded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := decodeConfig(t, `
hosts:
  h: { host: box, cwd: ~/work, isolation: { mode: namespace, fs: [~/far] } }
runtimes:
  far: { use: paseo, host: h, bin: ~/bin/paseo, home: ~/.paseo, isolation: { mode: namespace, fs: [~/far] } }
bots:
  b: { collections: { pm: { dir: ~/pm } } }
checks:
  c: { use: cli, workdir: ~/wd, isolation: { mode: namespace, fs: [~/media] }, ssh: { host: box, key: ~/.ssh/k, known_hosts: ~/.ssh/kh } }
workflows:
  w: { steps: [ { id: s, use: cli, workdir: ~/wd, isolation: { mode: namespace, fs: [~/media] } } ] }
triggers:
  - on: gh.push
    steps: [ { id: s, use: cli, workdir: ~/wd } ]
`)
	before := []string{c.Checks["c"].Fingerprint(), c.Workflows["w"].Steps[0].Fingerprint(), c.Triggers[0].Steps[0].Fingerprint()}
	c.expandPaths()
	h, far := c.Hosts["h"], c.Runtimes["far"]
	for name, got := range map[string]string{
		"hosts.cwd": h.Cwd, "hosts.isolation.fs": h.Isolation.FS[0],
		"remote runtime bin": far.Bin, "remote runtime home": far.Home, "remote runtime fs": far.Isolation.FS[0],
		"collection dir": c.Bots["b"].Collections["pm"].Dir,
		"check workdir":  c.Checks["c"].WorkDir, "check fs": c.Checks["c"].Isolation.FS[0], "check ssh key": c.Checks["c"].SSH.Key,
		"check known_hosts": c.Checks["c"].SSH.KnownHosts,
		"workflow workdir":  c.Workflows["w"].Steps[0].WorkDir, "workflow fs": c.Workflows["w"].Steps[0].Isolation.FS[0],
		"trigger workdir": c.Triggers[0].Steps[0].WorkDir,
	} {
		if !strings.HasPrefix(got, "~") {
			t.Errorf("%s: %q was expanded; it must stay as written", name, got)
		}
	}
	after := []string{c.Checks["c"].Fingerprint(), c.Workflows["w"].Steps[0].Fingerprint(), c.Triggers[0].Steps[0].Fingerprint()}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("step fingerprints moved: %v -> %v", before, after)
	}
}

// The pass is copy-on-write: a value the config shares with a step (one
// isolation: block both point at) is replaced in the config, never written
// through, so the step — and its fingerprint — keep what was written.
func TestPathsCopyOnWrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	iso := &IsolationConfig{Mode: "namespace", FS: []string{"~/media"}}
	step := Step{ID: "s", Use: "cli", Isolation: iso}
	fp := step.Fingerprint()
	c := &Config{Engines: map[string]EngineConfig{"e": {Isolation: iso}}, Checks: map[string]Step{"c": step},
		Hosts: map[string]HostConfig{"h": {Key: "~/k"}}}
	shared := c.Hosts
	c.expandPaths()
	if got := c.Engines["e"].Isolation.FS[0]; got != filepath.Join(home, "media") {
		t.Fatalf("engine fs: %q", got)
	}
	if iso.FS[0] != "~/media" || c.Checks["c"].Fingerprint() != fp {
		t.Fatalf("the shared isolation was written through: %v", iso.FS)
	}
	if shared["h"].Key != "~/k" || c.Hosts["h"].Key != filepath.Join(home, "k") {
		t.Fatalf("a changed map must be replaced, not written: %q / %q", shared["h"].Key, c.Hosts["h"].Key)
	}
}

// Connection fields only a connector, store or vault implementation decodes
// live in the raw node: a value under a key named path/dir/file (or ending
// _path, _dir, _file), at any depth, expands — the header, options: and
// other keys do not.
func TestRawConnectionPathsExpand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	c := decodeConfig(t, `
connectors:
  gh:
    use: github
    app: { id: 1, private_key_path: ~/.config/conductor/app.pem }
    certs: [ { ca_file: ~/ca.pem } ]
    token: ~/not-a-path
    options: { path: ~/opt }
stores:
  kv: { type: boltdb, path: ~/kv.db }
vaults:
  v: { type: file, dir: ~/vault }
`)
	raw := c.ConnectorsMap["gh"].raw
	c.expandPaths()
	var gh struct {
		App struct {
			PrivateKeyPath string `yaml:"private_key_path"`
		}
		Certs []struct {
			CAFile string `yaml:"ca_file"`
		}
		Token   string
		Options struct{ Path string }
	}
	if err := c.ConnectorsMap["gh"].Decode(&gh); err != nil {
		t.Fatal(err)
	}
	if gh.App.PrivateKeyPath != filepath.Join(home, ".config/conductor/app.pem") || gh.Certs[0].CAFile != filepath.Join(home, "ca.pem") {
		t.Fatalf("connection paths: %+v", gh)
	}
	if gh.Token != "~/not-a-path" || gh.Options.Path != "~/opt" || c.ConnectorsMap["gh"].Options["path"] != "~/opt" {
		t.Fatalf("non-path keys and options: must stay as written: %+v", gh)
	}
	var orig struct {
		App struct {
			PrivateKeyPath string `yaml:"private_key_path"`
		}
	}
	if err := raw.Decode(&orig); err != nil || orig.App.PrivateKeyPath != "~/.config/conductor/app.pem" {
		t.Fatalf("the original node was written through: %+v", orig)
	}
	var kv, v struct{ Path, Dir string }
	if err := c.Stores["kv"].Decode(&kv); err != nil || kv.Path != filepath.Join(home, "kv.db") {
		t.Fatalf("store path: %+v %v", kv, err)
	}
	if err := c.Vaults["v"].Decode(&v); err != nil || v.Dir != filepath.Join(home, "vault") {
		t.Fatalf("vault dir: %+v %v", v, err)
	}
}

// Through Load: config paths expand, step fields do not, and a step's
// fingerprint does not depend on whose home the daemon runs in.
func TestLoadExpandsPathsNotSteps(t *testing.T) {
	const body = `
memory: { dir: ~/mem }
hosts:
  h: { host: box, key: ~/.ssh/box }
workflows:
  run:
    steps:
      - id: go
        use: cli
        command: [bash, -c, "echo hi"]
        workdir: ~/wd
        isolation: { mode: namespace, fs: ["~/media"] }
`
	var fps []string
	for _, home := range []string{t.TempDir(), t.TempDir()} {
		t.Setenv("HOME", home)
		c, err := loadYAML(t, body)
		if err != nil {
			t.Fatal(err)
		}
		if c.Memory.Dir != filepath.Join(home, "mem") || c.Hosts["h"].Key != filepath.Join(home, ".ssh/box") {
			t.Fatalf("config paths: %q %q", c.Memory.Dir, c.Hosts["h"].Key)
		}
		s := c.Workflows["run"].Steps[0]
		if s.WorkDir != "~/wd" || s.Isolation.FS[0] != "~/media" {
			t.Fatalf("step fields must stay as written: %q %v", s.WorkDir, s.Isolation.FS)
		}
		fps = append(fps, s.Fingerprint())
	}
	if fps[0] != fps[1] {
		t.Fatalf("a step's fingerprint moved with $HOME: %v", fps)
	}
}

// A use:/source reference is parsed, not tagged: a local ~/ path expands
// where it is parsed (the spelling stays in Raw), and a local step's
// isolation fs: may name ~/ (it expands where the sandbox is built) — but not
// on another box, whose home this is not.
func TestRefsAndStepFSExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	u, err := ParseUse(UseKindConnector, "~/bin/conductor-jira")
	if err != nil || u.Origin != OriginLocal || u.Path != filepath.Join(home, "bin/conductor-jira") ||
		u.Raw != "~/bin/conductor-jira" || u.Name != "jira" {
		t.Fatalf("local use: %+v %v", u, err)
	}
	src, err := parseSource("~/packs/kit", "/etc/conductor")
	if err != nil || src.local != filepath.Join(home, "packs/kit") {
		t.Fatalf("local pack source: %+v %v", src, err)
	}
	iso := &IsolationConfig{Mode: "namespace", FS: []string{"~/media"}}
	if err := validateIsolation("step s", iso, false); err != nil {
		t.Fatalf("a local fs: ~/ path: %v", err)
	}
	if err := validateIsolation("step s", iso, true); err == nil {
		t.Fatal("a remote fs: ~/ path must be refused")
	}
	if err := validateIsolation("step s", &IsolationConfig{Mode: "namespace", FS: []string{"~other/x"}}, false); err == nil {
		t.Fatal("~user is not expanded, so it is not absolute")
	}
}
