package dashboard

import (
	"net/http"
	"strings"
	"testing"
)

// A bot's memory and a team's knowledge are written only by their operators:
// dev may not write ops' bot or team, whichever operator the body names; any
// operator reads them.
func TestBotAPIRepoWritesNeedAnOperatorOfTheTarget(t *testing.T) {
	g := newBotRig(t, false)
	for _, c := range []struct{ path, want string }{
		{"/api/v1/bots/sre/memory/edit", "dev is not an operator of bot:sre"},
		{"/api/v1/bots/sre/memory/new", "dev is not an operator of bot:sre"},
		{"/api/v1/bots/sre/memory/consolidate", "dev is not an operator of bot:sre"},
		{"/api/v1/teams/platform/knowledge/edit", "dev is not an operator of team:platform"},
		{"/api/v1/teams/platform/knowledge/new", "dev is not an operator of team:platform"},
	} {
		if rr := g.dev.do("POST", c.path, map[string]any{"op": "rm", "path": "knowledge/x.md", "base": "abcdef1"}); rr.Code != http.StatusForbidden ||
			!strings.Contains(rr.Body.String(), c.want) {
			t.Fatalf("dev on %s: %d %s", c.path, rr.Code, rr.Body)
		}
		if rr := g.dev.do("POST", c.path, map[string]any{"operator": "ops", "op": "rm"}); rr.Code != http.StatusForbidden ||
			!strings.Contains(rr.Body.String(), "can't act as ops") {
			t.Fatalf("dev naming ops on %s: %d %s", c.path, rr.Code, rr.Body)
		}
	}
	if rr := g.dev.do("GET", "/api/v1/bots/sre/memory", nil); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"target":"bot:sre"`) {
		t.Fatalf("dev reading sre's memory: %d %s", rr.Code, rr.Body)
	}
	// The owner's write reaches the handler (a stale base is the handler's
	// answer, not the gate's).
	if rr := g.ops.do("POST", "/api/v1/bots/sre/memory/edit", map[string]any{"op": "rm", "path": "knowledge/x.md", "base": "abcdef1"}); rr.Code != http.StatusConflict {
		t.Fatalf("ops on sre's memory: %d %s", rr.Code, rr.Body)
	}
}

// Approving with changes is bound to the exact text: a step-up for one text
// is a different digest from another's, and from a plain approval's.
func TestApprovalEditDigestBindsTheText(t *testing.T) {
	a := approvalEditDigest("sre", "p1", "sha", "one")
	if a == approvalEditDigest("sre", "p1", "sha", "two") || a == approvalDigest("sre", "p1", "sha", "approve") ||
		a != approvalEditDigest("sre", "p1", "sha", "one") {
		t.Fatal("the approve-with-changes digest doesn't bind the edited text")
	}
}
