package flow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/core/coretest"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/gitpush"
	"github.com/NodeSpy/conductor/internal/secrets"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// The pr-autopilot pack's progress hooks, end to end: testdata/packs/
// pr-autopilot is a copy of the published pack (conductor-packs, keep in
// sync), instantiated through the real pack loader (settings substitution,
// connector binding, namespacing) and run against the fixture forge
// connector, whose verbs a fake answers. What it pins is the pack's contract
// with the PR:
//
//   - start: 👀 on the subject, `pending` on the commit the run starts on;
//   - done: 🚀/👍, and `success` ONLY on that start commit — never the new
//     head, so after a push the PR shows no row of the context at all;
//   - fail: 😕, and `failure` on the PR's current head.

// ghAPI is a fake forge answering the pack's verbs (and the engine's head
// read), recording reactions and statuses.
type ghAPI struct {
	mu       sync.Mutex
	head     string
	merged   bool     // the PR merged (state closed)
	statuses []string // "<sha> <state> <context> | <description>"
	reacts   []string // "<kind> <id> <content>"; a removal is "<kind> <id> -<content>"
	// on is what each subject carries now: "<kind> <id>" -> contents — so a
	// test can assert what is LEFT, not just what was called.
	on map[string]map[string]bool
	// rows is each (sha, context)'s latest state — what the PR shows.
	rows map[string]string
	// writes are the review writes: "resolve_threads <comments>",
	// "rerequest_review <reviewers>".
	writes []string
}

func newGHAPI(t *testing.T, head string) *ghAPI {
	api := &ghAPI{head: head, on: map[string]map[string]bool{}, rows: map[string]string{}}
	coretest.Forge.Respond(func(req sdk.InvokeRequest) (sdk.InvokeResult, error) {
		api.mu.Lock()
		defer api.mu.Unlock()
		o := req.Options
		switch req.Verb {
		case "pr_head":
			state := "open"
			if api.merged {
				state = "merged"
			}
			return sdk.InvokeResult{Outputs: map[string]any{"sha": api.head, "state": state}}, nil
		case "set_status":
			// pr: resolves the PR's current head, as the plugin does.
			sha := fmt.Sprint(o["sha"])
			if o["sha"] == nil || sha == "" {
				sha = api.head
			}
			api.statuses = append(api.statuses, fmt.Sprintf("%s %v %v | %v", sha, o["state"], o["context"], o["description"]))
			api.rows[sha+" "+fmt.Sprint(o["context"])] = fmt.Sprint(o["state"])
			return sdk.InvokeResult{Outputs: map[string]any{"ok": true, "sha": sha}}, nil
		case "react":
			subjects, _ := o["subjects"].([]any)
			for _, sub := range subjects {
				m, _ := sub.(map[string]any)
				key := fmt.Sprintf("%v %v", m["kind"], m["id"])
				content := fmt.Sprint(o["content"])
				if o["remove"] == true {
					api.reacts = append(api.reacts, key+" -"+content)
					delete(api.on[key], content)
					continue
				}
				api.reacts = append(api.reacts, key+" "+content)
				if api.on[key] == nil {
					api.on[key] = map[string]bool{}
				}
				api.on[key][content] = true
			}
			return sdk.InvokeResult{Outputs: map[string]any{"ok": true}}, nil
		case "resolve_threads":
			api.writes = append(api.writes, fmt.Sprintf("resolve_threads %v", o["comments"]))
			return sdk.InvokeResult{Outputs: map[string]any{}}, nil
		case "rerequest_review":
			api.writes = append(api.writes, fmt.Sprintf("rerequest_review %v", o["reviewers"]))
			return sdk.InvokeResult{Outputs: map[string]any{}}, nil
		case "pr_get":
			// The PR's branch, for the moved-head steps.
			return sdk.InvokeResult{Outputs: map[string]any{"head": "pr-7", "head_sha": api.head, "state": "open"}}, nil
		case "check_failures":
			// The CI fixer's read of what failed (none, here).
			return sdk.InvokeResult{Outputs: map[string]any{"sha": api.head, "failures": []any{}}}, nil
		}
		t.Errorf("unexpected verb %s %v", req.Verb, o)
		return sdk.InvokeResult{}, sdk.Fail(sdk.CodeInvalid, "unexpected verb "+req.Verb, nil)
	})
	t.Cleanup(func() { coretest.Forge.Respond(nil) })
	return api
}

