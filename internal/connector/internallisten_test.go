package connector

import (
	"errors"
	"net"
	"reflect"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/secrets"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// registerPathListenerType registers an external listener type whose
// listener declares a path field, and an external exposure type. defPath is
// the listener's declared default path ("" for none).
func registerPathListenerType(t *testing.T, typ, defPath string) {
	t.Helper()
	l := sdk.Listener{Listen: "webhook.listen", Expose: "webhook.expose", URLTo: "webhook.public_url", Path: "webhook.path", DefaultPath: defPath}
	for _, ty := range []string{typ, typ + "-relay"} {
		d := &TypeDecl{Type: ty, Connection: Schema{"webhook": {Type: TMap}}}
		if ty == typ {
			d.Semantics = &sdk.ConnSemantics{Listeners: []sdk.Listener{l}}
		}
		if err := RegisterExternalTypeGroup(d, nil, "connectors/"+ty+"@v1.0.0", "connectors/"+ty); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { UnregisterExternalType(ty) })
	}
}

func forgetInternalListen(t *testing.T) {
	t.Helper()
	internalMu.Lock()
	internalListen = map[string]string{}
	internalMu.Unlock()
	t.Cleanup(func() {
		internalMu.Lock()
		internalListen = map[string]string{}
		internalMu.Unlock()
	})
}

func webhookField(t *testing.T, cfg *config.Config, name, field string) string {
	t.Helper()
	return stringAtPath(cfg.ConnectorsMap[name].Conn(), "webhook."+field)
}

// expose: with no listen gets a loopback address of conductor's choosing,
// and the path its exposure delivers to, written where the listener's
// declared fields are: the connector loads enabled (it was disabled before,
// TestListenerExposeRequiresListen) and its exposure is wired to that
// address.
func TestExposeWithoutListenGetsALocalAddress(t *testing.T) {
	forgetInternalListen(t)
	registerPathListenerType(t, "intlisten1", "")
	cfg := mustDecodeConfig(t, `
connectors:
  src:
    use: intlisten1
    webhook: { expose: relay, secret: s }
  relay:
    use: intlisten1-relay
`)
	changed := AssignListenAddrs(cfg, nil)
	addr := webhookField(t, cfg, "src", "listen")
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" {
		t.Fatalf("listen = %q, want a 127.0.0.1 address", addr)
	}
	if p := webhookField(t, cfg, "src", "path"); p != "/" {
		t.Fatalf("path = %q, want the exposure's resolved /", p)
	}
	if webhookField(t, cfg, "src", "secret") != "s" || webhookField(t, cfg, "src", "expose") != "relay" {
		t.Fatalf("the rest of the config was lost: %v", cfg.ConnectorsMap["src"].Conn())
	}
	if len(changed) != 1 || !reflect.DeepEqual(changed["src"], cfg.ConnectorsMap["src"].Conn()) {
		t.Fatalf("changed = %v", changed)
	}
	if got := ExposeTargets(cfg, nil); !reflect.DeepEqual(got, map[string][]string{"relay": {addr}}) {
		t.Fatalf("targets = %v", got)
	}
	// The next load hands it the same address: nothing moves on a reload.
	again := mustDecodeConfig(t, `
connectors:
  src:
    use: intlisten1
    webhook: { expose: relay }
  relay:
    use: intlisten1-relay
`)
	AssignListenAddrs(again, nil)
	if got := webhookField(t, again, "src", "listen"); got != addr {
		t.Fatalf("second load: listen = %q, want %q", got, addr)
	}
}

// The config TestListenerExposeRequiresListen shows disabled loads enabled
// once its listener has an address.
func TestExposeWithoutListenLoadsEnabled(t *testing.T) {
	forgetInternalListen(t)
	registerListenerTestType(t, "intlisten6")
	cfg := mustDecodeConfig(t, `
connectors:
  src:
    use: intlisten6
    webhook:
      expose: tun
  tun:
    use: tunnel
    command: [sh, -c, "echo https://unused.example"]
`)
	AssignListenAddrs(cfg, nil)
	reg, err := Build(cfg, Deps{Secrets: secrets.New()})
	if err != nil {
		t.Fatal(err)
	}
	if src, _ := reg.Get("src"); src.DisabledReason != "" {
		t.Fatalf("src disabled: %s", src.DisabledReason)
	}
}

