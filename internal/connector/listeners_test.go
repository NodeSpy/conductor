package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/secrets"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// --- load-time validation (checkListenerExposures) --------------------------

// A connector whose type declares `listeners` and whose config sets `expose`
// is validated exactly like web's own `expose:` — the target must exist and
// declare an exposes verb, and `listen` must be set.
func TestListenerExposeMustNameAnExposureConnector(t *testing.T) {
	registerListenerTestType(t, "listenerfake1")
	cfg := mustDecodeConfig(t, `
connectors:
  src:
    use: listenerfake1
    webhook:
      listen: 127.0.0.1:19101
      expose: nope
`)
	reg, err := Build(cfg, Deps{Secrets: secrets.New()})
	if err != nil {
		t.Fatal(err)
	}
	src, _ := reg.Get("src")
	if !strings.Contains(src.DisabledReason, `no connector named "nope"`) {
		t.Fatalf("disabled reason = %q", src.DisabledReason)
	}
}

func TestListenerExposeTargetMustExpose(t *testing.T) {
	registerListenerTestType(t, "listenerfake2")
	cfg := mustDecodeConfig(t, `
connectors:
  src:
    use: listenerfake2
    webhook:
      listen: 127.0.0.1:19102
      expose: hooks
  hooks:
    use: webhook
`)
	reg, err := Build(cfg, Deps{Secrets: secrets.New()})
	if err != nil {
		t.Fatal(err)
	}
	src, _ := reg.Get("src")
	if !strings.Contains(src.DisabledReason, "declares no exposes verb") {
		t.Fatalf("disabled reason = %q", src.DisabledReason)
	}
}

// expose: set with no listen is a config mistake — the engine has no address
// to open an exposure for.
func TestListenerExposeRequiresListen(t *testing.T) {
	registerListenerTestType(t, "listenerfake3")
	cfg := mustDecodeConfig(t, `
connectors:
  src:
    use: listenerfake3
    webhook:
      expose: tun
  tun:
    use: tunnel
    command: [sh, -c, "echo https://unused.example"]
`)
	reg, err := Build(cfg, Deps{Secrets: secrets.New()})
	if err != nil {
		t.Fatal(err)
	}
	src, _ := reg.Get("src")
	if !strings.Contains(src.DisabledReason, "is set with no webhook.listen") {
		t.Fatalf("disabled reason = %q", src.DisabledReason)
	}
}

// A listener with no `expose` configured at all is untouched — a plain local
// listener is not a config mistake.
func TestListenerWithNoExposeIsNotDisabled(t *testing.T) {
	registerListenerTestType(t, "listenerfake4")
	cfg := mustDecodeConfig(t, `
connectors:
  src:
    use: listenerfake4
    webhook:
      listen: 127.0.0.1:19104
`)
	reg, err := Build(cfg, Deps{Secrets: secrets.New()})
	if err != nil {
		t.Fatal(err)
	}
	src, _ := reg.Get("src")
	if src.DisabledReason != "" {
		t.Fatalf("src disabled: %s", src.DisabledReason)
	}
}

// A valid expose target leaves the connector enabled.
func TestListenerExposeValidConfigStaysEnabled(t *testing.T) {
	registerListenerTestType(t, "listenerfake5")
	cfg := mustDecodeConfig(t, `
connectors:
  src:
    use: listenerfake5
    webhook:
      listen: 127.0.0.1:19105
      expose: tun
  tun:
    use: tunnel
    command: [sh, -c, "echo https://unused.example"]
`)
	reg, err := Build(cfg, Deps{Secrets: secrets.New()})
	if err != nil {
		t.Fatal(err)
	}
	src, _ := reg.Get("src")
	if src.DisabledReason != "" {
		t.Fatalf("src disabled: %s", src.DisabledReason)
	}
}

