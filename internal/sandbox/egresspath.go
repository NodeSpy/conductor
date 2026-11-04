package sandbox

import (
	"net/url"
	"path"
	"strings"
)

// Path rules. An egress entry may be a URL with a path prefix —
// `http://athens.lan:3000/github.com/myorg/` — where a host-only entry
// allows any path.
//
// Plain HTTP (http://) is read by the proxy, so the prefix is enforced on
// every request: each absolute-URI request through the proxy is checked on
// its own (keep-alive and pipelining included, since the proxy parses and
// re-frames each one), and a CONNECT tunnel to a host that only http://
// entries allow is refused (its requests would pass unread).
//
// HTTPS (https://) paths are enforced only for a host the launch inspects
// (Inspector: conductor terminates its TLS with a per-launch CA); for any
// other host a CONNECT is a blind tunnel, host:port only, and a host that
// only https:// path entries allow is refused.

// pathEntry parses an http:// or https:// egress entry: its scheme,
// host:port (port 80 or 443 by default) and path prefix ("/" for any
// path). ok is false for anything that is not such a URL.
func pathEntry(e string) (scheme, hostport, prefix string, ok bool) {
	e = strings.TrimSpace(e)
	low := strings.ToLower(e)
	switch {
	case strings.HasPrefix(low, "http://"):
		scheme = "http"
	case strings.HasPrefix(low, "https://"):
		scheme = "https"
	default:
		return "", "", "", false
	}
	u, err := url.Parse(e)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", "", false
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[scheme]
	}
	prefix = u.Path
	if prefix == "" {
		prefix = "/"
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme, host + ":" + port, prefix, true
}

// entryHostPort is an entry's host:port pattern: a URL entry's host:port,
// any other entry as written.
func entryHostPort(e string) string {
	if _, hp, _, ok := pathEntry(e); ok {
		return hp
	}
	return e
}

// pathPrefixes are the path prefixes allow gives target (host:port) for
// scheme ("http" or "https"); nil when no entry of that scheme names it.
func pathPrefixes(allow []string, scheme, target string) []string {
	var out []string
	for _, e := range allow {
		sc, hp, prefix, ok := pathEntry(e)
		if ok && sc == scheme && EgressAllowed([]string{hp}, target) {
			out = append(out, prefix)
		}
	}
	return out
}

// requestPath is a plain-HTTP request's path as the prefix check reads it
// — decoded once — or why it is refused: a path whose meaning depends on
// who normalizes it (dot segments, empty segments, a backslash, a NUL, or
// a percent sign left after decoding) is ambiguous.
func requestPath(u *url.URL) (string, string) {
	p := u.Path
	switch {
	case !strings.HasPrefix(p, "/"):
		return "", "not an absolute path"
	case strings.ContainsAny(p, "\\\x00"):
		return "", "a backslash or NUL in the path"
	case strings.Contains(p, "%"):
		return "", "a percent sign left after decoding (double encoding)"
	}
	segs := strings.Split(p[1:], "/")
	for i, s := range segs {
		switch {
		case s == "." || s == "..":
			return "", "a dot segment in the path"
		case s == "" && i != len(segs)-1:
			return "", "an empty segment (//) in the path"
		}
	}
	return p, ""
}

// underPrefix reports p within prefix: "/" is every path; one ending in /
// is that directory; any other is itself and what lies below it.
func underPrefix(p, prefix string) bool {
	switch {
	case prefix == "/" || p == prefix:
		return true
	case strings.HasSuffix(prefix, "/"):
		return strings.HasPrefix(p, prefix)
	}
	return strings.HasPrefix(p, prefix+"/")
}

// pathAllowed reports p under one of prefixes.
func pathAllowed(p string, prefixes []string) bool {
	for _, pre := range prefixes {
		if underPrefix(p, path.Clean("/"+pre)+trailingSlash(pre)) {
			return true
		}
	}
	return false
}

func trailingSlash(s string) string {
	if strings.HasSuffix(s, "/") && s != "/" {
		return "/"
	}
	return ""
}
