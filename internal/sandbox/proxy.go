// The conductor-enforced egress path (#36 §15): a loopback HTTP forward
// proxy per distinct allowlist. Launched runtimes get HTTP(S)_PROXY pointed
// at it; CONNECT tunnels and absolute-URI requests are matched against the
// allowlist and denied with 403 (and an audit callback) when they don't.
// This is how conductor polices network egress for the runtimes it launches
// without owning a firewall: the filter runs inside conductor's own process.
package sandbox

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Proxy is one egress-filtering forward proxy bound to 127.0.0.1.
//
// Clients must authenticate: the proxy is a host-wide loopback listener, so
// without auth ANY local process could ride an allowlisted profile's egress
// (#36 iso-review M9). Each dispatch gets its own credential (MintCred),
// injected only into that launch's proxy env; a request without a valid
// Proxy-Authorization is refused with 407 before any target matching.
type Proxy struct {
	Allow []string // EgressAllowed patterns; empty = deny everything
	// OnDeny is called (if non-nil) with the denied host:port — the audit hook.
	OnDeny func(hostport string)

	ln   net.Listener
	srv  *http.Server
	tr   *http.Transport // plain-HTTP forwarding transport (SSRF-checked DialContext)
	ulns []net.Listener  // additional unix listeners (enforced-egress path)

	// resolveIP looks a hostname up; overridable in tests. nil ⇒ the system
	// resolver. It exists so the post-resolution SSRF check (dialAllowed) can
	// be exercised deterministically without depending on real DNS.
	resolveIP func(ctx context.Context, host string) ([]net.IPAddr, error)

	credMu sync.Mutex
	creds  map[string]bool // valid per-dispatch credentials (the basic-auth password)
}

// errEgressBlockedIP is returned by dialAllowed when every resolved address is
// in a special range (loopback / link-local incl. cloud metadata / private /
// unspecified / multicast) and none was explicitly allowlisted by literal IP —
// an SSRF or DNS-rebinding attempt. The handler surfaces it as a 403 + audit,
// exactly like an allowlist miss, rather than a 502.
var errEgressBlockedIP = errors.New("resolved address is in a blocked range")

// specialIP reports whether ip sits in a range the enforced-egress proxy must
// not reach unless the operator listed that exact IP literally (#36
// iso-review, SSRF round 2). Resolution happens daemon-side, so an
// allowlisted *name* that resolves to 169.254.169.254 (cloud metadata),
// loopback, RFC1918/ULA private space, or link-local would otherwise be a
// rebinding gateway straight off the daemon's own network.
func specialIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsPrivate()
}

// explicitIPAllow returns the exact IPs the operator listed literally in the
// allowlist (the host half parses as an IP). ONLY these opt a special-range
// address back in — a hostname or glob never can, which is what closes the
// DNS-rebinding path: the name may be allowlisted, but the internal address it
// resolves to is not, so the connection is refused. A deliberately-internal
// target (a metadata endpoint, an RFC1918 host) is reachable only by listing
// its IP outright.
func explicitIPAllow(allow []string) map[string]bool {
	out := map[string]bool{}
	for _, pat := range allow {
		pat = strings.TrimSpace(pat)
		if pat == "" || pat == "*" {
			continue
		}
		host, _ := splitHostPort(pat)
		if ip := net.ParseIP(host); ip != nil {
			out[ip.String()] = true
		}
	}
	return out
}

// lookup resolves host through the injected resolver (tests) or the system one.
func (p *Proxy) lookup(ctx context.Context, host string) ([]net.IPAddr, error) {
	if p.resolveIP != nil {
		return p.resolveIP(ctx, host)
	}
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

// dialAllowed resolves hostport ITSELF and dials the resolved IP directly, so
// the address checked is the address connected to — no re-resolve TOCTOU. Any
// resolved IP in a special range is skipped unless the operator allowlisted
// that exact IP literally; if every candidate is blocked that way, it returns
// errEgressBlockedIP so the caller can 403 + audit.
func (p *Proxy) dialAllowed(ctx context.Context, hostport string, timeout time.Duration) (net.Conn, error) {
	host, port := splitHostPort(hostport)
	if port == "" {
		return nil, fmt.Errorf("dial %s: missing port", hostport)
	}
	explicit := explicitIPAllow(p.Allow)
	ips, err := p.lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	var blocked bool
	var d net.Dialer
	var lastErr error
	for _, ipa := range ips {
		if specialIP(ipa.IP) && !explicit[ipa.IP.String()] {
			blocked = true
			continue
		}
		dctx, cancel := context.WithTimeout(ctx, timeout)
		conn, err := d.DialContext(dctx, "tcp", net.JoinHostPort(ipa.IP.String(), port))
		cancel()
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if blocked {
		return nil, errEgressBlockedIP
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("dial %s: no addresses resolved", hostport)
}

// proxyUser is the fixed basic-auth username; the per-dispatch credential is
// the password half.
const proxyUser = "conductor"

// MintCred registers and returns a fresh per-dispatch client credential.
func (p *Proxy) MintCred() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("sandbox: mint proxy credential: %w", err)
	}
	cred := hex.EncodeToString(buf)
	p.credMu.Lock()
	if p.creds == nil {
		p.creds = map[string]bool{}
	}
	p.creds[cred] = true
	p.credMu.Unlock()
	return cred, nil
}

// authorized checks the request's Proxy-Authorization against the registered
// per-dispatch credentials. No registered credentials ⇒ nothing authorizes
// (fail closed).
func (p *Proxy) authorized(r *http.Request) bool {
	h := r.Header.Get("Proxy-Authorization")
	scheme, b64, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Basic") {
		return false
	}
	dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return false
	}
	user, pass, ok := strings.Cut(string(dec), ":")
	if !ok || user != proxyUser {
		return false
	}
	p.credMu.Lock()
	defer p.credMu.Unlock()
	for c := range p.creds {
		if subtle.ConstantTimeCompare([]byte(c), []byte(pass)) == 1 {
			return true
		}
	}
	return false
}