func (a *ghAPI) push(sha string) {
	a.mu.Lock()
	a.head = sha
	a.mu.Unlock()
}

func (a *ghAPI) take() (statuses, reacts []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	statuses, reacts = a.statuses, a.reacts
	a.statuses, a.reacts = nil, nil
	return
}

// autopilotRig instantiates the pack (all triggers armed on org/repo) and
// builds a runner over it.
func autopilotRig(t *testing.T) (*testRig, *config.Config) {
	t.Helper()
	rig, cfg, _ := autopilotRigRemote(t)
	return rig, cfg
}

// autopilotRigRemote is autopilotRig with the moved-head steps' world: the
// pack's git connector bound to the git fixture (its own repository under
// the test's temp dir), and push_remote a bare repository the test fills
// in — returned.
func autopilotRigRemote(t *testing.T) (*testRig, *config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	gitFixture(t, filepath.Dir(remote), "init", "-q", "--bare", remote)
	src, err := os.ReadFile("testdata/packs/pr-autopilot/conductor-pack.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "pr-autopilot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pr-autopilot", "conductor-pack.yaml"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`
connectors:
  gh:
    use: github
    identity: { write_token: literal-tok }
    webhook: { listen: "127.0.0.1:0", secret: s }
  landing:
    use: git-land
    dir: `+filepath.Join(dir, "landing.git")+`
packs:
  autopilot:
    source: ./pr-autopilot
    connectors: { github: gh, git-land: landing }
    settings: { push_remote: "`+remote+`" }
    triggers:
      "*": { enabled: true, repos: [org/repo] }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ResolvePacks(path); err != nil {
		t.Fatalf("resolve pack: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	reg, err := connector.Build(cfg, connector.Deps{Secrets: secrets.New(), Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	// The pack's hooks validate where the user's config will: every {{.run.*}},
	// {{.hook.*}}, {{.me.login}}, {{.reaction_subjects}} reference in scope.
	if err := Validate(cfg, reg); err != nil {
		t.Fatalf("the pack's config doesn't validate: %v", err)
	}
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.sleep = fastSleep
	return rig, cfg, remote
}

func autopilotSpec(t *testing.T, cfg *config.Config, name string) config.TriggerSpec {
	t.Helper()
	for _, s := range cfg.Triggers {
		if s.Name == "autopilot/"+name {
			return s
		}
	}
	t.Fatalf("pack trigger %q not found", name)
	return config.TriggerSpec{}
}

func autopilotTrigger(kind string, ctx map[string]any) core.Trigger {
	if ctx == nil {
		ctx = map[string]any{}
	}
	ctx["me"] = map[string]any{"login": "octo-me"} // the source stamps it on every event
	for k, v := range map[string]any{"repo": "org/repo", "number": 7, "pr": 7} {
		if _, ok := ctx[k]; !ok {
			ctx[k] = v // …and its base facts, which its declared reads use
		}
	}
	return core.Trigger{Source: "github", Instance: "gh", Kind: kind, TargetTrusted: true,
		// The event saw an older head; the run must use the one it starts on.
		Target:  core.Target{Repo: "org/repo", Owner: "org", Name: "repo", Number: 7, PR: 7, HeadSHA: "event0000000"},
		Context: ctx}
}

func runAutopilot(rig *testRig, spec config.TriggerSpec, t core.Trigger) {
	rig.Runner.Run(context.Background(), emptyRun(), t, spec, rig.Runner.IndexOf(spec), nil, false)
}

func wantList(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s:\n  %s\nwant:\n  %s", what, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// A comment run that pushes: the done hook writes ONLY to the commit the run
// started on (after the slot wait — not the event's head), so the new head
// has no row of the context and it vanishes from the PR.
func TestAutopilotPackPushedRunLeavesNoRow(t *testing.T) {
	api := newGHAPI(t, "aaaaaaa1111") // the head when the run starts (event saw event0000000)
	rig, cfg := autopilotRig(t)
	spec := autopilotSpec(t, cfg, "on_new_comment")
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		st, re := api.take()
		wantList(t, "statuses before the agent ran", st, "aaaaaaa1111 pending octo-me / comment | replying to bob's comment")
		wantList(t, "reactions before the agent ran", re, "issue_comment 5 eyes")
		api.push("bbbbbbb2222")
		return dispatch.RunRef{Output: "{}", AgentID: "a1"}, nil
	}
	runAutopilot(rig, spec, autopilotTrigger("new_comment", map[string]any{
		"author": "bob", "reaction_subjects": []any{map[string]any{"kind": "issue_comment", "id": int64(5)}}}))
	st, re := api.take()
	wantList(t, "done statuses", st, "aaaaaaa1111 success octo-me / comment | pushed bbbbbbb")
	wantList(t, "done reactions", re, "issue_comment 5 rocket")
	for _, s := range st {
		if strings.HasPrefix(s, "bbbbbbb2222 ") {
			t.Fatalf("the done hook wrote to the NEW head: %q", s)
		}
	}
}

// No push: success (✓) on the unchanged head, 👍.
func TestAutopilotPackNoPush(t *testing.T) {
	api := newGHAPI(t, "aaaaaaa1111")
	rig, cfg := autopilotRig(t)
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{Output: "{}", AgentID: "a1"}, nil
	}
	runAutopilot(rig, autopilotSpec(t, cfg, "on_new_comment"), autopilotTrigger("new_comment", map[string]any{
		"author": "bob", "reaction_subjects": []any{map[string]any{"kind": "issue_comment", "id": int64(5)}}}))
	st, re := api.take()
	wantList(t, "statuses", st,
		"aaaaaaa1111 pending octo-me / comment | replying to bob's comment",
		"aaaaaaa1111 success octo-me / comment | done — no changes pushed")
	wantList(t, "reactions", re, "issue_comment 5 eyes", "issue_comment 5 +1")
}

// The incident: a dispatch that never came up (a worktree error) — 😕 and a
// failure on the PR's current head, within the run, no retry needed.
func TestAutopilotPackDispatchFailure(t *testing.T) {
	api := newGHAPI(t, "aaaaaaa1111")
	rig, cfg := autopilotRig(t)
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{}, dispatch.Unrecoverable(errors.New("gitwt: create worktree: exit status 128"))
	}
	runAutopilot(rig, autopilotSpec(t, cfg, "on_new_comment"), autopilotTrigger("new_comment", map[string]any{
		"author": "bob", "reaction_subjects": []any{map[string]any{"kind": "issue_comment", "id": int64(5)}}}))
	st, re := api.take()
	wantList(t, "statuses", st,
		"aaaaaaa1111 pending octo-me / comment | replying to bob's comment",
		"aaaaaaa1111 failure octo-me / comment | gave up: the agent couldn't be started")
	wantList(t, "reactions", re, "issue_comment 5 eyes", "issue_comment 5 confused")
}

// Two flows on one PR: two contexts, side by side — neither touches the
// other's row (conductor adds nothing to GitHub's per-(sha, context) rule).
// A subject-less flow (failing checks) gets a status and no reaction.
func TestAutopilotPackContextsCoexist(t *testing.T) {
	api := newGHAPI(t, "aaaaaaa1111")
	rig, cfg := autopilotRig(t)
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{Output: "{}", AgentID: "a1"}, nil
	}
	runAutopilot(rig, autopilotSpec(t, cfg, "on_failing_checks"), autopilotTrigger("failing_checks", map[string]any{"failing_check": "build"}))
	runAutopilot(rig, autopilotSpec(t, cfg, "on_merge_conflict"), autopilotTrigger("merge_conflict", nil))
	st, re := api.take()
	wantList(t, "statuses", st,
		"aaaaaaa1111 pending octo-me / ci-fix | fixing failing check build",
		"aaaaaaa1111 success octo-me / ci-fix | done — no changes pushed",
		"aaaaaaa1111 pending octo-me / conflict | resolving the merge conflict",
		"aaaaaaa1111 success octo-me / conflict | done — no changes pushed")
	wantList(t, "reactions (none: no subject)", re)
}

