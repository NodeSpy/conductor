package config

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
		{"container on", func(b *BrowserConfig) { b.Container = "on" }, ""},
		{"container bad", func(b *BrowserConfig) { b.Container = "yes" }, "auto|on|off"},
		{"evaluate one identity", func(b *BrowserConfig) { b.Evaluate = BrowserEvaluate{"agent:triage@acme/app"} }, ""},
		{"evaluate a prefix", func(b *BrowserConfig) { b.Evaluate = BrowserEvaluate{"agent:triage@*"} }, ""},
		{"evaluate everyone", func(b *BrowserConfig) { b.Evaluate = BrowserEvaluate{"*"} }, "name each identity"},
		{"evaluate empty", func(b *BrowserConfig) { b.Evaluate = BrowserEvaluate{""} }, "name each identity"},
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
		{"value not ref", func(b *BrowserConfig) { b.Credentials[0].Password = &BrowserSecretRef{Literal: "hunter2"} }, "typed secret"},
		{"typed otp", func(b *BrowserConfig) { b.Credentials[0].OTP = &BrowserSecretRef{Literal: "JBSWY3DPEHPK3PXP"} }, "typed secret"},
		{"key without vault", func(b *BrowserConfig) { b.Credentials[0].Password = &BrowserSecretRef{Key: "hunter2"} }, "names a vault and a key"},
		{"two sources", func(b *BrowserConfig) { b.Credentials[0].Password = &BrowserSecretRef{Vault: "op", Key: "k", Env: "X"} }, "names one source"},
		{"passkey from env", func(b *BrowserConfig) { b.Credentials[0].Passkey = &BrowserSecretRef{Env: "PK"} }, "writable vault"},
		{"unknown vault", func(b *BrowserConfig) { b.Credentials[0].Password = &BrowserSecretRef{Vault: "nope", Key: "k"} }, "no vault"},
		{"nothing to fill", func(b *BrowserConfig) { b.Credentials[0].Password, b.Credentials[0].Passkey = nil, nil }, "nothing to fill"},
		{"login url elsewhere", func(b *BrowserConfig) { b.Credentials[0].LoginURL = "https://evil.example.com/login" }, "not on the origin"},
		{"login url other scheme", func(b *BrowserConfig) { b.Credentials[0].LoginURL = "http://app.example.com/login" }, "not on the origin"},
		{"cred unreachable", func(b *BrowserConfig) {
			b.Credentials[0].Origin, b.Credentials[0].LoginURL = "https://other.example.com", ""
		}, "not reachable"},
		{"cred port unreachable", func(b *BrowserConfig) {
			b.Credentials[0].Origin, b.Credentials[0].LoginURL = "http://app.example.com", ""
		}, "not reachable"},
		{"cred loopback port", func(b *BrowserConfig) {
			b.Credentials[0].Origin, b.Credentials[0].LoginURL = "http://127.0.0.1:8080", ""
		}, ""},
	}
	mbox := func(b *BrowserConfig) {
		b.Mailboxes = map[string]BrowserMailbox{"inbox": {IMAP: "imap.example.com:993", Username: ref, Password: ref}}
		b.Credentials[0].EmailCode = &BrowserEmailCode{Mailbox: "inbox", From: "no-reply@app.example.com", Subject: "sign-in code", Code: `code is ([0-9]{6})`}
	}
	with := func(f func(b *BrowserConfig)) func(b *BrowserConfig) { return func(b *BrowserConfig) { mbox(b); f(b) } }
	cases = append(cases, []struct {
		name string
		mod  func(b *BrowserConfig)
		want string
	}{
		{"email code", mbox, ""},
		{"email code @domain", with(func(b *BrowserConfig) { b.Credentials[0].EmailCode.From = "@app.example.com" }), ""},
		{"email code and otp", with(func(b *BrowserConfig) { b.Credentials[0].OTP = ref }), "keep one"},
		{"email code no mailbox", with(func(b *BrowserConfig) { b.Credentials[0].EmailCode.Mailbox = "nope" }), "not in browser.mailboxes"},
		{"email code any sender", with(func(b *BrowserConfig) { b.Credentials[0].EmailCode.From = "" }), "exact address"},
		{"email code wildcard sender", with(func(b *BrowserConfig) { b.Credentials[0].EmailCode.From = "*@app.example.com" }), "exact address"},
		{"email code no subject", with(func(b *BrowserConfig) { b.Credentials[0].EmailCode.Subject = "" }), "subject is required"},
		{"email code bad pattern", with(func(b *BrowserConfig) { b.Credentials[0].EmailCode.Code = "(" }), "regular expression"},
		{"email code two groups", with(func(b *BrowserConfig) { b.Credentials[0].EmailCode.Code = "(a)(b)" }), "at most one"},
		{"email code long wait", with(func(b *BrowserConfig) { b.Credentials[0].EmailCode.Wait = "5m" }), "up to 90s"},
		{"mailbox no port", with(func(b *BrowserConfig) {
			b.Mailboxes["inbox"] = BrowserMailbox{IMAP: "imap.example.com", Username: ref, Password: ref}
		}), "host:port"},
		{"mailbox plaintext remote", with(func(b *BrowserConfig) {
			b.Mailboxes["inbox"] = BrowserMailbox{IMAP: "imap.example.com:143", PlaintextLoopback: true, Username: ref, Password: ref}
		}), "loopback address only"},
		{"mailbox plaintext loopback", with(func(b *BrowserConfig) {
			b.Mailboxes["inbox"] = BrowserMailbox{IMAP: "127.0.0.1:1143", PlaintextLoopback: true, Username: ref, Password: ref}
		}), ""},
		{"typed username is fine", func(b *BrowserConfig) { b.Credentials[0].Username = &BrowserSecretRef{Literal: "agent@example.com"} }, ""},
		{"env password is fine", func(b *BrowserConfig) { b.Credentials[0].Password = &BrowserSecretRef{Env: "APP_PW"} }, ""},
		{"named secret is fine", func(b *BrowserConfig) {
			b.Credentials[0].Password = &BrowserSecretRef{Vault: "op", Key: "Agents/App/password", Named: true}
		}, ""},
		{"mailbox literal password", with(func(b *BrowserConfig) {
			b.Mailboxes["inbox"] = BrowserMailbox{IMAP: "imap.example.com:993", Username: ref, Password: &BrowserSecretRef{Literal: "hunter2"}}
		}), "typed secret"},
	}...)
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

