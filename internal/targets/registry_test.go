package targets

import (
	"testing"
	"time"
)

func TestMarkClosedAndClosed(t *testing.T) {
	r := New()
	if _, ok := r.Closed("acme/app", 1); ok {
		t.Fatal("nothing marked yet")
	}
	r.MarkClosed("acme/app", 1, true)
	if outcome, ok := r.Closed("acme/app", 1); !ok || outcome != "merged" {
		t.Fatalf("Closed = %q, %v, want merged, true", outcome, ok)
	}
	r.MarkClosed("acme/app", 2, false)
	if outcome, ok := r.Closed("acme/app", 2); !ok || outcome != "closed" {
		t.Fatalf("Closed = %q, %v, want closed, true", outcome, ok)
	}
}

func TestClosedExpiresPastTTL(t *testing.T) {
	r := New()
	r.mu.Lock()
	r.closed[key("acme/app", 1)] = entry{outcome: "merged", at: time.Now().Add(-(ttl + time.Hour))}
	r.mu.Unlock()
	if _, ok := r.Closed("acme/app", 1); ok {
		t.Fatal("an entry past ttl should read as not-closed")
	}
}

func TestMarkClosedPrunesExpiredEntries(t *testing.T) {
	r := New()
	r.mu.Lock()
	r.closed[key("acme/app", 1)] = entry{outcome: "merged", at: time.Now().Add(-(ttl + time.Hour))}
	r.mu.Unlock()
	r.MarkClosed("acme/other", 2, true) // any mutation should prune
	r.mu.Lock()
	_, stillThere := r.closed[key("acme/app", 1)]
	r.mu.Unlock()
	if stillThere {
		t.Fatal("expired entry should have been pruned on the next mutation")
	}
}