// registerListenerTestType registers a minimal source-only connector type
// declaring the `listeners` semantic (webhook.listen/webhook.expose/
// webhook.public_url, mirroring the github plugin's decl sketch), backed by
// a no-op Impl — these tests only exercise Build's load-time gate, not a
// real source. typ must be unique per call (RegisterType panics on a dup
// within the same test binary).
func registerListenerTestType(t *testing.T, typ string) {
	t.Helper()
	registerTypeForTest(&TypeDecl{
		Type: typ,
		Connection: Schema{
			"webhook": {Type: TMap},
		},
		Semantics: &sdk.ConnSemantics{
			Listeners: []sdk.Listener{{Listen: "webhook.listen", Expose: "webhook.expose", URLTo: "webhook.public_url"}},
		},
	}, func(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
		return &noopImpl{}, nil
	})
}

type noopImpl struct{}

func (noopImpl) Validate() error { return nil }
func (noopImpl) Invoke(context.Context, string, map[string]any) (map[string]any, error) {
	return nil, fmt.Errorf("noop")
}
func (noopImpl) Source([]CompiledTrigger) (core.Integration, error) { return nil, nil }
func (noopImpl) DeclaredEvents() []string                           { return nil }

// --- instance start (pluginSourceIntegration.openListeners) -----------------

// fakeBlockingSourcer captures the config StartSource is handed and blocks
// until ctx is cancelled, like a real source plugin's long-lived stream —
// so a test can observe the exposure lease while Start is running and its
// release once Start returns.
type fakeBlockingSourcer struct {
	mu      sync.Mutex
	gotCfg  map[string]any
	started chan struct{}
}

func (f *fakeBlockingSourcer) StartSource(ctx context.Context, req plugin.StartSourceRequest, emit func(json.RawMessage)) error {
	f.mu.Lock()
	f.gotCfg = req.Config
	f.mu.Unlock()
	close(f.started)
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeBlockingSourcer) config() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gotCfg
}

