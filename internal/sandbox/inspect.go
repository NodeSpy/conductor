package sandbox

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/http2"
)

// TLS inspection, opt in per host (sandbox.inspect / isolation.inspect).
//
// For an inspected host the egress proxy terminates the client's TLS with
// a leaf minted by a per-launch CA, reads each HTTP request, enforces the
// launch's https:// path entries on every one (HTTP/1.1 keep-alive and
// HTTP/2 streams alike), and forwards it upstream over a fresh TLS
// connection verified against the system roots plus the host's own `ca:`
// file. Every other host stays a blind tunnel.
//
// The CA is ECDSA P-256, valid for InspectValidity, name-constrained to
// the inspected hosts (no IP addresses), and its key lives only in this
// process's memory: the sandbox is shown its certificate, never the key.

// InspectHost is one host whose TLS a launch's proxy terminates.
type InspectHost struct {
	Host string `json:"host"`
	// CA is a PEM file of extra roots the upstream's certificate may chain
	// to (a private mirror's own CA), on top of the system roots.
	CA string `json:"ca,omitempty"`
}

// InspectValidity is how long a launch's inspection CA (and its leaves)
// stays valid.
var InspectValidity = 24 * time.Hour

// SystemBundlePaths are where a PEM bundle of the system's roots is looked
// for, after $SSL_CERT_FILE (Debian, Fedora/RHEL, openSUSE, Alpine, macOS).
var SystemBundlePaths = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
	"/etc/ssl/ca-bundle.pem",
	"/etc/ssl/cert.pem",
}

// InspectCAEnv are the variables a launch that inspects gets, each naming
// the bundle (system roots plus the launch's CA).
var InspectCAEnv = []string{"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE", "GIT_SSL_CAINFO"}

// Inspector is one launch's inspection CA and the hosts it covers.
type Inspector struct {
	roots  map[string]*x509.CertPool // upstream roots, by lower-case host
	caDER  []byte
	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey

	mu     sync.Mutex
	leaves map[string]*tls.Certificate
}

