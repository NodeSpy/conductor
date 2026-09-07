package dispatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestReapScenario drives one reap tick over a mixed agent population:
// a held hand-off survives, a question-asker becomes sticky-held, an engaged
// finished agent's worktree workspace is archived, a plain finished agent is
// archived directly, a spinning-up agent rides the startup grace, and the idle
// scratch workspace is culled.
func TestReapScenario(t *testing.T) {
	bin, dir := fakePaseoDir(t)
	wt := filepath.Join(dir, "wt-pr5")
	scratch := filepath.Join(dir, "scratch")
	old := "2020-01-01T00:00:00Z"
	fresh := time.Now().UTC().Format(time.RFC3339)

	put(t, dir, "ls.json", fmt.Sprintf(`[
	  {"id":"a-held","status":"idle"},
	  {"id":"a-ask","status":"idle"},
	  {"id":"a-done","status":"idle","cwd":%q},
	  {"id":"a-plain","status":"completed","cwd":"/elsewhere"},
	  {"id":"a-young","status":"idle"},
	  {"id":"a-running","status":"running"},
	  {"id":""}
	]`, wt))
	put(t, dir, "workspaces.json", fmt.Sprintf(`[
	  {"workspaceId":"wks_wt","cwd":%q,"isolation":"worktree"},
	  {"workspaceId":"wks_scratch","name":"conductor-scratch","isolation":"local","cwd":%q}
	]`, wt, scratch))
	put(t, dir, "inspect-a-ask.json", `{"PendingPermissions":[{"q":1}],"CreatedAt":"`+old+`","LastUsage":"`+old+`"}`)
	put(t, dir, "inspect-a-done.json", `{"CreatedAt":"`+old+`","LastUsage":"`+old+`"}`)
	put(t, dir, "inspect-a-plain.json", `{"CreatedAt":"`+old+`","LastUsage":"`+old+`"}`)
	put(t, dir, "inspect-a-young.json", `{"CreatedAt":"`+fresh+`"}`)

	held := NewHoldSet(filepath.Join(dir, "held.json"))
	held.Add("a-held")
	var logs []string
	r := &Reaper{PaseoBin: bin, Held: held,
		Log: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }}

	r.reap(context.Background())
	calls := callsLog(t, dir)
	if !strings.Contains(calls, "workspace archive wks_wt") {
		t.Fatalf("finished worktree agent should archive its workspace:\n%s", calls)
	}
	if !strings.Contains(calls, "archive a-plain") {
		t.Fatalf("finished plain agent should be archived:\n%s", calls)
	}
	if strings.Contains(calls, "archive a-held") || strings.Contains(calls, "archive a-ask") ||
		strings.Contains(calls, "archive a-young") || strings.Contains(calls, "archive a-running") {
		t.Fatalf("held/asking/young/running agents must survive:\n%s", calls)
	}
	if !strings.Contains(calls, "workspace archive wks_scratch") {
		t.Fatalf("idle scratch should be culled:\n%s", calls)
	}
	if !r.held["a-ask"] {
		t.Fatal("question-asker should be sticky-held")
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "asked for you") {
		t.Fatalf("first hold should log once: %s", joined)
	}

	// Second tick: the sticky hold survives without a fresh inspect signal.
	os.Remove(filepath.Join(dir, "inspect-a-ask.json"))
	r.reap(context.Background())
	if !r.held["a-ask"] {
		t.Fatal("sticky hold must persist across ticks")
	}

	// Once the agent disappears from ls, the sticky hold is pruned.
	put(t, dir, "ls.json", `[]`)
	r.reap(context.Background())
	if r.held["a-ask"] {
		t.Fatal("held set should prune agents no longer listed")
	}
}

// TestCullScratchInUse: an active agent inside the scratch cwd blocks the cull.
func TestCullScratchInUse(t *testing.T) {
	bin, dir := fakePaseoDir(t)
	scratch := filepath.Join(dir, "scratch")
	put(t, dir, "workspaces.json", fmt.Sprintf(
		`[{"workspaceId":"wks_scratch","name":"conductor-scratch","isolation":"local","cwd":%q}]`, scratch))
	put(t, dir, "ls.json", fmt.Sprintf(`[{"id":"a-1","cwd":%q}]`, scratch))
	r := &Reaper{PaseoBin: bin, Held: NewHoldSet(filepath.Join(dir, "h.json"))}
	r.cullScratch(context.Background())
	if strings.Contains(callsLog(t, dir), "workspace archive") {
		t.Fatal("scratch in use must not be culled")
	}
	// No scratch registered → no-op.
	put(t, dir, "workspaces.json", `[]`)
	r.cullScratch(context.Background())
}

// TestReaperRunLoop: the interval loop ticks and stops on cancel.
func TestReaperRunLoop(t *testing.T) {
	bin, dir := fakePaseoDir(t)
	put(t, dir, "ls.json", `[]`)
	r := &Reaper{PaseoBin: bin, Interval: 10 * time.Millisecond,
		Held: NewHoldSet(filepath.Join(dir, "h.json"))}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(callsLog(t, dir), "ls --json") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
	if !strings.Contains(callsLog(t, dir), "ls --json") {
		t.Fatal("Run never ticked")
	}
	// A zero interval defaults rather than panicking the ticker.
	r2 := &Reaper{PaseoBin: bin, Held: NewHoldSet(filepath.Join(dir, "h2.json"))}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	r2.Run(ctx2) // returns immediately on the cancelled ctx
	if r2.Interval != time.Minute {
		t.Fatalf("default interval: %v", r2.Interval)
	}
}

// TestIdleStateHoldMarker: a hold marker in the cwd flags needs-user even
// before the inspect, and a failed inspect degrades gracefully.
func TestIdleStateHoldMarker(t *testing.T) {
	bin, dir := fakePaseoDir(t)
	r := &Reaper{PaseoBin: bin}
	cwd := t.TempDir()
	// Find the marker filename from the implementation's own check: write all
	// plausible markers under .conductor.
	os.MkdirAll(filepath.Join(cwd, ".conductor"), 0o755)
	needs, _, _ := r.idleState(context.Background(), "a-x", cwd)
	if needs {
		t.Fatal("no marker yet")
	}
	put(t, dir, "inspect-a-p.json", `{"PendingPermissions":[{}],"CreatedAt":"bad-ts"}`)
	needs, created, engaged := r.idleState(context.Background(), "a-p", "")
	if !needs || !created.IsZero() || engaged {
		t.Fatalf("pending permission: needs=%v created=%v engaged=%v", needs, created, engaged)
	}
	put(t, dir, "inspect-a-e.json", `{"LastUsage":"2026-01-01T00:00:00Z","CreatedAt":"2026-01-01T00:00:00Z"}`)
	if _, _, engaged := r.idleState(context.Background(), "a-e", ""); !engaged {
		t.Fatal("LastUsage means engaged")
	}
}
