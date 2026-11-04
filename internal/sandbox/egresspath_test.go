package sandbox

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRequestPathAndPrefixes(t *testing.T) {
	for raw, want := range map[string]string{
		"/github.com/myorg/repo/@v/list": "",
		"/github.com%2fmyorg%2frepo":     "",
		"/a/../b":                        "a dot segment in the path",
		"/a/%2e%2e/b":                    "a dot segment in the path",
		"/a/./b":                         "a dot segment in the path",
		"/a//b":                          "an empty segment (//) in the path",
		"/a/%252e%252e/b":                "a percent sign left after decoding (double encoding)",
		"/a%5c..%5cb":                    "a backslash or NUL in the path",
		"/a%00b":                         "a backslash or NUL in the path",
		"/dir/":                          "",
	} {
		u, err := url.Parse("http://h" + raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if _, why := requestPath(u); why != want {
			t.Errorf("%s: %q, want %q", raw, why, want)
		}
	}
	for _, c := range []struct {
		p, prefix string
		ok        bool
	}{
		{"/github.com/myorg/x", "/github.com/myorg/", true},
		{"/github.com/myorgx/x", "/github.com/myorg/", false},
		{"/github.com/myorg", "/github.com/myorg/", false},
		{"/@myorg/pkg", "/@myorg", true},
		{"/@myorg", "/@myorg", true},
		{"/@myorgevil", "/@myorg", false},
		{"/anything", "/", true},
	} {
		if got := pathAllowed(c.p, []string{c.prefix}); got != c.ok {
			t.Errorf("%s under %s: %v", c.p, c.prefix, got)
		}
	}
	for e, want := range map[string]string{
		"http://Athens.lan:3000/github.com/myorg/": "athens.lan:3000 /github.com/myorg/",
		"http://verdaccio/@myorg/":                 "verdaccio:80 /@myorg/",
		"http://devpi.lan:3141":                    "devpi.lan:3141 /",
	} {
		hp, prefix, ok := pathEntry(e)
		if !ok || hp+" "+prefix != want {
			t.Errorf("%s: %s %s %v", e, hp, prefix, ok)
		}
	}
	// A path entry is not a host entry; an http:// URL with no path is.
	if EgressAllowed([]string{"http://athens.lan:3000/github.com/myorg/"}, "athens.lan:3000") {
		t.Fatal("a path entry allowed its host at every path")
	}
	if !EgressAllowed([]string{"http://devpi.lan:3141/"}, "devpi.lan:3141") {
		t.Fatal("a pathless http:// entry did not allow its host")
	}
}

// A path-restricted mirror: each plain-HTTP request is checked — the
// second on a keep-alive connection too — escape attempts are refused,
// CONNECT to it is refused, and allowed requests are audited with their
// method and path (never the query).
func TestPlainHTTPPathRules(t *testing.T) {
	var seen []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.EscapedPath())
		fmt.Fprint(w, "ok")
	}))
	defer up.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(up.URL, "http://"))
	host := "athens.lan:" + port
	p := &Proxy{Allow: []string{"http://" + host + "/github.com/myorg/", "http://" + host + "/@myorg/"},
		resolveIP: func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		}}
	addr, rec := startProxy(t, p)
	cred, _ := p.MintSession(SessionOpts{Run: "run-path", RequireTLS: true})
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	auth := "Basic " + b64(proxyUser+":"+cred)
	// do sends one request on the shared keep-alive connection.
	do := func(method, rawPath string) int {
		t.Helper()
		fmt.Fprintf(conn, "%s http://%s%s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n", method, host, rawPath, host, auth)
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatalf("%s %s: %v", method, rawPath, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, c := range []struct {
		path string
		code int
	}{
		{"/github.com/myorg/repo/@v/list?x=secret", 200},
		{"/github.com/other/repo/@v/list", 403}, // the second request on the connection
		{"/@myorg%2fpkg", 200},
		{"/@myorg/pkg/-/pkg-1.0.0.tgz", 200},
		{"/github.com/myorg/../other/x", 403},
		{"/github.com/myorg/%2e%2e/other/x", 403},
		{"/github.com/myorg/%252e%252e/other", 403},
		{"/github.com/myorg//x", 403},
		{"/github.com/myorgevil/x", 403},
		{"/@myorgevil/pkg", 403},
		{"/github.com/myorg/repo/@v/v1.0.0.zip", 200}, // and the connection still serves
	} {
		if got := do("GET", c.path); got != c.code {
			t.Errorf("GET %s: %d, want %d", c.path, got, c.code)
		}
	}
	for _, s := range seen {
		if !strings.HasPrefix(s, "/github.com/myorg/") && !strings.HasPrefix(s, "/@myorg") {
			t.Fatalf("a refused path reached the mirror: %v", seen)
		}
	}
	r := rec.wait(t, func(c ConnRecord) bool { return c.Outcome == "closed" && c.Method == "GET" })
	if r.Path != "/github.com/myorg/repo/@v/list" || strings.Contains(r.Path, "secret") {
		t.Fatalf("audited path %q", r.Path)
	}
	rec.wait(t, func(c ConnRecord) bool {
		return c.Outcome == "denied" && c.Path == "/github.com/other/repo/@v/list" && strings.Contains(c.Reason, "not under an allowed path")
	})
	// A tunnel's requests would pass unread: CONNECT to it is refused.
	c, _, code := connectVia(t, addr, cred, host)
	c.Close()
	if code != http.StatusForbidden {
		t.Fatalf("CONNECT to a path-restricted host: %d", code)
	}
	rec.wait(t, func(c ConnRecord) bool { return strings.Contains(c.Reason, "path-restricted") })
	// A host-only entry for the same host: any path.
	p2 := &Proxy{Allow: []string{host}, resolveIP: p.resolveIP}
	addr2, _ := startProxy(t, p2)
	resp, err := viaProxy(p2, addr2).Get("http://" + host + "/anything/at/all")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("host-only entry: %v %v", resp, err)
	}
	resp.Body.Close()
}
