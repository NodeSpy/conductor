package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const allowDoc = optoutBase + `
sandbox:
  allow: { tunnels: [tailscale] }
workflows:
  deploy:
    isolation:
      allow:
        tunnels: [cloudflared@*.trycloudflare.com, ssh@bastion.example.com:22]
        non_tls: [bastion.example.com:22]
      detect: { action: alert }
    steps: [ { id: ship, type: agent, prompt: p } ]
  review:
    steps: [ { id: look, type: agent, prompt: p } ]
triggers:
  - on: svc.x
    steps: [ { id: a, workflow: deploy }, { id: b, workflow: review } ]
`

// An allowance is the operator's, scoped: it loads on the workflow that
// names it (labelled with that scope), the other workflow carries none, and
// validate lists it with the global one.
func TestScopedAllowanceLoadsAndIsListed(t *testing.T) {
	cfg, err := loadDoc(t, allowDoc)
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Workflows["deploy"].Isolation.Allow
	if a.Origin != "workflow deploy" {
		t.Fatalf("deploy's allowance: %+v", a)
	}
	if got := a.TunnelTools(); strings.Join(got, ",") != "cloudflared,ssh" {
		t.Fatalf("tools %v", got)
	}
	if got := a.TunnelHosts(); strings.Join(got, ",") != "*.trycloudflare.com,bastion.example.com:22" {
		t.Fatalf("hosts %v", got)
	}
	if cfg.Workflows["review"].Isolation != nil {
		t.Fatal("the other workflow got an isolation")
	}
	if d := cfg.Workflows["deploy"].Isolation.Detect; d.Origin != "workflow deploy" {
		t.Fatalf("deploy's detection override is not labelled: %+v", d)
	}
	if site := cfg.SandboxScopeSources().Sites["workflow deploy"]; site == nil || site.Allow != a {
		t.Fatalf("deploy is not a scope source: %+v", site)
	}
	if cfg.Sandbox.Allow.Origin != "sandbox" {
		t.Fatalf("global origin %q", cfg.Sandbox.Allow.Origin)
	}
	notes := strings.Join(cfg.AllowanceNotes(), "\n")
	for _, want := range []string{
		"sandbox.allow loosens EVERY agent launch: tunnel tools tailscale",
		"workflow deploy: sandbox allowance for its agent launches: tunnel tools cloudflared@*.trycloudflare.com, ssh@bastion.example.com:22; raw (unchecked protocol) traffic to bastion.example.com:22",
		"workflow deploy: sandbox detection loosened for its agent launches: detection action alert",
	} {
		if !strings.Contains(notes, want) {
			t.Errorf("validate does not say %q:\n%s", want, notes)
		}
	}
	if strings.Contains(notes, "review") {
		t.Fatalf("a strict scope is listed:\n%s", notes)
	}
}

