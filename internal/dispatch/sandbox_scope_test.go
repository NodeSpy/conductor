package dispatch

import (
	"reflect"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/config"
)

// A dispatch's sandbox scope is its own isolation's allowance and
// overrides, joined by any pack grant on its step; a step without them
// changes nothing, and an agent-authored dispatch never loosens anything.
func TestSandboxScope(t *testing.T) {
	if s := SandboxScope(Request{}); s != nil {
		t.Fatalf("a plain step's scope: %+v", s)
	}
	off := false
	step := config.Step{
		Isolation: &config.IsolationConfig{
			Allow:        &config.SandboxAllow{Tunnels: []string{"Cloudflared@*.trycloudflare.com"}, NonTLS: []string{"bastion.example.com:22"}, Origin: "workflow deploy"},
			Detect:       &config.IsolationDetect{Action: "alert", Tripwires: &off, Origin: "workflow deploy"},
			EgressLimits: &config.SandboxEgressLimits{Idle: "1h", Download: "off", ModelUpload: "32GiB", Origin: "workflow deploy"},
			Limits:       &config.IsolationLimits{Memory: "16G", Pids: 16384},
		},
		GrantedAllow: &config.SandboxAllow{Tunnels: []string{"ssh"}, Pty: true, Origin: "pack rv grant tunnel"},
	}
	got := SandboxScope(Request{Step: step})
	want := &agentsandbox.Scope{
		Allow: &agentsandbox.Allowance{
			Tunnels: []string{"cloudflared", "ssh"}, TunnelHosts: []string{"*.trycloudflare.com"},
			// ssh is allowed by name alone: to any host.
			TunnelAnyHost: true,
			NonTLS:        []string{"bastion.example.com:22"}, Pty: true, By: "workflow deploy + pack rv grant tunnel",
		},
		DetectAction: "alert", Tripwires: &off,
		EgressIdle: int64(3600e9), EgressDownload: -1, ModelUpload: 32 << 30,
		DetectBy: "workflow deploy", LimitsBy: "workflow deploy",
		Memory: "16G", Pids: 16384,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scope\n got %+v %+v\nwant %+v %+v", got, got.Allow, want, want.Allow)
	}
	if s := SandboxScope(Request{Step: step, AgentAuthored: true}); s != nil {
		t.Fatalf("an agent-authored dispatch got a scope: %+v", s)
	}
	// Caps alone are a scope; a recorded one keeps them.
	capsOnly := SandboxScope(Request{Step: config.Step{Isolation: &config.IsolationConfig{Limits: &config.IsolationLimits{Pids: 100}}}})
	if capsOnly == nil || capsOnly.Pids != 100 || capsOnly.Memory != "" {
		t.Fatalf("caps-only scope: %+v", capsOnly)
	}
	if r := Rescope(capsOnly, config.ScopeSources{}); r == nil || r.Pids != 100 {
		t.Fatalf("rescoped caps: %+v", r)
	}
	// Every knob reaches the scope, and a recorded one keeps it.
	no := false
	iso := &config.IsolationConfig{
		EgressOnly: &[]string{"registry.lan:443"}, EgressOnlyNarrow: true, EgressProtocolCheck: &no, Persist: "cache",
		Inspect: []config.InspectEntry{{Host: "npm.lan", CA: "/etc/lan-ca.pem"}},
		Limits:  &config.IsolationLimits{CPU: "150%", CPUWeight: 50, DiskFreeMin: "10GiB"},
		Detect:  &config.IsolationDetect{Interval: "1s", Connections: "close", Origin: "workflow w"},
	}
	k := SandboxScope(Request{Step: config.Step{Isolation: iso}})
	if k == nil || !k.EgressOnlySet || !k.EgressOnlyNarrow || k.EgressOnly[0] != "registry.lan:443" || *k.ProtocolCheck || k.Persist != "cache" ||
		k.CPU != "150%" || k.CPUWeight != 50 || !k.DiskSet || k.DiskFreeBytes != 10<<30 || k.DetectInterval != 1e9 || k.Connections != "close" {
		t.Fatalf("knobs: %+v", k)
	}
	r := Rescope(k, config.ScopeSources{Sites: map[string]*config.IsolationConfig{"workflow w": iso}})
	if len(k.Inspect) != 1 || k.Inspect[0].Host != "npm.lan" || k.Inspect[0].CA != "/etc/lan-ca.pem" {
		t.Fatalf("inspect: %+v", k.Inspect)
	}
	if r == nil || r.Persist != "cache" || !r.EgressOnlySet || r.Connections != "close" || r.CPU != "150%" || len(r.Inspect) != 1 {
		t.Fatalf("rescoped knobs: %+v", r)
	}
	// With a mode, cpu is the wrapper's own.
	if m := SandboxScope(Request{Step: config.Step{Isolation: &config.IsolationConfig{Mode: "namespace", Limits: &config.IsolationLimits{CPU: "2"}}}}); m != nil && m.CPU != "" {
		t.Fatalf("a mode's cpu reached the agent scope: %+v", m)
	}
}