// The PR merges while the agent works: conductor stops the fixer, and the
// pack's `stop` hooks leave nothing behind — no pending row (the start
// commit's turns success, "stopped — the PR merged") and no 👀 (removed). No
// failure, no 😕.
func TestAutopilotPackClosedMidRunLeavesNothing(t *testing.T) {
	api := newGHAPI(t, "aaaaaaa1111")
	rig, cfg := autopilotRig(t)
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		api.mu.Lock()
		api.merged = true // merged under the agent; the engine stops the fixer
		api.mu.Unlock()
		return dispatch.RunRef{}, dispatch.ErrTargetClosed
	}
	runAutopilot(rig, autopilotSpec(t, cfg, "on_new_comment"), autopilotTrigger("new_comment", map[string]any{
		"author": "bob", "reaction_subjects": []any{map[string]any{"kind": "issue_comment", "id": int64(5)}}}))
	st, re := api.take()
	wantList(t, "statuses", st,
		"aaaaaaa1111 pending octo-me / comment | replying to bob's comment",
		"aaaaaaa1111 success octo-me / comment | stopped — the PR merged")
	wantList(t, "reactions", re, "issue_comment 5 eyes", "issue_comment 5 -eyes")
	api.mu.Lock()
	defer api.mu.Unlock()
	for row, state := range api.rows {
		if state == "pending" {
			t.Fatalf("row %q left pending on the closed PR", row)
		}
	}
	if n := len(api.on["issue_comment 5"]); n != 0 {
		t.Fatalf("%d reaction(s) left on the comment: %v", n, api.on["issue_comment 5"])
	}
}

