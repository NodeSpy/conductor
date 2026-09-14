package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

func TestPaseoTemplateErrors(t *testing.T) {
	d := &Dispatcher{PaseoBin: "paseo", DryRun: true, repoDirs: map[string]string{}}
	base := Request{Trigger: core.Trigger{Target: core.Target{Repo: "a/w", PR: 1}}}

	bad := base
	bad.Action = config.Action{Type: "agent", Prompt: "{{.broken"}
	if _, err := d.paseo(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "render prompt") {
		t.Fatalf("prompt render error: %v", err)
	}
	bad.Action = config.Action{Type: "agent", Prompt: "p", WorkDir: "{{.broken"}
	if _, err := d.paseo(context.Background(), bad); err == nil {
		t.Fatal("workdir render error expected")
	}
	bad.Action = config.Action{Type: "agent", Prompt: "p", Checkout: "none", Env: map[string]string{"X": "{{.broken"}}
	if _, err := d.paseo(context.Background(), bad); err == nil {
		t.Fatal("env render error expected")
	}
}

// TestPaseoDryRunArgvShape: the preview argv carries every profile flag, the
// identity env, labels, and the wait/output-schema tail — without touching a
// daemon.
func TestPaseoDryRunArgvShape(t *testing.T) {
	d := &Dispatcher{PaseoBin: "paseo", DryRun: true, repoDirs: map[string]string{}}
	req := Request{
		Wait: true,
		Trigger: core.Trigger{Kind: "review_requested", Source: "github", Instance: "gh",
			Target: core.Target{Repo: "a/w", PR: 5, Number: 5}},
		Action: config.Action{Type: "agent", Prompt: "review {{.repo}}#{{.pr}}", Checkout: "none",
			OutputSchema: map[string]any{"type": "object"},
			Env:          map[string]string{"SCOPE": "{{.kind}}"}},
		Model: "m1",
		Step: config.Step{Model: config.ModelSpecOf("m1"), Thinking: "high", Mode: "auto",
			WaitTimeout: config.Duration(5 * time.Minute)},
		Tokens: Tokens{App: "at", User: "ut"},
		Author: Author{Name: "Me", Email: "me@x"},
	}
	ref, err := d.paseo(context.Background(), req)
	if err != nil || !ref.Shadowed {
		t.Fatalf("dry-run: %v %+v", err, ref)
	}
	argv := strings.Join(ref.Argv, " ")
	for _, want := range []string{
		"review a/w#5", "--model m1", "--thinking high", "--mode auto",
		"--env GH_TOKEN=ut", "--env PC_GH_APP_TOKEN=at",
		"--env GIT_AUTHOR_NAME=Me", "--env GIT_COMMITTER_EMAIL=me@x",
		"--env SCOPE=review_requested",
		"--label conductor=1", "--wait-timeout 5m0s", "--output-schema", "--json",
	} {
		if !strings.Contains(argv, want) {
			t.Fatalf("argv missing %q:\n%s", want, argv)
		}
	}
	if strings.Contains(argv, "--background") {
		t.Fatal("wait mode must not be background")
	}

	// Background (non-wait) shape.
	req.Wait = false
	ref, _ = d.paseo(context.Background(), req)
	if !strings.Contains(strings.Join(ref.Argv, " "), "--background") {
		t.Fatal("non-wait runs in background")
	}
}

