package dispatch

import (
	"strings"
	"testing"
)

// TestCheckPromptSize proves the oversized-prompt guard: a prompt at the limit
// passes, one over it is rejected with an actionable message (instead of the
// kernel's cryptic E2BIG at fork/exec).
func TestCheckPromptSize(t *testing.T) {
	if err := checkPromptSize(strings.Repeat("x", maxPromptArgBytes)); err != nil {
		t.Fatalf("a prompt at the limit must pass: %v", err)
	}
	err := checkPromptSize(strings.Repeat("x", maxPromptArgBytes+1))
	if err == nil {
		t.Fatal("a prompt over the limit must be rejected before fork/exec")
	}
	if !strings.Contains(err.Error(), "cap large inlined fields") {
		t.Fatalf("error should tell the operator how to fix it, got: %v", err)
	}
}