// The live bug: on_changes_requested's `address` agent committed a fix,
// nothing pushed it, and `resolve` resolved the reviewer's threads on the
// agent's word. resolve now runs only when conductor reports it pushed the
// step's work; a commit left unpushed on a branch that did not move fails
// the run (the pack's unpushed step), the commit kept.
func TestAutopilotPackResolveOnlyWhenPushed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		commits   int // left in the agent's workspace, unpushed
		push      func() (map[string]any, error)
		wantFail  bool
		wantWrite []string
	}{
		{name: "pushed", push: func() (map[string]any, error) {
			return map[string]any{"pushed": true, "commits": 1, "up_to_date": false}, nil
		}, wantWrite: []string{"resolve_threads [11 12]", "rerequest_review [alice]"}},
		{name: "claims a fix, pushed nothing", push: func() (map[string]any, error) {
			return map[string]any{"pushed": false, "commits": 0, "up_to_date": true}, nil
		}, wantWrite: []string{"rerequest_review [alice]"}},
		{name: "committed, not pushed", commits: 1, push: func() (map[string]any, error) {
			return nil, fmt.Errorf("%w: conductor has no branch to push this workspace to", ErrNothingToPush)
		}, wantFail: true},
		{name: "unsandboxed", push: func() (map[string]any, error) { return nil, ErrUnsandboxed },
			wantWrite: []string{"rerequest_review [alice]"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			head := "aaaaaaa1111"
			rig, cfg, remote := autopilotRigRemote(t)
			ws := cloneWithCommits(t, tc.commits)
			if tc.commits > 0 {
				// The PR branch, where the workspace started: it did not move.
				gitFixture(t, ws, "push", "-q", remote, "origin/HEAD:refs/heads/pr-7")
				head = gitFixture(t, ws, "rev-parse", "origin/HEAD")
			}
			api := newGHAPI(t, head)
			var req dispatch.Request
			rig.Agents.dispatchFunc = func(_ context.Context, r dispatch.Request) (dispatch.RunRef, error) {
				req = r
				return dispatch.RunRef{Workdir: ws, Output: `{"addressed_comments":[11,12],"summary":"fixed"}`, AgentID: "a1"}, nil
			}
			rig.Runner.PushWork = func(context.Context, string) (map[string]any, error) { return tc.push() }
			runAutopilot(rig, autopilotSpec(t, cfg, "on_changes_requested"), autopilotTrigger("changes_requested", map[string]any{
				"author": "alice", "review_id": int64(9), "feedback_cursor": "c1"}))
			if !req.Step.ExpectPush || len(req.Step.OutputSchema) == 0 || dispatch.ReviewRole(req) {
				t.Fatalf("address must be an output_schema fixer (expect_push), got expect_push=%v review=%v", req.Step.ExpectPush, dispatch.ReviewRole(req))
			}
			failed, e := rig.workflowFailed()
			if failed != tc.wantFail {
				t.Fatalf("failed=%v (%s), want %v", failed, e, tc.wantFail)
			}
			api.mu.Lock()
			writes := api.writes
			api.mu.Unlock()
			wantList(t, "review writes", writes, tc.wantWrite...)
			if tc.wantFail {
				st, _ := api.take()
				if !strings.Contains(strings.Join(st, "\n"), `failure octo-me / review | gave up: step "unpushed" failed`) {
					t.Fatalf("the PR must show the failure: %v (%s)", st, e)
				}
				kept := gitFixture(t, filepath.Join(filepath.Dir(remote), "landing.git"), "rev-parse", "refs/kept/org/repo/pr-7/a1")
				if head := gitFixture(t, ws, "rev-parse", "HEAD"); kept != head {
					t.Fatalf("the unpushed commit is kept: %s, want %s", kept, head)
				}
			}
		})
	}
}

