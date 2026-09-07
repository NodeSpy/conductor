// The conductor-enforced egress path (#36 §15): a loopback HTTP forward
// proxy per distinct allowlist. Launched runtimes get HTTP(S)_PROXY pointed
// at it; CONNECT tunnels and absolute-URI requests are matched against the
// allowlist and denied with 403 (and an audit callback) when they don't.
// This is how conductor polices network egress for the runtimes it launches
// without owning a firewall: the filter runs inside conductor's own process.
package sandbox

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Proxy is one egress-filtering forward proxy bound to 127.0.0.1.
type Proxy struct {
	Allow []string // EgressAllowed patterns; empty = deny everything
	// OnDeny is called (if non-nil) with the denied host:port — the audit hook.
	OnDeny func(hostport string)

	ln  net.Listener
	srv *http.Server
}

// Start listens on a fresh loopback port and serves until Close. It returns
// the proxy's address ("127.0.0.1:PORT").
func (p *Proxy) Start() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("sandbox: egress proxy listen: %w", err)
	}
	p.ln = ln
	p.srv = &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() { _ = p.srv.Serve(ln) }()
	return ln.Addr().String(), nil
}

// Close shuts the proxy down.
func (p *Proxy) Close() {
	if p.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = p.srv.Shutdown(ctx)
	}
}

// ServeHTTP filters one proxied request: CONNECT opens a raw tunnel to an
// allowed target; an absolute-URI request (plain-HTTP proxying) is forwarded.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	resp, err := http.DefaultTransport.RoundTrip(out)
	if err != nil {
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
	upstream, err := net.DialTimeout("tcp", target, 30*time.Second)
	if err != nil {
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
// HTTP(S) traffic through the proxy at addr. Loopback stays direct so a
// runtime can still reach conductor's own local surfaces (the skill socket,
// a local opencode server).
func ProxyEnv(addr string) []string {
	u := "http://" + addr
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

	mu    sync.Mutex
	byKey map[string]*Proxy
	addrs map[string]string
}

// NewProxyManager builds an empty manager. onDeny may be nil.
func NewProxyManager(onDeny func(key, hostport string)) *ProxyManager {
	return &ProxyManager{OnDeny: onDeny, byKey: map[string]*Proxy{}, addrs: map[string]string{}}
}

// Addr returns the proxy address enforcing exactly this allowlist, starting
// the proxy on first use. An empty (or nil) allowlist is the deny-all proxy.
func (m *ProxyManager) Addr(allow []string) (string, error) {
	key := allowKey(allow)
	m.mu.Lock()
	defer m.mu.Unlock()
	if addr, ok := m.addrs[key]; ok {
		return addr, nil
	}
	p := &Proxy{Allow: allow}
	if m.OnDeny != nil {
		p.OnDeny = func(hostport string) { m.OnDeny(key, hostport) }
	}
	addr, err := p.Start()
	if err != nil {
		return "", err
	}
	m.byKey[key] = p
	m.addrs[key] = addr
	return addr, nil
}

// Close shuts every proxy down.
func (m *ProxyManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.byKey {
		p.Close()
	}
	m.byKey, m.addrs = map[string]*Proxy{}, map[string]string{}
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
