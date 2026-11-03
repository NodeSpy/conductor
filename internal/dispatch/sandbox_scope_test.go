package dispatch

import (
	"reflect"
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
			Detect:       &config.IsolationDetect{Action: "alert", Tripwires: &off},
			EgressLimits: &config.SandboxEgressLimits{Idle: "1h", Download: "off"},
		},
		GrantedAllow: &config.SandboxAllow{Tunnels: []string{"ssh"}, Pty: true, Origin: "pack rv grant tunnel"},
	}
	got := SandboxScope(Request{Step: step})
	want := &agentsandbox.Scope{
		Allow: &agentsandbox.Allowance{
			Tunnels: []string{"cloudflared", "ssh"}, TunnelHosts: []string{"*.trycloudflare.com"},
			NonTLS: []string{"bastion.example.com:22"}, Pty: true, By: "workflow deploy + pack rv grant tunnel",
		},
		DetectAction: "alert", Tripwires: &off,
		EgressIdle: int64(3600e9), EgressDownload: -1,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scope\n got %+v %+v\nwant %+v %+v", got, got.Allow, want, want.Allow)
	}
	if s := SandboxScope(Request{Step: step, AgentAuthored: true}); s != nil {
		t.Fatalf("an agent-authored dispatch got a scope: %+v", s)
	}
}
