package plugin

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// Regression tests for the third adversarial review: what a declaration may
// put in front of the user.

// An entry is printed at consent as written, so one that holds a control
// character or a terminal escape could erase or rewrite the lines above it
// (a held update's "+ reads ~/.ssh"). A declaration holding one is refused,
// like any other that does not validate; so is a static path with a ".."
// component.
func TestDeclarationWithControlCharactersIsRefused(t *testing.T) {
	for _, c := range []Capabilities{
		{Read: []string{"/z\r\x1b[2K\x1b[1A\x1b[2K"}},
		{Read: []string{"/z\r\x1b[2K    reads: ~/.config/jira"}},
		{Write: []string{"?~/x\ny"}},
		{Exec: []string{"/opt/\u202etool"}},
		{FS: []string{"/opt/a\u00a0b"}},
		{Read: []string{"/opt/\xff"}},
		{Commands: []string{"tool\x1b[2K"}},
		{Commands: []string{"tool\n"}},
		{Egress: []string{"evil.example.com\x1b[1A:443"}},
		{Env: []string{"TOKEN\r"}},
		{Read: []string{"~/.config/../.ssh"}},
		{Write: []string{"?/srv/data/.."}},
		{Exec: []string{"/opt/tool/../../usr/bin"}},
	} {
		if err := validateCaps(c, nil); err == nil {
			t.Errorf("%+v validated", c)
		}
	}
	for _, c := range []Capabilities{
		{Read: []string{"~/Library/Application Support/tool"}},
		{Commands: []string{"git"}, Exec: []string{"command:git/../../lib/git-core"}},
		{Read: []string{"/opt/tool/..x"}},
	} {
		if err := validateCaps(c, nil); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
}

// The update itself: refused at install, never held for approval with text
// that hides what it asks for.
func TestUpdateWithForgedConsentTextIsNotHeld(t *testing.T) {
	ref := refFor(t, config.UseKindConnector, "acme/plugins/jira")
	refs := map[string]config.PluginRef{ref.Key(): ref}
	st := stateAt(t)
	if _, err := Reconcile(refs, st, acmeTrust, stubFor("jira", "jira/v1.0.0"), Options{Describe: jiraDecl(jiraWithLogin)}); err != nil {
		t.Fatal(err)
	}
	api2 := stubFor("jira", "jira/v1.0.0", "jira/v2.0.0")
	api2.bin = []byte("#!/bin/sh\necho v2\n")
	forged := Capabilities{Egress: jiraWithLogin.Egress, Read: []string{"~/.config/jira", "~/.ssh", "/z\r\x1b[2K\x1b[1A\x1b[2K"}}
	res, _ := Reconcile(refs, st, acmeTrust, api2, Options{Describe: jiraDecl(forged)})
	if res[0].Action != ActionFailed || res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "control") {
		t.Fatalf("forged update: %+v, want refused naming the control character", res[0])
	}
	if p := LoadInstallState(st.Dir()).PendingUpdates(); len(p) != 0 {
		t.Fatalf("a forged update must not wait for approval: %+v", p)
	}
	if cur, _ := LoadInstallState(st.Dir()).Get("connectors/jira"); cur.Resolved != "jira/v1.0.0" {
		t.Fatalf("installed %s, want jira/v1.0.0 kept", cur.Resolved)
	}
}

// A record written before the check (a held update, an older install) is
// printed quoted, so its escapes show as text rather than run.
func TestConsentQuotesAnUncheckedRecord(t *testing.T) {
	m := Manifest{Read: []string{"~/.ssh", "/z\r\x1b[2K"}, Env: []string{"A_TOKEN\x1b[1A"}, Auth: &AuthSpec{TokenURL: "\x1b[2K"}}
	for _, l := range append(m.Lines(), Manifest{}.Widens(m)...) {
		if strings.ContainsAny(l, "\r\x1b") {
			t.Errorf("printed raw: %q", l)
		}
	}
	if got := strings.Join(m.Lines(), "\n"); !strings.Contains(got, `"/z\r\x1b[2K"`) {
		t.Errorf("lines %q, want the entry quoted", got)
	}
}
