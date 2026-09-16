package code

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/hosts"
	iplugin "github.com/NodeSpy/conductor/internal/plugin"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// fakeEngine is a PluginEngine that is not a subprocess: it runs a closure and
// exposes the host callback it was handed, so the dispatch half can be tested
// without a binary.
type fakeEngine struct {
	run func(req sdk.RunRequest, host func(sdk.HostRequest) sdk.HostResult) (map[string]any, error)
}

func (f fakeEngine) Run(_ context.Context, req sdk.RunRequest, host func(sdk.HostRequest) sdk.HostResult) (map[string]any, error) {
	return f.run(req, host)
}

func engineExecutor(name string, e PluginEngine) *Executor {
	return &Executor{Engines: func(n string) (PluginEngine, bool) {
		if n != name {
			return nil, false
		}
		return e, true
	}}
}

// The whole code-step ABI crosses the wire: the step's code, args, env and the
// rendered ctx document go out as a RunRequest, and the engine's outputs come
// back as the step's outputs.
func TestPluginEngineCarriesTheStepABI(t *testing.T) {
	var got sdk.RunRequest
	e := engineExecutor("acme-engine", fakeEngine{run: func(req sdk.RunRequest, _ func(sdk.HostRequest) sdk.HostResult) (map[string]any, error) {
		got = req
		return map[string]any{"ok": true, "saw": req.Inputs["repo"]}, nil
	}})
	out, err := e.Exec(context.Background(), Spec{
		Run: "acme-engine", Plugin: true, Code: "print(1)",
		Args: []string{"a", "b"}, Env: map[string]string{"K": "V"},
	}, map[string]any{"repo": "o/r"})
	if err != nil {
		t.Fatal(err)
	}
	if out["ok"] != true || out["saw"] != "o/r" {
		t.Fatalf("outputs = %+v", out)
	}
	if got.Code != "print(1)" || len(got.Args) != 2 || got.Env["K"] != "V" || got.Inputs["repo"] != "o/r" {
		t.Fatalf("the step did not cross intact: %+v", got)
	}
}

// The host callback the engine is handed is THE SAME CtxHandler a `use: cli`
// step reaches over its socket, carrying THIS step's guard. An out-of-allowlist
// store is refused host-side, and the refusal is typed as one.
func TestPluginEngineHostCallbackIsGuarded(t *testing.T) {
	tempKV(t) // registers store "s"
	other, err := openSecondStore(t, "other")
	if err != nil {
		t.Fatal(err)
	}

	var allowed, refused sdk.HostResult
	e := engineExecutor("acme-engine", fakeEngine{run: func(_ sdk.RunRequest, host func(sdk.HostRequest) sdk.HostResult) (map[string]any, error) {
		allowed = host(sdk.HostRequest{Kind: "kv", Op: "set", Resource: "s", Args: []any{"ns", "k", "v"}})
		refused = host(sdk.HostRequest{Kind: "kv", Op: "set", Resource: "other", Args: []any{"ns", "k", "v"}})
		return map[string]any{}, nil
	}})
	if _, err := e.Exec(context.Background(), Spec{
		Run: "acme-engine", Plugin: true,
		DataGuard: guardDenyingStore("s", "hunter2"),
	}, nil); err != nil {
		t.Fatal(err)
	}
	if !allowed.OK {
		t.Fatalf("the allowed store was refused: %+v", allowed)
	}
	if refused.OK || !refused.Refused || !strings.Contains(refused.Error, "allowlist") {
		t.Fatalf("an out-of-allowlist store was NOT refused host-side: %+v", refused)
	}
	if _, found, _ := other.Get("ns", "k"); found {
		t.Fatal("the refused write reached the out-of-allowlist store")
	}

	// The secret-egress half of the same guard, over the same callback.
	var secret sdk.HostResult
	e = engineExecutor("acme-engine", fakeEngine{run: func(_ sdk.RunRequest, host func(sdk.HostRequest) sdk.HostResult) (map[string]any, error) {
		secret = host(sdk.HostRequest{Kind: "kv", Op: "set", Resource: "s", Args: []any{"ns", "p", "token=hunter2"}})
		return nil, nil
	}})
	if _, err := e.Exec(context.Background(), Spec{
		Run: "acme-engine", Plugin: true, DataGuard: guardDenyingStore("s", "hunter2"),
	}, nil); err != nil {
		t.Fatal(err)
	}
	if secret.OK || !secret.Refused || !strings.Contains(secret.Error, "no_secret_egress") {
		t.Fatalf("a secret write through the plugin callback was not refused: %+v", secret)
	}
}

