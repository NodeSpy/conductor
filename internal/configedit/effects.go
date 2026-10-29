package configedit

import (
	"errors"
	"fmt"
	"strings"
)

// ErrStale is returned by Apply when the confirm token does not bind to the
// candidate being applied — the staged set changed since Review, so the
// operator must review again.
var ErrStale = errors.New("configedit: the review is stale (the staged changes changed since it was shown); review again")

// describe renders one change in words and reports whether it loosens a
// guardrail (a "widening" the review surfaces prominently). cur is the current
// value at the path, for a before→after phrasing.
func describe(c Change, cur any) (text string, widens bool) {
	path := pathKey(c.Path)
	if c.Delete {
		return fmt.Sprintf("removes %s", path), wideningDelete(c.Path)
	}
	after := fmt.Sprintf("%v", c.Value)
	if cur == nil {
		text = fmt.Sprintf("sets %s to %s", path, after)
	} else {
		text = fmt.Sprintf("changes %s from %v to %s", path, cur, after)
	}
	return text, isWidening(c.Path, cur, c.Value)
}

// isWidening encodes conductor's least-privilege guardrails: the handful of
// edits that loosen the security posture, named so the review can lead with
// them (strong-defaults-optin-footguns). The list mirrors the prototype's
// widening classifier (store.js), scoped to the scalar edits the dashboard
// stages.
func isWidening(path []string, before, after any) bool {
	p := pathKey(path)
	last := ""
	if len(path) > 0 {
		last = path[len(path)-1]
	}
	truthy := func(v any) bool {
		switch x := v.(type) {
		case bool:
			return x
		case string:
			return x == "true" || x == "yes" || x == "on"
		}
		return false
	}
	switch {
	// isolation mode set to none, anywhere in the tree (steps, packs, runtimes).
	case last == "mode" && contains(path, "isolation") && fmt.Sprintf("%v", after) == "none":
		return true
	// the agent sandbox turned off.
	case p == "sandbox.agents" && fmt.Sprintf("%v", after) == "off":
		return true
	// egress widened to a wildcard.
	case contains(path, "egress") && (fmt.Sprintf("%v", after) == "*" || strings.HasPrefix(fmt.Sprintf("%v", after), "*.")):
		return true
	// a trigger made callable.
	case last == "callable" && truthy(after) && !truthy(before):
		return true
	// a browser identity's evaluate turned on.
	case last == "evaluate" && contains(path, "browser") && fmt.Sprintf("%v", after) != "off" && fmt.Sprintf("%v", before) != fmt.Sprintf("%v", after):
		return true
	// unattended autonomy switched on.
	case last == "unattended" && truthy(after) && !truthy(before):
		return true
		// the dashboard exposed beyond localhost would be a widening, but listen is
		// a list, not a scalar — the dashboard stages that as a section add.
	// dashboard sign-in loosened: a password allowed without TOTP, plain HTTP
	// acknowledged on a dashboard listener, or the tailnet listener's TLS off.
	case p == "dashboard.auth.password_requires_totp" && !truthy(after) && fmt.Sprintf("%v", after) != "":
		return true
	case contains(path, "dashboard") && last == "allow_plain_http" && truthy(after) && !truthy(before):
		return true
	case p == "dashboard.tailscale.tls" && (fmt.Sprintf("%v", after) == "off" || fmt.Sprintf("%v", after) == "false"):
		return true
	}
	return false
}

// wideningDelete flags deletes that remove a guardrail (e.g. deleting an
// isolation block that pinned a sandbox).
func wideningDelete(path []string) bool {
	return contains(path, "isolation") || contains(path, "sandbox")
}

func contains(path []string, seg string) bool {
	for _, s := range path {
		if s == seg {
			return true
		}
	}
	return false
}