// TestPaseoProviderFlag: a Request carrying a resolved Provider alongside its
// Model must pass BOTH --provider and --model to `paseo run` — real paseo
// rejects a model-pinned run with --model but no --provider
// (MISSING_PROVIDER, exit 1); --model alone is not enough. Regression guard
// for the v0.9.0 dispatch bug: the resolver knew the model's provider
// (models.Decision) but dispatch never threaded it through to argv.
func TestPaseoProviderFlag(t *testing.T) {
	d := &Dispatcher{PaseoBin: "paseo", DryRun: true, repoDirs: map[string]string{}}
	req := Request{
		Wait:     true,
		Trigger:  core.Trigger{Kind: "merge_conflict", Target: core.Target{Repo: "a/w", PR: 1, Number: 1}},
		Action:   config.Action{Type: "agent", Prompt: "fix it", Checkout: "none"},
		Model:    "claude-opus-4-8[1m]",
		Provider: "claude",
	}
	ref, err := d.paseo(context.Background(), req)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	argv := strings.Join(ref.Argv, " ")
	if !strings.Contains(argv, "--provider claude") {
		t.Fatalf("argv missing --provider claude (real paseo requires it alongside --model): %s", argv)
	}
	if !strings.Contains(argv, "--model claude-opus-4-8[1m]") {
		t.Fatalf("argv missing --model: %s", argv)
	}

	// A bare launch (no resolved model/provider) must still omit both flags —
	// the runtime's own default, unaffected by this fix.
	bare := req
	bare.Model, bare.Provider = "", ""
	ref, err = d.paseo(context.Background(), bare)
	if err != nil {
		t.Fatalf("dry-run bare: %v", err)
	}
	argv = strings.Join(ref.Argv, " ")
	if strings.Contains(argv, "--provider") || strings.Contains(argv, "--model") {
		t.Fatalf("bare launch must pass neither --provider nor --model: %s", argv)
	}
}

func TestPaseoCatchUpSkipsAndQueueFailure(t *testing.T) {
	bin, dir := fakePaseoDir(t)
	put(t, dir, "ls.json", `[{"id":"a-live"}]`)
	d := &Dispatcher{PaseoBin: bin, repoDirs: map[string]string{}}
	req := Request{
		CatchUp: true,
		Trigger: core.Trigger{Kind: "new_comment", Target: core.Target{Repo: "a/w", PR: 2, Number: 2}},
		Action:  config.Action{Type: "agent", Prompt: "p", Checkout: "none"},
	}
	ref, err := d.paseo(context.Background(), req)
	if err != nil || !ref.Skipped || !strings.Contains(ref.Output, "a-live") {
		t.Fatalf("catch-up with a live agent should skip: %v %+v", err, ref)
	}

	// Queue path: send fails → the error names the agent.
	req.CatchUp = false
	put(t, dir, "send-fail", "1")
	if _, err := d.paseo(context.Background(), req); err == nil || !strings.Contains(err.Error(), "queue to agent a-live") {
		t.Fatalf("queue failure: %v", err)
	}
	os.Remove(filepath.Join(dir, "send-fail"))
	ref, err = d.paseo(context.Background(), req)
	if err != nil || !ref.Queued || ref.AgentID != "a-live" {
		t.Fatalf("queue success: %v %+v", err, ref)
	}
}

// GUARD (#60, D1): every migrated flow step — even a single-step autonomous
// fixer, not just a genuine multi-step workflow — dispatches with Wait=true
// now (the #60 foreground-wait fix). The one-worker-per-PR dedup must not
// have quietly stopped applying to it: a second event on a PR that already
// has a live agent must queue onto it (RunRef.Queued=true), not spawn a
// fresh one, regardless of Wait. A step whose OutputSchema a later step
// reads is the one exception — it must actually dispatch and capture real
// output, since queueing hands back a canned string instead of the schema.
func TestPaseoForegroundWaitDispatchStillQueuesOntoLiveAgent(t *testing.T) {
	bin, dir := fakePaseoDir(t)
	put(t, dir, "ls-label.json", `[{"id":"a-live"}]`)
	d := &Dispatcher{PaseoBin: bin, repoDirs: map[string]string{}}
	req := Request{
		Wait: true, // foreground — what flow now sets for every non-background step
		Trigger: core.Trigger{Kind: "new_comment",
			Target: core.Target{Repo: "grpd/burst", PR: 1, Number: 1}},
		Action: config.Action{Type: "agent", Prompt: "p", Checkout: "none"},
	}
	ref, err := d.paseo(context.Background(), req)
	if err != nil || !ref.Queued || ref.AgentID != "a-live" {
		t.Fatalf("a foreground (Wait=true) dispatch onto a PR with a live agent "+
			"should queue, not spawn a fresh one: %v %+v", err, ref)
	}

	req.Action.OutputSchema = map[string]any{"type": "object"}
	ref, err = d.paseo(context.Background(), req)
	if err != nil {
		t.Fatalf("output-schema dispatch: %v", err)
	}
	if ref.Queued {
		t.Fatal("a step with an OutputSchema must actually dispatch — queueing would " +
			"hand it a canned \"queued to live agent\" string instead of the schema it declared")
	}
}

