package core

import "testing"

func TestCheckoutRepo(t *testing.T) {
	if got := (Target{Repo: "a/w"}).CheckoutRepo(); got != "a/w" {
		t.Fatalf("repo fallback: %q", got)
	}
	if got := (Target{Repo: "a/w", Project: "org/proj"}).CheckoutRepo(); got != "org/proj" {
		t.Fatalf("project wins: %q", got)
	}
	if itoa(0) != "0" || itoa(42) != "42" || itoa(-3) != "-3" {
		t.Fatal("itoa")
	}
}
