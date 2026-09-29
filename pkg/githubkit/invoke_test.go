package githubkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPRFilesPatch verifies pr_files surfaces each file's patch when GitHub
// sends one, and leaves it "" (not an error) for a file GitHub omits it for —
// a binary file or one past the diff size cap.
func TestPRFilesPatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/pulls/7/files" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"filename": "a.go", "status": "modified", "additions": 1, "deletions": 1, "changes": 2, "patch": "@@ -1 +1 @@\n-old\n+new"},
			{"filename": "b.png", "status": "modified", "additions": 0, "deletions": 0, "changes": 0}
		]`))
	}))
	defer srv.Close()

	c, err := NewClient(Config{Token: "tok", APIBase: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Invoke(context.Background(), "pr_files", map[string]any{"repo": "o/r", "pr": 7})
	if err != nil {
		t.Fatal(err)
	}
	files, _ := out["files"].([]any)
	if len(files) != 2 {
		t.Fatalf("want 2 files, got %d", len(files))
	}
	a := files[0].(map[string]any)
	if a["patch"] != "@@ -1 +1 @@\n-old\n+new" {
		t.Errorf("a.go patch = %q", a["patch"])
	}
	b := files[1].(map[string]any)
	if b["patch"] != "" {
		t.Errorf("b.png (no patch in payload) patch = %q, want empty", b["patch"])
	}
}