// movedHead is a PR whose branch moves while the pack's fixer works, real
// git end to end: a bare remote (the pack's push_remote), conductor's base
// clone, a linked worktree per dispatch checked out at the branch's head as
// of that dispatch (removed when its agent is archived), conductor's own
// end-of-step push (gitpush: fast-forward only, a moved branch refused),
// and the git fixture landing what the pack hands it.
type movedHead struct {
	t            *testing.T
	remote, base string
	mu           sync.Mutex
	ws, start    map[string]string // dispatch id -> workspace, the head it started at
	byAgent      map[string]string // agent id -> workspace
	archived     []string
	refuse       error // the remote refuses every push with this
}

func newMovedHead(t *testing.T, remote string) *movedHead {
	t.Helper()
	m := &movedHead{t: t, remote: remote, base: filepath.Join(t.TempDir(), "base"),
		ws: map[string]string{}, start: map[string]string{}, byAgent: map[string]string{}}
	gitFixture(t, filepath.Dir(m.base), "clone", "-q", remote, m.base)
	m.write(m.base, "app.txt", "one\n")
	gitFixture(t, m.base, "add", "app.txt")
	gitFixture(t, m.base, "commit", "-q", "-m", "init")
	gitFixture(t, m.base, "push", "-q", "origin", "HEAD:refs/heads/pr-7")
	return m
}

func (m *movedHead) write(dir, name, body string) {
	m.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		m.t.Fatal(err)
	}
}

// checkout is a dispatch's workspace at pr-7's head now.
func (m *movedHead) checkout(dispatchID, agentID string) string {
	gitFixture(m.t, m.base, "fetch", "-q", "origin", "+refs/heads/pr-7:refs/remotes/origin/pr-7")
	ws := filepath.Join(m.t.TempDir(), "ws")
	gitFixture(m.t, m.base, "worktree", "add", "-q", "--detach", ws, "origin/pr-7")
	gitFixture(m.t, ws, "checkout", "-q", "-B", "pr-7-"+agentID)
	m.mu.Lock()
	m.ws[dispatchID], m.start[dispatchID], m.byAgent[agentID] = ws, gitFixture(m.t, ws, "rev-parse", "HEAD"), ws
	m.mu.Unlock()
	return ws
}