func TestPaseoAdoptCatchUpSkip(t *testing.T) {
	repo := gitRepoAt(t, "feat/x", "git@github.com:a/w.git")
	bin, dir := fakePaseoDir(t)
	// The label-filtered ls (liveAgentForPR) sees no conductor agent; the plain
	// ls (adoption scan) sees the open workspace agent.
	put(t, dir, "ls-label.json", `[]`)
	put(t, dir, "ls.json", `[{"id":"a-open","cwd":"`+repo+`"}]`)
	put(t, dir, "inspect-a-open.json", `{"LastUsage":"2026-01-01T00:00:00Z"}`)
	d := &Dispatcher{PaseoBin: bin, AdoptOpenWorkspaces: true, repoDirs: map[string]string{}}
	req := Request{
		CatchUp: true,
		Trigger: core.Trigger{Kind: "new_comment",
			Target:  core.Target{Repo: "a/w", PR: 3, Number: 3},
			Context: map[string]any{"head_ref": "feat/x"}},
		Action: config.Action{Type: "agent", Prompt: "p", Checkout: "none"},
	}
	ref, err := d.paseo(context.Background(), req)
	if err != nil || !ref.Skipped || !strings.Contains(ref.Output, "your open agent a-open") {
		t.Fatalf("adopt catch-up should skip: %v %+v", err, ref)
	}
	// Non-catch-up adopts by queueing.
	req.CatchUp = false
	ref, err = d.paseo(context.Background(), req)
	if err != nil || !ref.Adopted || ref.AgentID != "a-open" {
		t.Fatalf("adopt queue: %v %+v", err, ref)
	}
}

// TestPaseoRetryTransient: a git-lock failure retries and succeeds; a
// non-transient failure does not retry.
func TestPaseoRetryTransient(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "paseo")
	script := `#!/usr/bin/env bash
dir="$(cd "$(dirname "$0")" && pwd)"
case "$1" in
  ls) echo '[]' ;;
  run)
    if [ ! -f "$dir/tried" ]; then
      touch "$dir/tried"
      echo "fatal: could not lock config file" >&2
      exit 1
    fi
    echo '{"agentId":"a-retry"}' ;;
  *) echo '{}' ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{PaseoBin: bin, RetryMax: 2, RetryBackoff: time.Millisecond, repoDirs: map[string]string{}}
	req := Request{
		Trigger: core.Trigger{Kind: "merge_conflict", Target: core.Target{Repo: "a/w", PR: 4, Number: 4}},
		Action:  config.Action{Type: "agent", Prompt: "p", Checkout: "none"},
	}
	ref, err := d.paseo(context.Background(), req)
	if err != nil || ref.AgentID != "a-retry" {
		t.Fatalf("transient failure should retry to success: %v %+v", err, ref)
	}

	// Non-transient error: fails once, with the detail attached.
	bin2 := filepath.Join(t.TempDir(), "paseo")
	os.WriteFile(bin2, []byte("#!/usr/bin/env bash\ncase \"$1\" in ls) echo '[]';; run) echo 'bad provider' >&2; exit 1;; *) echo '{}';; esac\n"), 0o755)
	d2 := &Dispatcher{PaseoBin: bin2, RetryMax: 3, RetryBackoff: time.Millisecond, repoDirs: map[string]string{}}
	if _, err := d2.paseo(context.Background(), req); err == nil || !strings.Contains(err.Error(), "bad provider") {
		t.Fatalf("non-transient failure: %v", err)
	}
}

func TestDispatchBackendRouting(t *testing.T) {
	d := &Dispatcher{PaseoBin: "paseo", DryRun: true, repoDirs: map[string]string{}}
	if _, err := d.Dispatch(context.Background(), Request{Action: config.Action{Backend: "nope"}}); err == nil || !strings.Contains(err.Error(), `unknown backend "nope"`) {
		t.Fatalf("unknown backend: %v", err)
	}
	// A command routes to local; dry-run shadows it.
	ref, err := d.Dispatch(context.Background(), Request{Action: config.Action{Type: "command", Command: []string{"echo", "hi"}}})
	if err != nil || ref.Backend != "local" {
		t.Fatalf("local routing: %v %+v", err, ref)
	}
}
