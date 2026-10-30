package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/dashboard/auth"
)

// A plain retry is the session's own: it never repeats what a step already
// did. A forced retry re-runs steps that already succeeded, so with sign-in
// on it needs the run.replay step-up, bound to the run and the starting
// step, and described with the run and each step it replays.
func TestForceRetryNeedsAStepup(t *testing.T) {
	g := newBotRig(t, false)
	seedRuns(t, g.st)
	var calls []string
	g.s.deps.Retry = func(_ context.Context, id, from string, force bool) (string, error) {
		calls = append(calls, id+":"+from+":"+map[bool]string{true: "force", false: "plain"}[force])
		return "the new run is r-next", nil
	}
	path := "/api/v1/runs/r-ok/retry"
	if rr := g.dev.do("POST", path, map[string]any{"from": "review"}); rr.Code != http.StatusOK {
		t.Fatalf("a plain retry needs only the session: %d %s", rr.Code, rr.Body)
	}
	force := map[string]any{"from": "review", "force": true}
	rr := g.dev.do("POST", path, force)
	var refused struct {
		Stepup struct{ Purpose, Digest, Description string } `json:"stepup"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &refused)
	if rr.Code != http.StatusUnauthorized || refused.Stepup.Purpose != string(auth.PurposeRunReplay) {
		t.Fatalf("a forced retry without a step-up: %d %s", rr.Code, rr.Body)
	}
	for _, want := range []string{"re-run run r-ok", "from step review", "  - review", "  - post", "run again"} {
		if !strings.Contains(refused.Stepup.Description, want) {
			t.Fatalf("the description lacks %q:\n%s", want, refused.Stepup.Description)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("the refused forced retry ran: %v", calls)
	}
	tok := sshStepupAs(t, g.dev, g.devKey, auth.PurposeRunReplay, refused.Stepup.Digest)
	// Bound to the run and the starting step.
	if rr := g.dev.doWith("POST", path, map[string]any{"from": "post", "force": true}, tok); rr.Code != http.StatusUnauthorized {
		t.Fatalf("a step-up for another starting step: %d %s", rr.Code, rr.Body)
	}
	tok = sshStepupAs(t, g.dev, g.devKey, auth.PurposeRunReplay, refused.Stepup.Digest)
	if rr := g.dev.doWith("POST", path, force, tok); rr.Code != http.StatusOK {
		t.Fatalf("with the step-up: %d %s", rr.Code, rr.Body)
	}
	if len(calls) != 2 || calls[1] != "r-ok:review:force" {
		t.Fatalf("calls: %v", calls)
	}
}
