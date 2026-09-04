package flow

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/dispatch"
)

// botTrigger is a trigger whose event context carries the author facts the
// github source publishes.
func botTrigger(author string, isBot bool) core.Trigger {
	return newTrigger("new_comment", map[string]any{
		"msg": "x", "author": author, "author_is_bot": isBot,
	})
}

// TestReplyToBotsOffSkipsGithubReply (enforcement, mode off): with the
// trigger authored by a bot, a uses: gh.comment step (and hook) never reaches
// the GitHub API; a human-authored trigger dispatches it.
func TestReplyToBotsOffSkipsGithubReply(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(201)
		w.Write([]byte(`{"id": 7, "html_url": "u"}`))
	}))
	defer srv.Close()
	t.Setenv("PC_GITHUB_API_BASE", srv.URL)

	cfg := loadConfig(t, `
connectors:
  gh:
    type: github
    token: dummy
    identity: { write_token: w-token }
    webhook: { listen: "127.0.0.1:0", secret: s }
    repos: ["o/r"]
`)
	reg := buildRegistry(t, cfg)
	spec := mustSpec(t, `
on: gh.new_comment
policy: { reply_to_bots: off }
steps:
  - { id: note, uses: gh.comment, options: { repo: o/r, number: 1, body: "on it" } }
hooks:
  - { at: done, uses: gh.comment, options: { repo: o/r, number: 1, body: "done" } }
`)

	// Bot author: both the step and the hook are skipped structurally.
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, botTrigger("cursor[bot]", true), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("bot-authored trigger must not reach the API, got %d calls", n)
	}
	var skipped int
	for _, e := range rig.Store.audits {
		if e["outcome"] == "skipped_reply_to_bots" {
			skipped++
		}
	}
	if skipped != 2 {
		t.Fatalf("want the step and the hook audited as skipped, got %d", skipped)
	}

	// Human author: the verb dispatches.
	rig = newTestRunner(t, cfg, reg)
	runTrigger(rig, botTrigger("alice", false), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	if n := hits.Load(); n != 2 { // step + hook
		t.Fatalf("human-authored trigger should dispatch step+hook, got %d calls", n)
	}
}

// TestReplyToBotsDeclineOnlyGuidance (default mode): a bot-authored trigger
// injects the no-pleasantries guidance into the agent prompt; a human trigger
// and mode full do not.
func TestReplyToBotsDeclineOnlyGuidance(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { type: fake }
agents:
  fixer: { provider: claude }
`)
	reg := buildRegistry(t, cfg)
	newFakeState(t, "svc")
	base := `
on: svc.ping
steps:
  - { id: fix, type: agent, agent: fixer, prompt: "Handle the comment." }
`

	prompt := func(t *testing.T, specYAML string, trig core.Trigger) string {
		t.Helper()
		rig := newTestRunner(t, cfg, reg)
		runTrigger(rig, trig, mustSpec(t, specYAML))
		reqs := rig.Agents.requests()
		if len(reqs) != 1 {
			t.Fatalf("want 1 agent dispatch, got %d", len(reqs))
		}
		return reqs[0].Action.Prompt
	}

	// Default (no policy block) = decline_only: bot author gets the guidance.
	if p := prompt(t, base, botTrigger("cursor[bot]", true)); !strings.Contains(p, dispatch.BotReplyGuidance) {
		t.Error("bot author under the default mode should inject the bot guidance")
	}
	// Human author: no bot guidance.
	if p := prompt(t, base, botTrigger("alice", false)); strings.Contains(p, dispatch.BotReplyGuidance) {
		t.Error("human author must not get the bot guidance")
	}
	// Mode full: bot author, no guidance.
	full := strings.Replace(base, "steps:", "policy: { reply_to_bots: full }\nsteps:", 1)
	if p := prompt(t, full, botTrigger("cursor[bot]", true)); strings.Contains(p, dispatch.BotReplyGuidance) {
		t.Error("mode full must not inject the bot guidance")
	}
}
