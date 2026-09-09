package dispatch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/plugin"
)

// buildRuntimePlugin compiles the in-repo acme-runtime reference plugin (built
// only against the public pkg/plugin SDK) into a temp dir, mirroring
// internal/plugin's buildExamplePlugin helper.
//
// It deliberately does NOT build a concrete runtime plugin. The paseo plugin
// now lives in the conductor-plugins repo, where its own verbs and CLI
// shell-out are tested against a protocol client
// (conductor-plugins/e2e/paseo_test.go). What belongs HERE is the daemon's half
// of the contract — rpcBackend over a real subprocess — so this drives the
// generic runtime-shaped reference plugin instead, and stays green regardless
// of what any external plugin does.
func buildRuntimePlugin(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "acme-runtime")
	build := exec.Command("go", "build", "-o", bin, "github.com/NodeSpy/conductor/test/plugins/acme-runtime")
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build acme-runtime: %v\n%s", err, out)
	}
	return bin
}

// TestRPCBackendRoundTripsListAgentsAndArchive drives rpcBackend through a
// *plugin.Client against a REAL plugin subprocess over the REAL transport, the
// way the daemon would: Backend method -> verb call + options across the wire
// -> plugin reply -> parsed back into Backend's typed result. This proves the
// wiring the Dispatcher uses when configured to run a runtime via a plugin,
// without changing the default (cliBackend) path at all.
func TestRPCBackendRoundTripsListAgentsAndArchive(t *testing.T) {
	pluginBin := buildRuntimePlugin(t)
	logPath := filepath.Join(t.TempDir(), "calls.log")

	spec := plugin.Spec{Name: "rt1", Kind: plugin.KindConnector, Provides: "acme-runtime",
		BinPath: pluginBin, AllowUnverified: true, AllowUnsandboxed: true}
	client := plugin.NewClient(spec, plugin.Deps{})
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	decl, err := client.Describe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Every operation the Backend interface needs must be declared, or
	// rpcBackend has nothing to call.
	wantVerbs := map[string]bool{
		"run": false, "list_agents": false, "inspect": false, "archive_agent": false,
		"archive_workspace": false, "create_worktree": false, "create_workspace": false,
		"list_workspaces": false, "clone": false, "send": false, "wait": false,
	}
	for _, v := range decl.Verbs {
		if _, ok := wantVerbs[v.Name]; ok {
			wantVerbs[v.Name] = true
		}
	}
	for name, seen := range wantVerbs {
		if !seen {
			t.Errorf("decl missing verb %q", name)
		}
	}

	conn := map[string]any{
		"calls_log":         logPath,
		"reply_list_agents": `{"agents":[{"id":"a-1","cwd":"/wt/one","status":"idle"}]}`,
	}
	backend := NewRPCBackend(client, "rt1", conn, 0, 0)

	agents, err := backend.ListAgents(ctx, map[string]string{"conductor": "1"})
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 || agents[0].ID != "a-1" || agents[0].Cwd != "/wt/one" || agents[0].Status != "idle" {
		t.Fatalf("ListAgents result = %+v", agents)
	}

	if err := backend.ArchiveAgent(ctx, "a-1"); err != nil {
		t.Fatalf("ArchiveAgent: %v", err)
	}

	calls, _ := os.ReadFile(logPath)
	if !strings.Contains(string(calls), `list_agents {"labels":{"conductor":"1"}}`) {
		t.Errorf("ListAgents should have sent the label filter across the wire, got:\n%s", calls)
	}
	if !strings.Contains(string(calls), `archive_agent {"id":"a-1"}`) {
		t.Errorf("ArchiveAgent should have sent verb archive_agent with id a-1, got:\n%s", calls)
	}
}

// TestRPCBackendRetriesOverRealSubprocess proves the retry loop against a real
// plugin subprocess, not just an in-process double: the plugin fails the first
// two `run` calls with a transient git-lock message and succeeds on the third.
func TestRPCBackendRetriesOverRealSubprocess(t *testing.T) {
	pluginBin := buildRuntimePlugin(t)
	logPath := filepath.Join(t.TempDir(), "calls.log")

	spec := plugin.Spec{Name: "rt2", Kind: plugin.KindConnector, Provides: "acme-runtime",
		BinPath: pluginBin, AllowUnverified: true, AllowUnsandboxed: true}
	client := plugin.NewClient(spec, plugin.Deps{})
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn := map[string]any{
		"calls_log": logPath,
		"fail_run":  "fatal: could not lock config file .git/config: File exists#2",
		"reply_run": `{"output":"ran","agentId":"a-ok"}`,
	}
	backend := NewRPCBackend(client, "rt2", conn, 2, time.Millisecond)

	res, err := backend.RunAgent(ctx, RunAgentOptions{Args: []string{"run", "p"}})
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if res.AgentID != "a-ok" || res.Output != "ran" {
		t.Fatalf("RunAgent result = %+v", res)
	}
	calls, _ := os.ReadFile(logPath)
	if got := strings.Count(string(calls), "run "); got != 3 {
		t.Fatalf("expected 3 run attempts (2 transient failures + 1 success) across the wire, got %d:\n%s", got, calls)
	}
}