func TestSandboxAllowValidation(t *testing.T) {
	bad := map[string]string{
		"non_tls without a port": strings.Replace(allowDoc, "non_tls: [bastion.example.com:22]", "non_tls: [bastion.example.com]", 1),
		"a tool with a space":    strings.Replace(allowDoc, "[tailscale]", "['tail scale']", 1),
		"an empty destination":   strings.Replace(allowDoc, "ssh@bastion.example.com:22", "ssh@", 1),
		"a bad action":           strings.Replace(allowDoc, "action: alert", "action: ignore", 1),
		"a bad egress limit":     strings.Replace(allowDoc, "detect: { action: alert }", "egress_limits: { upload: lots }", 1),
		"allow with mode none":   strings.Replace(allowDoc, "    isolation:\n      allow:", "    isolation:\n      mode: none\n      allow:", 1),
		"allow on a code step":   strings.Replace(allowDoc, "steps: [ { id: look, type: agent, prompt: p } ]", "steps: [ { id: look, run: sh, code: 'true', isolation: { mode: namespace, allow: { pty: true } } } ]", 1),
		"allow on a host":        allowDoc + "hosts:\n  box: { host: box.example, isolation: { mode: namespace, allow: { pty: true } } }\n",
		"a cpu cap not a quota":  strings.Replace(allowDoc, "detect: { action: alert }", "limits: { cpu: \"2\" }", 1),
		"mode-less code limits":  strings.Replace(allowDoc, "steps: [ { id: look, type: agent, prompt: p } ]", "steps: [ { id: look, run: sh, code: 'true', isolation: { limits: { memory: 1G } } } ]", 1),
	}
	for name, doc := range bad {
		if doc == allowDoc {
			t.Fatalf("%s: fixture edit did not apply", name)
		}
		_, err := loadDoc(t, doc)
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "unmarshal") {
			t.Errorf("%s: refused for its shape, not its allowance: %v", name, err)
		}
	}
	// A broad pattern is the operator's to write; a knobs-only block needs
	// no mode, on a paseo step too.
	ok := strings.Replace(allowDoc, "cloudflared@*.trycloudflare.com", "cloudflared@*:*", 1)
	ok = strings.Replace(ok, "{ id: look, type: agent, prompt: p }", "{ id: look, type: agent, prompt: p, isolation: { allow: { pty: true } } }", 1)
	if _, err := loadDoc(t, ok); err != nil {
		t.Fatal(err)
	}
	// A scope's agent launch caps: limits: with no mode, on a paseo step too.
	caps := strings.Replace(allowDoc, "{ id: look, type: agent, prompt: p }", "{ id: look, type: agent, prompt: p, isolation: { limits: { memory: 16G, pids: 16384 } } }", 1)
	cfg, err := loadDoc(t, caps)
	if err != nil {
		t.Fatal(err)
	}
	if l := cfg.Workflows["review"].Steps[0].Isolation.Limits; l == nil || l.Memory != "16G" || l.Pids != 16384 {
		t.Fatalf("a step's caps: %+v", l)
	}
}

// A knobs-only block layers onto the scope outside it; a block with a mode
// replaces it whole, as ever.
func TestLayerIsolation(t *testing.T) {
	outer := &IsolationConfig{Mode: "namespace", Network: &IsolationNetwork{Egress: []string{"a.example"}}, Allow: &SandboxAllow{Pty: true}}
	knobs := &IsolationConfig{Allow: &SandboxAllow{Tunnels: []string{"ssh"}}}
	got := LayerIsolation(outer, knobs)
	if got.Mode != "namespace" || got.Network != outer.Network || got.Allow != knobs.Allow || outer.Allow.Tunnels != nil {
		t.Fatalf("layered %+v (outer %+v)", got, outer)
	}
	caps := &IsolationConfig{Limits: &IsolationLimits{Memory: "16G"}}
	if got := LayerIsolation(outer, caps); got.Mode != "namespace" || got.Allow != outer.Allow || got.Limits != caps.Limits {
		t.Fatalf("caps layered %+v", got)
	}
	full := &IsolationConfig{Mode: "namespace"}
	if LayerIsolation(outer, full) != full || LayerIsolation(nil, knobs) != knobs || LayerIsolation(outer, nil) != outer {
		t.Fatal("replacement semantics changed")
	}
}

const allowPack = `
pack: { name: rv, version: "1.0.0", requires: { conductor: ">=0.1", connectors: { svc: "*" } } }
workflows:
  review:
    steps:
      - { id: look, type: agent, prompt: p, background: true }
      - { id: post, type: agent, prompt: p, background: true }
triggers:
  - name: watch
    on: svc.x
    steps: [ { id: s, workflow: review } ]
allowances:
  tunnel:
    reason: publishes a preview through a tunnel
    steps: [review/look]
    allow: { tunnels: [cloudflared@*.trycloudflare.com] }
`

