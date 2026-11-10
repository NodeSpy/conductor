package dashboard

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/dashboard/auth"
)

// heldPlugins stands in for the daemon's install state: one plugin waiting
// for approval, cleared when approved.
type heldPlugins struct {
	held     []api.PluginHeld
	approved []string
	declined []string
}

func (h *heldPlugins) list() []api.PluginHeld { return h.held }

func (h *heldPlugins) approve(name, fp string) (string, error) {
	h.approved = append(h.approved, name+"@"+fp)
	h.held = nil
	return "approved what " + name + " may do", nil
}

func (h *heldPlugins) decline(name, fp string) (string, error) {
	h.declined = append(h.declined, name+"@"+fp)
	h.held = nil
	return "kept " + name, nil
}

func (h *heldPlugins) wire(s *Server) {
	s.deps.PluginHeld, s.deps.ApprovePlugin, s.deps.DeclinePlugin = h.list, h.approve, h.decline
}

func githubHeld() *heldPlugins {
	return &heldPlugins{held: []api.PluginHeld{{Name: "github", Running: "connectors/github/v1.4.0", Version: "connectors/github/v1.5.0",
		Fingerprint: "3f2a1b4c5d6e", Adds: []string{"connects to uploads.example.com:443"}, Removes: []string{"connects to old.example.com:443"}}}}
}

// The Plugins screen marks a plugin whose update waits for approval, with
// the running and held versions and what it adds and removes; approving it
// sends the update shown, approves exactly that and reloads the daemon so
// the held build runs. A changed update is refused, and with nothing waiting
// it is a no-op (404).
func TestPluginApprovalFromTheDashboard(t *testing.T) {
	s, _, reloaded := newEditServer(t, nil)
	h := githubHeld()
	h.wire(s)

	var plugins api.PluginsResponse
	json.Unmarshal(do(t, s, http.MethodGet, "/api/v1/plugins", nil).Body.Bytes(), &plugins)
	if len(plugins.Plugins) != 1 || plugins.Plugins[0].Held == nil || plugins.Plugins[0].Held.Adds[0] != "connects to uploads.example.com:443" ||
		plugins.Plugins[0].Held.Running != "connectors/github/v1.4.0" || plugins.Plugins[0].Held.Removes[0] != "connects to old.example.com:443" {
		t.Fatalf("plugins: %+v", plugins.Plugins)
	}

	if rr := do(t, s, http.MethodPost, "/api/v1/plugins/github/approve", api.ApprovePluginRequest{Fingerprint: "000000000000"}); rr.Code != http.StatusConflict {
		t.Fatalf("a declaration other than the one waiting: %d %s", rr.Code, rr.Body)
	}
	if rr := do(t, s, http.MethodPost, "/api/v1/plugins/slack/approve", api.ApprovePluginRequest{Fingerprint: "3f2a1b4c5d6e"}); rr.Code != http.StatusNotFound {
		t.Fatalf("a plugin with nothing waiting: %d %s", rr.Code, rr.Body)
	}
	if len(h.approved) != 0 || *reloaded {
		t.Fatalf("refused approvals approved: %v", h.approved)
	}
	rr := do(t, s, http.MethodPost, "/api/v1/plugins/github/approve", api.ApprovePluginRequest{Fingerprint: "3f2a1b4c5d6e"})
	if rr.Code != http.StatusOK || len(h.approved) != 1 || h.approved[0] != "github@3f2a1b4c5d6e" || !*reloaded {
		t.Fatalf("approve: %d %s, approved %v, reloaded %v", rr.Code, rr.Body, h.approved, *reloaded)
	}

	plugins = api.PluginsResponse{}
	json.Unmarshal(do(t, s, http.MethodGet, "/api/v1/plugins", nil).Body.Bytes(), &plugins)
	if plugins.Plugins[0].Held != nil {
		t.Fatalf("approved, nothing waits: %+v", plugins.Plugins[0])
	}
	*reloaded = false
	if rr := do(t, s, http.MethodPost, "/api/v1/plugins/github/approve", api.ApprovePluginRequest{Fingerprint: "3f2a1b4c5d6e"}); rr.Code != http.StatusNotFound || len(h.approved) != 1 || *reloaded {
		t.Fatalf("approved again with nothing waiting: %d %s, approved %v, reloaded %v", rr.Code, rr.Body, h.approved, *reloaded)
	}
}

// Declining keeps the running build: it records the decline for the update
// shown, and nothing is reloaded. Nothing waiting: a no-op (404).
func TestPluginDeclineFromTheDashboard(t *testing.T) {
	s, _, reloaded := newEditServer(t, nil)
	h := githubHeld()
	h.wire(s)
	if rr := do(t, s, http.MethodPost, "/api/v1/plugins/github/decline", api.ApprovePluginRequest{Fingerprint: "000000000000"}); rr.Code != http.StatusConflict || len(h.declined) != 0 {
		t.Fatalf("an update other than the one waiting: %d %s", rr.Code, rr.Body)
	}
	rr := do(t, s, http.MethodPost, "/api/v1/plugins/github/decline", api.ApprovePluginRequest{Fingerprint: "3f2a1b4c5d6e"})
	if rr.Code != http.StatusOK || len(h.declined) != 1 || len(h.approved) != 0 || *reloaded {
		t.Fatalf("decline: %d %s, declined %v, approved %v, reloaded %v", rr.Code, rr.Body, h.declined, h.approved, *reloaded)
	}
	if rr := do(t, s, http.MethodPost, "/api/v1/plugins/github/decline", api.ApprovePluginRequest{Fingerprint: "3f2a1b4c5d6e"}); rr.Code != http.StatusNotFound {
		t.Fatalf("nothing waiting: %d %s", rr.Code, rr.Body)
	}
}

// With sign-in on, approving a held update needs the plugin.approve step-up,
// bound to the plugin, the versions and the declaration, and described with
// what it adds and removes.
func TestPluginApprovalNeedsAStepup(t *testing.T) {
	g := newBotRig(t, false)
	h := githubHeld()
	h.wire(g.s)
	path := "/api/v1/plugins/github/approve"
	body := api.ApprovePluginRequest{Fingerprint: "3f2a1b4c5d6e"}
	rr := g.dev.do("POST", path, body)
	var refused struct {
		Stepup struct{ Purpose, Digest, Description string } `json:"stepup"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &refused)
	if rr.Code != http.StatusUnauthorized || refused.Stepup.Purpose != string(auth.PurposePluginApprove) {
		t.Fatalf("without a step-up: %d %s", rr.Code, rr.Body)
	}
	if d := refused.Stepup.Description; !strings.Contains(d, "uploads.example.com:443") || !strings.Contains(d, "old.example.com:443") || !strings.Contains(d, "v1.4.0 with connectors/github/v1.5.0") {
		t.Fatalf("the description lacks what it adds: %s", refused.Stepup.Description)
	}
	if len(h.approved) != 0 {
		t.Fatalf("approved without a step-up: %v", h.approved)
	}
	tok := sshStepupAs(t, g.dev, g.devKey, auth.PurposePluginApprove, refused.Stepup.Digest)
	if rr := g.dev.doWith("POST", path, body, tok); rr.Code != http.StatusOK || len(h.approved) != 1 {
		t.Fatalf("with the step-up: %d %s (%v)", rr.Code, rr.Body, h.approved)
	}
}
