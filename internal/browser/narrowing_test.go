package browser

import "testing"

// TestBotBrowserNarrowingEnforced (C1): a bot whose bots.<name>.browser list
// narrows its team's logins/credentials must reach only the entries it kept,
// even though a team: binding on the broader entry would otherwise match. The
// narrowing is validated at config load but has to bind at access time too.
func TestBotBrowserNarrowingEnforced(t *testing.T) {
	// Team "platform" owns two credentials; the team binding is on both.
	app := Credential{Name: "app", Identities: []string{"team:platform"}}
	sensitive := Credential{Name: "sensitive", Identities: []string{"team:platform"}}

	// Bot "intern" (team platform) narrowed browser.credentials to [app].
	intern := Principal{Identity: "agent:intern@o/r", Groups: []string{"bot:intern", "team:platform"}, BotCredentials: []string{"app"}}
	if !app.ForPrincipal(intern) {
		t.Fatal("the narrowed bot must still use the credential it kept (app)")
	}
	if sensitive.ForPrincipal(intern) {
		t.Fatal("C1: a bot narrowed to [app] must NOT receive its team's 'sensitive' credential")
	}

	// A bot with no narrowing keeps the whole team set.
	full := Principal{Identity: "agent:lead@o/r", Groups: []string{"bot:lead", "team:platform"}}
	if !app.ForPrincipal(full) || !sensitive.ForPrincipal(full) {
		t.Fatal("an un-narrowed bot keeps the whole team set")
	}

	// Logins narrow by site, including a "*.domain" entry covering a host.
	status := LoginSite{Site: "status.example.com", Identities: []string{"team:platform"}}
	billing := LoginSite{Site: "billing.example.com", Identities: []string{"team:platform"}}
	under := LoginSite{Site: "app.corp.example.com", Identities: []string{"team:platform"}}
	narrowed := Principal{Identity: "agent:intern@o/r", Groups: []string{"bot:intern", "team:platform"},
		BotLogins: []string{"status.example.com", "*.corp.example.com"}}
	if !status.ForPrincipal(narrowed) {
		t.Fatal("the narrowed bot keeps status.example.com")
	}
	if !under.ForPrincipal(narrowed) {
		t.Fatal("a *.corp.example.com narrowing covers a host under it")
	}
	if billing.ForPrincipal(narrowed) {
		t.Fatal("C1: a bot narrowed away from billing.example.com must NOT reach it")
	}

	// An empty (non-nil) narrowing list caps the bot to nothing.
	none := Principal{Identity: "agent:intern@o/r", Groups: []string{"bot:intern", "team:platform"}, BotCredentials: []string{}}
	if app.ForPrincipal(none) {
		t.Fatal("C1: an empty credential narrowing must reach no credential")
	}
}
