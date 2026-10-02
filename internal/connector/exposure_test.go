package connector

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/handoff"
	"github.com/NodeSpy/conductor/internal/secrets"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// A web hand-off's expose: names an exposure connector; each draft's link
// comes from that connector's exposes verb, and closing the presentation
// releases it. The tunnel builtin runs a command and reads the URL off it —
// conductor names no tunnel vendor.
func TestWebExposeThroughTheTunnelBuiltin(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	cfg := mustDecodeConfig(t, `
connectors:
  review:
    use: web
    listen: 127.0.0.1:18099
    expose: tun
  tun:
    use: tunnel
    command: [sh, -c, "echo serving at https://t.example/{{.port}}; sleep 30"]
`)
	reg, err := Build(cfg, Deps{Secrets: secrets.New(), Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	review, _ := reg.Get("review")
	if review.DisabledReason != "" {
		t.Fatalf("review disabled: %s", review.DisabledReason)
	}
	ch := review.Impl.(*webImpl).Channel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pres, err := ch.Present(ctx, handoff.Draft{Title: "t", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pres.Ref(), "https://t.example/18099") {
		t.Fatalf("draft link %q does not come from the tunnel", pres.Ref())
	}
	tun := mustTunnel(t, reg)
	if n := tun.leaseCount(); n != 1 {
		t.Fatalf("leases = %d, want 1 while presented", n)
	}
	pres.Close()
	if n := tun.leaseCount(); n != 0 {
		t.Fatalf("leases = %d after close, want 0 (released)", n)
	}
}

// An expose: naming a connector that cannot expose disables the hand-off at
// build, naming why.
func TestWebExposeMustNameAnExposureConnector(t *testing.T) {
	cfg := mustDecodeConfig(t, `
connectors:
  review:
    use: web
    expose: hooks
  hooks:
    use: webhook
`)
	reg, err := Build(cfg, Deps{Secrets: secrets.New()})
	if err != nil {
		t.Fatal(err)
	}
	review, _ := reg.Get("review")
	if !strings.Contains(review.DisabledReason, "declares no exposes verb") {
		t.Fatalf("disabled reason = %q", review.DisabledReason)
	}
	cfg = mustDecodeConfig(t, `
connectors:
  review:
    use: web
    expose: nope
`)
	reg, _ = Build(cfg, Deps{Secrets: secrets.New()})
	review, _ = reg.Get("review")
	if !strings.Contains(review.DisabledReason, `no connector named "nope"`) {
		t.Fatalf("disabled reason = %q", review.DisabledReason)
	}
}

// lan answers with this machine's LAN address (or the host it is given).
func TestLANExposure(t *testing.T) {
	cfg := mustDecodeConfig(t, `
connectors:
  review:
    use: web
    listen: 127.0.0.1:18100
    expose: here
  here:
    use: lan
    host: 192.168.1.50
`)
	reg, err := Build(cfg, Deps{Secrets: secrets.New()})
	if err != nil {
		t.Fatal(err)
	}
	review, _ := reg.Get("review")
	pres, err := review.Impl.(*webImpl).Channel().Present(context.Background(), handoff.Draft{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pres.Ref(), "http://192.168.1.50:18100") {
		t.Fatalf("lan link = %q", pres.Ref())
	}
}

type leaseCounter struct{ t interface{ Leases() int } }

func (l leaseCounter) leaseCount() int { return l.t.Leases() }

// mustTunnel finds the tunnel builtin's handler behind the registry (the
// in-process plugin's own state).
func mustTunnel(t *testing.T, _ *Registry) leaseCounter {
	t.Helper()
	return leaseCounter{t: inprocessTunnel}
}

// Pack consent follows the declaration: any connector type declaring a
// consent scope gets it, and one declaring none does not — whatever its name.
func TestScopeConsentIsDeclared(t *testing.T) {
	RegisterType(&TypeDecl{Type: "acmeforge", Semantics: &sdk.ConnSemantics{
		Scope: &sdk.ConnScope{Dimension: "repo", Option: "repos", Consent: true}}}, nil)
	if dim, ok := config.ScopeConsent("acmeforge"); !ok || dim != "repo" {
		t.Fatalf("declared consent scope not seen: %q %v", dim, ok)
	}
	if _, ok := config.ScopeConsent("cron"); ok {
		t.Fatal("a connector declaring no consent scope got one")
	}
	if _, ok := config.ScopeConsent("github"); !ok {
		t.Fatal("github declares its repo consent scope")
	}
}