// A pack's requested allowance is inert until the operator grants it, and
// then applies to the step it names only; a pack cannot write one itself.
func TestPackAllowanceTakesEffectOnlyWhenGranted(t *testing.T) {
	load := func(pack, inst string) (*Config, error) {
		t.Helper()
		dir := t.TempDir()
		writePackSource(t, dir, "src/rv", pack)
		return resolveAndLoad(t, writeDoc(t, dir, "connectors:\n  svc: { use: web }\npacks:\n  rv:\n    source: ./src/rv\n    connectors: { svc: svc }\n"+inst))
	}
	cfg, err := load(allowPack, "")
	if err != nil {
		t.Fatal(err)
	}
	steps := cfg.Workflows["rv/review"].Steps
	if steps[0].GrantedAllow != nil || steps[0].Isolation != nil {
		t.Fatalf("an ungranted request took effect: %+v", steps[0])
	}
	notes := strings.Join(cfg.AllowanceNotes(), "\n")
	if !strings.Contains(notes, `pack rv requests sandbox allowance "tunnel" for review/look (publishes a preview through a tunnel)`) || !strings.Contains(notes, "NOT granted") {
		t.Fatalf("validate does not show the request:\n%s", notes)
	}

	pin := grantPin(t, notes, "tunnel")
	cfg, err = load(allowPack, "    grant: [tunnel@"+pin+"]\n")
	if err != nil {
		t.Fatal(err)
	}
	steps = cfg.Workflows["rv/review"].Steps
	g := steps[0].GrantedAllow
	if g == nil || g.Origin != "pack rv grant tunnel" || strings.Join(g.TunnelTools(), ",") != "cloudflared" {
		t.Fatalf("the granted step: %+v", g)
	}
	if steps[1].GrantedAllow != nil {
		t.Fatal("a grant reached a step its request does not name")
	}
	notes = strings.Join(cfg.AllowanceNotes(), "\n")
	if !strings.Contains(notes, "pack rv grant tunnel (pinned @"+pin+"): tunnel tools cloudflared@*.trycloudflare.com") || strings.Contains(notes, "NOT granted") {
		t.Fatalf("validate after the grant:\n%s", notes)
	}

	if _, err := load(allowPack, "    grant: [nope]\n"); err == nil || !strings.Contains(err.Error(), "names no allowance") {
		t.Fatalf("an unknown grant: %v", err)
	}
	for name, edit := range map[string][2]string{
		"step allow":     {"- { id: look, type: agent, prompt: p, background: true }", "- { id: look, type: agent, prompt: p, background: true, isolation: { allow: { pty: true } } }"},
		"workflow allow": {"  review:\n    steps:", "  review:\n    isolation: { allow: { tunnels: [ssh] } }\n    steps:"},
		"trigger detect": {"    on: svc.x\n", "    on: svc.x\n    isolation: { detect: { action: off } }\n"},
		"egress limits":  {"- { id: post, type: agent, prompt: p, background: true }", "- { id: post, type: agent, prompt: p, background: true, isolation: { egress_limits: { upload: off } } }"},
		"agent caps":     {"- { id: post, type: agent, prompt: p, background: true }", "- { id: post, type: agent, prompt: p, background: true, isolation: { limits: { pids: 65536 } } }"},
	} {
		body := strings.Replace(allowPack, edit[0], edit[1], 1)
		if body == allowPack {
			t.Fatalf("%s: fixture edit did not apply", name)
		}
		if _, err := load(body, "    grant: [tunnel@"+pin+"]\n"); err == nil || !strings.Contains(err.Error(), "never loosen") {
			t.Errorf("%s: a pack loosened its own sandbox: %v", name, err)
		}
	}
	// The operator may write one on a pack's step themselves.
	cfg, err = load(allowPack, "    steps:\n      review/post: { isolation: { allow: { tunnels: [ssh] } } }\n")
	if err != nil {
		t.Fatal(err)
	}
	if a := cfg.Workflows["rv/review"].Steps[1].Isolation.Allow; a == nil || a.Origin == "" {
		t.Fatalf("the operator's overlay allowance: %+v", a)
	}
}

