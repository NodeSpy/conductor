package sandbox

import (
	"net/http"
	"strings"
	"testing"
)

// F: with per-repo scoping gone, the credential rides on ANY path that ends
// in /git-upload-pack (POST) or /info/refs?service=git-upload-pack (GET),
// and such a request skips the host's path allowlist. On GHES the REST API
// is on the same host (/api/v3/...), which accepts the token as Basic auth.
func TestGitCredentialOnlyOnRepositoryFetchPaths(t *testing.T) {
	rig, want := gitRig(t, false) // allowlist: one unrelated path only
	c := rig.client(caPool(rig.in.CAPEM()), false)
	for _, f := range []struct{ method, path string }{
		{"POST", "/api/v3/repos/o/r/statuses/git-upload-pack"},
		{"GET", "/api/v3/repos/o/private/contents/info/refs?service=git-upload-pack"},
		{"GET", "/api/v3/user/info/refs?service=git-upload-pack&per_page=100"},
		{"POST", "/api/graphql/git-upload-pack"},
		{"GET", "/login/oauth/info/refs?service=git-upload-pack"},
	} {
		code, body := do(t, c, f.method, rig.mirrorURL(f.path), http.Header{})
		if code == 200 && strings.Contains(body, "auth="+want+" ") {
			t.Errorf("%s %s: not a repository path, yet it passed the path allowlist carrying the git credential", f.method, f.path)
		}
	}
}
