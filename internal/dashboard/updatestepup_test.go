package dashboard

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/dashboard/auth"
)

// Installing (or rolling back) the running binary changes what code the
// daemon runs for everything on it — the same weight as a config apply, so
// it needs the same step-up, bound to the exact version, and only a version
// ListReleases actually published may be named. `force` is not an accepted
// field at all: the preflight the updater runs is never skippable from the
// dashboard, unlike the CLI's own --force.
func TestUpdateInstallNeedsAStepup(t *testing.T) {
	g := newBotRig(t, false)
	var calls []string
	g.s.deps.ListReleases = func() ([]string, error) { return []string{"v0.70", "v0.71"}, nil }
	g.s.deps.InstallUpdate = func(tag string) (string, error) {
		calls = append(calls, tag)
		return tag, nil
	}
	path := "/api/v1/system/update/install"

	// Unknown version: never reaches the step-up or the installer.
	if rr := g.dev.do("POST", path, map[string]any{"version": "v9.9.9"}); rr.Code != http.StatusBadRequest {
		t.Fatalf("an unpublished version: %d %s", rr.Code, rr.Body)
	}
	if len(calls) != 0 {
		t.Fatalf("an unpublished version installed: %v", calls)
	}

	// force is not a field the handler reads at all — it's silently ignored,
	// never a way to skip the preflight or the step-up below.
	rr := g.dev.do("POST", path, map[string]any{"version": "v0.70", "force": true})
	var refused struct {
		Stepup struct{ Purpose, Digest, Description string } `json:"stepup"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &refused)
	if rr.Code != http.StatusForbidden || refused.Stepup.Purpose != string(auth.PurposeUpdateNow) {
		t.Fatalf("install without a step-up (force=true in the body): %d %s", rr.Code, rr.Body)
	}
	if len(calls) != 0 {
		t.Fatalf("the refused install ran: %v", calls)
	}
	if !strings.Contains(refused.Stepup.Description, "v0.70") {
		t.Fatalf("the description doesn't name the version: %q", refused.Stepup.Description)
	}

	// Bound to the exact version: a token confirmed for v0.71 doesn't cover v0.70.
	rr = g.dev.do("POST", path, map[string]any{"version": "v0.71"})
	var refusedOther struct {
		Stepup struct{ Digest string } `json:"stepup"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &refusedOther)
	otherTok := sshStepupAs(t, g.dev, g.devKey, auth.PurposeUpdateNow, refusedOther.Stepup.Digest)
	if rr := g.dev.doWith("POST", path, map[string]any{"version": "v0.70"}, otherTok); rr.Code != http.StatusForbidden {
		t.Fatalf("a step-up confirmed for a different version: %d %s", rr.Code, rr.Body)
	}
	if len(calls) != 0 {
		t.Fatalf("install ran with a mismatched step-up: %v", calls)
	}

	tok := sshStepupAs(t, g.dev, g.devKey, auth.PurposeUpdateNow, refused.Stepup.Digest)
	if rr := g.dev.doWith("POST", path, map[string]any{"version": "v0.70"}, tok); rr.Code != http.StatusOK {
		t.Fatalf("install with its own step-up: %d %s", rr.Code, rr.Body)
	}
	if len(calls) != 1 || calls[0] != "v0.70" {
		t.Fatalf("calls: %v", calls)
	}

	// An audit entry was written: who, which version, when.
	entries := mustRead(t, g.auditLog)
	if !strings.Contains(entries, `"event":"dashboard.update.install"`) || !strings.Contains(entries, `"version":"v0.70"`) {
		t.Fatalf("no audit entry for the install:\n%s", entries)
	}
}

// An unauthenticated request is rejected for an unpublished version before
// it ever gets to the sign-in check — the version allowlist applies
// regardless of who's asking.
func TestUpdateInstallUnknownVersionRejectedUnauthenticated(t *testing.T) {
	g := newBotRig(t, false)
	g.s.deps.ListReleases = func() ([]string, error) { return []string{"v0.70"}, nil }
	g.s.deps.InstallUpdate = func(tag string) (string, error) { return tag, nil }
	rr := do(t, g.s, "POST", "/api/v1/system/update/install", map[string]any{"version": "v9.9.9"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("an unpublished version, unauthenticated: %d %s", rr.Code, rr.Body)
	}
}