// NewInspector mints a launch's CA for hosts (exact names: no wildcard, no
// IP address, no port) and reads each host's extra upstream roots.
func NewInspector(hosts []InspectHost) (*Inspector, error) {
	if len(hosts) == 0 {
		return nil, errors.New("sandbox: inspect: no hosts")
	}
	sys, err := x509.SystemCertPool()
	if err != nil || sys == nil {
		sys = x509.NewCertPool()
	}
	in := &Inspector{roots: map[string]*x509.CertPool{}, leaves: map[string]*tls.Certificate{}}
	var names []string
	for _, h := range hosts {
		name := strings.ToLower(strings.TrimSpace(h.Host))
		if !inspectableName(name) {
			return nil, fmt.Errorf("sandbox: inspect: %q: want an exact hostname (no wildcard, address or port)", h.Host)
		}
		pool := sys.Clone()
		if h.CA != "" {
			b, err := os.ReadFile(h.CA)
			if err != nil {
				return nil, fmt.Errorf("sandbox: inspect %s: ca: %w", name, err)
			}
			if !pool.AppendCertsFromPEM(b) {
				return nil, fmt.Errorf("sandbox: inspect %s: ca: %s holds no PEM certificate", name, h.CA)
			}
		}
		if _, dup := in.roots[name]; !dup {
			names = append(names, name)
		}
		in.roots[name] = pool
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("sandbox: inspect: CA key: %w", err)
	}
	now := time.Now()
	_, all4, _ := net.ParseCIDR("0.0.0.0/0")
	_, all6, _ := net.ParseCIDR("::/0")
	tmpl := &x509.Certificate{
		SerialNumber:                serial(),
		Subject:                     pkix.Name{CommonName: "conductor sandbox inspection CA (one launch)"},
		NotBefore:                   now.Add(-5 * time.Minute),
		NotAfter:                    now.Add(InspectValidity),
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:                 []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid:       true,
		IsCA:                        true,
		MaxPathLenZero:              true,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         names,
		ExcludedIPRanges:            []*net.IPNet{all4, all6},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("sandbox: inspect: CA: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	in.caDER, in.caCert, in.caKey = der, cert, key
	return in, nil
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return n
}

// inspectableName reports an exact DNS name: no wildcard, address or port.
func inspectableName(h string) bool {
	if h == "" || strings.HasPrefix(h, ".") || strings.HasSuffix(h, ".") || strings.Contains(h, "..") || net.ParseIP(h) != nil {
		return false
	}
	for _, c := range h {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

// Covers reports host (no port) as one this launch inspects.
func (in *Inspector) Covers(host string) bool {
	if in == nil {
		return false
	}
	_, ok := in.roots[strings.ToLower(host)]
	return ok
}

// CAPEM is the launch CA's certificate (never its key).
func (in *Inspector) CAPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: in.caDER})
}

// Bundle is the system roots followed by the launch's CA: what the
// sandbox's TLS clients are pointed at.
func (in *Inspector) Bundle() ([]byte, error) {
	paths := SystemBundlePaths
	if f := os.Getenv("SSL_CERT_FILE"); f != "" {
		paths = append([]string{f}, paths...)
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil || !strings.Contains(string(b), "-----BEGIN CERTIFICATE-----") {
			continue
		}
		if !strings.HasSuffix(string(b), "\n") {
			b = append(b, '\n')
		}
		return append(b, in.CAPEM()...), nil
	}
	return nil, fmt.Errorf("sandbox: inspect: no system CA bundle found (looked in %s)", strings.Join(paths, ", "))
}

// leaf is the certificate the proxy presents for host, minted once per
// launch.
func (in *Inspector) leaf(host string) (*tls.Certificate, error) {
	host = strings.ToLower(host)
	if !in.Covers(host) {
		return nil, fmt.Errorf("sandbox: inspect: %s is not an inspected host", host)
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if c := in.leaves[host]; c != nil {
		return c, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    in.caCert.NotBefore,
		NotAfter:     in.caCert.NotAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, in.caCert, &key.PublicKey, in.caKey)
	if err != nil {
		return nil, err
	}
	c := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	in.leaves[host] = c
	return c, nil
}

// upstreamTLS verifies host's upstream: the system roots plus its ca:,
// verification never off.
func (in *Inspector) upstreamTLS(host string) *tls.Config {
	return &tls.Config{ServerName: host, RootCAs: in.roots[strings.ToLower(host)], MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}}
}

// Protocols an inspected request is recorded with.
const (
	ProtoInspectedH1 = "https/1.1"
	ProtoInspectedH2 = "https/2"
)

// prefixConn replays pre before reading from Conn: the ClientHello the
// protocol check already read.
type prefixConn struct {
	net.Conn
	pre []byte
}

func (c *prefixConn) Read(b []byte) (int, error) {
	if len(c.pre) > 0 {
		n := copy(b, c.pre)
		c.pre = c.pre[n:]
		return n, nil
	}
	return c.Conn.Read(b)
}

// oneConnListener hands out one connection, then waits until closed.
type oneConnListener struct {
	conn net.Conn
	once sync.Once
	done chan struct{}
	shut sync.Once
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	var c net.Conn
	l.once.Do(func() { c = l.conn })
	if c != nil {
		return c, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *oneConnListener) Close() error   { l.shut.Do(func() { close(l.done) }); return nil }
func (l *oneConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

// handshakeWait bounds the client's TLS handshake with the proxy.
var handshakeWait = 30 * time.Second

// serveInspected terminates the client's TLS on conn (the tunnel's inner
// end) for host and serves its requests until the connection ends.
// prefixes nil allows every path.
func (p *Proxy) serveInspected(conn net.Conn, sess *Session, target, host string, ips []net.IP, port string, prefixes []string) {
	in := sess.inspect
	tc := tls.Server(conn, &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(hi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if !strings.EqualFold(hi.ServerName, host) {
				return nil, fmt.Errorf("server name %q is not %s", hi.ServerName, host)
			}
			return in.leaf(host)
		},
	})
	_ = tc.SetDeadline(time.Now().Add(handshakeWait))
	if err := tc.Handshake(); err != nil {
		p.record(ConnRecord{Run: sess.Run, Target: target, SNI: host, Proto: ProtoTLS, Inspected: true, Outcome: "refused", Reason: handshakeReason(err)})
		conn.Close()
		return
	}
	_ = tc.SetDeadline(time.Time{})
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialIPs(ctx, ips, port, 30*time.Second, target)
		},
		TLSClientConfig:     in.upstreamTLS(host),
		TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{}, // HTTP/1.1 upstream
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 30 * time.Second,
	}
	defer tr.CloseIdleConnections()
	h := &inspectHandler{p: p, sess: sess, target: target, host: host, prefixes: prefixes, tr: tr, proto: ProtoInspectedH1}
	if tc.ConnectionState().NegotiatedProtocol == "h2" {
		h.proto = ProtoInspectedH2
		(&http2.Server{}).ServeConn(tc, &http2.ServeConnOpts{Handler: h})
		tc.Close()
		return
	}
	// The server closes the connection itself; a hijacked one (an upgrade)
	// lives on with the reverse proxy until either end closes.
	ln := &oneConnListener{conn: tc, done: make(chan struct{})}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 30 * time.Second, ErrorLog: log.New(io.Discard, "", 0),
		ConnState: func(_ net.Conn, s http.ConnState) {
			if s == http.StateClosed || s == http.StateHijacked {
				ln.Close()
			}
		}}
	_ = srv.Serve(ln)
}

