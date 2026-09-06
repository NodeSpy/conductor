package dispatch

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/secrets"
)

// {{secret "name"}} in an agent step's prompt/env renders the OPAQUE handle
// here (#36 §12): the dispatched runtime never receives the value at rest.
func TestRenderSecretHandle(t *testing.T) {
	out, err := render(`use {{secret "tok"}} now`, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	want := "use " + secrets.Handle("tok") + " now"
	if out != want {
		t.Fatalf("render = %q, want %q", out, want)
	}
	if strings.Contains(out, "tok\n") {
		t.Fatal("unreachable")
	}
}