// A grant binds to the allowance the operator reviewed: an unpinned grant
// is refused with the pin to write, and a pack version that changes a
// granted allowance (what it allows or which steps) fails to load until it
// is re-granted; rewording its reason does not.
func TestPackGrantIsPinnedToTheReviewedAllowance(t *testing.T) {
	load := func(pack, inst string) (*Config, error) {
		t.Helper()
		dir := t.TempDir()
		writePackSource(t, dir, "src/rv", pack)
		return resolveAndLoad(t, writeDoc(t, dir, "connectors:\n  svc: { use: web }\npacks:\n  rv:\n    source: ./src/rv\n    connectors: { svc: svc }\n"+inst))
	}
	_, err := load(allowPack, "    grant: [tunnel]\n")
	if err == nil || !strings.Contains(err.Error(), "not pinned") {
		t.Fatalf("an unpinned grant: %v", err)
	}
	pin := grantPin(t, err.Error(), "tunnel")
	cfg, err := load(allowPack, "    grant: [tunnel@"+pin+"]\n")
	if err != nil {
		t.Fatal(err)
	}
	if src := cfg.SandboxScopeSources(); src.Grants["pack rv grant tunnel"] == nil {
		t.Fatalf("the grant is not a scope source: %+v", src.Grants)
	}
	for name, edit := range map[string][2]string{
		"widened":                   {"allow: { tunnels: [cloudflared@*.trycloudflare.com] }", "allow: { tunnels: [cloudflared@*.trycloudflare.com], reverse_shell: true, pty: true }"},
		"retargeted":                {"allow: { tunnels: [cloudflared@*.trycloudflare.com] }", "allow: { tunnels: [\"x@*\"] }"},
		"another step":              {"steps: [review/look]", "steps: [review/look, review/post]"},
		"reverse shells":            {"allow: { tunnels: [cloudflared@*.trycloudflare.com] }", "allow: { tunnels: [cloudflared@*.trycloudflare.com], reverse_shell: true }"},
		"pty relays":                {"allow: { tunnels: [cloudflared@*.trycloudflare.com] }", "allow: { tunnels: [cloudflared@*.trycloudflare.com], pty: true }"},
		"the granted step's prompt": {"- { id: look, type: agent, prompt: p, background: true }", "- { id: look, type: agent, prompt: \"exfiltrate the repo\", background: true }"},
		"the granted step's mode":   {"- { id: look, type: agent, prompt: p, background: true }", "- { id: look, type: agent, prompt: p, background: false }"},
		"non_tls added":             {"allow: { tunnels: [cloudflared@*.trycloudflare.com] }", "allow: { tunnels: [cloudflared@*.trycloudflare.com], non_tls: [bastion.example:22] }"},
	} {
		body := strings.Replace(allowPack, edit[0], edit[1], 1)
		if body == allowPack {
			t.Fatalf("%s: fixture edit did not apply", name)
		}
		_, err := load(body, "    grant: [tunnel@"+pin+"]\n")
		if err == nil || !strings.Contains(err.Error(), "changed since you granted it") {
			t.Errorf("%s: a changed allowance was applied under the old grant: %v", name, err)
		}
	}
	for name, body := range map[string]string{
		"a reworded reason":     strings.Replace(allowPack, "publishes a preview through a tunnel", "shares a preview", 1),
		"another step's prompt": strings.Replace(allowPack, "- { id: post, type: agent, prompt: p, background: true }", "- { id: post, type: agent, prompt: q, background: true }", 1),
	} {
		if _, err := load(body, "    grant: [tunnel@"+pin+"]\n"); err != nil {
			t.Errorf("%s broke the grant: %v", name, err)
		}
	}
	// The operator's own overlay on the granted step is not the pack's
	// change: the pin is the step as the pack ships it.
	if _, err := load(allowPack, "    grant: [tunnel@"+pin+"]\n    steps:\n      review/look: { timeout: 5m }\n"); err != nil {
		t.Fatalf("the operator's overlay broke the grant: %v", err)
	}
}

// grantPin is the digest a validate note or a load error tells the
// operator to grant name with.
func grantPin(t *testing.T, text, name string) string {
	t.Helper()
	m := regexp.MustCompile("grant: \\[" + name + "@([0-9a-f]{16})\\]").FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no pinned grant to write for %q in:\n%s", name, text)
	}
	return m[1]
}

// raw: is the protocol-check exemption's name; non_tls: still reads as it.
// The protocol check's knob is egress_protocol_check, egress_require_tls
// its earlier name.
func TestRawAllowanceAndProtocolCheckKnob(t *testing.T) {
	cfg, err := loadDoc(t, strings.Replace(allowDoc, "non_tls: [bastion.example.com:22]", "raw: [bastion.example.com:22, git.lan:22]", 1))
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Workflows["deploy"].Isolation.Allow
	if got := strings.Join(a.RawTargets(), ","); got != "bastion.example.com:22,git.lan:22" {
		t.Fatalf("raw targets %q", got)
	}
	both := &SandboxAllow{Raw: []string{"a.lan:22"}, NonTLS: []string{"b.lan:22", "a.lan:22"}}
	if got := strings.Join(both.RawTargets(), ","); got != "a.lan:22,b.lan:22" {
		t.Fatalf("joined %q", got)
	}
	if _, err := loadDoc(t, strings.Replace(allowDoc, "non_tls: [bastion.example.com:22]", "raw: [bastion.example.com]", 1)); err == nil || !strings.Contains(err.Error(), ".raw:") {
		t.Fatalf("a raw target without a port: %v", err)
	}
	f, tr := false, true
	for _, c := range []struct {
		s    *SandboxConfig
		want bool
	}{
		{nil, true}, {&SandboxConfig{}, true},
		{&SandboxConfig{EgressProtocolCheck: &f}, false},
		{&SandboxConfig{EgressRequireTLS: &f}, false},
		{&SandboxConfig{EgressProtocolCheck: &tr, EgressRequireTLS: &f}, true},
	} {
		if got := c.s.EgressTLSRequired(); got != c.want {
			t.Errorf("%+v: %v", c.s, got)
		}
	}
}

