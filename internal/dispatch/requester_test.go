package dispatch

import (
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/skill"
)

// TestSkillIdentityRequester: a dispatch's token says who asked — the
// operator only for a trigger they fired themselves.
func TestSkillIdentityRequester(t *testing.T) {
	id, ok := SkillIdentityFor(Request{Trigger: core.Trigger{Operator: true}})
	if !ok || id.Requester != skill.RequesterOperator {
		t.Fatalf("operator run: %q", id.Requester)
	}
	id, _ = SkillIdentityFor(Request{Trigger: core.Trigger{Source: "manual", Force: true}})
	if id.Requester != skill.RequesterEvent {
		t.Fatalf("a manual-looking trigger without the operator flag: %q", id.Requester)
	}
	id, _ = SkillIdentityFor(Request{Trigger: core.Trigger{Kind: "review_requested", TargetTrusted: true}})
	if id.Requester != skill.RequesterEvent {
		t.Fatalf("an event: %q", id.Requester)
	}
}