// fakeInvoker is an in-process Invoker double — no plugin subprocess at all —
// for asserting rpcBackend's verb+options mapping and output parsing in
// isolation, IN ADDITION to the real-binary round trips above, for cheap
// coverage of every method.
type fakeInvoker struct {
	calls []plugin.InvokeRequest
	// outputs, keyed by verb; err, keyed by verb (checked first).
	outputs map[string]map[string]any
	errs    map[string]error
}

func (f *fakeInvoker) Invoke(_ context.Context, req plugin.InvokeRequest) (map[string]any, error) {
	f.calls = append(f.calls, req)
	if err, ok := f.errs[req.Verb]; ok {
		return nil, err
	}
	return f.outputs[req.Verb], nil
}

func (f *fakeInvoker) last() plugin.InvokeRequest { return f.calls[len(f.calls)-1] }

func TestRPCBackendVerbAndOptionMapping(t *testing.T) {
	ctx := context.Background()
	fi := &fakeInvoker{outputs: map[string]map[string]any{
		"run":               {"output": "ran", "agentId": "a-new"},
		"list_agents":       {"agents": []any{map[string]any{"id": "a-1", "cwd": "/c", "status": "idle"}}},
		"inspect":           {"cwd": "/c", "lastUsage": "L", "updatedAt": "U", "createdAt": "C"},
		"create_worktree":   {"workspaceId": "wks_1", "cwd": "/wt"},
		"create_workspace":  {"workspaceId": "wks_2"},
		"list_workspaces":   {"workspaces": []any{map[string]any{"workspaceId": "wks_1", "project": "a/w", "cwd": "/wt", "isolation": "worktree", "name": ""}}},
		"send":              {"output": "sent"},
		"archive_agent":     {},
		"archive_workspace": {},
		"clone":             {},
		"wait":              {},
	}}
	b := NewRPCBackend(fi, "paseo1", map[string]any{"paseo_bin": "stub-paseo"}, 2, time.Millisecond)

	res, err := b.RunAgent(ctx, RunAgentOptions{Args: []string{"run", "p", "--json"}, Cwd: "/c"})
	if err != nil || res.Output != "ran" || res.AgentID != "a-new" {
		t.Fatalf("RunAgent: %v %+v", err, res)
	}
	if got := fi.last(); got.Verb != "run" || got.Instance != "paseo1" || got.Connection["paseo_bin"] != "stub-paseo" {
		t.Fatalf("RunAgent invoke = %+v", got)
	}
	if args, ok := fi.last().Options["args"].([]string); !ok || len(args) != 3 {
		t.Fatalf("RunAgent options[args] = %#v", fi.last().Options["args"])
	}

	agents, err := b.ListAgents(ctx, map[string]string{"conductor": "1", "pr": "a/w#1"})
	if err != nil || len(agents) != 1 || agents[0].ID != "a-1" {
		t.Fatalf("ListAgents: %v %+v", err, agents)
	}
	labels, _ := fi.last().Options["labels"].(map[string]any)
	if labels["conductor"] != "1" || labels["pr"] != "a/w#1" {
		t.Fatalf("ListAgents options[labels] = %#v", fi.last().Options["labels"])
	}

	det, err := b.Inspect(ctx, "a-1")
	if err != nil || det.Cwd != "/c" || det.LastUsage != "L" || det.UpdatedAt != "U" || det.CreatedAt != "C" {
		t.Fatalf("Inspect: %v %+v", err, det)
	}
	if fi.last().Options["id"] != "a-1" {
		t.Fatalf("Inspect options = %#v", fi.last().Options)
	}

	if err := b.ArchiveAgent(ctx, "a-1"); err != nil || fi.last().Verb != "archive_agent" || fi.last().Options["id"] != "a-1" {
		t.Fatalf("ArchiveAgent: %v %+v", err, fi.last())
	}
	if err := b.ArchiveWorkspace(ctx, "wks_1"); err != nil || fi.last().Verb != "archive_workspace" || fi.last().Options["id"] != "wks_1" {
		t.Fatalf("ArchiveWorkspace: %v %+v", err, fi.last())
	}

	wt, err := b.CreateWorktree(ctx, CreateWorktreeOptions{Isolation: "worktree", Path: "/base", Strategy: "checkout-pr", PRNumber: 5, Forge: "github"})
	if err != nil || wt.WorkspaceID != "wks_1" || wt.Cwd != "/wt" {
		t.Fatalf("CreateWorktree: %v %+v", err, wt)
	}
	if fi.last().Options["strategy"] != "checkout-pr" || fi.last().Options["prNumber"] != 5 {
		t.Fatalf("CreateWorktree options = %#v", fi.last().Options)
	}

	ws, err := b.CreateWorkspace(ctx, CreateWorkspaceOptions{Isolation: "local", Path: "/home", Title: "conductor-scratch"})
	if err != nil || ws.WorkspaceID != "wks_2" {
		t.Fatalf("CreateWorkspace: %v %+v", err, ws)
	}

	wl, err := b.ListWorkspaces(ctx)
	if err != nil || len(wl) != 1 || wl[0].WorkspaceID != "wks_1" || wl[0].Isolation != "worktree" {
		t.Fatalf("ListWorkspaces: %v %+v", err, wl)
	}

	if err := b.Clone(ctx, CloneOptions{Repo: "a/w", Dir: "/d", Protocol: "ssh"}); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if fi.last().Options["repo"] != "a/w" || fi.last().Options["protocol"] != "ssh" {
		t.Fatalf("Clone options = %#v", fi.last().Options)
	}

	sr, err := b.Send(ctx, SendOptions{ID: "a-1", Prompt: "go", JSON: true})
	if err != nil || sr.Output != "sent" {
		t.Fatalf("Send: %v %+v", err, sr)
	}
	if fi.last().Options["prompt"] != "go" || fi.last().Options["json"] != true {
		t.Fatalf("Send options = %#v", fi.last().Options)
	}

	if err := b.Wait(ctx, "a-1"); err != nil || fi.last().Verb != "wait" || fi.last().Options["id"] != "a-1" {
		t.Fatalf("Wait: %v %+v", err, fi.last())
	}
}

