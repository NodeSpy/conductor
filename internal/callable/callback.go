package callable

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/netguard"
)

// Errors the callback poster refuses a delivery with, before any packet leaves
// the daemon. Surfaced verbatim in the callable_callback audit reason.
var (
	errCallbackScheme    = errors.New("callback_url must be https (set callable.callback_allow_http to allow http)")
	errCallbackBlockedIP = errors.New("callback_url resolves to a blocked (loopback/private/link-local/CGNAT) address")
	errCallbackBadURL    = errors.New("callback_url is not a valid absolute http(s) URL")
)

// callbackPoster is the SSRF-guarded deliverer for completion callbacks (#36
// §13 review, item 1). The daemon — not the sandbox — makes this request, so a
// caller-supplied `callback_url` pointed at cloud metadata, loopback, or an
// RFC1918 host would let it reach straight off the daemon's own network with no
// egress policy in the way. Every callback therefore:
//   - requires https (unless the operator opted into http);
//   - resolves the host ITSELF and refuses any resolved IP in a blocked range
//     (no re-resolve TOCTOU — the address checked is the address dialed);
//   - dials only the checked IP;
//   - never follows redirects (a 30x could bounce it onto an internal host).
type callbackPoster struct {
	allowHTTP bool
	allowIP   map[string]bool                                              // literal IPs opted back in
	resolve   func(ctx context.Context, host string) ([]net.IPAddr, error) // nil ⇒ system resolver
}

// newCallbackPoster builds the poster from the callable config. The literal
// allow-list is parsed once; a non-IP entry is ignored (only an exact IP can
// opt a blocked range back in — a hostname never can, which is the point).
func newCallbackPoster(cfg config.CallableConfig) *callbackPoster {
	allow := map[string]bool{}
	for _, h := range cfg.CallbackAllowHosts {
		if ip := net.ParseIP(strings.TrimSpace(h)); ip != nil {
			allow[ip.String()] = true
		}
	}
	return &callbackPoster{allowHTTP: cfg.CallbackAllowHTTP, allowIP: allow}
}

// post validates the scheme, then dials through the guarded transport. Scheme
// and URL failures return before any DNS lookup or connection; a blocked
// resolved IP fails inside the dialer, so no request is ever sent to it.
func (c *callbackPoster) post(ctx context.Context, rawURL string, body []byte) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || !u.IsAbs() {
		return errCallbackBadURL
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !c.allowHTTP {
			return errCallbackScheme
		}
	default:
		return errCallbackScheme
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext:         c.safeDial,
			TLSHandshakeTimeout: 10 * time.Second,
			DisableKeepAlives:   true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			// Never follow a redirect off the vetted host — a 30x to an internal
			// URL would sidestep the resolved-IP check entirely.
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("callback returned %d", resp.StatusCode)
	}
	return nil
}

// safeDial resolves the target host and dials ONLY a resolved IP that passes
// the guard. Because the dialer connects to the exact IP it just vetted (not a
// re-resolved name), a hostname that resolves to an internal address is refused
// even when DNS is attacker-controlled. If every candidate is blocked it
// returns errCallbackBlockedIP so the caller can 403/audit rather than 502.
func (c *callbackPoster) safeDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []net.IPAddr
	if c.resolve != nil {
		ips, err = c.resolve(ctx, host)
	} else {
		ips, err = net.DefaultResolver.LookupIPAddr(ctx, host)
	}
	if err != nil {
		return nil, err
	}
	var blocked bool
	var d net.Dialer
	var lastErr error
	for _, ipa := range ips {
		if netguard.Blocked(ipa.IP) && !c.allowIP[ipa.IP.String()] {
			blocked = true
			continue
		}
		conn, derr := d.DialContext(ctx, network, net.JoinHostPort(ipa.IP.String(), port))
		if derr == nil {
			return conn, nil
		}
		lastErr = derr
	}
	if blocked {
		return nil, errCallbackBlockedIP
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errCallbackBadURL
}
