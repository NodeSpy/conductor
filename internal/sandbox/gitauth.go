package sandbox

import (
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Git fetches over HTTPS, authenticated by conductor.
//
// An agent's sandbox holds no forge credential and no SSH key. A
// repository whose dependencies are other private repositories (a
// package.json naming ssh://git@<forge>/<org>/<repo>.git) is fetched over
// HTTPS instead: the launch's git rewrites the SSH forms of the declared
// hosts to https:// (GitRewriteEnv), the launch's proxy inspects those
// hosts, and on each git fetch request — smart HTTP's upload-pack, read
// only — it adds the credential itself, for any repository on the host.
//
// A launch gets a git fetch credential only when it already holds that
// very token (GitAuthHeld): another credential it was granted carries the
// identical value. The fetch then reaches nothing the agent could not
// already reach with what it holds; a launch holding no such token gets no
// git auth, and its fetch of a private repository fails. The proxy never
// shows the credential to the sandbox. A push (receive-pack) to a declared
// host is refused outright — conductor pushes, never the agent.

// GitAuth is one host's git fetch credential for a launch.
type GitAuth struct {
	// Host is an exact host name, with :port when not 443.
	Host string `json:"host"`
	// For is the repository the launch's work is for (owner/name): what a
	// refusal names.
	For string `json:"for,omitempty"`
	// Username is the HTTP basic username sent with Secret.
	Username string `json:"username"`
	// Secret is the credential. Never serialized.
	Secret string `json:"-"`
}

// GitAuthHeld is the auths whose Secret the launch already holds: equal,
// byte for byte, to one of held (the credential values the launch was
// granted). Compared in constant time; what a credential is named or for
// never matters, only its value.
func GitAuthHeld(auths []GitAuth, held []string) []GitAuth {
	var out []GitAuth
	for _, g := range auths {
		if g.Secret == "" {
			continue
		}
		has := 0
		for _, v := range held {
			if len(v) == len(g.Secret) {
				has |= subtle.ConstantTimeCompare([]byte(v), []byte(g.Secret))
			}
		}
		if has == 1 {
			out = append(out, g)
		}
	}
	return out
}

// Validate refuses a GitAuth the proxy cannot apply exactly.
func (g GitAuth) Validate() error {
	h, port := splitGitHost(g.Host)
	if !inspectableName(h) {
		return fmt.Errorf("sandbox: git credential host %q: want an exact host name, with :port when not 443", g.Host)
	}
	if n, err := strconv.Atoi(port); err != nil || n <= 0 || n > 65535 || strconv.Itoa(n) != port {
		return fmt.Errorf("sandbox: git credential host %q: bad port", g.Host)
	}
	if g.Username == "" || strings.ContainsAny(g.Username, ":\r\n") {
		return fmt.Errorf("sandbox: git credential for %s: username is required and holds no ':' or newline", g.Host)
	}
	if g.Secret == "" || strings.ContainsAny(g.Secret, "\r\n") {
		return fmt.Errorf("sandbox: git credential for %s: no usable secret", g.Host)
	}
	return nil
}

// HostName is Host without its port.
func (g GitAuth) HostName() string { h, _ := splitGitHost(g.Host); return h }

// target is Host as host:port.
func (g GitAuth) target() string { h, p := splitGitHost(g.Host); return h + ":" + p }

func splitGitHost(hp string) (host, port string) {
	hp = strings.ToLower(strings.TrimSpace(hp))
	if h, p, err := net.SplitHostPort(hp); err == nil {
		return h, p
	}
	return hp, "443"
}

// GitRewriteEnv is the git configuration (GIT_CONFIG_COUNT and its
// GIT_CONFIG_KEY_n / GIT_CONFIG_VALUE_n pairs, after any env already
// carries) that sends the SSH forms of each host's URLs over HTTPS.
func GitRewriteEnv(env []string, auths []GitAuth) []string {
	n := 0
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GIT_CONFIG_COUNT="); ok {
			fmt.Sscanf(v, "%d", &n)
		}
	}
	out := make([]string, 0, len(env)+8*len(auths))
	for _, kv := range env {
		if !strings.HasPrefix(kv, "GIT_CONFIG_COUNT=") {
			out = append(out, kv)
		}
	}
	seen := map[string]bool{}
	for _, a := range auths {
		h, port := splitGitHost(a.Host)
		if seen[a.Host] {
			continue
		}
		seen[a.Host] = true
		https := "https://" + h + "/"
		if port != "443" {
			https = "https://" + h + ":" + port + "/"
		}
		for _, from := range []string{"ssh://git@" + h + "/", "git+ssh://git@" + h + "/", "ssh://" + h + "/", "git@" + h + ":"} {
			out = append(out, fmt.Sprintf("GIT_CONFIG_KEY_%d=url.%s.insteadOf", n, https), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", n, from))
			n++
		}
	}
	return append(out, fmt.Sprintf("GIT_CONFIG_COUNT=%d", n))
}