// requireAuth answers 407 when the request carries no valid credential.
func (p *Proxy) requireAuth(w http.ResponseWriter, r *http.Request) bool {
	if p.authorized(r) {
		return true
	}
	w.Header().Set("Proxy-Authenticate", `Basic realm="conductor egress"`)
	http.Error(w, "sandbox egress proxy: proxy authentication required", http.StatusProxyAuthRequired)
	return false
}

// Start listens on a fresh loopback port and serves until Close. It returns
// the proxy's address ("127.0.0.1:PORT").
func (p *Proxy) Start() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("sandbox: egress proxy listen: %w", err)
	}
	p.ln = ln
	// The plain-HTTP forwarding transport dials through dialAllowed, so the
	// absolute-URI path gets the SAME post-resolution SSRF check as CONNECT —
	// resolution happens once, in dialAllowed, and the transport connects to the
	// checked IP (no re-resolve TOCTOU).
	p.tr = &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return p.dialAllowed(ctx, addr, 30*time.Second)
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	p.srv = &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() { _ = p.srv.Serve(ln) }()
	return ln.Addr().String(), nil
}

// ServeUnix attaches a unix-socket listener serving the same filtered proxy
// — the daemon-side end of the enforced-egress forwarder (#36 iso-review
// C1). The socket file is 0600: only the daemon's own user reaches it, and
// the per-dispatch credential still applies on top.
func (p *Proxy) ServeUnix(path string) error {
	if p.srv == nil {
		return fmt.Errorf("sandbox: egress proxy not started")
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("sandbox: egress proxy unix listen: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return fmt.Errorf("sandbox: egress proxy socket perms: %w", err)
	}
	p.ulns = append(p.ulns, ln)
	go func() { _ = p.srv.Serve(ln) }()
	return nil
}

// Close shuts the proxy down.
func (p *Proxy) Close() {
	if p.tr != nil {
		p.tr.CloseIdleConnections()
	}
	if p.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = p.srv.Shutdown(ctx)
	}
}

