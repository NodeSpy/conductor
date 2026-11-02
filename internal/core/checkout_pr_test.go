package core

import (
	"testing"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

func TestCheckoutPRIsTheDeclaredCheckout(t *testing.T) {
	manual := Trigger{Source: "manual", Target: Target{Repo: "manual:eval"}, CheckoutPR: &PRRef{Repo: "acme/app", Number: 12}}
	co, ok := manual.Checkout()
	if !ok || co.FetchRef != "refs/pull/12/head" || co.Hints["pr_number"] != "12" || len(co.Hints) != 1 || co.Remote != "" || co.PushBranch != "" {
		t.Fatalf("Checkout() = %+v, %v", co, ok)
	}
	if !manual.DeclaresCheckout() || manual.HasSemantics() {
		t.Fatal("a CheckoutPR declares the checkout without being event semantics")
	}
	// an event's own declaration is replaced, keeping only its forge hint
	ev := Trigger{
		Target:     Target{Repo: "acme/app", PR: 3},
		Sem:        &sdk.EventSemantics{Checkout: &sdk.CheckoutSemantics{Remote: "git@h:acme/app.git", FetchRef: "refs/pull/3/head", PushBranch: "head_ref", RuntimeHints: map[string]string{"forge": "github", "pr_number": "3"}}},
		CheckoutPR: &PRRef{Repo: "acme/app", Number: 12},
	}
	co, _ = ev.Checkout()
	if co.FetchRef != "refs/pull/12/head" || co.Hints["pr_number"] != "12" || co.Hints["forge"] != "github" || co.Remote != "" || co.PushBranch != "" {
		t.Fatalf("event Checkout() = %+v", co)
	}
	if (Trigger{}).DeclaresCheckout() {
		t.Fatal("a plain trigger declares no checkout")
	}
}
