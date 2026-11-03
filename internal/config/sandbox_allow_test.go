package config

import (
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
		"workflow deploy: sandbox allowance for its agent launches: tunnel tools cloudflared@*.trycloudflare.com, ssh@bastion.example.com:22; non-TLS traffic to bastion.example.com:22",
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

	pin := PackAllowance{Steps: []string{"review/look"}, Allow: SandboxAllow{Tunnels: []string{"cloudflared@*.trycloudflare.com"}}}.Digest()
	if !strings.Contains(notes, "grant: [tunnel@"+pin+"]") {
		t.Fatalf("validate does not print the pinned grant to write:\n%s", notes)
	}
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
	pin := PackAllowance{Steps: []string{"review/look"}, Allow: SandboxAllow{Tunnels: []string{"cloudflared@*.trycloudflare.com"}}}.Digest()
	if _, err := load(allowPack, "    grant: [tunnel]\n"); err == nil || !strings.Contains(err.Error(), "not pinned") || !strings.Contains(err.Error(), "grant: [tunnel@"+pin+"]") {
		t.Fatalf("an unpinned grant: %v", err)
	}
	cfg, err := load(allowPack, "    grant: [tunnel@"+pin+"]\n")
	if err != nil {
		t.Fatal(err)
	}
	if src := cfg.SandboxScopeSources(); src.Grants["pack rv grant tunnel"] == nil {
		t.Fatalf("the grant is not a scope source: %+v", src.Grants)
	}
	for name, edit := range map[string][2]string{
		"widened":        {"allow: { tunnels: [cloudflared@*.trycloudflare.com] }", "allow: { tunnels: [cloudflared@*.trycloudflare.com], reverse_shell: true, pty: true }"},
		"retargeted":     {"allow: { tunnels: [cloudflared@*.trycloudflare.com] }", "allow: { tunnels: [\"x@*\"] }"},
		"another step":   {"steps: [review/look]", "steps: [review/look, review/post]"},
		"reverse shells": {"allow: { tunnels: [cloudflared@*.trycloudflare.com] }", "allow: { tunnels: [cloudflared@*.trycloudflare.com], reverse_shell: true }"},
		"pty relays":     {"allow: { tunnels: [cloudflared@*.trycloudflare.com] }", "allow: { tunnels: [cloudflared@*.trycloudflare.com], pty: true }"},
		"non_tls added":  {"allow: { tunnels: [cloudflared@*.trycloudflare.com] }", "allow: { tunnels: [cloudflared@*.trycloudflare.com], non_tls: [bastion.example:22] }"},
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
	reworded := strings.Replace(allowPack, "publishes a preview through a tunnel", "shares a preview", 1)
	if _, err := load(reworded, "    grant: [tunnel@"+pin+"]\n"); err != nil {
		t.Fatalf("a reworded reason broke the grant: %v", err)
	}
}