// A recorded scope is what its config says now: recomputed from the scopes
// and grants it names, so a revoked grant or a removed allow: tightens a
// recorded workspace at once, and one whose scopes are gone loosens nothing.
func TestRescopeFollowsTheLiveConfig(t *testing.T) {
	off := false
	deploy := func() *config.IsolationConfig {
		return &config.IsolationConfig{
			Allow:        &config.SandboxAllow{Tunnels: []string{"cloudflared@*.trycloudflare.com"}, Origin: "workflow deploy"},
			Detect:       &config.IsolationDetect{Action: "off", Tripwires: &off, Origin: "workflow deploy"},
			EgressLimits: &config.SandboxEgressLimits{Upload: "off", Origin: "workflow deploy"},
		}
	}
	grant := &config.SandboxAllow{ReverseShell: true, Pty: true, Origin: "pack rv grant shell"}
	iso := deploy()
	recorded := SandboxScope(Request{Step: config.Step{Isolation: iso, GrantedAllow: grant}})
	live := config.ScopeSources{Sites: map[string]*config.IsolationConfig{"workflow deploy": iso}, Grants: map[string]*config.SandboxAllow{"pack rv grant shell": grant}}
	if got := Rescope(recorded, live); !reflect.DeepEqual(got, recorded) {
		t.Fatalf("an unchanged config changed the scope:\n got %+v %+v\nwant %+v %+v", got, got.Allow, recorded, recorded.Allow)
	}

	// The grant revoked: the shell and pty allowances go, deploy's stays.
	revoked := config.ScopeSources{Sites: live.Sites}
	got := Rescope(recorded, revoked)
	if got.Allow == nil || got.Allow.ReverseShell || got.Allow.Pty || strings.Join(got.Allow.Tunnels, ",") != "cloudflared" || got.Allow.By != "workflow deploy" {
		t.Fatalf("a revoked grant still applies: %+v", got.Allow)
	}

	// deploy's allow: and detection removed: only its limits remain.
	narrowed := deploy()
	narrowed.Allow, narrowed.Detect = nil, nil
	got = Rescope(recorded, config.ScopeSources{Sites: map[string]*config.IsolationConfig{"workflow deploy": narrowed}})
	if got == nil || got.Allow != nil || got.DetectAction != "" || got.Tripwires != nil || got.EgressUpload != -1 {
		t.Fatalf("removed knobs still apply: %+v", got)
	}

	// deploy's allowance narrowed in place.
	narrowed = deploy()
	narrowed.Allow = &config.SandboxAllow{Tunnels: []string{"ssh@bastion.example:22"}, Origin: "workflow deploy"}
	got = Rescope(recorded, config.ScopeSources{Sites: map[string]*config.IsolationConfig{"workflow deploy": narrowed}})
	if got.Allow == nil || strings.Join(got.Allow.Tunnels, ",") != "ssh" || got.Allow.TunnelAnyHost {
		t.Fatalf("the live allowance was not used: %+v", got.Allow)
	}

	// Every scope gone: nothing.
	if got := Rescope(recorded, config.ScopeSources{}); got != nil {
		t.Fatalf("a scope with no config left: %+v %+v", got, got.Allow)
	}
	// A site of that name whose knobs another scope wrote is not it.
	other := deploy()
	other.Allow.Origin, other.Detect.Origin, other.EgressLimits.Origin = "workflow other", "workflow other", "workflow other"
	if got := Rescope(recorded, config.ScopeSources{Sites: map[string]*config.IsolationConfig{"workflow deploy": other}}); got != nil {
		t.Fatalf("knobs inherited from another scope: %+v", got)
	}
}