// An egress entry is a host pattern, a CIDR range or an http(s):// URL
// with a path prefix; an https:// path entry needs its host inspected,
// else it is an error naming inspect: and the host to list.
func TestEgressEntryForms(t *testing.T) {
	for _, e := range []string{"proxy.golang.org", "athens.lan:3000", "10.0.0.0/8:*", "100.64.0.0/10:4873", "*.example.com",
		"http://athens.lan:3000/github.com/myorg/", "http://verdaccio:4873/@myorg/", "http://devpi.lan:3141", "https://npm.lan/@myorg/"} {
		if err := validateEgressEntry("sandbox.egress", e); err != nil {
			t.Errorf("%s: %v", e, err)
		}
	}
	for e, want := range map[string]string{
		"ftp://mirror.lan/pub":     "only http:// and https:// URLs",
		"athens.lan/github.com":    "a / is only for a CIDR range",
		"http://*.lan/x/":          "no wildcard",
		"https://*.lan/x/":         "no wildcard",
		"http://athens.lan/a/../b": "no dot segments",
		"http://athens.lan/x?y=1":  "no user, query or fragment",
	} {
		if err := validateEgressEntry("sandbox.egress", e); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", e, err, want)
		}
	}
	doc := strings.Replace(allowDoc, "sandbox:\n", "sandbox:\n  egress: [\"https://registry.example/@myorg/\"]\n", 1)
	if _, err := loadDoc(t, doc); err == nil || !strings.Contains(err.Error(), "add registry.example to inspect:") || !strings.Contains(err.Error(), "(registry.example:443)") {
		t.Fatalf("an https path entry loaded without inspect: %v", err)
	}
	doc = strings.Replace(allowDoc, "      detect: { action: alert }", "      detect: { action: alert }\n      mode: namespace\n      network: { egress: [\"https://registry.example:8443/x\"] }", 1)
	if _, err := loadDoc(t, doc); err == nil || !strings.Contains(err.Error(), "(registry.example:8443)") {
		t.Fatalf("an https path entry in a scope loaded: %v", err)
	}
	// Inspected — globally, or by the scope — it loads.
	doc = strings.Replace(allowDoc, "sandbox:\n", "sandbox:\n  inspect: [registry.example]\n  egress: [\"https://registry.example/@myorg/\"]\n", 1)
	if _, err := loadDoc(t, doc); err != nil {
		t.Fatalf("an inspected https path entry: %v", err)
	}
	doc = strings.Replace(allowDoc, "      detect: { action: alert }", "      detect: { action: alert }\n      inspect: [registry.example]\n      egress_only: [\"https://registry.example/@myorg/\"]", 1)
	if _, err := loadDoc(t, doc); err != nil {
		t.Fatalf("a scope-inspected https path entry: %v", err)
	}
}