// At instance start, a listener whose `expose` field is set gets its
// exposure opened on the named connector (here the real `tunnel` builtin
// running a fake script), and the public URL lands in the config field
// `url_to` names — in the exact request a plugin's start_source receives.
// The lease is held while the source runs and released once Start returns
// (ctx cancelled: stop or reload).
func TestPluginSourceOpensListenerExposureAndReleasesOnStop(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	cfg := mustDecodeConfig(t, `
connectors:
  tun:
    use: tunnel
    command: [sh, -c, "echo serving at https://hook.example/{{.port}}; sleep 30"]
`)
	reg, err := Build(cfg, Deps{Secrets: secrets.New(), Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}

	src := &fakeBlockingSourcer{started: make(chan struct{})}
	psi := &pluginSourceIntegration{
		source:   src,
		instance: "gh1",
		typ:      "github",
		config: map[string]any{
			"webhook": map[string]any{"listen": "127.0.0.1:18199", "expose": "tun"},
		},
		listeners: []sdk.Listener{{Listen: "webhook.listen", Expose: "webhook.expose", URLTo: "webhook.public_url"}},
		lookup:    reg.Get,
		log:       t.Logf,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- psi.Start(ctx, func(context.Context, core.Trigger) {}) }()

	select {
	case <-src.started:
	case <-time.After(10 * time.Second):
		t.Fatal("StartSource never called")
	}

	webhook, _ := src.config()["webhook"].(map[string]any)
	url, _ := webhook["public_url"].(string)
	if !strings.HasPrefix(url, "https://hook.example/18199") {
		t.Fatalf("webhook.public_url = %q, want the tunnel's URL for port 18199", url)
	}
	// p.config itself (the instance's stored config, shared with verb
	// Invoke calls) must be untouched — openListeners returns a copy.
	if _, mutated := psi.config["webhook"].(map[string]any)["public_url"]; mutated {
		t.Fatal("openListeners mutated the shared instance config")
	}

	tun := mustTunnel(t, reg)
	if n := tun.leaseCount(); n != 1 {
		t.Fatalf("leases = %d while running, want 1", n)
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start returned %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return after cancel")
	}
	if n := tun.leaseCount(); n != 0 {
		t.Fatalf("leases = %d after stop, want 0 (released)", n)
	}
}

// A listener with `expose` unset is left alone: the config StartSource
// receives is exactly the instance's config, and no exposure opens.
func TestPluginSourceListenerWithNoExposeIsUntouched(t *testing.T) {
	src := &fakeBlockingSourcer{started: make(chan struct{})}
	psi := &pluginSourceIntegration{
		source:    src,
		instance:  "gh1",
		typ:       "github",
		config:    map[string]any{"webhook": map[string]any{"listen": "127.0.0.1:18200"}},
		listeners: []sdk.Listener{{Listen: "webhook.listen", Expose: "webhook.expose", URLTo: "webhook.public_url"}},
		lookup:    func(string) (*Instance, bool) { return nil, false },
		log:       t.Logf,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- psi.Start(ctx, func(context.Context, core.Trigger) {}) }()
	select {
	case <-src.started:
	case <-time.After(5 * time.Second):
		t.Fatal("StartSource never called")
	}
	cancel()
	<-done
	webhook, _ := src.config()["webhook"].(map[string]any)
	if _, ok := webhook["public_url"]; ok {
		t.Fatalf("public_url set with no expose configured: %+v", webhook)
	}
}

// Start refuses outright (no retry loop entered) when `expose` is set but
// `listen` is not — the same defensive check checkListenerExposures already
// enforces at load, kept here too since Start is reachable directly (tests,
// a future caller that skips Build).
func TestPluginSourceListenerStartRequiresListen(t *testing.T) {
	psi := &pluginSourceIntegration{
		source:    &fakeBlockingSourcer{started: make(chan struct{})},
		instance:  "gh1",
		config:    map[string]any{"webhook": map[string]any{"expose": "tun"}},
		listeners: []sdk.Listener{{Listen: "webhook.listen", Expose: "webhook.expose", URLTo: "webhook.public_url"}},
		lookup:    func(string) (*Instance, bool) { return nil, false },
		log:       t.Logf,
	}
	err := psi.Start(context.Background(), func(context.Context, core.Trigger) {})
	if err == nil || !strings.Contains(err.Error(), "is set with no webhook.listen") {
		t.Fatalf("Start err = %v, want a complaint about the missing listen address", err)
	}
}

// resolveListenerPath: no Path field declared, or the field unset at this
// instance, both default to "/"; a configured value is normalized to start
// with "/".
func TestResolveListenerPath(t *testing.T) {
	cases := []struct {
		name string
		cfg  map[string]any
		l    sdk.Listener
		want string
	}{
		{"no path field declared", map[string]any{}, sdk.Listener{}, "/"},
		{"path field declared, unset in config", map[string]any{}, sdk.Listener{Path: "webhook.path"}, "/"},
		{"path field set, already slash-prefixed", map[string]any{"webhook": map[string]any{"path": "/webhook"}}, sdk.Listener{Path: "webhook.path"}, "/webhook"},
		{"path field set, no leading slash", map[string]any{"webhook": map[string]any{"path": "webhook"}}, sdk.Listener{Path: "webhook.path"}, "/webhook"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveListenerPath(c.cfg, c.l); got != c.want {
				t.Fatalf("resolveListenerPath = %q, want %q", got, c.want)
			}
		})
	}
}

