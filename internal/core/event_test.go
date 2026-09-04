package core

import "testing"

func TestCheckoutRepoAndCompletionHook(t *testing.T) {
	if got := (Target{Repo: "a/w"}).CheckoutRepo(); got != "a/w" {
		t.Fatalf("repo fallback: %q", got)
	}
	if got := (Target{Repo: "a/w", Project: "org/proj"}).CheckoutRepo(); got != "org/proj" {
		t.Fatalf("project wins: %q", got)
	}
	var got string
	SetCompletionHook(func(tr Trigger, outcome string) { got = tr.Kind + ":" + outcome })
	defer SetCompletionHook(nil)
	CompletionHook(Trigger{Kind: "k"}, "ok")
	if got != "k:ok" {
		t.Fatalf("hook: %q", got)
	}
	if itoa(0) != "0" || itoa(42) != "42" || itoa(-3) != "-3" {
		t.Fatal("itoa")
	}
}