// An explicit listen and path win: nothing is written, and the exposure
// targets the listener where the operator put it.
func TestExplicitListenAndPathUntouched(t *testing.T) {
	forgetInternalListen(t)
	registerPathListenerType(t, "intlisten2", "/hook")
	cfg := mustDecodeConfig(t, `
connectors:
  a:
    use: intlisten2
    webhook: { listen: "0.0.0.0:19201", path: /mine, expose: relay }
  b:
    use: intlisten2
    webhook: { listen: "127.0.0.1:19202", expose: relay }
  off:
    use: intlisten2
    enabled: false
    webhook: { expose: relay }
  plain:
    use: intlisten2
    webhook: {}
  relay:
    use: intlisten2-relay
`)
	before := map[string]map[string]any{}
	for n, r := range cfg.ConnectorsMap {
		before[n] = r.Conn()
	}
	if changed := AssignListenAddrs(cfg, nil); len(changed) != 0 {
		t.Fatalf("changed = %v", changed)
	}
	for n, r := range cfg.ConnectorsMap {
		if !reflect.DeepEqual(r.Conn(), before[n]) {
			t.Fatalf("%s: %v, was %v", n, r.Conn(), before[n])
		}
	}
	// b's path is unset: its exposure delivers to the declared default.
	if got := resolveListenerPath(cfg.ConnectorsMap["b"].Conn(), sdk.Listener{Path: "webhook.path", DefaultPath: "/hook"}); got != "/hook" {
		t.Fatalf("b's path resolves to %q", got)
	}
}

// With no path set, the path written is the listener's declared default,
// so the plugin serves where its exposure delivers.
func TestInternalListenUsesDeclaredDefaultPath(t *testing.T) {
	forgetInternalListen(t)
	registerPathListenerType(t, "intlisten3", "/hook")
	cfg := mustDecodeConfig(t, `
connectors:
  src:
    use: intlisten3
    webhook: { expose: relay }
  relay:
    use: intlisten3-relay
`)
	AssignListenAddrs(cfg, nil)
	if p := webhookField(t, cfg, "src", "path"); p != "/hook" {
		t.Fatalf("path = %q, want the declared default /hook", p)
	}
}

// Two listeners never share an address, and none takes a port a configured
// listener names.
func TestInternalListenAddressesDistinct(t *testing.T) {
	forgetInternalListen(t)
	registerPathListenerType(t, "intlisten4", "")
	picks := []string{"127.0.0.1:19301", "127.0.0.1:19301", "127.0.0.1:19302", "127.0.0.1:19303"}
	old := pickLoopbackAddr
	pickLoopbackAddr = func() (string, error) {
		a := picks[0]
		picks = picks[1:]
		return a, nil
	}
	t.Cleanup(func() { pickLoopbackAddr = old })
	cfg := mustDecodeConfig(t, `
connectors:
  a:
    use: intlisten4
    webhook: { expose: relay }
  b:
    use: intlisten4
    webhook: { expose: relay }
  c:
    use: intlisten4
    webhook: { listen: "127.0.0.1:19302" }
  relay:
    use: intlisten4-relay
`)
	AssignListenAddrs(cfg, nil)
	if a, b := webhookField(t, cfg, "a", "listen"), webhookField(t, cfg, "b", "listen"); a != "127.0.0.1:19301" || b != "127.0.0.1:19303" {
		t.Fatalf("a = %q, b = %q", a, b)
	}
}

// No address to be had: the listen stays unset and the connector is
// disabled for it, as before.
func TestInternalListenNoAddressDisables(t *testing.T) {
	forgetInternalListen(t)
	registerListenerTestType(t, "intlisten5")
	old := pickLoopbackAddr
	pickLoopbackAddr = func() (string, error) { return "", errors.New("no ports") }
	t.Cleanup(func() { pickLoopbackAddr = old })
	cfg := mustDecodeConfig(t, `
connectors:
  src:
    use: intlisten5
    webhook:
      expose: tun
  tun:
    use: tunnel
    command: [sh, -c, "echo https://unused.example"]
`)
	var logged []string
	AssignListenAddrs(cfg, func(f string, a ...any) { logged = append(logged, f) })
	if len(logged) != 1 {
		t.Fatalf("logged = %v", logged)
	}
	reg, err := Build(cfg, Deps{Secrets: secrets.New()})
	if err != nil {
		t.Fatal(err)
	}
	if src, _ := reg.Get("src"); src.DisabledReason == "" {
		t.Fatal("src enabled with no listen address")
	}
}
