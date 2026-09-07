package config

import (
	"strings"
	"testing"
)

func callableBase(triggers []TriggerSpec, cc CallableConfig) *Config {
	return &Config{Triggers: triggers, Callable: cc}
}

func boolp(b bool) *bool { return &b }

func TestValidateCallable(t *testing.T) {
	manualCallable := []TriggerSpec{{On: "manual", Name: "triage", Callable: boolp(true)}}

	cases := []struct {
		name     string
		triggers []TriggerSpec
		cc       CallableConfig
		wantErr  string
	}{
		{"disabled — no block", []TriggerSpec{{On: "manual", Name: "triage"}}, CallableConfig{}, ""},
		{
			"callable on non-manual", []TriggerSpec{{On: "cron", Name: "x", Callable: boolp(true)}},
			CallableConfig{}, "requires `on: manual`",
		},
		{
			"callable without name", []TriggerSpec{{On: "manual", Callable: boolp(true)}},
			CallableConfig{}, "requires a name",
		},
		{
			"listen without tokens", manualCallable,
			CallableConfig{Listen: ":8080"}, "would refuse every request",
		},
		{
			"tokens without listen", manualCallable,
			CallableConfig{Tokens: []CallableToken{{Name: "a", Bearer: "x", Workflows: []string{"triage"}}}},
			"nothing would serve",
		},
		{
			"token without name", manualCallable,
			CallableConfig{Listen: ":8080", Tokens: []CallableToken{{Bearer: "x"}}}, "requires a name",
		},
		{
			"duplicate token name", manualCallable,
			CallableConfig{Listen: ":8080", Tokens: []CallableToken{
				{Name: "a", Bearer: "x", Workflows: []string{"triage"}},
				{Name: "a", Bearer: "y", Workflows: []string{"triage"}},
			}}, "duplicate token name",
		},
		{
			"both bearer and hmac", manualCallable,
			CallableConfig{Listen: ":8080", Tokens: []CallableToken{
				{Name: "a", Bearer: "x", HMAC: &CallableHMAC{Secret: "s", Header: "H"}, Workflows: []string{"triage"}},
			}}, "exactly one of bearer",
		},
		{
			"neither bearer nor hmac (empty credential)", manualCallable,
			CallableConfig{Listen: ":8080", Tokens: []CallableToken{{Name: "a", Workflows: []string{"triage"}}}},
			"empty credential is never accepted",
		},
		{
			"hmac without header", manualCallable,
			CallableConfig{Listen: ":8080", Tokens: []CallableToken{
				{Name: "a", HMAC: &CallableHMAC{Secret: "s"}, Workflows: []string{"triage"}},
			}}, "hmac.header is required",
		},
		{
			"hmac bad scheme", manualCallable,
			CallableConfig{Listen: ":8080", Tokens: []CallableToken{
				{Name: "a", HMAC: &CallableHMAC{Secret: "s", Header: "H", Scheme: "rot13"}, Workflows: []string{"triage"}},
			}}, "hmac.scheme must be",
		},
		{
			"scope names unknown workflow", manualCallable,
			CallableConfig{Listen: ":8080", Tokens: []CallableToken{
				{Name: "a", Bearer: "x", Workflows: []string{"ghost"}},
			}}, "not an addressable callable trigger",
		},
		{
			"valid bearer", manualCallable,
			CallableConfig{Listen: ":8080", Tokens: []CallableToken{
				{Name: "a", Bearer: "x", Workflows: []string{"triage"}},
			}}, "",
		},
		{
			"valid hmac", manualCallable,
			CallableConfig{Listen: ":8080", Tokens: []CallableToken{
				{Name: "a", HMAC: &CallableHMAC{Secret: "s", Header: "X-Sig", Scheme: "base64"}, Workflows: []string{"triage"}},
			}}, "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := callableBase(c.triggers, c.cc).validateCallable()
			if c.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("got %v, want error containing %q", err, c.wantErr)
			}
		})
	}
}

func TestCallableEnabled(t *testing.T) {
	if (CallableConfig{}).Enabled() {
		t.Fatal("empty config should be disabled")
	}
	if (CallableConfig{Listen: ":8080"}).Enabled() {
		t.Fatal("listen without tokens should be disabled")
	}
	cc := CallableConfig{Listen: ":8080", Tokens: []CallableToken{{Name: "a", Bearer: "x"}}}
	if !cc.Enabled() {
		t.Fatal("listen + token should be enabled")
	}
}