// ServeHTTP filters one proxied request: CONNECT opens a raw tunnel to an
// allowed target; an absolute-URI request (plain-HTTP proxying) is forwarded.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !p.requireAuth(w, r) {
		return
	}
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	// A plain-HTTP proxy request carries an absolute URI; anything else is a
	// direct hit on the proxy port, which serves nothing.
	if !r.URL.IsAbs() {
		http.Error(w, "sandbox egress proxy: absolute-URI or CONNECT only", http.StatusBadRequest)
		return
	}
	target := r.URL.Host
	if !strings.Contains(target, ":") {
		target += ":80"
	}
	if !EgressAllowed(p.Allow, target) {
		p.deny(w, target)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header.Del("Proxy-Authorization") // the credential never leaves the box
	resp, err := p.tr.RoundTrip(out)
	if err != nil {
		if errors.Is(err, errEgressBlockedIP) {
			p.deny(w, target) // resolved into a special range → 403 + audit
			return
		}
		http.Error(w, "sandbox egress proxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// connect handles a CONNECT tunnel (the HTTPS path).
func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if !strings.Contains(target, ":") {
		target += ":443"
	}
	if !EgressAllowed(p.Allow, target) {
		p.deny(w, target)
		return
	}
	upstream, err := p.dialAllowed(r.Context(), target, 30*time.Second)
	if err != nil {
		if errors.Is(err, errEgressBlockedIP) {
			p.deny(w, target) // resolved into a special range → 403 + audit
			return
		}
		http.Error(w, "sandbox egress proxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "sandbox egress proxy: cannot hijack", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	_, _ = buf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	_ = buf.Flush()
	go func() {
		defer client.Close()
		defer upstream.Close()
		_, _ = io.Copy(upstream, client)
	}()
	go func() {
		defer client.Close()
		defer upstream.Close()
		_, _ = io.Copy(client, upstream)
	}()
}

func (p *Proxy) deny(w http.ResponseWriter, target string) {
	if p.OnDeny != nil {
		p.OnDeny(target)
	}
	http.Error(w, fmt.Sprintf("sandbox egress proxy: %s is not in this profile's egress allowlist", target), http.StatusForbidden)
}

// ProxyEnv renders the environment a launched runtime needs to route its
// HTTP(S) traffic through the proxy at addr, carrying this dispatch's
// credential in the URL (standard HTTP stacks send it as
// Proxy-Authorization). Loopback stays direct so a runtime can still reach
// conductor's own local surfaces (the skill socket, a local opencode
// server).
func ProxyEnv(addr, cred string) []string {
	u := "http://" + addr
	if cred != "" {
		u = "http://" + proxyUser + ":" + cred + "@" + addr
	}
	return []string{
		"HTTP_PROXY=" + u, "http_proxy=" + u,
		"HTTPS_PROXY=" + u, "https_proxy=" + u,
		"NO_PROXY=127.0.0.1,localhost,::1", "no_proxy=127.0.0.1,localhost,::1",
	}
}

// ProxyManager owns one Proxy per distinct allowlist, created lazily and
// shared across dispatches — the daemon-lifetime registry cmd/conductor
// wires into the controller package.
type ProxyManager struct {
	// OnDeny is the audit hook, called with the allowlist key and the denied
	// host:port.
	OnDeny func(key, hostport string)

	mu      sync.Mutex
	byKey   map[string]*Proxy
	addrs   map[string]string
	socks   map[string]string // allowlist key → unix socket path
	sockDir string            // 0700 directory holding the unix sockets
}

// NewProxyManager builds an empty manager. onDeny may be nil.
func NewProxyManager(onDeny func(key, hostport string)) *ProxyManager {
	return &ProxyManager{OnDeny: onDeny, byKey: map[string]*Proxy{}, addrs: map[string]string{}}
}

// Endpoint returns the proxy enforcing exactly this allowlist (starting it
// on first use) plus a FRESH per-dispatch client credential — the launch env
// carries it, and the proxy refuses clients without one. An empty (or nil)
// allowlist is the deny-all proxy.
func (m *ProxyManager) Endpoint(allow []string) (addr, cred string, err error) {
	p, addr, err := m.proxyFor(allow)
	if err != nil {
		return "", "", err
	}
	cred, err = p.MintCred()
	if err != nil {
		return "", "", err
	}
	return addr, cred, nil
}

// UnixEndpoint returns the unix-socket path of the proxy enforcing exactly
// this allowlist (creating the socket on first use) plus a fresh
// per-dispatch credential — the daemon-side end the in-sandbox forwarder
// pipes into (#36 iso-review C1).
func (m *ProxyManager) UnixEndpoint(allow []string) (sock, cred string, err error) {
	p, _, err := m.proxyFor(allow)
	if err != nil {
		return "", "", err
	}
	key := allowKey(allow)
	m.mu.Lock()
	if m.socks == nil {
		m.socks = map[string]string{}
	}
	sock, ok := m.socks[key]
	if !ok {
		if m.sockDir == "" {
			m.sockDir, err = os.MkdirTemp("", "conductor-egress")
			if err != nil {
				m.mu.Unlock()
				return "", "", fmt.Errorf("sandbox: egress socket dir: %w", err)
			}
		}
		sock = filepath.Join(m.sockDir, fmt.Sprintf("egress-%d.sock", len(m.socks)))
		if err := p.ServeUnix(sock); err != nil {
			m.mu.Unlock()
			return "", "", err
		}
		m.socks[key] = sock
	}
	m.mu.Unlock()
	cred, err = p.MintCred()
	if err != nil {
		return "", "", err
	}
	return sock, cred, nil
}

// proxyFor returns (starting on first use) the shared proxy for an allowlist.
func (m *ProxyManager) proxyFor(allow []string) (*Proxy, string, error) {
	key := allowKey(allow)
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.byKey[key]; ok {
		return p, m.addrs[key], nil
	}
	p := &Proxy{Allow: allow}
	if m.OnDeny != nil {
		p.OnDeny = func(hostport string) { m.OnDeny(key, hostport) }
	}
	addr, err := p.Start()
	if err != nil {
		return nil, "", err
	}
	m.byKey[key] = p
	m.addrs[key] = addr
	return p, addr, nil
}

// Close shuts every proxy down.
func (m *ProxyManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.byKey {
		p.Close()
	}
	if m.sockDir != "" {
		_ = os.RemoveAll(m.sockDir)
		m.sockDir = ""
	}
	m.byKey, m.addrs, m.socks = map[string]*Proxy{}, map[string]string{}, nil
}

// allowKey canonicalizes an allowlist (sorted, joined) so equivalent lists
// share one proxy. The empty list keys the deny-all proxy.
func allowKey(allow []string) string {
	if len(allow) == 0 {
		return "" // deny-all
	}
	c := make([]string, 0, len(allow))
	for _, a := range allow {
		if a = strings.TrimSpace(a); a != "" {
			c = append(c, strings.ToLower(a))
		}
	}
	sort.Strings(c)
	return strings.Join(c, "\x00")
}
