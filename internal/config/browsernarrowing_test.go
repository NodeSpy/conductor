package config

import (
	"strings"
	"testing"
)

// TestBrowserNarrowingWarning (M2): a bot's or team's browser: narrowing that
// names a login site or credential not defined under the top-level
// browser.logins[] / browser.credentials[] must produce a load-time warning. At
// runtime the narrowing only caps (ForPrincipal intersects it), so a typo'd
// entry silently leaves the session no login/credential for it — previously with
// no sign at load or `conductor validate` (checkTeamCeilings only checks the
// bot<=team ceiling, never existence against the real definitions).
func TestBrowserNarrowingWarning(t *testing.T) {
	cfg := &Config{
		Browser: &BrowserConfig{
			Logins:      []BrowserLogin{{Site: "status.example.com", Identities: []string{"team:platform"}}, {Site: "*.corp.example.com", Identities: []string{"team:platform"}}},
			Credentials: []BrowserCredential{{Name: "app", Origin: "https://app.example.com", Identities: []string{"team:platform"}}},
		},
		Bots: map[string]BotConfig{
			// A typo'd login site and a typo'd credential: both flagged.
			"intern": {Team: "platform", Browser: &BotBrowser{Logins: []string{"staus.example.com"}, Credentials: []string{"ap"}}},
			// A real exact site and the exact wildcard login, plus a real
			// credential: none flagged. (A subdomain under a *.wildcard login
			// would grant nothing at runtime — siteWithn(narrowing, realSite) —
			// so the narrowing entry must equal or be broader than a real site.)
			"lead": {Team: "platform", Browser: &BotBrowser{Logins: []string{"status.example.com", "*.corp.example.com"}, Credentials: []string{"app"}}},
		},
		Teams: map[string]TeamConfig{
			"platform": {Browser: &BotBrowser{Logins: []string{"wrong.example.com"}, Credentials: []string{"app"}}},
		},
	}
	w := strings.Join(cfg.BotWarnings(), "\n")

	if !strings.Contains(w, `bots.intern.browser.logins: "staus.example.com"`) {
		t.Fatalf("M2: a typo'd bot login site was not flagged: %s", w)
	}
	if !strings.Contains(w, `bots.intern.browser.credentials: "ap"`) {
		t.Fatalf("M2: a typo'd bot credential was not flagged: %s", w)
	}
	if !strings.Contains(w, `teams.platform.browser.logins: "wrong.example.com"`) {
		t.Fatalf("M2: a typo'd team login site was not flagged: %s", w)
	}
	// Real entries (exact site, exact wildcard, real credential) are not flagged.
	for _, ok := range []string{`"status.example.com" matches no site`, `"*.corp.example.com" matches no site`, `bots.lead.browser.credentials: "app"`, `teams.platform.browser.credentials: "app"`} {
		if strings.Contains(w, ok) {
			t.Fatalf("M2: a valid narrowing entry was flagged (%q): %s", ok, w)
		}
	}

	// With no top-level browser: block at all, every narrowing entry is flagged
	// as silently leaving the session nothing.
	none := &Config{Bots: map[string]BotConfig{
		"m": {Browser: &BotBrowser{Logins: []string{"status.example.com"}, Credentials: []string{"app"}}},
	}}
	nw := strings.Join(none.BotWarnings(), "\n")
	if !strings.Contains(nw, "no browser.logins are configured") || !strings.Contains(nw, "no browser.credentials are configured") {
		t.Fatalf("M2: a narrowing with no browser: block was not flagged: %s", nw)
	}

	// A bot with no browser: block emits no narrowing note.
	clean := &Config{
		Browser: &BrowserConfig{Logins: []BrowserLogin{{Site: "status.example.com", Identities: []string{"team:platform"}}}},
		Bots:    map[string]BotConfig{"m": {Team: "platform"}},
	}
	if got := strings.Join(clean.BotWarnings(), "\n"); strings.Contains(got, ".browser.logins:") {
		t.Fatalf("M2: a bot with no browser narrowing was flagged: %s", got)
	}
}
