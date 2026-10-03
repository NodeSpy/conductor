package connector

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
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

// pathAwareExposer is a fake exposes-verb Impl that records the options its
// "open" call received and answers with a fixed URL — for asserting whether
// the host passed a resolved listener path as an OPTION (exposes.path
// declared) or left it to append to the returned URL itself.
type pathAwareExposer struct {
	mu   sync.Mutex
	opts map[string]any
	url  string
}

func (e *pathAwareExposer) Validate() error                                    { return nil }
func (e *pathAwareExposer) DeclaredEvents() []string                           { return nil }
func (e *pathAwareExposer) Source([]CompiledTrigger) (core.Integration, error) { return nil, nil }
func (e *pathAwareExposer) Invoke(_ context.Context, verb string, opts map[string]any) (map[string]any, error) {
	if verb != "open" {
		return nil, fmt.Errorf("pathAwareExposer: unknown verb %q", verb)
	}
	e.mu.Lock()
	e.opts = opts
	e.mu.Unlock()
	return map[string]any{"public_url": e.url}, nil
}

func (e *pathAwareExposer) gotOptions() map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.opts
}

// An exposure verb declaring `exposes.path` gets the resolved path passed as
// that option, and its returned URL is used AS IS — smee's whole point: the
// channel URL already IS the public URL, and the path only matters to the
// plugin's own local relay.
func TestOpenPathPassesPathOptionWhenExposesDeclaresIt(t *testing.T) {
	exp := &pathAwareExposer{url: "http://mock-smee:8080/e2e-gh-channel"}
	decl := &TypeDecl{
		Type: "pathtun",
		Verbs: []VerbDecl{{
			Name:      "open",
			Semantics: &sdk.VerbSemantics{Exposes: &sdk.Exposes{Local: "local_addr", URL: "public_url", Path: "path"}},
		}},
	}
	in := &Instance{Name: "relay", Decl: decl, Enabled: true, Impl: exp}
	lookup := func(name string) (*Instance, bool) {
		if name == "relay" {
			return in, true
		}
		return nil, false
	}
	tun := exposureTunnel{lookup: lookup, name: "relay", from: "gh"}
	url, _, err := tun.OpenPath(context.Background(), "127.0.0.1:8788", "/webhook")
	if err != nil {
		t.Fatal(err)
	}
	if url != exp.url {
		t.Fatalf("url = %q, want the exposure's own URL unchanged (the path went in as an option, not appended)", url)
	}
	if got := exp.gotOptions()["path"]; got != "/webhook" {
		t.Fatalf("open options = %+v, want path=/webhook", exp.gotOptions())
	}
}

// An exposure verb that declares no `exposes.path` (cloudflared, ngrok, the
// builtin tunnel — anything forwarding the whole origin) gets no path
// option at all; the host appends the resolved path to the URL it returns.
func TestOpenPathAppendsPathWhenExposesDoesNotDeclareIt(t *testing.T) {
	exp := &pathAwareExposer{url: "https://abc123.trycloudflare.com"}
	decl := &TypeDecl{
		Type: "notpathtun",
		Verbs: []VerbDecl{{
			Name:      "open",
			Semantics: &sdk.VerbSemantics{Exposes: &sdk.Exposes{Local: "local_addr", URL: "public_url"}},
		}},
	}
	in := &Instance{Name: "tun", Decl: decl, Enabled: true, Impl: exp}
	lookup := func(name string) (*Instance, bool) {
		if name == "tun" {
			return in, true
		}
		return nil, false
	}
	tun := exposureTunnel{lookup: lookup, name: "tun", from: "gh"}
	url, _, err := tun.OpenPath(context.Background(), "127.0.0.1:8099", "/webhook")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://abc123.trycloudflare.com/webhook" {
		t.Fatalf("url = %q, want the path appended", url)
	}
	if _, ok := exp.gotOptions()["path"]; ok {
		t.Fatalf("open options = %+v, want no path option (the verb declares none)", exp.gotOptions())
	}
}

// Open (the legacy, path-less entry point web.go uses) never passes or
// appends a path, even against an exposure that DOES declare exposes.path —
// the web hand-off page has no listener/path concept at all, and must keep
// behaving exactly as before this feature.
func TestOpenNeverTouchesPathEvenWhenExposesDeclaresIt(t *testing.T) {
	exp := &pathAwareExposer{url: "http://mock-smee:8080/e2e-gh-channel"}
	decl := &TypeDecl{
		Type: "pathtun2",
		Verbs: []VerbDecl{{
			Name:      "open",
			Semantics: &sdk.VerbSemantics{Exposes: &sdk.Exposes{Local: "local_addr", URL: "public_url", Path: "path"}},
		}},
	}
	in := &Instance{Name: "relay", Decl: decl, Enabled: true, Impl: exp}
	lookup := func(name string) (*Instance, bool) {
		if name == "relay" {
			return in, true
		}
		return nil, false
	}
	tun := exposureTunnel{lookup: lookup, name: "relay", from: "web"}
	url, _, err := tun.Open(context.Background(), "127.0.0.1:18099")
	if err != nil {
		t.Fatal(err)
	}
	if url != exp.url {
		t.Fatalf("url = %q, want the exposure's own URL", url)
	}
	if _, ok := exp.gotOptions()["path"]; ok {
		t.Fatalf("Open passed a path option: %+v", exp.gotOptions())
	}
}

// joinExposedPath's edge cases: no double slashes, a query string surviving
// past the newly-added path, and "" / "/" both being a no-op.
func TestJoinExposedPath(t *testing.T) {
	cases := []struct{ url, path, want string }{
		{"https://hook.example/18877", "", "https://hook.example/18877"},
		{"https://hook.example/18877", "/", "https://hook.example/18877"},
		{"https://hook.example/18877", "/webhook", "https://hook.example/18877/webhook"},
		{"https://hook.example/18877/", "/webhook", "https://hook.example/18877/webhook"},
		{"https://abc.trycloudflare.com", "/webhook", "https://abc.trycloudflare.com/webhook"},
		{"https://abc.trycloudflare.com", "webhook", "https://abc.trycloudflare.com/webhook"},
		{"https://x.example?token=abc", "/webhook", "https://x.example/webhook?token=abc"},
	}
	for _, c := range cases {
		if got := joinExposedPath(c.url, c.path); got != c.want {
			t.Errorf("joinExposedPath(%q, %q) = %q, want %q", c.url, c.path, got, c.want)
		}
	}
}

// Pack consent follows the declaration: any connector type declaring a
// consent scope gets it, and one declaring none does not — whatever its name.
func TestScopeConsentIsDeclared(t *testing.T) {
	registerTypeForTest(&TypeDecl{Type: "acmeforge", Semantics: &sdk.ConnSemantics{
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