// handshakeReason names a failed client handshake; a client that refuses
// the launch's CA (a pinned or platform-store client) is called out.
func handshakeReason(err error) string {
	s := err.Error()
	for _, a := range []string{"bad certificate", "unknown certificate authority", "unknown certificate", "certificate unknown", "unsupported certificate"} {
		if strings.Contains(s, "remote error: tls: "+a) {
			return "client rejected inspection CA (" + a + ")"
		}
	}
	if errors.Is(err, io.EOF) || strings.Contains(s, "EOF") || strings.Contains(s, "reset by peer") {
		return "client rejected inspection CA (it closed the handshake)"
	}
	return "TLS handshake with the client: " + s
}

// inspectHandler serves one inspected connection's requests.
type inspectHandler struct {
	p            *Proxy
	sess         *Session
	target, host string
	prefixes     []string
	tr           *http.Transport
	proto        string
}

func (h *inspectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := ConnRecord{Run: h.sess.Run, Target: h.target, SNI: h.host, Proto: h.proto, Inspected: true, Method: r.Method, Path: r.URL.Path}
	refuse := func(code int, outcome, why string) {
		rec.Outcome, rec.Reason, rec.Duration = outcome, why, time.Since(start)
		h.p.record(rec)
		http.Error(w, fmt.Sprintf("sandbox egress proxy: %s %s%s refused: %s", r.Method, h.host, r.URL.Path, why), code)
	}
	if why := h.sess.refused(); why != "" {
		refuse(http.StatusForbidden, "refused", why)
		return
	}
	reqHost, _ := splitHostPort(r.Host)
	if r.Host == "" || !strings.EqualFold(strings.TrimSuffix(reqHost, "."), h.host) || (r.URL.Host != "" && !strings.EqualFold(r.URL.Hostname(), h.host)) {
		refuse(http.StatusMisdirectedRequest, "refused", "the request names another host ("+r.Host+") than its TLS server name")
		return
	}
	if r.Method == http.MethodConnect {
		refuse(http.StatusMethodNotAllowed, "refused", "CONNECT inside an inspected connection")
		return
	}
	if h.prefixes != nil {
		np, why := requestPath(r.URL)
		if why == "" && !pathAllowed(np, h.prefixes) {
			why = "not under an allowed path (" + strings.Join(h.prefixes, ", ") + ")"
		}
		if why != "" {
			if h.p.OnDeny != nil {
				h.p.OnDeny(h.target + r.URL.Path)
			}
			refuse(http.StatusForbidden, "denied", "path "+why)
			return
		}
	}
	var up, down atomic.Int64
	var failed string
	if r.Body != nil && r.Body != http.NoBody {
		r.Body = &countingBody{r: r.Body, n: &up}
	}
	// No X-Forwarded-For: the request goes on as the client sent it.
	r.RemoteAddr = ""
	rp := &httputil.ReverseProxy{
		Director: func(o *http.Request) {
			o.URL.Scheme, o.URL.Host = "https", h.target
			o.Host = r.Host
		},
		Transport:     h.tr,
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode != http.StatusSwitchingProtocols && resp.Body != nil {
				resp.Body = &countingBody{r: resp.Body, n: &down}
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			var cv *tls.CertificateVerificationError
			if errors.As(err, &cv) {
				failed = "upstream certificate refused: " + cv.Err.Error()
			} else {
				failed = err.Error()
			}
			http.Error(w, "sandbox egress proxy: "+h.host+": "+failed, http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
	rec.Up, rec.Down, rec.Duration = up.Load(), down.Load(), time.Since(start)
	rec.Outcome = "closed"
	if failed != "" {
		rec.Outcome, rec.Reason = "refused", failed
	}
	h.p.record(rec)
}
