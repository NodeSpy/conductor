package config

import (
	"strings"
	"testing"
)

func TestValidateBrowser(t *testing.T) {
	ref := &BrowserSecretRef{Vault: "op", Key: "Private/Site/password"}
	good := func() *BrowserConfig {
		return &BrowserConfig{
			Allow:  []string{"app.example.com", "cdn.example.net", "127.0.0.1:8080"},
			Logins: []BrowserLogin{{Site: "app.example.com", Identities: []string{"agent:triage@*"}}},
			Credentials: []BrowserCredential{{Name: "app", Origin: "https://app.example.com", Identities: []string{"agent:triage@acme/app"},
				Password: ref, Passkey: &BrowserSecretRef{Vault: "op", Key: "k"}, LoginURL: "https://app.example.com/login"}},
		}
	}
	cases := []struct {
		name string
		mod  func(b *BrowserConfig)
		want string
	}{
		{"good", func(*BrowserConfig) {}, ""},
		{"empty allow", func(b *BrowserConfig) { b.Allow = nil; b.Logins = nil; b.Credentials = nil }, "browser.allow is empty"},
		{"whole web", func(b *BrowserConfig) { b.Allow = append(b.Allow, "*") }, "whole web"},
		{"whole web any port", func(b *BrowserConfig) { b.Allow = append(b.Allow, "*:*") }, "whole web"},
		{"evaluate", func(b *BrowserConfig) { b.Evaluate = "on" }, "off|read-only"},
		{"evaluate read-only", func(b *BrowserConfig) { b.Evaluate = "read-only" }, ""},
		{"idle", func(b *BrowserConfig) { b.IdleTimeout = "soon" }, "not a duration"},
		{"negative", func(b *BrowserConfig) { b.MaxBrowsers = -1 }, "negative"},
		{"relative chrome", func(b *BrowserConfig) { b.Chrome = "bin/chrome" }, "absolute"},
		{"login site with port", func(b *BrowserConfig) { b.Logins[0].Site = "app.example.com:443" }, "not a host"},
		{"login no identities", func(b *BrowserConfig) { b.Logins[0].Identities = nil }, "names no identities"},
		{"login star identity", func(b *BrowserConfig) { b.Logins[0].Identities = []string{"*"} }, "name each identity"},
		{"login unreachable", func(b *BrowserConfig) { b.Logins[0].Site = "other.example.com" }, "not reachable"},
		{"login wildcard reachable", func(b *BrowserConfig) {
			b.Allow = append(b.Allow, "*.corp.example")
			b.Logins = append(b.Logins, BrowserLogin{Site: "*.corp.example", Identities: []string{"x"}})
		}, ""},
		{"login twice", func(b *BrowserConfig) { b.Logins = append(b.Logins, b.Logins[0]) }, "twice"},
		{"cred no name", func(b *BrowserConfig) { b.Credentials[0].Name = "" }, "name"},
		{"cred twice", func(b *BrowserConfig) { b.Credentials = append(b.Credentials, b.Credentials[0]) }, "twice"},
		{"cred wildcard origin", func(b *BrowserConfig) { b.Credentials[0].Origin = "https://*.example.com" }, "exactly"},
		{"cred origin no scheme", func(b *BrowserConfig) { b.Credentials[0].Origin = "app.example.com" }, "exactly"},
		{"cred origin query", func(b *BrowserConfig) { b.Credentials[0].Origin = "https://app.example.com/?x=1" }, "exactly"},
		{"cred origin userinfo", func(b *BrowserConfig) { b.Credentials[0].Origin = "https://u@app.example.com" }, "exactly"},
		{"cred no identities", func(b *BrowserConfig) { b.Credentials[0].Identities = nil }, "names no identities"},
		{"cred star identity", func(b *BrowserConfig) { b.Credentials[0].Identities = []string{"*"} }, "name each identity"},
		{"passkey prefix identity", func(b *BrowserConfig) { b.Credentials[0].Identities = []string{"agent:triage@*"} }, "exactly one identity"},
		{"passkey two identities", func(b *BrowserConfig) { b.Credentials[0].Identities = []string{"a", "b"} }, "exactly one identity"},
		{"prefix without passkey", func(b *BrowserConfig) {
			b.Credentials[0].Passkey = nil
			b.Credentials[0].Identities = []string{"agent:triage@*"}
		}, ""},
		{"value not ref", func(b *BrowserConfig) { b.Credentials[0].Password = &BrowserSecretRef{Key: "hunter2"} }, "never the value"},
		{"unknown vault", func(b *BrowserConfig) { b.Credentials[0].Password = &BrowserSecretRef{Vault: "nope", Key: "k"} }, "no vault"},
		{"nothing to fill", func(b *BrowserConfig) { b.Credentials[0].Password, b.Credentials[0].Passkey = nil, nil }, "nothing to fill"},
		{"login url elsewhere", func(b *BrowserConfig) { b.Credentials[0].LoginURL = "https://evil.example.com/login" }, "not on the origin"},
		{"login url other scheme", func(b *BrowserConfig) { b.Credentials[0].LoginURL = "http://app.example.com/login" }, "not on the origin"},
		{"cred unreachable", func(b *BrowserConfig) { b.Credentials[0].Origin, b.Credentials[0].LoginURL = "https://other.example.com", "" }, "not reachable"},
		{"cred port unreachable", func(b *BrowserConfig) { b.Credentials[0].Origin, b.Credentials[0].LoginURL = "http://app.example.com", "" }, "not reachable"},
		{"cred loopback port", func(b *BrowserConfig) {
			b.Credentials[0].Origin, b.Credentials[0].LoginURL = "http://127.0.0.1:8080", ""
		}, ""},
	}
	for _, tc := range cases {
		b := good()
		tc.mod(b)
		c := &Config{Browser: b, Vaults: map[string]VaultRef{"op": {}}}
		err := c.validateBrowser()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: want %q, got %v", tc.name, tc.want, err)
		}
	}
}
