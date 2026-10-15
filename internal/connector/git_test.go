package connector

import (
	"context"
	"strings"
	"testing"
)

// git.push names no ref: an option is refused, never ignored, and the
// dispatch comes only from the daemon-injected token identity.
func TestGitPushTakesNoOptions(t *testing.T) {
	var got string
	SetGitOps(func() *GitOps {
		return &GitOps{Push: func(_ context.Context, d string) (map[string]any, error) { got = d; return map[string]any{"pushed": true}, nil }}
	})
	defer SetGitOps(nil)
	for _, opts := range []map[string]any{{"branch": "main", "__dispatch": "d"}, {"ref": "refs/heads/x", "__dispatch": "d"}, {"force": true, "__dispatch": "d"}} {
		if _, err := (gitImpl{}).Invoke(context.Background(), "push", opts); err == nil || !strings.Contains(err.Error(), "takes no options") {
			t.Errorf("%v must be refused: %v", opts, err)
		}
	}
	if got != "" {
		t.Fatal("nothing pushed for a refused call")
	}
	if _, err := (gitImpl{}).Invoke(context.Background(), "push", map[string]any{}); err == nil {
		t.Fatal("no dispatch (no token identity): refused")
	}
	if out, err := (gitImpl{}).Invoke(context.Background(), "push", map[string]any{"__dispatch": "d1"}); err != nil || got != "d1" || out["pushed"] != true {
		t.Fatalf("%v %v %q", out, err, got)
	}
	if _, err := (gitImpl{}).Invoke(context.Background(), "pull", map[string]any{"__dispatch": "d1"}); err == nil {
		t.Fatal("unknown verb")
	}
}