// At instance start, a listener's resolved path is passed to an exposure
// that declares `exposes.path` AS AN OPTION, and the returned URL lands in
// url_to UNCHANGED — the smee case: the channel URL already IS the public
// URL, and the path only matters to the plugin's own local relay.
func TestPluginSourceListenerPathPassedToExposureThatDeclaresIt(t *testing.T) {
	exp := &pathAwareExposer{url: "http://mock-smee:8080/e2e-gh-channel"}
	decl := &TypeDecl{
		Type: "pathtun3",
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
	src := &fakeBlockingSourcer{started: make(chan struct{})}
	psi := &pluginSourceIntegration{
		source:   src,
		instance: "gh1",
		config: map[string]any{
			"webhook": map[string]any{"listen": "127.0.0.1:18203", "expose": "relay", "path": "/webhook"},
		},
		listeners: []sdk.Listener{{Listen: "webhook.listen", Expose: "webhook.expose", URLTo: "webhook.public_url", Path: "webhook.path"}},
		lookup:    lookup,
		log:       t.Logf,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- psi.Start(ctx, func(context.Context, core.Trigger) {}) }()
	select {
	case <-src.started:
	case <-time.After(5 * time.Second):
		t.Fatal("StartSource never called")
	}
	webhook, _ := src.config()["webhook"].(map[string]any)
	if url, _ := webhook["public_url"].(string); url != exp.url {
		t.Fatalf("webhook.public_url = %q, want the exposure's own URL unchanged", url)
	}
	if got := exp.gotOptions()["path"]; got != "/webhook" {
		t.Fatalf("open options = %+v, want path=/webhook", exp.gotOptions())
	}
	cancel()
	<-done
}

// The same listener semantic against an exposure that does NOT declare
// `exposes.path` (the builtin tunnel): the resolved path is appended to the
// returned URL instead, with no option passed.
func TestPluginSourceListenerPathAppendedWhenExposureDoesNotDeclareIt(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	cfg := mustDecodeConfig(t, `
connectors:
  tun:
    use: tunnel
    command: [sh, -c, "echo serving at https://hook.example/{{.port}}; sleep 30"]
`)
	reg, err := Build(cfg, Deps{Secrets: secrets.New(), Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	src := &fakeBlockingSourcer{started: make(chan struct{})}
	psi := &pluginSourceIntegration{
		source:   src,
		instance: "gh1",
		config: map[string]any{
			"webhook": map[string]any{"listen": "127.0.0.1:18204", "expose": "tun", "path": "/webhook"},
		},
		listeners: []sdk.Listener{{Listen: "webhook.listen", Expose: "webhook.expose", URLTo: "webhook.public_url", Path: "webhook.path"}},
		lookup:    reg.Get,
		log:       t.Logf,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- psi.Start(ctx, func(context.Context, core.Trigger) {}) }()
	select {
	case <-src.started:
	case <-time.After(10 * time.Second):
		t.Fatal("StartSource never called")
	}
	webhook, _ := src.config()["webhook"].(map[string]any)
	url, _ := webhook["public_url"].(string)
	if !strings.HasPrefix(url, "https://hook.example/18204/webhook") {
		t.Fatalf("webhook.public_url = %q, want the tunnel's URL with the path appended", url)
	}
	cancel()
	<-done
}

// A listener that declares a Path field, but whose config never sets it,
// defaults to "/" — unchanged from a type decl written before this feature
// existed (old decls keep working, path optional).
func TestPluginSourceListenerPathDefaultsToRootWhenUnconfigured(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	cfg := mustDecodeConfig(t, `
connectors:
  tun:
    use: tunnel
    command: [sh, -c, "echo serving at https://hook.example/{{.port}}; sleep 30"]
`)
	reg, err := Build(cfg, Deps{Secrets: secrets.New(), Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	src := &fakeBlockingSourcer{started: make(chan struct{})}
	psi := &pluginSourceIntegration{
		source:   src,
		instance: "gh1",
		config: map[string]any{
			"webhook": map[string]any{"listen": "127.0.0.1:18205", "expose": "tun"},
		},
		listeners: []sdk.Listener{{Listen: "webhook.listen", Expose: "webhook.expose", URLTo: "webhook.public_url", Path: "webhook.path"}},
		lookup:    reg.Get,
		log:       t.Logf,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- psi.Start(ctx, func(context.Context, core.Trigger) {}) }()
	select {
	case <-src.started:
	case <-time.After(10 * time.Second):
		t.Fatal("StartSource never called")
	}
	webhook, _ := src.config()["webhook"].(map[string]any)
	url, _ := webhook["public_url"].(string)
	if url != "https://hook.example/18205" {
		t.Fatalf("webhook.public_url = %q, want the tunnel's URL unchanged (default path is a no-op)", url)
	}
	cancel()
	<-done
}

// flakyExposer fails the first N-1 calls to its exposes verb, then succeeds
// — proving openExposure retries with backoff rather than giving up (or
// hot-looping) on a transient failure.
type flakyExposer struct {
	mu        sync.Mutex
	failUntil int
	calls     int
}

func (f *flakyExposer) Validate() error                                    { return nil }
func (f *flakyExposer) DeclaredEvents() []string                           { return nil }
func (f *flakyExposer) Source([]CompiledTrigger) (core.Integration, error) { return nil, nil }
func (f *flakyExposer) Invoke(_ context.Context, verb string, opts map[string]any) (map[string]any, error) {
	if verb != "open" {
		return nil, fmt.Errorf("flakyExposer: unknown verb %q", verb)
	}
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	if n < f.failUntil {
		return nil, fmt.Errorf("flaky: not yet (attempt %d)", n)
	}
	return map[string]any{"public_url": "https://flaky.example"}, nil
}

// A transient exposure failure retries with backoff and eventually succeeds
// — the source does not give up on the first error, and does not hot-loop.
func TestPluginSourceListenerRetriesTransientExposureFailure(t *testing.T) {
	oldInitial, oldMax := exposureRetryInitial, exposureRetryMax
	exposureRetryInitial, exposureRetryMax = 5*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { exposureRetryInitial, exposureRetryMax = oldInitial, oldMax })

	flaky := &flakyExposer{failUntil: 3}
	decl := &TypeDecl{
		Type: "flakytun",
		Verbs: []VerbDecl{{
			Name: "open", Semantics: &sdk.VerbSemantics{Exposes: &sdk.Exposes{Local: "local_addr", URL: "public_url"}},
		}},
	}
	in := &Instance{Name: "tun", Decl: decl, Enabled: true, Impl: flaky}
	lookup := func(name string) (*Instance, bool) {
		if name == "tun" {
			return in, true
		}
		return nil, false
	}

	src := &fakeBlockingSourcer{started: make(chan struct{})}
	psi := &pluginSourceIntegration{
		source:    src,
		instance:  "gh1",
		config:    map[string]any{"webhook": map[string]any{"listen": "127.0.0.1:18201", "expose": "tun"}},
		listeners: []sdk.Listener{{Listen: "webhook.listen", Expose: "webhook.expose", URLTo: "webhook.public_url"}},
		lookup:    lookup,
		log:       t.Logf,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- psi.Start(ctx, func(context.Context, core.Trigger) {}) }()

	select {
	case <-src.started:
	case <-time.After(5 * time.Second):
		t.Fatal("StartSource never called — exposure retry did not converge")
	}
	webhook, _ := src.config()["webhook"].(map[string]any)
	if url, _ := webhook["public_url"].(string); url != "https://flaky.example" {
		t.Fatalf("webhook.public_url = %q after retries", url)
	}
	if flaky.calls < 3 {
		t.Fatalf("exposes verb called %d times, want >= 3 (it failed twice first)", flaky.calls)
	}
	cancel()
	<-done
}

// A failing exposure never gets a chance to retry forever once the daemon
// is stopping: ctx cancellation during backoff returns promptly instead of
// waiting out the retry delay.
func TestPluginSourceListenerExposureFailureStopsOnCancel(t *testing.T) {
	oldInitial, oldMax := exposureRetryInitial, exposureRetryMax
	exposureRetryInitial, exposureRetryMax = time.Minute, time.Minute // would hang the test if cancel didn't short-circuit it
	t.Cleanup(func() { exposureRetryInitial, exposureRetryMax = oldInitial, oldMax })

	alwaysFails := &flakyExposer{failUntil: 1 << 30}
	decl := &TypeDecl{
		Type: "foreverflaky",
		Verbs: []VerbDecl{{
			Name: "open", Semantics: &sdk.VerbSemantics{Exposes: &sdk.Exposes{Local: "local_addr", URL: "public_url"}},
		}},
	}
	in := &Instance{Name: "tun", Decl: decl, Enabled: true, Impl: alwaysFails}
	lookup := func(name string) (*Instance, bool) {
		if name == "tun" {
			return in, true
		}
		return nil, false
	}
	psi := &pluginSourceIntegration{
		source:    &fakeBlockingSourcer{started: make(chan struct{})},
		instance:  "gh1",
		config:    map[string]any{"webhook": map[string]any{"listen": "127.0.0.1:18202", "expose": "tun"}},
		listeners: []sdk.Listener{{Listen: "webhook.listen", Expose: "webhook.expose", URLTo: "webhook.public_url"}},
		lookup:    lookup,
		log:       t.Logf,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- psi.Start(ctx, func(context.Context, core.Trigger) {}) }()
	time.Sleep(20 * time.Millisecond) // let it fail at least once and enter the backoff wait
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not honor cancellation during exposure backoff")
	}
}

// openExposure's exponential backoff must never exceed exposureRetryMax, no
// matter how many times the exposure keeps failing — an uncapped doubling
// schedule would eventually leave a struggling exposure retrying only once
// an hour, then once a day.
func TestPluginSourceListenerExposureBackoffNeverExceedsCap(t *testing.T) {
	oldInitial, oldMax, oldWait := exposureRetryInitial, exposureRetryMax, exposureWait
	exposureRetryInitial, exposureRetryMax = 2*time.Millisecond, 10*time.Millisecond
	// Record each requested wait instead of sleeping: the schedule itself is
	// what's under test, and wall-clock gaps flake under a loaded -race run.
	var waits []time.Duration
	exposureWait = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}
	t.Cleanup(func() { exposureRetryInitial, exposureRetryMax, exposureWait = oldInitial, oldMax, oldWait })

	// Uncapped doubling from 2ms would be 2,4,8,16,32,…,512ms by the 9th
	// wait — many multiples of the 10ms cap. failUntil=10 forces 9 failed
	// attempts (and therefore 9 backoff waits) before success.
	const failUntil = 10
	flaky := &flakyExposer{failUntil: failUntil}
	decl := &TypeDecl{
		Type: "capcheck",
		Verbs: []VerbDecl{{
			Name: "open", Semantics: &sdk.VerbSemantics{Exposes: &sdk.Exposes{Local: "local_addr", URL: "public_url"}},
		}},
	}
	in := &Instance{Name: "tun", Decl: decl, Enabled: true, Impl: flaky}
	lookup := func(name string) (*Instance, bool) {
		if name == "tun" {
			return in, true
		}
		return nil, false
	}
	p := &pluginSourceIntegration{instance: "gh1", lookup: lookup, log: t.Logf}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := p.openExposure(ctx, "tun", "127.0.0.1:0", "/"); err != nil {
		t.Fatalf("openExposure: %v", err)
	}
	want := []time.Duration{2, 4, 8, 10, 10, 10, 10, 10, 10}
	if len(waits) != len(want) {
		t.Fatalf("got %d waits %v, want %d", len(waits), waits, len(want))
	}
	for i, w := range want {
		if waits[i] != w*time.Millisecond {
			t.Fatalf("wait %d = %s, want %s (schedule %v)", i+1, waits[i], w*time.Millisecond, waits)
		}
	}
}