// Git smart-HTTP operations a request is.
const (
	gitNone  = ""
	gitFetch = "fetch"
	gitPush  = "push"
)

// gitOp classifies a request to an inspected host: a smart-HTTP fetch or
// push, and the repository path it names (".git" trimmed); gitNone for
// anything else. p is the request's checked path (requestPath).
func gitOp(r *http.Request, p string) (op, repo string) {
	switch {
	case strings.HasSuffix(p, "/info/refs"):
		repo = strings.TrimSuffix(p, "/info/refs")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			return gitNone, ""
		}
		// Exactly one service: a server may read either of two.
		svc := r.URL.Query()["service"]
		switch {
		case slices.Contains(svc, "git-receive-pack"):
			op = gitPush
		case len(svc) == 1 && svc[0] == "git-upload-pack":
			op = gitFetch
		default:
			return gitNone, ""
		}
	case strings.HasSuffix(p, "/git-upload-pack"):
		repo, op = strings.TrimSuffix(p, "/git-upload-pack"), gitFetch
		if r.Method != http.MethodPost {
			return gitNone, ""
		}
	case strings.HasSuffix(p, "/git-receive-pack"):
		repo, op = strings.TrimSuffix(p, "/git-receive-pack"), gitPush
	default:
		return gitNone, ""
	}
	repo = strings.TrimSuffix(repo, ".git")
	if op == gitFetch && !gitFetchShape(r, p, repo) {
		// Not a repository's smart-HTTP fetch: no credential, and the
		// host's path allowlist decides it like any other request.
		return gitNone, ""
	}
	return op, repo
}

// gitReserved are first path segments a forge serves something other than
// a repository under (GitHub Enterprise Server's REST API is /api/v3 on the
// git host itself): never a fetch the credential is added to.
var gitReserved = map[string]bool{"api": true, "login": true, "graphql": true, "site": true, "_": true, "-": true}

// gitFetchShape reports a fetch request that is exactly a repository's:
// /owner/name[.git]/info/refs?service=git-upload-pack (GET or HEAD) or
// /owner/name[.git]/git-upload-pack (POST, no query) — two plain segments,
// the first not reserved. Anything else (an API route that merely ends in
// git-upload-pack, an extra query parameter, a nested path) carries no
// credential.
func gitFetchShape(r *http.Request, p, repo string) bool {
	segs := strings.Split(strings.TrimPrefix(repo, "/"), "/")
	if len(segs) != 2 || !strings.HasPrefix(repo, "/") || gitReserved[strings.ToLower(segs[0])] {
		return false
	}
	for _, s := range segs {
		if !gitNameRE.MatchString(s) {
			return false
		}
	}
	if strings.HasSuffix(p, "/info/refs") {
		return r.URL.RawQuery == "service=git-upload-pack"
	}
	return r.URL.RawQuery == ""
}

// gitNameRE is an owner or repository name as forges allow them.
var gitNameRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

// gitAuths are the session's credentials for target (host:port).
func (s *Session) gitAuths(target string) []GitAuth {
	if s == nil {
		return nil
	}
	var out []GitAuth
	for _, g := range s.git {
		if strings.EqualFold(g.target(), target) {
			out = append(out, g)
		}
	}
	return out
}

// gitCredentialFor is the credential a fetch of repo on target carries
// (nil: none for target).
func (s *Session) gitCredentialFor(target, repo string) *GitAuth {
	if repo == "" {
		return nil
	}
	if g := s.gitAuths(target); len(g) > 0 {
		return &g[0]
	}
	return nil
}

// basicAuth is the Authorization header value for g.
func (g GitAuth) basicAuth() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(g.Username+":"+g.Secret))
}
