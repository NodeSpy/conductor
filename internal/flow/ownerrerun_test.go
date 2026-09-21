package flow

import (
	"context"
	"testing"
)

// TestOwnerRerunCtxRoundTrip locks the ctx accessor the flow runner uses to hand
// a background hand-off deep in a workflow's step loop a closure that re-runs the
// whole owning workflow (handoff.refresh). Absent by default; retrievable once set.
func TestOwnerRerunCtxRoundTrip(t *testing.T) {
	if ownerRerun(context.Background()) != nil {
		t.Fatal("no owning-workflow rerun closure should be present by default")
	}
	called := false
	ctx := withOwnerRerun(context.Background(), func(context.Context) error { called = true; return nil })
	fn := ownerRerun(ctx)
	if fn == nil {
		t.Fatal("stored rerun closure not retrievable")
	}
	if err := fn(context.Background()); err != nil {
		t.Fatalf("closure returned error: %v", err)
	}
	if !called {
		t.Fatal("retrieved closure was not the one stored")
	}
}