// archive is the agent's release: its worktree goes with it.
func (m *movedHead) archive(agentID string) {
	m.mu.Lock()
	ws := m.byAgent[agentID]
	m.archived = append(m.archived, agentID)
	m.mu.Unlock()
	if ws != "" {
		_ = exec.Command("git", "-C", m.base, "worktree", "remove", "--force", ws).Run()
	}
}

// authorPushes moves pr-7 on the remote from another clone.
func (m *movedHead) authorPushes(name, body string) string {
	other := filepath.Join(m.t.TempDir(), "author")
	gitFixture(m.t, filepath.Dir(other), "clone", "-q", "-b", "pr-7", m.remote, other)
	m.write(other, name, body)
	gitFixture(m.t, other, "add", name)
	gitFixture(m.t, other, "commit", "-q", "-m", "author: "+name)
	gitFixture(m.t, other, "push", "-q", "origin", "pr-7")
	return gitFixture(m.t, other, "rev-parse", "HEAD")
}

// push is conductor's end-of-step push for a dispatch, as the daemon wires
// it (a refusal is a PushError naming where the workspace started).
func (m *movedHead) push(ctx context.Context, dispatchID string) (map[string]any, error) {
	m.mu.Lock()
	ws, start, refuse := m.ws[dispatchID], m.start[dispatchID], m.refuse
	m.mu.Unlock()
	headDir := gitFixture(m.t, ws, "rev-parse", "--absolute-git-dir")
	if refuse != nil {
		return nil, &PushError{Err: refuse, Since: start}
	}
	res, err := gitpush.Push(ctx, gitpush.Request{Workspace: ws, GitDir: filepath.Join(m.base, ".git"),
		HeadDir: headDir, Branch: "pr-7", Remote: m.remote})
	if err != nil {
		return nil, &PushError{Err: err, Since: start}
	}
	return map[string]any{"pushed": !res.UpToDate, "commits": res.Commits, "head": res.New, "up_to_date": res.UpToDate}, nil
}

// commit writes name=body in ws and commits it, as the fixer would.
func (m *movedHead) commit(ws, name, body string) string {
	m.write(ws, name, body)
	gitFixture(m.t, ws, "add", name)
	gitFixture(m.t, ws, "commit", "-q", "-m", "fixer: "+name)
	return gitFixture(m.t, ws, "rev-parse", "HEAD")
}

func (m *movedHead) tip() string { return gitFixture(m.t, m.remote, "rev-parse", "pr-7") }

// movedHeadRun runs on_changes_requested over a movedHead; attempts[i] is
// what the i-th dispatched agent does in its workspace.
func movedHeadRun(t *testing.T, attempts ...func(m *movedHead, ws, prompt string)) (*movedHead, *testRig, *ghAPI, []dispatch.Request) {
	t.Helper()
	rig, cfg, remote := autopilotRigRemote(t)
	m := newMovedHead(t, remote)
	api := newGHAPI(t, m.tip()) // the run starts on pr-7's head: where the fixer does
	var reqs []dispatch.Request
	rig.Agents.dispatchFunc = func(_ context.Context, r dispatch.Request) (dispatch.RunRef, error) {
		reqs = append(reqs, r)
		agent := fmt.Sprintf("a%d", len(reqs))
		ws := m.checkout(r.DispatchID, agent)
		if n := len(reqs); n <= len(attempts) {
			// The task as the agent reads it: rendered over the run's data
			// at dispatch, the step's own guidance included.
			prompt, err := dispatch.RenderPrompt(r)
			if err != nil {
				t.Fatalf("render the task: %v", err)
			}
			attempts[n-1](m, ws, prompt)
		}
		return dispatch.RunRef{Workdir: ws, Output: fmt.Sprintf(`{"addressed_comments":[%d],"summary":"fixed"}`, 10+len(reqs)), AgentID: agent}, nil
	}
	rig.Runner.Agents.Guidance = func(_ string, s config.Step, _ config.Policy) string {
		if s.Guidance == nil {
			return ""
		}
		return "\n\n" + strings.Join(s.Guidance.Parts, "\n\n")
	}
	rig.Runner.Agents.Archive = m.archive
	rig.Runner.PushWork = m.push
	runAutopilot(rig, autopilotSpec(t, cfg, "on_changes_requested"), autopilotTrigger("changes_requested", map[string]any{
		"author": "alice", "review_id": int64(9), "feedback_cursor": "c1"}))
	return m, rig, api, reqs
}

