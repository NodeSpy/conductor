package config

import (
	"regexp"
	"strings"
	"testing"
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
		"cpu as an agent cap":    strings.Replace(allowDoc, "detect: { action: alert }", "limits: { cpu: 200% }", 1),
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

// An egress entry is a host pattern, a CIDR range or an http:// URL with a
// path prefix; an https:// URL with a path is an error naming the host.
func TestEgressEntryForms(t *testing.T) {
	for _, e := range []string{"proxy.golang.org", "athens.lan:3000", "10.0.0.0/8:*", "100.64.0.0/10:4873", "*.example.com",
		"http://athens.lan:3000/github.com/myorg/", "http://verdaccio:4873/@myorg/", "http://devpi.lan:3141"} {
		if err := validateEgressEntry("sandbox.egress", e); err != nil {
			t.Errorf("%s: %v", e, err)
		}
	}
	for e, want := range map[string]string{
		"https://registry.example/@myorg/": "a path cannot be enforced on encrypted traffic without TLS interception, which conductor does not do; list the host alone (registry.example:443)",
		"https://athens.lan:8443/x":         "(athens.lan:8443)",
		"ftp://mirror.lan/pub":              "only http:// URLs",
		"athens.lan/github.com":             "a / is only for a CIDR range",
		"http://*.lan/x/":                   "no wildcard",
		"http://athens.lan/a/../b":          "no dot segments",
		"http://athens.lan/x?y=1":           "no user, query or fragment",
	} {
		if err := validateEgressEntry("sandbox.egress", e); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", e, err, want)
		}
	}
	doc := strings.Replace(allowDoc, "sandbox:\n", "sandbox:\n  egress: [\"https://registry.example/@myorg/\"]\n", 1)
	if _, err := loadDoc(t, doc); err == nil || !strings.Contains(err.Error(), "list the host alone") {
		t.Fatalf("an https path entry loaded: %v", err)
	}
	doc = strings.Replace(allowDoc, "      detect: { action: alert }", "      detect: { action: alert }\n      mode: namespace\n      network: { egress: [\"https://registry.example/x\"] }", 1)
	if _, err := loadDoc(t, doc); err == nil || !strings.Contains(err.Error(), "list the host alone") {
		t.Fatalf("an https path entry in a scope loaded: %v", err)
	}
}
