package pkgproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/netguard"
)

// Fetcher is the proxy's only way out: plain GETs to configured upstreams.
// Every URL it is given is built by an ecosystem from its fixed upstream and
// validated names. It follows an upstream's redirects (proxy.golang.org
// sends large zips to Cloud Storage) itself — https only, a few hops, and
// never to an address in a private, loopback or link-local range unless the
// host is one the operator configured.
type Fetcher struct {
	client  *http.Client
	trusted map[string]bool // configured upstream hosts (lower-case, no port)

	// lookup resolves a host; a var for tests.
	lookup func(ctx context.Context, host string) ([]net.IPAddr, error)
}

// errBlockedAddr: a redirect led to an internal address.
var errBlockedAddr = errors.New("redirect target resolves to a blocked address range")

// maxRedirects bounds an upstream's redirect chain.
const maxRedirects = 5

// NewFetcher builds a fetcher whose configured upstreams are the hosts of
// upstreams (URLs).
func NewFetcher(upstreams ...string) (*Fetcher, error) {
	f := &Fetcher{trusted: map[string]bool{}, lookup: net.DefaultResolver.LookupIPAddr}
	for _, u := range upstreams {
		pu, err := url.Parse(u)
		if err != nil || pu.Hostname() == "" {
			return nil, fmt.Errorf("pkgproxy: upstream %q is not a URL", u)
		}
		f.trusted[strings.ToLower(pu.Hostname())] = true
	}
	tr := &http.Transport{
		// No HTTP_PROXY from the environment: the dial below is the guard.
		Proxy:                 nil,
		DialContext:           f.dial,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
	f.client = &http.Client{Transport: tr, CheckRedirect: f.checkRedirect}
	return f, nil
}

func (f *Fetcher) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("upstream redirected more than %d times", maxRedirects)
	}
	if req.URL.Scheme != "https" && !f.trusted[strings.ToLower(req.URL.Hostname())] {
		return fmt.Errorf("upstream redirected to non-https %s", req.URL.Redacted())
	}
	// Nothing of the client's goes along: there is nothing to send anyway.
	req.Header.Del("Authorization")
	return nil
}

// dial connects a configured upstream as-is, and any other host (a
// redirect's target) only at a resolved address outside the blocked ranges
// — the address checked is the address dialled.
func (f *Fetcher) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	if f.trusted[strings.ToLower(host)] {
		return d.DialContext(ctx, network, addr)
	}
	ips, err := f.lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error = errBlockedAddr
	for _, ip := range ips {
		if netguard.Blocked(ip.IP) {
			continue
		}
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// Get fetches u. The caller closes the body.
func (f *Fetcher) Get(ctx context.Context, u string) (*http.Response, error) {
	return f.GetAccept(ctx, u, "")
}

// GetAccept fetches u asking for accept ("" = any).
func (f *Fetcher) GetAccept(ctx context.Context, u, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "conductor-package-proxy")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	return f.client.Do(req)
}

// GetDerived fetches a URL an upstream's own metadata named (a tarball on
// the registry's download host): treated like a redirect target — https
// unless it is a configured upstream host, and dialled only at a public
// address (dial).
func (f *Fetcher) GetDerived(ctx context.Context, u string) (*http.Response, error) {
	pu, err := url.Parse(u)
	if err != nil || pu.Host == "" || pu.User != nil {
		return nil, fmt.Errorf("upstream metadata names a malformed URL %q", u)
	}
	if pu.Scheme != "https" && !(pu.Scheme == "http" && f.trusted[strings.ToLower(pu.Hostname())]) {
		return nil, fmt.Errorf("upstream metadata names a non-https URL %s", pu.Redacted())
	}
	return f.Get(ctx, u)
}