// A plugin engine is local-only, and an engine nobody loaded says so rather
// than falling through to a PATH lookup.
func TestPluginEngineRefusals(t *testing.T) {
	e := engineExecutor("acme-engine", fakeEngine{run: func(sdk.RunRequest, func(sdk.HostRequest) sdk.HostResult) (map[string]any, error) {
		return nil, nil
	}})
	_, err := e.Exec(context.Background(), Spec{Run: "nobody", Plugin: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "not loaded") {
		t.Fatalf("want a not-loaded error, got %v", err)
	}
	_, err = (&Executor{}).Exec(context.Background(), Spec{Run: "acme-engine", Plugin: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "no plugin engines are wired") {
		t.Fatalf("want a not-wired error, got %v", err)
	}
}

// buildEnginePlugin compiles the reference acme-engine into a temp dir.
func buildEnginePlugin(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	dir := t.TempDir()
	// The binary's name matters: a local `use: ./bin/conductor-acme-engine`
	// resolves to the engine name "acme-engine" (config.localUseName strips
	// the conventional prefix), and Describe refuses a plugin whose declared
	// type is not the name it was configured to provide.
	bin := filepath.Join(dir, "conductor-acme-engine")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/NodeSpy/conductor/test/plugins/acme-engine")
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build reference engine: %v\n%s", err, out)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return bin, hex.EncodeToString(sum[:])
}

// THE WHOLE PATH, end to end, with nothing faked: the reference engine plugin
// runs as a real verified subprocess, speaks plugin.run over the real
// transport, calls host.kv BACK during the run, and every one of those calls
// lands in this step's CtxHandler behind this step's guard.
//
// It is the plugin-engine twin of the cli engine's socket test, and it is what
// proves the two out-of-process engines share one authorization path: the
// allowed store round-trips through a real kv backend, and the denied store is
// refused by the DAEMON — the engine only reports what it was told.
func TestReferenceEnginePluginEndToEnd(t *testing.T) {
	bin, sum := buildEnginePlugin(t)
	st := tempKV(t) // registers "s"
	if _, err := openSecondStore(t, "other"); err != nil {
		t.Fatal(err)
	}

	cl := iplugin.NewClient(iplugin.Spec{
		Name: "acme-engine", Kind: iplugin.KindStep, Provides: "acme-engine",
		BinPath: bin, Sha256: sum,
	}, iplugin.Deps{Log: func(string, ...any) {}})
	defer cl.Close()

	decl, err := cl.Describe(context.Background())
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if decl.Kind != iplugin.KindStep || decl.ABI != iplugin.EngineABI {
		t.Fatalf("decl = %+v", decl)
	}

	e := engineExecutor("acme-engine", cl)
	out, err := e.Exec(context.Background(), Spec{
		Run: "acme-engine", Plugin: true, Code: "# nine  #",
		Env: map[string]string{
			"STORE": "s", "NS": "run", "KEY": "attempts", "VALUE": "three",
			"DENY_STORE": "other", "ECHO": "repo",
		},
		DataGuard: guardDenyingStore("s", "hunter2"),
	}, map[string]any{"repo": "o/r"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if out["engine"] != "acme-engine" {
		t.Fatalf("outputs = %+v", out)
	}
	if out["roundtrip"] != "three" {
		t.Fatalf("the host round trip did not return the written value: %+v", out)
	}
	if n, _ := out["code_len"].(float64); int(n) != len("# nine  #") {
		t.Fatalf("the step's code did not cross intact: %+v", out["code_len"])
	}
	// The ctx document crossed with its VALUES, not merely its shape.
	if out["echo"] != "o/r" {
		t.Fatalf("the rendered ctx did not reach the engine: %+v", out["echo"])
	}
	// The write really landed in the real store — the engine holds no store,
	// so the only way a value is here is that conductor performed the op.
	if v, found, _ := st.Get("run", "attempts"); !found || v != "three" {
		t.Fatalf("store = %#v (found %v)", v, found)
	}
	// And the denied store was refused HOST-SIDE. The engine reports what it
	// was told; the decision was never its to make.
	if out["denied"] != true || out["refused"] != true {
		t.Fatalf("the out-of-allowlist store was not refused: denied=%v refused=%v denial=%v",
			out["denied"], out["refused"], out["denial"])
	}
	if msg, _ := out["denial"].(string); !strings.Contains(msg, "allowlist") {
		t.Fatalf("the refusal did not come from the guard: %q", msg)
	}
}

// A run granted NO data plane (nil host) still runs: the engine is told, and
// says so, rather than failing the step — the remote `use: cli` posture.
func TestReferenceEnginePluginWithoutADataPlane(t *testing.T) {
	bin, sum := buildEnginePlugin(t)
	cl := iplugin.NewClient(iplugin.Spec{
		Name: "acme-engine", Kind: iplugin.KindStep, Provides: "acme-engine",
		BinPath: bin, Sha256: sum,
	}, iplugin.Deps{Log: func(string, ...any) {}})
	defer cl.Close()

	out, err := cl.Run(context.Background(), sdk.RunRequest{
		Env: map[string]string{"STORE": "s", "NS": "n", "KEY": "k"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["no_data_plane"] != true {
		t.Fatalf("outputs = %+v", out)
	}
	if _, ok := out["roundtrip"]; ok {
		t.Fatal("an engine with no data plane reported a round trip")
	}
}

// A step that names a plugin engine AND a host: is refused, not silently run
// locally — the callback would have to cross the ssh hop to come back.
func TestPluginEngineIsLocalOnly(t *testing.T) {
	e := engineExecutor("acme-engine", fakeEngine{run: func(sdk.RunRequest, func(sdk.HostRequest) sdk.HostResult) (map[string]any, error) {
		return nil, fmt.Errorf("must not run")
	}})
	_, err := e.Exec(context.Background(), Spec{Run: "acme-engine", Plugin: true, Host: &hosts.Target{Name: "box"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "local-only") {
		t.Fatalf("want a local-only refusal, got %v", err)
	}
}
