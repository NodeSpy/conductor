package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// inspectRig is an inspected mirror (example.com, the test server's own
// name) behind a proxy whose allowlist gives it /@myorg/ only, plus a
// second, uninspected TLS server reached blind by its literal address.
type inspectRig struct {
	p        *Proxy
	in       *Inspector
	mirror   *httptest.Server
	blind    *httptest.Server
	proxyURL *url.URL
	mu       sync.Mutex
	recs     []ConnRecord
}

func (r *inspectRig) records() []ConnRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ConnRecord(nil), r.recs...)
}

func serverPEM(t *testing.T, s *httptest.Server) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(f, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func newInspectRig(t *testing.T, trustMirror bool) *inspectRig {
	t.Helper()
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" {
			c, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer c.Close()
			_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
			_ = rw.Flush()
			line, _ := rw.ReadString('\n')
			_, _ = rw.WriteString("echo:" + line)
			_ = rw.Flush()
			return
		}
		body, _ := io.ReadAll(r.Body)
		_, _ = io.WriteString(w, "upstream "+r.Method+" "+r.URL.RequestURI()+" host="+r.Host+" xff="+r.Header.Get("X-Forwarded-For")+" auth="+r.Header.Get("Authorization")+" body="+string(body))
	})
	rig := &inspectRig{mirror: httptest.NewTLSServer(echo), blind: httptest.NewTLSServer(echo)}
	t.Cleanup(rig.mirror.Close)
	t.Cleanup(rig.blind.Close)
	_, mport, _ := net.SplitHostPort(rig.mirror.Listener.Addr().String())
	host := InspectHost{Host: "example.com"}
	if trustMirror {
		host.CA = serverPEM(t, rig.mirror)
	}
	in, err := NewInspector([]InspectHost{host})
	if err != nil {
		t.Fatal(err)
	}
	rig.in = in
	rig.p = &Proxy{
		Allow: []string{"https://example.com:" + mport + "/@myorg/", rig.blind.Listener.Addr().String()},
		resolveIP: func(_ context.Context, h string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		},
		OnConn: func(c ConnRecord) { rig.mu.Lock(); rig.recs = append(rig.recs, c); rig.mu.Unlock() },
	}
	addr, err := rig.p.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rig.p.Close)
	cred, err := rig.p.MintSession(SessionOpts{Run: "r1", RequireTLS: true, Inspect: in, Limits: NoEgressLimits})
	if err != nil {
		t.Fatal(err)
	}
	rig.proxyURL, _ = url.Parse("http://" + proxyUser + ":" + cred + "@" + addr)
	return rig
}

// client trusts roots and goes through the proxy; h2 offers HTTP/2.
func (r *inspectRig) client(roots *x509.CertPool, h2 bool) *http.Client {
	tr := &http.Transport{Proxy: http.ProxyURL(r.proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots}, ForceAttemptHTTP2: h2}
	if !h2 {
		tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	}
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

func (r *inspectRig) mirrorURL(path string) string {
	_, port, _ := net.SplitHostPort(r.mirror.Listener.Addr().String())
	return "https://example.com:" + port + path
}

func caPool(pemBytes []byte) *x509.CertPool {
	p := x509.NewCertPool()
	p.AppendCertsFromPEM(pemBytes)
	return p
}

func get(t *testing.T, c *http.Client, u string, h http.Header) (int, string, *http.Response) {
	t.Helper()
	req, _ := http.NewRequest("GET", u, nil)
	for k, v := range h {
		req.Header[k] = v
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(b), resp
}

// TestInspectedPathsOnKeepAliveAndHTTP2: an allowed path passes, another is
// refused — the second request on one HTTP/1.1 keep-alive connection, and
// an HTTP/2 stream — each audited with method, host and path, never the
// query; the request reaches upstream as sent.
func TestInspectedPathsOnKeepAliveAndHTTP2(t *testing.T) {
	rig := newInspectRig(t, true)
	roots := caPool(rig.in.CAPEM())

	h1 := rig.client(roots, false)
	reused := false
	trace := &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) { reused = i.Reused }}
	code, body, resp := get(t, h1, rig.mirrorURL("/@myorg/pkg?token=s3cret"), http.Header{"Authorization": {"Bearer tok"}})
	if code != 200 || !strings.Contains(body, "upstream GET /@myorg/pkg?token=s3cret host=example.com") || !strings.Contains(body, "xff= ") || !strings.Contains(body, "auth=Bearer tok") {
		t.Fatalf("allowed path: %d %q", code, body)
	}
	if resp.ProtoMajor != 1 {
		t.Fatalf("h1 client got %s", resp.Proto)
	}
	req, _ := http.NewRequest("GET", rig.mirrorURL("/@other/pkg"), nil)
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	r2, err := h1.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != 403 || strings.Contains(string(b2), "upstream") || !reused {
		t.Fatalf("second keep-alive request: %d reused=%v %q", r2.StatusCode, reused, b2)
	}
	for _, p := range []string{"/@myorg/../@other/x", "/@myorg/%252e%252e/x", "/@myorg//x", "/@myorg/a%5Cb"} {
		if code, body, _ := get(t, h1, rig.mirrorURL(p), nil); code != 403 {
			t.Errorf("%s: %d %q", p, code, body)
		}
	}

	h2 := rig.client(roots, true)
	code, body, resp = get(t, h2, rig.mirrorURL("/@myorg/two"), nil)
	if code != 200 || resp.ProtoMajor != 2 || !strings.Contains(body, "upstream GET /@myorg/two") {
		t.Fatalf("h2 allowed: %d %s %q", code, resp.Proto, body)
	}
	if code, body, resp = get(t, h2, rig.mirrorURL("/elsewhere"), nil); code != 403 || resp.ProtoMajor != 2 {
		t.Fatalf("h2 refused stream: %d %s %q", code, resp.Proto, body)
	}

	var allowed, denied int
	for _, r := range rig.records() {
		js, _ := json.Marshal(r)
		if strings.Contains(string(js), "s3cret") || strings.Contains(string(js), "Bearer") {
			t.Fatalf("a record carries the query or a header: %s", js)
		}
		if r.Method == "" {
			continue
		}
		if !r.Inspected || r.SNI != "example.com" {
			t.Fatalf("request record: %+v", r)
		}
		switch {
		case r.Outcome == "closed" && strings.HasPrefix(r.Path, "/@myorg/"):
			allowed++
		case r.Outcome == "denied" && strings.Contains(r.Reason, "path"):
			denied++
		}
	}
	if allowed != 2 || denied != 6 {
		t.Fatalf("allowed %d denied %d: %+v", allowed, denied, rig.records())
	}
}