// landing is the git connector's own repository (autopilotRigRemote).
func (m *movedHead) landing() string { return filepath.Join(filepath.Dir(m.remote), "landing.git") }

// The PR branch moved while the fixer worked and its commit replays cleanly:
// conductor's push is refused (never forced), the pack keeps the commit,
// replays it onto the new head and pushes that — a fast-forward of the
// author's commit — with one dispatch. The threads it addressed resolve and
// the fixer's agent (with its workspace) is released when the run ends.
func TestAutopilotPackLandsOnAMovedHead(t *testing.T) {
	var theirs, agentHead string
	m, rig, api, reqs := movedHeadRun(t, func(m *movedHead, ws, _ string) {
		theirs = m.authorPushes("theirs.txt", "theirs\n")
		agentHead = m.commit(ws, "fix.txt", "fix\n")
	})
	if failed, e := rig.workflowFailed(); failed {
		t.Fatalf("the moved head lands: %s", e)
	}
	if len(reqs) != 1 {
		t.Fatalf("a clean replay needs no second agent: %d dispatches", len(reqs))
	}
	tip := m.tip()
	gitFixture(t, m.remote, "merge-base", "--is-ancestor", theirs, tip) // the author's push stands
	if got := gitFixture(t, m.remote, "rev-parse", tip+"^"); got != theirs {
		t.Fatalf("the replayed commit sits on the author's: %s", got)
	}
	if files := gitFixture(t, m.remote, "show", "--name-only", "--format=", tip); files != "fix.txt" {
		t.Fatalf("the replayed commit carries the fixer's change only: %q", files)
	}
	if got := gitFixture(t, m.landing(), "rev-parse", "refs/kept/org/repo/pr-7/a1"); got != agentHead {
		t.Fatalf("kept ref = %s, want the fixer's commit %s", got, agentHead)
	}
	api.mu.Lock()
	writes := api.writes
	api.mu.Unlock()
	wantList(t, "review writes", writes, "resolve_threads [11]", "rerequest_review [alice]")
	if strings.Join(m.archived, ",") != "a1" {
		t.Fatalf("the fixer's agent is released when the run ends: archived %v", m.archived)
	}
}

// conflictAttempt is a first fixer whose commit conflicts with what the
// author pushes meanwhile (both change app.txt).
func conflictAttempt(theirs, first *string) func(m *movedHead, ws, _ string) {
	return func(m *movedHead, ws, _ string) {
		*theirs = m.authorPushes("app.txt", "author's one\n")
		*first = m.commit(ws, "app.txt", "fixer's one\n")
	}
}

