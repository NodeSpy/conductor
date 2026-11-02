package config

import (
	"strings"
	"testing"
)

func checkoutPRConfig(step string) string {
	return `
connectors:
  gh:
    use: github
triggers:
  - on: gh.new_comment
    steps:
      - id: review
` + step
}

func TestCheckoutPRLoads(t *testing.T) {
	cfg, err := Load(writeTestConfig(t, checkoutPRConfig(`        type: agent
        checkout: checkout-pr
        checkout_pr: { repo: "{{.inputs.repo}}", pr: "{{.inputs.pr}}" }
`)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := cfg.Triggers[0].Steps[0].CheckoutPR
	if got == nil || got.Repo != "{{.inputs.repo}}" || got.PR != "{{.inputs.pr}}" {
		t.Fatalf("checkout_pr = %+v", got)
	}
	// inert under checkout: none — an operator's override of a pack's step
	if _, err := Load(writeTestConfig(t, checkoutPRConfig(`        type: agent
        checkout: none
        checkout_pr: { repo: "{{.inputs.repo}}", pr: "{{.inputs.pr}}" }
`))); err != nil {
		t.Fatalf("Load (checkout: none): %v", err)
	}
	// a literal number is a PR too
	cfg, err = Load(writeTestConfig(t, checkoutPRConfig(`        type: agent
        checkout: checkout-pr
        checkout_pr: { repo: acme/app, pr: 12 }
`)))
	if err != nil {
		t.Fatalf("Load (literal): %v", err)
	}
	if got := cfg.Triggers[0].Steps[0].CheckoutPR; got.Repo != "acme/app" || got.PR != "12" {
		t.Fatalf("literal checkout_pr = %+v", got)
	}
}

func TestCheckoutPRIsRefusedWhereItCannotApply(t *testing.T) {
	for _, c := range []struct{ step, want string }{
		{"        type: agent\n        checkout_pr: { repo: acme/app, pr: 1 }\n", "needs `checkout: checkout-pr`"},
		{"        type: agent\n        checkout: branch-off\n        checkout_pr: { repo: acme/app, pr: 1 }\n", "needs `checkout: checkout-pr`"},
		{"        type: command\n        command: [\"true\"]\n        checkout_pr: { repo: acme/app, pr: 1 }\n", "agent steps only"},
		{"        type: agent\n        checkout: checkout-pr\n        repo: acme/web\n        checkout_pr: { repo: acme/app, pr: 1 }\n", "both name the checkout"},
		{"        type: agent\n        checkout: checkout-pr\n        checkout_pr: { repo: acme/app }\n", "needs both repo and pr"},
		{"        type: agent\n        checkout: checkout-pr\n        checkout_pr: { repo: \"git@evil.example:x/y\", pr: 1 }\n", "is not owner/name"},
		{"        type: agent\n        checkout: checkout-pr\n        checkout_pr: { repo: acme/app, pr: 0 }\n", "is not a PR number"},
	} {
		_, err := Load(writeTestConfig(t, checkoutPRConfig(c.step)))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("step:\n%s\ngot %v, want an error containing %q", c.step, err, c.want)
		}
	}
}

func TestParseCheckoutPR(t *testing.T) {
	if repo, n, err := ParseCheckoutPR(" acme/app ", " 12 "); err != nil || repo != "acme/app" || n != 12 {
		t.Fatalf("ParseCheckoutPR = %q %d %v", repo, n, err)
	}
	for _, c := range [][2]string{
		{"acme", "1"}, {"acme/app/x", "1"}, {"../app", "1"}, {"acme/..", "1"}, {".x/app", "1"}, {"-x/app", "1"},
		{"https://h/acme/app", "1"}, {"acme/app", "-3"}, {"acme/app", "1.5"}, {"acme/app", "x"}, {"", ""},
	} {
		if _, _, err := ParseCheckoutPR(c[0], c[1]); err == nil {
			t.Errorf("ParseCheckoutPR(%q, %q) accepted", c[0], c[1])
		}
	}
}
