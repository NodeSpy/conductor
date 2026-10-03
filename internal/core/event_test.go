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

func TestTriggerKey(t *testing.T) {
	tr := Trigger{Source: "github", TargetTrusted: true, Target: Target{Repo: "acme/w", Number: 42}}
	if tr.Key() != "acme/w#42" {
		t.Fatalf("key=%q", tr.Key())
	}
	// A target the event's SENDER chose keys in its own namespace, so it can
	// never equal the key a real dispatch for that repo produces — which is
	// the key the session broker, the dedup store and the PR labels all use.
	forged := Trigger{Source: "webhook", Instance: "hooks", Target: Target{Repo: "acme/w", Number: 42}}
	if forged.Key() == tr.Key() {
		t.Fatalf("a forged target produced the real PR's key %q — it would land on that "+
			"PR's live review session and share its dedup entry", forged.Key())
	}
	if forged.Key() == (Trigger{Source: "webhook", Instance: "hooks",
		Target: Target{Repo: "acme/w", Number: 43}}).Key() {
		t.Fatal("untrusted keys must still discriminate per target")
	}
	// No repo → falls back to source:instance.
	tr2 := Trigger{Source: "slack", Instance: "team"}
	if tr2.Key() != "slack:team" {
		t.Fatalf("fallback key=%q", tr2.Key())
	}
}