func TestRPCBackendRunAgentRetriesTransientThenSucceeds(t *testing.T) {
	ctx := context.Background()

	// A transient failure (git lock) retries and succeeds.
	cf := &countingInvoker{failUntil: 2, okOutput: map[string]any{"output": "ran", "agentId": "a-ok"}}
	b := NewRPCBackend(cf, "paseo1", nil, 2, time.Millisecond)
	res, err := b.RunAgent(ctx, RunAgentOptions{Args: []string{"run", "p"}})
	if err != nil || res.AgentID != "a-ok" {
		t.Fatalf("expected transient retry to succeed: %v %+v", err, res)
	}
	if cf.calls != 3 {
		t.Fatalf("expected 3 attempts (2 failures + 1 success), got %d", cf.calls)
	}

	// A non-transient failure must not retry.
	cf2 := &countingInvoker{failUntil: 99, permanent: true}
	b2 := NewRPCBackend(cf2, "paseo1", nil, 3, time.Millisecond)
	if _, err := b2.RunAgent(ctx, RunAgentOptions{Args: []string{"run", "p"}}); err == nil {
		t.Fatal("expected a non-transient failure to surface an error")
	}
	if cf2.calls != 1 {
		t.Fatalf("non-transient failure must not retry, got %d attempts", cf2.calls)
	}
}

// countingInvoker fails its first failUntil calls (transiently, unless
// permanent is set) and succeeds afterward, for exercising rpcBackend's retry
// loop deterministically.
type countingInvoker struct {
	calls     int
	failUntil int
	permanent bool
	okOutput  map[string]any
}

func (c *countingInvoker) Invoke(_ context.Context, req plugin.InvokeRequest) (map[string]any, error) {
	c.calls++
	if c.calls <= c.failUntil {
		if c.permanent {
			return nil, fmt.Errorf("authentication failed")
		}
		return nil, fmt.Errorf("fatal: could not lock config file .git/config: File exists")
	}
	return c.okOutput, nil
}
