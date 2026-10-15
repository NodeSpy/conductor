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
	a, _ := ReadFresh(ctx, "op", k)
	b, _ := ReadFresh(ctx, "op", k)
	if a != "111111" || b != "222222" || n != 2 {
		t.Fatalf("fresh reads: %q %q after %d op calls", a, b, n)
	}
	if _, ok := o.c.get(k); ok {
		t.Fatal("a fresh read left the value cached")
	}
	if c, _ := Read(ctx, "op", k); c != "333333" || n != 3 {
		t.Fatalf("a plain read after: %q", c)
	}
}
