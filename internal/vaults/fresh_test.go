package vaults

import (
	"context"
	"strings"
	"testing"
)

// TestReadFreshBypassesTheCache: a one-time code changes between reads;
// ReadFresh asks op every time and leaves nothing cached.
func TestReadFreshBypassesTheCache(t *testing.T) {
	Reset()
	defer Reset()
	n := 0
	o := &OnePasswordVault{Exec: func(_ context.Context, _ string, _ []string, name string, args ...string) (string, error) {
		n++
		if name != "op" || !strings.HasSuffix(strings.Join(args, " "), "op://Private/Site/one-time password?attribute=otp") {
			t.Fatalf("op %v", args)
		}
		return []string{"111111", "222222", "333333"}[n-1], nil
	}}
	if err := Register("op", "onepassword", o); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	k := "Private/Site/one-time password?attribute=otp"
	// A plain read caches; a fresh read never answers from that cache…
	if a, _ := Read(ctx, "op", k); a != "111111" {
		t.Fatalf("plain read: %q", a)
	}
	b, _ := ReadFresh(ctx, "op", k)
	c, _ := ReadFresh(ctx, "op", k)
	if b != "222222" || c != "333333" || n != 3 {
		t.Fatalf("fresh reads: %q %q after %d op calls", b, c, n)
	}
	// …and leaves nothing in it.
	if _, ok := o.c.get(k); ok {
		t.Fatal("a fresh read left the value cached")
	}
}