// TestUninspectedHostIsBlind: a host not in inspect is a plain tunnel — the
// client sees the server's own certificate, no inspection CA in the chain.
func TestUninspectedHostIsBlind(t *testing.T) {
	rig := newInspectRig(t, true)
	c := rig.client(caPool(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rig.blind.Certificate().Raw})), false)
	code, _, resp := get(t, c, rig.blind.URL+"/any/path", nil)
	if code != 200 {
		t.Fatalf("blind host: %d", code)
	}
	for _, cert := range resp.TLS.PeerCertificates {
		if cert.Issuer.CommonName == rig.in.caCert.Subject.CommonName || cert.CheckSignatureFrom(rig.in.caCert) == nil {
			t.Fatal("the inspection CA is in a blind tunnel's chain")
		}
	}
	if !resp.TLS.PeerCertificates[0].Equal(rig.blind.Certificate()) {
		t.Fatal("the blind tunnel did not show the server's own certificate")
	}
}

// TestInspectionCANamesOnlyItsHosts: the proxy mints no leaf for another
// host, and a leaf the CA's key signs for one fails a real Go client's
// verification, as does one for an IP address.
func TestInspectionCANamesOnlyItsHosts(t *testing.T) {
	in, err := NewInspector([]InspectHost{{Host: "npm.mirror.test"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.leaf("other.test"); err == nil {
		t.Fatal("a leaf was minted for a host not inspected")
	}
	// What leaves the process — the certificate and the bundle — holds no
	// part of the key.
	keyDER, _ := x509.MarshalECPrivateKey(in.caKey)
	secret := in.caKey.D.Bytes()
	bundle, err := in.Bundle()
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range [][]byte{in.CAPEM(), bundle} {
		if bytes.Contains(b, keyDER) || bytes.Contains(b, secret) || bytes.Contains(b, []byte("PRIVATE KEY")) {
			t.Fatal("the CA key left the inspector")
		}
	}
	if in.caCert.NotAfter.Sub(in.caCert.NotBefore) > InspectValidity+10*time.Minute || in.caCert.PublicKeyAlgorithm != x509.ECDSA {
		t.Fatalf("CA: %v..%v %v", in.caCert.NotBefore, in.caCert.NotAfter, in.caCert.PublicKeyAlgorithm)
	}
	forge := func(dns []string, ips []net.IP) tls.Certificate {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tmpl := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "forged"}, DNSNames: dns, IPAddresses: ips,
			NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, in.caCert, &key.PublicKey, in.caKey)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	}
	dial := func(cert tls.Certificate, name string) error {
		ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		go func() {
			if c, err := ln.Accept(); err == nil {
				_ = c.(*tls.Conn).Handshake()
				c.Close()
			}
		}()
		c, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{RootCAs: caPool(in.CAPEM()), ServerName: name})
		if err == nil {
			c.Close()
		}
		return err
	}
	ok, _ := in.leaf("npm.mirror.test")
	if err := dial(*ok, "npm.mirror.test"); err != nil {
		t.Fatalf("the inspected host's own leaf: %v", err)
	}
	var inv x509.CertificateInvalidError
	if err := dial(forge([]string{"registry.npmjs.org"}, nil), "registry.npmjs.org"); !errors.As(err, &inv) || inv.Reason != x509.CANotAuthorizedForThisName {
		t.Fatalf("a forged leaf for another host: %v", err)
	}
	if err := dial(forge(nil, []net.IP{net.ParseIP("127.0.0.1")}), "127.0.0.1"); !errors.As(err, &inv) || inv.Reason != x509.CANotAuthorizedForThisName {
		t.Fatalf("a forged leaf for an address: %v", err)
	}
}

