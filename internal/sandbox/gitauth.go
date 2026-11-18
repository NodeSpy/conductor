package sandbox

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"path"
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
// only — for a repository in scope it adds the credential itself. The
// credential never enters the sandbox: what an agent can do with it is
// fetch the repositories in scope, through this proxy, while its launch
// lasts. A push (receive-pack) to a declared host is refused outright —
// conductor pushes, never the agent.

// GitAuth is one host's git fetch credential for a launch.
type GitAuth struct {
	// Host is an exact host name, with :port when not 443.
	Host string `json:"host"`
	// Repos are the repositories on Host the credential is used for
	// ("/org/repo"), each matched exactly — case-insensitively, as forges
	// name them.
	Repos []string `json:"repos"`
	// For is the repository the launch's work is for (owner/name): what a
	// refusal names.
	For string `json:"for,omitempty"`
	// Username is the HTTP basic username sent with Secret.
	Username string `json:"username"`
	// Secret is the credential. Never serialized.
	Secret string `json:"-"`
	// Learn: a default-branch cache warm's credential, used for a fetch
	// of any repository on Host (Repos unused); OnFetch hears each one the
	// forge served. Never serialized: no launch but a warm in this process
	// holds one.
	Learn   bool              `json:"-"`
	OnFetch func(repo string) `json:"-"`
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
	if len(g.Repos) == 0 && !g.Learn {
		return fmt.Errorf("sandbox: git credential for %s: no repositories in scope", g.Host)
	}
	for _, r := range g.Repos {
		if !strings.HasPrefix(r, "/") || r == "/" || path.Clean(r) != r {
			return fmt.Errorf("sandbox: git credential for %s: repository %q is not a clean path below /", g.Host, r)
		}
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
	return op, strings.TrimSuffix(repo, ".git")
}

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
// (nil: none covers it). Repository paths compare case-insensitively.
func (s *Session) gitCredentialFor(target, repo string) *GitAuth {
	if repo == "" {
		return nil
	}
	lr := strings.ToLower(repo)
	for _, g := range s.gitAuths(target) {
		if g.Learn {
			return &g
		}
		for _, r := range g.Repos {
			if strings.ToLower(r) == lr {
				return &g
			}
		}
	}
	return nil
}

// gitWithheld is why a fetch of repo on target carries no credential.
func (s *Session) gitWithheld(target, repo string) string {
	of := ""
	for _, g := range s.gitAuths(target) {
		if g.For != "" {
			of = g.For
			break
		}
	}
	if of == "" {
		return "git credential withheld for " + strings.TrimPrefix(repo, "/") + ": not in this launch's scope"
	}
	return "git credential withheld for " + strings.TrimPrefix(repo, "/") + ": not a learned dependency of " + of +
		"; it is learned when the default branch's cache warm fetches it"
}

// basicAuth is the Authorization header value for g.
func (g GitAuth) basicAuth() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(g.Username+":"+g.Secret))
}