// inspect: takes exact hostnames, optionally with an upstream ca: file;
// wildcards, addresses and ports are refused, and a pack may not set it.
func TestInspectForms(t *testing.T) {
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, testCAPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}
	good := strings.Replace(allowDoc, "sandbox:\n", "sandbox:\n  inspect: [npm.lan, {host: pypi.lan, ca: "+ca+"}]\n", 1)
	cfg, err := loadDoc(t, good)
	if err != nil {
		t.Fatal(err)
	}
	if in := cfg.Sandbox.Inspect; len(in) != 2 || in[0].Host != "npm.lan" || in[1].Host != "pypi.lan" || in[1].CA != ca {
		t.Fatalf("inspect: %+v", in)
	}
	if notes := strings.Join(cfg.AllowanceNotes(), "\n"); !strings.Contains(notes, "sandbox.inspect: conductor terminates TLS to npm.lan, pypi.lan") {
		t.Fatalf("validate does not list the inspection: %s", notes)
	}
	for list, want := range map[string]string{
		"[\"*.npm.lan\"]":                     "exact hostnames only",
		"[\"npm.*\"]":                         "exact hostnames only",
		"[10.0.0.5]":                          "not addresses",
		"[npm.lan:443]":                       "no port",
		"[\"https://npm.lan\"]":               "no port, scheme or path",
		"[{host: npm.lan, ca: /nonexistent}]": "ca:",
	} {
		doc := strings.Replace(allowDoc, "sandbox:\n", "sandbox:\n  inspect: "+list+"\n", 1)
		if _, err := loadDoc(t, doc); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("inspect %s: %v, want %q", list, err, want)
		}
	}
	scoped := strings.Replace(allowDoc, "      detect: { action: alert }", "      detect: { action: alert }\n      inspect: [\"*.lan\"]", 1)
	if _, err := loadDoc(t, scoped); err == nil || !strings.Contains(err.Error(), "isolation.inspect") {
		t.Fatalf("a wildcard scope inspect: %v", err)
	}
	if why := isolationLoosens(&IsolationConfig{Inspect: []InspectEntry{{Host: "npm.lan"}}}, true); !strings.Contains(why, "inspect:") {
		t.Fatalf("a pack's inspect is not refused: %q", why)
	}
	iso := LayerIsolation(&IsolationConfig{Mode: "namespace"}, &IsolationConfig{Inspect: []InspectEntry{{Host: "npm.lan"}}})
	if iso.Mode != "namespace" || len(iso.Inspect) != 1 {
		t.Fatalf("layered: %+v", iso)
	}
}

