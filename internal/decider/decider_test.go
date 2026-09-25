package decider

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/systemone"
)

func buildDecider(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	bin := filepath.Join(t.TempDir(), "acme-decider")
	build := exec.Command("go", "build", "-o", bin, "github.com/NodeSpy/conductor/test/plugins/acme-decider")
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build acme-decider: %v\n%s", err, out)
	}
	return bin
}

// realRuntime starts the reference decision runtime as a real subprocess.
func realRuntime(t *testing.T, conn map[string]any) (*Runtime, *plugin.Decl) {
	t.Helper()
	spec := plugin.Spec{Name: "acme", Kind: plugin.KindRuntime, Provides: "acme-decider", BinPath: buildDecider(t), Local: true}
	client := plugin.NewClient(spec, plugin.Deps{})
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	decl, err := client.Describe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return New("acme", decl.Protocols, client, conn), decl
}

func questions(t *testing.T) systemone.Questions {
	t.Helper()
	var qs systemone.Questions
	if err := yaml.Unmarshal([]byte(`
refuted: { type: noul, instructions: "contradicted?" }
risk: { type: choice, criteria: { high: x, low: y } }
tests: { type: score, criteria: [none, some, full] }
`), &qs); err != nil {
		t.Fatal(err)
	}
	return qs
}

func TestRealDecisionRuntimeRoundTrip(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "calls.log")
	rt, decl := realRuntime(t, map[string]any{"noul": "0.64", "calls_log": logPath, "api_key": "k-123"})
	if !IsDecisionRuntime(decl) {
		t.Fatalf("the reference plugin must classify as a decision runtime: %+v", decl)
	}
	ans, err := rt.Decide(context.Background(), systemone.ProtocolV1,
		systemone.Request{Model: "acme-latest", State: "the diff", Questions: questions(t)})
	if err != nil {
		t.Fatal(err)
	}
	if ans.Model != "acme-2" {
		t.Fatalf("the runtime's resolved model is reported: %q", ans.Model)
	}
	if got := ans.Answers["refuted"].(map[string]any)["noul"]; got != 0.64 {
		t.Fatalf("refuted.noul = %v", got)
	}
	if ans.Answers["risk"].(map[string]any)["choice"] != "high" {
		t.Fatalf("risk = %v", ans.Answers["risk"])
	}
	if lg := ans.Answers["tests"].(map[string]any)["legend"].(map[string]any); lg["2"] != "full" {
		t.Fatalf("the legend comes from the question: %v", lg)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	line := string(raw)
	for _, want := range []string{"key=k-123", `"protocol":"system_one/v1"`, `"state":"the diff"`, `"model":"acme-latest"`} {
		if !strings.Contains(line, want) {
			t.Errorf("the request on the wire should carry %s:\n%s", want, line)
		}
	}
}

// The subprocess outlives a call: a per-call timeout must bound the call, not
// the process — otherwise every decision restarts the plugin and the client's
// crash-loop guard parks it after a few.
func TestRealDecisionRuntimeProcessSurvivesCalls(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "calls.log")
	rt, _ := realRuntime(t, map[string]any{"calls_log": logPath})
	for i := 0; i < 6; i++ {
		if _, err := rt.Decide(context.Background(), systemone.ProtocolV1,
			systemone.Request{State: "s", Questions: questions(t)}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	pids := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		for _, f := range strings.Fields(line) {
			if strings.HasPrefix(f, "pid=") {
				pids[f] = true
			}
		}
	}
	if len(pids) != 1 {
		t.Fatalf("six decisions should share one process, got %d: %v", len(pids), pids)
	}
}

func TestRealDecisionRuntimeLists(t *testing.T) {
	rt, _ := realRuntime(t, nil)
	roster, err := rt.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(roster.IDs(), ",") != "acme-2,acme-1" {
		t.Fatalf("roster = %v (newest first)", roster.IDs())
	}
}

// An untrusted runtime's invalid answer never reaches a workflow.
func TestRealDecisionRuntimeInvalidAnswersRefused(t *testing.T) {
	for _, bad := range []string{"range", "missing"} {
		t.Run(bad, func(t *testing.T) {
			rt, _ := realRuntime(t, map[string]any{"bad": bad})
			_, err := rt.Decide(context.Background(), systemone.ProtocolV1,
				systemone.Request{State: "s", Questions: questions(t)})
			if err == nil || !strings.Contains(err.Error(), "invalid answer") {
				t.Fatalf("want an invalid-answer refusal, got %v", err)
			}
		})
	}
}

type fakeInvoker struct {
	out  map[string]any
	err  error
	reqs []plugin.InvokeRequest
}

func (f *fakeInvoker) Invoke(ctx context.Context, req plugin.InvokeRequest) (map[string]any, error) {
	f.reqs = append(f.reqs, req)
	return f.out, f.err
}

func TestDecideRefusesAProtocolItDoesNotSpeak(t *testing.T) {
	inv := &fakeInvoker{}
	rt := New("acme", []string{systemone.ProtocolV1}, inv, nil)
	if _, err := rt.Decide(context.Background(), "system_one/v2", systemone.Request{Questions: questions(t)}); err == nil {
		t.Fatal("a protocol the runtime didn't declare must be refused before the call")
	}
	if len(inv.reqs) != 0 {
		t.Fatal("no call may be made for an undeclared protocol")
	}
}

func TestDecidePropagatesTransportErrors(t *testing.T) {
	rt := New("acme", []string{systemone.ProtocolV1}, &fakeInvoker{err: errors.New("503")}, nil)
	if _, err := rt.Decide(context.Background(), systemone.ProtocolV1, systemone.Request{Questions: questions(t)}); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("got %v", err)
	}
}

func TestSetHooks(t *testing.T) {
	s := Set{"jev": New("jev", []string{systemone.ProtocolV1}, &fakeInvoker{}, nil)}
	if !s.DecisionOnly("jev") || s.DecisionOnly("paseo") {
		t.Fatal("DecisionOnly must be exactly the set")
	}
	if p := s.NativeProtocols("jev"); len(p) != 1 || p[0] != systemone.ProtocolV1 {
		t.Fatalf("protocols = %v", p)
	}
	var none Set
	if none.DecisionOnly("jev") {
		t.Fatal("a nil set marks nothing decision-only")
	}
}

func TestIsDecisionRuntimeNeedsTheDecideVerb(t *testing.T) {
	if IsDecisionRuntime(&plugin.Decl{Protocols: []string{systemone.ProtocolV1}}) {
		t.Fatal("declaring a protocol without serving decide is not a decision runtime")
	}
	if IsDecisionRuntime(&plugin.Decl{Verbs: []plugin.Verb{{Name: "decide"}}}) {
		t.Fatal("serving decide without declaring a protocol is not a decision runtime")
	}
}