// The moved head conflicts with the fixer's commit: nothing is forced, the
// commit stays kept, and the fixer runs once more on the new head, its task
// naming the conflict, carrying the first attempt's diff and the kept
// commit. That second attempt resolves it and conductor's push lands; the
// threads it addressed resolve.
func TestAutopilotPackRerunsOnAConflictingMovedHead(t *testing.T) {
	var theirs, first string
	m, rig, api, reqs := movedHeadRun(t, conflictAttempt(&theirs, &first),
		func(m *movedHead, ws, prompt string) {
			for _, want := range []string{"NOTE: this is a second attempt", "it is now at " + theirs, "they conflict in [app.txt]",
				"+fixer's one", "git cat-file -e " + first} {
				if !strings.Contains(prompt, want) {
					t.Fatalf("the re-run's task lacks %q:\n%s", want, prompt)
				}
			}
			if got := gitFixture(t, ws, "rev-parse", "HEAD"); got != theirs {
				t.Fatalf("the re-run starts on the new head %s, not %s", theirs, got)
			}
			m.commit(ws, "app.txt", "author's one\nfixer's one\n")
		})
	if failed, e := rig.workflowFailed(); failed {
		t.Fatalf("the re-run lands the work: %s", e)
	}
	if len(reqs) != 2 || reqs[0].DispatchID == reqs[1].DispatchID {
		t.Fatalf("one re-run, its own dispatch: %d dispatches", len(reqs))
	}
	if strings.Contains(reqs[0].Action.Prompt, "second attempt") {
		t.Fatal("a first attempt carries no re-run note")
	}
	tip := m.tip()
	gitFixture(t, m.remote, "merge-base", "--is-ancestor", theirs, tip)
	if body := gitFixture(t, m.remote, "show", tip+":app.txt"); body != "author's one\nfixer's one" {
		t.Fatalf("app.txt on the branch = %q", body)
	}
	if got := gitFixture(t, m.landing(), "rev-parse", "refs/kept/org/repo/pr-7/a1"); got != first {
		t.Fatalf("the first attempt's commit stays kept: %s", got)
	}
	api.mu.Lock()
	writes := api.writes
	api.mu.Unlock()
	wantList(t, "review writes", writes, "resolve_threads [12]", "rerequest_review [alice]")
}

// The re-run is bounded to once: its own push is refused too (the branch
// moved again under it). The run fails as unpushed — no third dispatch,
// nothing forced, no thread resolved — and the first attempt's commit is
// still at its kept ref.
func TestAutopilotPackMovedHeadRerunIsBounded(t *testing.T) {
	var theirs, first, last string
	m, rig, api, reqs := movedHeadRun(t, conflictAttempt(&theirs, &first),
		func(m *movedHead, ws, _ string) {
			m.commit(ws, "app.txt", "author's one\nfixer's one\n")
			last = m.authorPushes("again.txt", "again\n")
		})
	failed, e := rig.workflowFailed()
	if !failed || !strings.Contains(e, "never pushed") {
		t.Fatalf("a re-run that still doesn't land fails the run: failed=%v %s", failed, e)
	}
	if len(reqs) != 2 {
		t.Fatalf("bounded to one re-run: %d dispatches", len(reqs))
	}
	if got := m.tip(); got != last {
		t.Fatalf("the remote is the author's last push %s, not %s: nothing forced", last, got)
	}
	if got := gitFixture(t, m.landing(), "rev-parse", "refs/kept/org/repo/pr-7/a1"); got != first {
		t.Fatalf("kept ref = %s, want %s", got, first)
	}
	api.mu.Lock()
	writes := api.writes
	api.mu.Unlock()
	if strings.Contains(strings.Join(writes, "\n"), "resolve_threads") {
		t.Fatalf("nothing landed, so no thread resolves: %v", writes)
	}
	st, _ := api.take()
	if !strings.Contains(strings.Join(st, "\n"), "failure octo-me / review | gave up: the change was never pushed") {
		t.Fatalf("the PR shows the failure: %v", st)
	}
}

// A push refused while the branch did NOT move (here the remote refuses
// the push outright) is no moved head: no re-run, the run fails, the commit
// kept.
func TestAutopilotPackRefusalWithoutAMoveFails(t *testing.T) {
	var head string
	m, rig, _, reqs := movedHeadRun(t, func(m *movedHead, ws, _ string) {
		head = m.commit(ws, "fix.txt", "fix\n")
		m.refuse = errors.New("the remote refused the push")
	})
	failed, e := rig.workflowFailed()
	if !failed || len(reqs) != 1 || !strings.Contains(e, "never pushed") {
		t.Fatalf("failed=%v %q after %d dispatches, want a failure after 1", failed, e, len(reqs))
	}
	if got := gitFixture(t, m.landing(), "rev-parse", "refs/kept/org/repo/pr-7/a1"); got != head {
		t.Fatalf("kept ref = %s, want %s", got, head)
	}
}