// testCAPEM is a self-signed certificate (a mirror's private CA).
func testCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mirror ca"}, IsCA: true, BasicConstraintsValid: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// Every agent sandbox knob is the operator's per scope, without a mode and
// on a paseo step too: caps (cpu, cpu_weight, disk_free_min), egress_only,
// the protocol check, persist, and detection's interval and connections.
func TestScopeKnobsLoadAndLayer(t *testing.T) {
	knobs := `    isolation:
      egress_only: [registry.lan:443, "http://athens.lan:3000/github.com/myorg/"]
      egress_protocol_check: false
      persist: cache
      detect: { action: kill, interval: 1s, connections: close }
      limits: { memory: 2G, pids: 512, cpu: 150%, cpu_weight: 50, disk_free_min: 10% }
`
	doc := strings.Replace(allowDoc, "  review:\n    steps:", "  review:\n"+knobs+"    steps:", 1)
	cfg, err := loadDoc(t, doc)
	if err != nil {
		t.Fatal(err)
	}
	iso := cfg.Workflows["review"].Isolation
	if !iso.SandboxKnobsOnly() || len(*iso.EgressOnly) != 2 || *iso.EgressProtocolCheck || iso.Persist != "cache" ||
		iso.Detect.Interval != "1s" || iso.Detect.Connections != "close" || iso.Limits.CPU != "150%" || iso.Limits.CPUWeight != 50 || iso.Limits.DiskFreeMin != "10%" {
		t.Fatalf("knobs: %+v %+v %+v", iso, iso.Detect, iso.Limits)
	}
	if iso.EgressOnlyNarrow {
		t.Fatal("the operator's egress_only marked as a pack's")
	}
	outer := &IsolationConfig{Mode: "namespace", Network: &IsolationNetwork{Egress: []string{"a.example"}}}
	got := LayerIsolation(outer, iso)
	if got.Mode != "namespace" || got.EgressOnly != iso.EgressOnly || got.Persist != "cache" || got.EgressProtocolCheck != iso.EgressProtocolCheck {
		t.Fatalf("layered %+v", got)
	}
	empty := strings.Replace(allowDoc, "  review:\n    steps:", "  review:\n    isolation: { egress_only: [] }\n    steps:", 1)
	if cfg, err := loadDoc(t, empty); err != nil || cfg.Workflows["review"].Isolation.EgressOnly == nil || len(*cfg.Workflows["review"].Isolation.EgressOnly) != 0 {
		t.Fatalf("egress_only: [] (the model alone): %v", err)
	}
	for name, block := range map[string]string{
		"a bad persist":        "{ persist: forever }",
		"a bad interval":       "{ detect: { interval: 1ms } }",
		"a bad connections":    "{ detect: { connections: maybe } }",
		"a bad weight":         "{ limits: { cpu_weight: 20000 } }",
		"a bad disk minimum":   "{ limits: { disk_free_min: lots } }",
		"an https egress_only": `{ egress_only: ["https://registry.lan/x"] }`,
		"an empty egress_only": `{ egress_only: [""] }`,
	} {
		bad := strings.Replace(allowDoc, "  review:\n    steps:", "  review:\n    isolation: "+block+"\n    steps:", 1)
		if _, err := loadDoc(t, bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	code := strings.Replace(allowDoc, "steps: [ { id: look, type: agent, prompt: p } ]", "steps: [ { id: look, run: sh, code: 'true', isolation: { mode: namespace, persist: home } } ]", 1)
	if _, err := loadDoc(t, code); err == nil {
		t.Error("an agent-only knob on a code step: accepted")
	}
	global := strings.Replace(allowDoc, "sandbox:\n", "sandbox:\n  limits: { cpu: 400%, aggregate_cpu: 1200%, cpu_weight: 80 }\n", 1)
	if cfg, err := loadDoc(t, global); err != nil || cfg.Sandbox.Limits.CPU != "400%" || cfg.Sandbox.AggregateCPU() != "1200%" {
		t.Fatalf("global cpu knobs: %v", err)
	}
	if _, err := loadDoc(t, strings.Replace(global, "cpu: 400%", "cpu: lots", 1)); err == nil {
		t.Error("a bad global cpu: accepted")
	}
}

// A pack may narrow with every knob, never loosen: its egress_only is
// marked to narrow only; a looser persist, protocol check, detection or cap
// is the operator's.
func TestPackScopeKnobs(t *testing.T) {
	no := false
	for _, c := range []struct {
		iso   *IsolationConfig
		loose bool
	}{
		{&IsolationConfig{EgressOnly: &[]string{"registry.lan:443"}}, false},
		{&IsolationConfig{EgressProtocolCheck: &no}, true},
		{&IsolationConfig{Persist: "cache"}, true},
		{&IsolationConfig{Persist: "none"}, false},
		{&IsolationConfig{Detect: &IsolationDetect{Connections: "off"}}, true},
		{&IsolationConfig{Detect: &IsolationDetect{Connections: "close", Interval: "1s"}}, false},
		{&IsolationConfig{Detect: &IsolationDetect{Interval: "30s"}}, true},
		{&IsolationConfig{Limits: &IsolationLimits{CPU: "50%", CPUWeight: 20, DiskFreeMin: "10%"}}, false},
		{&IsolationConfig{Limits: &IsolationLimits{CPUWeight: 1000}}, true},
		{&IsolationConfig{Limits: &IsolationLimits{DiskFreeMin: "off"}}, true},
		{&IsolationConfig{Limits: &IsolationLimits{DiskFreeMin: "1%"}}, true},
	} {
		if got := isolationLoosens(c.iso, true) != ""; got != c.loose {
			t.Errorf("%+v: loosens %v, want %v", c.iso, got, c.loose)
		}
	}
	man := &PackManifest{Pack: PackMeta{Name: "rv"}, Workflows: map[string]WorkflowDef{"w": {
		Isolation: &IsolationConfig{EgressOnly: &[]string{"registry.lan:443"}},
		Steps:     []Step{{ID: "s", Type: "agent", Prompt: "p", Isolation: &IsolationConfig{EgressOnly: &[]string{}}}},
	}}}
	if p := checkPackSandboxLoosening(man); len(p) != 0 {
		t.Fatalf("a pack's egress_only refused: %v", p)
	}
	if !man.Workflows["w"].Isolation.EgressOnlyNarrow || !man.Workflows["w"].Steps[0].Isolation.EgressOnlyNarrow {
		t.Fatal("a pack's egress_only is not marked to narrow only")
	}
}
