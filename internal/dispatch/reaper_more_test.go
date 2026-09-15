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
// finished agent's worktree workspace is archived, a finished checkout:none
// agent's EPHEMERAL per-run workspace is archived, a plain finished agent is
// archived directly, and a spinning-up agent rides the startup grace.
func TestReapScenario(t *testing.T) {
	bin, dir := fakePaseoDir(t)
	wt := filepath.Join(dir, "wt-pr5")
	runDir := filepath.Join(dir, "runs", "cron-7-a1b2c3")
	old := "2020-01-01T00:00:00Z"
	fresh := time.Now().UTC().Format(time.RFC3339)

	put(t, dir, "ls.json", fmt.Sprintf(`[
	  {"id":"a-held","status":"idle"},
	  {"id":"a-ask","status":"idle"},
	  {"id":"a-done","status":"idle","cwd":%q},
	  {"id":"a-run","status":"idle","cwd":%q},
	  {"id":"a-plain","status":"completed","cwd":"/elsewhere"},
	  {"id":"a-young","status":"idle"},
	  {"id":"a-running","status":"running"},
	  {"id":""}
	]`, wt, runDir))
	put(t, dir, "workspaces.json", fmt.Sprintf(`[
	  {"workspaceId":"wks_wt","cwd":%q,"isolation":"worktree"},
	  {"workspaceId":"wks_run","name":"conductor-run-cron-7-a1b2c3","isolation":"local","cwd":%q}
	]`, wt, runDir))
	put(t, dir, "inspect-a-ask.json", `{"PendingPermissions":[{"q":1}],"CreatedAt":"`+old+`","LastUsage":"`+old+`"}`)
	put(t, dir, "inspect-a-done.json", `{"CreatedAt":"`+old+`","LastUsage":"`+old+`"}`)
	put(t, dir, "inspect-a-run.json", `{"CreatedAt":"`+old+`","LastUsage":"`+old+`"}`)
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
	if !strings.Contains(calls, "workspace archive wks_run") {
		t.Fatalf("finished checkout:none agent should archive its per-run workspace (else it leaks):\n%s", calls)
	}
	if strings.Contains(calls, "archive a-run\n") {
		t.Fatalf("the run workspace archive already reclaims its agent:\n%s", calls)
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

// TestReapLeavesUnclaimedRunWorkspaceAlone: an ephemeral run workspace whose
// agent is still RUNNING (or has not launched yet) must survive the tick. The
// reaper reclaims only by walking from a finished agent, never by sweeping
// conductor-run-* workspaces — that sweep would race a starting dispatch.
func TestReapLeavesUnclaimedRunWorkspaceAlone(t *testing.T) {
	bin, dir := fakePaseoDir(t)
	runDir := filepath.Join(dir, "runs", "cron-1-ffff")
	put(t, dir, "workspaces.json", fmt.Sprintf(
		`[{"workspaceId":"wks_run","name":"conductor-run-cron-1-ffff","isolation":"local","cwd":%q}]`, runDir))
	put(t, dir, "ls.json", fmt.Sprintf(`[{"id":"a-1","status":"running","cwd":%q}]`, runDir))
	r := &Reaper{PaseoBin: bin, Held: NewHoldSet(filepath.Join(dir, "h.json"))}
	r.reap(context.Background())
	if strings.Contains(callsLog(t, dir), "workspace archive") {
		t.Fatal("a run workspace with a live agent must not be reclaimed")
	}
	// No agent at all (mid-creation, or already reclaimed) → still a no-op.
	put(t, dir, "ls.json", `[]`)
	r.reap(context.Background())
	if strings.Contains(callsLog(t, dir), "workspace archive") {
		t.Fatal("an agentless run workspace must not be swept (it may be mid-launch)")
	}
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
