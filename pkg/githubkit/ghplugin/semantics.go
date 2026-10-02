package ghplugin

import (
	"encoding/json"

	"github.com/NodeSpy/conductor/pkg/plugin"
)

// What the ENGINE does with each github event, declared in the contract's
// generic terms (docs/design/plugin-contract.md §4.1). Conductor implements
// each semantic once for every plugin; nothing in it knows these names.

// prTarget is the semantics every PR-shaped event shares: the PR as the
// target, its head as the revision, the author and labels facts, and what
// is never shown to an agent.
func prTarget() plugin.EventSemantics {
	return plugin.EventSemantics{
		Target: &plugin.TargetSemantics{
			Key: "{{.repo}}#{{.number}}", URL: "url", Label: "pull request",
			Assigned: json.RawMessage(`true`),
			Scope:    []plugin.ScopeFact{{Dimension: "repo", Fact: "repo"}},
		},
		Revision: &plugin.RevisionSemantics{Fact: "head", Branch: "head_ref", Base: "base"},
		Author:   &plugin.AuthorSemantics{Login: "author", Automated: "author_is_bot"},
		Labels:   "labels",
		Private:  []string{"installation_id", "reaction_subjects"},
		Secret:   []string{"app_token", "gh_token"},
	}
}

func level(rearm bool) *plugin.Completion {
	l := &plugin.LevelCompletion{}
	if rearm {
		l.RearmOn = "revision"
	}
	return &plugin.Completion{Level: l}
}

// EventSemantics is the declared semantics of the github event name — the
// same declaration whether the bundled connector or the plugin emits it.
func EventSemantics(name string) *plugin.EventSemantics { return eventSemantics(name) }

func eventSemantics(name string) *plugin.EventSemantics {
	s := prTarget()
	switch name {
	case "review_requested":
		s.Priority = "interactive"
		s.Completion = level(true)
	case "changes_requested":
		s.BoundToTarget, s.Feedback = true, true
		s.Completion = level(false)
		// Its own cursor namespace: the review it folds is dispatched for
		// different comments than new_comment's, and a shared mark would let
		// one starve the other.
		s.Cursor = &plugin.CursorSemantics{ID: "comment_id", Stream: "changes_requested:{{.comment_kind}}"}
	case "new_comment":
		s.BoundToTarget, s.Feedback = true, true
		s.Attempts = &plugin.AttemptsSemantics{Cap: "none"}
		s.Cursor = &plugin.CursorSemantics{ID: "comment_id", Stream: "{{.comment_kind}}"}
	case "merge_conflict":
		s.BoundToTarget = true
		s.Completion = level(false)
	case "pr_behind":
		s.BoundToTarget = true
	case "failing_checks":
		s.BoundToTarget = true
		s.VerificationFailed = true
		s.Remediate = &plugin.RemediateSemantics{
			Option: "flaky_rerun", Run: "run_id", Budget: 1,
			Status: plugin.RemediateCheck{Verb: "get_run", DoneWhen: "status == 'completed'"},
			Action: plugin.RemediateVerb{Verb: "rerun_run"},
		}
	case ClosedEvent:
		s.ClosesTarget = &plugin.ClosesTargetSemantics{
			Outcome: &plugin.OutcomeMap{Fact: "merged", True: "accepted", False: "rejected"},
			Reverts: &plugin.RevertsFact{Fact: "reverts", Corroborated: "reverts_corroborated"},
		}
	case "merge_ready", "self_review", "stuck_checks":
		// the PR target, nothing more
	case "issue_matched":
		s.Target.Label = "issue"
		s.Revision = nil
	default:
		// release, deployment_status, alerts: repo-level events whose target
		// the source assigns, with no revision of their own.
		s.Target.Key = "{{.repo}}"
		s.Target.Label = "repository"
		s.Revision = nil
	}
	return &s
}

// ClosedEvent is the event a PR's close emits: terminal for its target.
const ClosedEvent = "_closed"
