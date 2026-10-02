package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// The pr-autopilot pack's progress hooks, end to end: testdata/packs/
// pr-autopilot is a copy of the published pack (conductor-packs, keep in
// sync), instantiated through the real pack loader (settings substitution,
// connector binding, namespacing) and run against the real github connector
// over a fake GitHub API. What it pins is the pack's contract with the PR:
//
//   - start: 👀 on the subject, `pending` on the commit the run starts on;
//   - done: 🚀/👍, and `success` ONLY on that start commit — never the new
//     head, so after a push the PR shows no row of the context at all;
//   - fail: 😕, and `failure` on the PR's current head.

// ghAPI is a fake GitHub API recording reactions and statuses.
type ghAPI struct {
	mu       sync.Mutex
	head     string
	statuses []string // "<sha> <state> <context> | <description>"
	reacts   []string // "<kind> <id> <content>"
}

var (
	reIssueReact = regexp.MustCompile(`^/repos/org/repo/issues/comments/(\d+)/reactions$`)
	reStatus     = regexp.MustCompile(`^/repos/org/repo/statuses/(\w+)$`)
)

func newGHAPI(t *testing.T, head string) *ghAPI {
	api := &ghAPI{head: head}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch p := r.URL.Path; {
		case r.Method == http.MethodGet && p == "/repos/org/repo/pulls/7":
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "open", "head": map[string]any{"sha": api.head}})
		case r.Method == http.MethodGet && p == "/user":
			_ = json.NewEncoder(w).Encode(map[string]any{"login": "octo-me"})
		case r.Method == http.MethodPost && reIssueReact.MatchString(p):
			api.reacts = append(api.reacts, fmt.Sprintf("issue_comment %s %v", reIssueReact.FindStringSubmatch(p)[1], body["content"]))
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && reStatus.MatchString(p):
			api.statuses = append(api.statuses, fmt.Sprintf("%s %v %v | %v", reStatus.FindStringSubmatch(p)[1], body["state"], body["context"], body["description"]))
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected API call %s %s", r.Method, p)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("PC_GITHUB_API_BASE", srv.URL)
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
	dir := t.TempDir()
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
packs:
  autopilot:
    source: ./pr-autopilot
    connectors: { github: gh }
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
	return newTestRunner(t, cfg, reg), cfg
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
	return core.Trigger{Source: "github", Instance: "gh", Kind: kind, TargetTrusted: true,
		// The event saw an older head; the run must use the one it starts on.
		Target: core.Target{Repo: "org/repo", Owner: "org", Name: "repo", Number: 7, PR: 7, HeadSHA: "event0000000"},
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