func TestBrowserEvaluateYAML(t *testing.T) {
	for in, want := range map[string]string{
		"evaluate: off": "[]",
		"evaluate: [\"agent:a@o/r\", \"dir:/w\"]": "[agent:a@o/r dir:/w]",
		"evaluate: read-only":                     "opted in per identity",
		"evaluate: on":                            "must be a list",
	} {
		var b BrowserConfig
		err := yaml.Unmarshal([]byte(in), &b)
		got := fmt.Sprint([]string(b.Evaluate))
		if err != nil {
			got = err.Error()
		}
		if !strings.Contains(got, want) {
			t.Errorf("%s: %s", in, got)
		}
	}
}

// Every value source, as written in YAML: a string in the reference
// syntax conductor uses elsewhere, or the old map.
func TestBrowserSecretRefYAML(t *testing.T) {
	for in, want := range map[string]string{
		`password: '{{ vault "op" "Agents/App/password" }}'`: `{{ vault "op" "Agents/App/password" }}`,
		`password: '{{secret "house/app_pw"}}'`:              `{{secret "house/app_pw"}}`,
		`password: '${env.APP_PW}'`:                          `${env.APP_PW}`,
		`password: {vault: op, key: "Agents/App/password"}`:  `{{ vault "op" "Agents/App/password" }}`,
		`password: agent@example.com`:                        `(typed value)`,
		`password: '${settings.app_pw}'`:                     `plain config`,
		`password: '{{secret "nokey"}}'`:                     `"<vault>/<key>"`,
	} {
		var cr BrowserCredential
		err := yaml.Unmarshal([]byte(in), &cr)
		got := ""
		if err != nil {
			got = err.Error()
		} else {
			got = cr.Password.Source()
		}
		if !strings.Contains(got, want) {
			t.Errorf("%s: %s", in, got)
		}
	}
}

// A secret field's text is checked before ${…} substitution: a setting is
// plain config, and the loader's ${NAME} would put the value into the
// config at load. A username may take a setting; ${env.NAME} is read at
// fill time and passes.
func TestCheckSecretSources(t *testing.T) {
	for body, want := range map[string]string{
		"browser:\n  credentials:\n    - {name: a, password: \"${settings.pw}\"}\n": "plain config",
		"browser:\n  credentials:\n    - name: a\n      otp: ${settings.seed}\n":    "plain config",
		"browser:\n  credentials:\n    - name: a\n      password: ${APP_PW}\n":      "${env.APP_PW}",
		"browser:\n  mailboxes:\n    m: {password: \"${settings.x}\"}\n":            "plain config",
		"browser:\n  credentials:\n    - name: a\n      username: ${settings.u}\n":  "",
		"browser:\n  credentials:\n    - name: a\n      password: ${env.APP_PW}\n":  "",
		"settings: {pw: x}\nother: ${settings.pw}\n":                                "",
	} {
		err := checkSecretSources("c.yaml", []byte(body))
		switch {
		case want == "" && err != nil:
			t.Errorf("%q: %v", body, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%q: want %q, got %v", body, want, err)
		}
	}
}

// The check runs in the real load path, before expansion.
func TestLoadRefusesASettingAsASecret(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/conductor.yaml"
	body := "settings: {pw: hunter2}\nbrowser:\n  allow: [app.example.com]\n  credentials:\n    - name: a\n      origin: https://app.example.com\n      identities: [x]\n      password: \"${settings.pw}\"\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "plain config") {
		t.Fatalf("load: %v", err)
	}
}