// TestInspectedUpstreamBadCertRefused: an upstream whose certificate does
// not verify (no ca: for it) is refused and audited, never relayed.
func TestInspectedUpstreamBadCertRefused(t *testing.T) {
	rig := newInspectRig(t, false)
	code, body, _ := get(t, rig.client(caPool(rig.in.CAPEM()), false), rig.mirrorURL("/@myorg/pkg"), nil)
	if code != http.StatusBadGateway || strings.Contains(body, "upstream GET") {
		t.Fatalf("untrusted upstream: %d %q", code, body)
	}
	for _, r := range rig.records() {
		if r.Method == "GET" && r.Outcome == "refused" && strings.HasPrefix(r.Reason, "upstream certificate refused") {
			return
		}
	}
	t.Fatalf("no refusal audited: %+v", rig.records())
}

// TestInspectedClientRefusingTheCA: a client that does not trust the
// launch's CA (a pinned one) fails, audited as rejecting it.
func TestInspectedClientRefusingTheCA(t *testing.T) {
	rig := newInspectRig(t, true)
	if _, err := rig.client(x509.NewCertPool(), false).Get(rig.mirrorURL("/@myorg/pkg")); err == nil {
		t.Fatal("a client without the CA connected")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, r := range rig.records() {
			if r.Inspected && strings.HasPrefix(r.Reason, "client rejected inspection CA") {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("not audited: %+v", rig.records())
}

// TestInspectedWebsocketFollowsThePath: an upgrade passes on an allowed
// path only.
func TestInspectedWebsocketFollowsThePath(t *testing.T) {
	rig := newInspectRig(t, true)
	try := func(path string) (string, error) {
		pc, err := net.Dial("tcp", rig.proxyURL.Host)
		if err != nil {
			t.Fatal(err)
		}
		defer pc.Close()
		_, port, _ := net.SplitHostPort(rig.mirror.Listener.Addr().String())
		target := "example.com:" + port
		pw, _ := rig.proxyURL.User.Password()
		auth := "Basic " + basicAuth(proxyUser, pw)
		_, _ = io.WriteString(pc, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\nProxy-Authorization: "+auth+"\r\n\r\n")
		br := bufio.NewReader(pc)
		if resp, err := http.ReadResponse(br, nil); err != nil || resp.StatusCode != 200 {
			t.Fatalf("CONNECT: %v", err)
		}
		tc := tls.Client(&prefixConn{Conn: pc, pre: drain(br)}, &tls.Config{RootCAs: caPool(rig.in.CAPEM()), ServerName: "example.com", NextProtos: []string{"http/1.1"}})
		_ = tc.SetDeadline(time.Now().Add(5 * time.Second))
		_, _ = io.WriteString(tc, "GET "+path+" HTTP/1.1\r\nHost: example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		tr := bufio.NewReader(tc)
		resp, err := http.ReadResponse(tr, nil)
		if err != nil {
			return "", err
		}
		if resp.StatusCode != 101 {
			return resp.Status, nil
		}
		_, _ = io.WriteString(tc, "hello\n")
		line, err := tr.ReadString('\n')
		return line, err
	}
	if got, err := try("/@myorg/ws"); err != nil || got != "echo:hello\n" {
		t.Fatalf("allowed upgrade: %q %v %+v", got, err, rig.records())
	}
	if got, err := try("/ws"); err != nil || !strings.HasPrefix(got, "403") {
		t.Fatalf("refused upgrade: %q %v", got, err)
	}
}

func basicAuth(u, p string) string {
	req, _ := http.NewRequest("GET", "http://x", nil)
	req.SetBasicAuth(u, p)
	return strings.TrimPrefix(req.Header.Get("Authorization"), "Basic ")
}

func drain(br *bufio.Reader) []byte {
	b, _ := br.Peek(br.Buffered())
	return append([]byte(nil), b...)
}

// TestNewInspectorRefusesWildcards: only exact names are inspected.
func TestNewInspectorRefusesWildcards(t *testing.T) {
	for _, h := range []string{"*.example.com", "example.*", "10.0.0.1", "example.com:443", ".example.com", ""} {
		if _, err := NewInspector([]InspectHost{{Host: h}}); err == nil {
			t.Errorf("%q accepted", h)
		}
	}
}
