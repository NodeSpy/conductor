package dashboard

// Tailnet listener support (design §6.2). The dashboard binds the node's own
// Tailscale addresses and serves HTTPS with the MagicDNS certificate — it never
// uses Funnel or `tailscale serve`, so nothing is published. Everything it needs
// from tailscaled comes through the LocalAPI unix socket:
//
//   - /localapi/v0/status   → the MagicDNS name, the node's tailnet addresses,
//     and whether this node is tagged;
//   - /localapi/v0/cert/<name>?type=pair → the key+cert PEM pair for the name;
//   - /localapi/v0/whois?addr=<ip:port> → the peer node and login behind a
//     connection, and whether that peer is tagged.
//
// The LocalAPI client is an interface so the security tests can fake it (a
// tagged-node WhoIs is refused, the Host allowlist defeats rebinding) without a
// running tailscaled. "auto" certs come from the LocalAPI; an explicit
// {cert_file, key_file} pair is loaded instead.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// defaultTSSocket is tailscaled's LocalAPI socket on Linux.
const defaultTSSocket = "/run/tailscale/tailscaled.sock"

// tsStatus is the slice of /localapi/v0/status we read.
type tsStatus struct {
	MagicDNSName string   // "box.tailnet.ts.net." (trailing dot trimmed on read)
	IPs          []string // the node's tailnet addresses
	Tagged       bool     // this node is a tagged node
}

// tsWho is the slice of /localapi/v0/whois we read about a peer.
type tsWho struct {
	Node   string // a stable node identifier
	Login  string // the user login behind the peer ("" for a tagged node)
	Tagged bool   // the peer is a tagged node (server/CI)
}

// localAPI is tailscaled's LocalAPI, as the dashboard uses it. Faked in tests.
type localAPI interface {
	Status(ctx context.Context) (tsStatus, error)
	CertPair(ctx context.Context, name string) (tls.Certificate, error)
	WhoIs(ctx context.Context, remoteAddr string) (tsWho, error)
}

// tsLocalAPI is the real LocalAPI client over the unix socket.
type tsLocalAPI struct {
	socket string
	hc     *http.Client
}

func newTSLocalAPI(socket string) *tsLocalAPI {
	if socket == "" {
		socket = defaultTSSocket
	}
	return &tsLocalAPI{
		socket: socket,
		hc: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socket)
				},
			},
		},
	}
}

func (c *tsLocalAPI) get(ctx context.Context, path string) ([]byte, error) {
	// tailscaled serves the LocalAPI on a fixed pseudo-host.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://local-tailscaled.sock"+path, nil)
	if err != nil {
		return nil, err
	}
	req.Host = "local-tailscaled.sock"
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tailscaled LocalAPI (%s): %w", c.socket, err)
	}
	defer resp.Body.Close()
	b := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b = append(b, buf[:n]...)
		if err != nil {
			break
		}
		if len(b) > 1<<20 {
			break
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tailscaled LocalAPI %s: %s", path, strings.TrimSpace(string(b)))
	}
	return b, nil
}

func (c *tsLocalAPI) Status(ctx context.Context) (tsStatus, error) {
	b, err := c.get(ctx, "/localapi/v0/status")
	if err != nil {
		return tsStatus{}, err
	}
	var raw struct {
		Self struct {
			DNSName      string   `json:"DNSName"`
			TailscaleIPs []string `json:"TailscaleIPs"`
			Tags         []string `json:"Tags"`
		} `json:"Self"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return tsStatus{}, fmt.Errorf("parse tailscale status: %w", err)
	}
	return tsStatus{
		MagicDNSName: strings.TrimSuffix(raw.Self.DNSName, "."),
		IPs:          raw.Self.TailscaleIPs,
		Tagged:       len(raw.Self.Tags) > 0,
	}, nil
}

func (c *tsLocalAPI) CertPair(ctx context.Context, name string) (tls.Certificate, error) {
	// type=pair returns the key PEM then the cert PEM, concatenated.
	b, err := c.get(ctx, "/localapi/v0/cert/"+name+"?type=pair")
	if err != nil {
		return tls.Certificate{}, err
	}
	key, cert := splitPEMPair(b)
	if len(key) == 0 || len(cert) == 0 {
		return tls.Certificate{}, fmt.Errorf("tailscale cert for %q: incomplete key/cert pair", name)
	}
	return tls.X509KeyPair(cert, key)
}

func (c *tsLocalAPI) WhoIs(ctx context.Context, remoteAddr string) (tsWho, error) {
	b, err := c.get(ctx, "/localapi/v0/whois?addr="+remoteAddr)
	if err != nil {
		return tsWho{}, err
	}
	var raw struct {
		Node struct {
			Name     string   `json:"Name"`
			StableID string   `json:"StableID"`
			Tags     []string `json:"Tags"`
		} `json:"Node"`
		UserProfile struct {
			LoginName string `json:"LoginName"`
		} `json:"UserProfile"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return tsWho{}, fmt.Errorf("parse tailscale whois: %w", err)
	}
	node := raw.Node.StableID
	if node == "" {
		node = raw.Node.Name
	}
	return tsWho{Node: node, Login: raw.UserProfile.LoginName, Tagged: len(raw.Node.Tags) > 0}, nil
}

// splitPEMPair splits a concatenated PEM blob into (key, cert) by block type.
func splitPEMPair(b []byte) (key, cert []byte) {
	s := string(b)
	const keyHdr = "-----BEGIN PRIVATE KEY-----"
	const ecHdr = "-----BEGIN EC PRIVATE KEY-----"
	const rsaHdr = "-----BEGIN RSA PRIVATE KEY-----"
	const certHdr = "-----BEGIN CERTIFICATE-----"
	ci := strings.Index(s, certHdr)
	if ci < 0 {
		return nil, nil
	}
	cert = []byte(s[ci:])
	for _, h := range []string{keyHdr, ecHdr, rsaHdr} {
		if ki := strings.Index(s, h); ki >= 0 && ki < ci {
			key = []byte(s[ki:ci])
			return key, cert
		}
	}
	return key, cert
}

// certCache serves the tailnet cert through tls.Config.GetCertificate, refreshing
// from the LocalAPI at most daily (design §6.2: "reloaded daily … with no
// restart").
type certCache struct {
	api  localAPI
	name string
	mu   sync.Mutex
	cert *tls.Certificate
	next time.Time
	now  func() time.Time
}

func newCertCache(api localAPI, name string, now func() time.Time) *certCache {
	if now == nil {
		now = time.Now
	}
	return &certCache{api: api, name: name, now: now}
}

// GetCertificate is the tls.Config hook.
func (c *certCache) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cert != nil && c.now().Before(c.next) {
		return c.cert, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cert, err := c.api.CertPair(ctx, c.name)
	if err != nil {
		if c.cert != nil {
			return c.cert, nil // serve the last good cert rather than fail a handshake
		}
		return nil, err
	}
	c.cert = &cert
	c.next = c.now().Add(24 * time.Hour)
	return c.cert, nil
}

// peerAllowed applies the tailnet peer checks (design §6.2): a tagged node is
// refused unless allow_tagged, and allow_users narrows to named logins.
func peerAllowed(who tsWho, allowTagged bool, allowUsers []string) (bool, string) {
	if who.Tagged && !allowTagged {
		return false, "tagged tailnet nodes (servers, CI) are refused — set dashboard.tailscale.allow_tagged to permit"
	}
	if len(allowUsers) > 0 {
		for _, u := range allowUsers {
			if strings.EqualFold(strings.TrimSpace(u), who.Login) {
				return true, ""
			}
		}
		return false, fmt.Sprintf("tailnet user %q is not in dashboard.tailscale.allow_users", who.Login)
	}
	return true, ""
}

// hostAllowed is the Host-header allowlist that defeats DNS rebinding (design
// §6.2). The localhost listener (magicDNS "" and no ips) accepts only the
// loopback names; the tailnet listener only its MagicDNS name and tailnet IPs —
// never a loopback name, so a tailnet peer can't present itself under the
// localhost listener's origin.
func hostAllowed(host, magicDNS string, ips []string, port int) bool {
	h := bareHost(host)
	if magicDNS == "" && len(ips) == 0 {
		return isLoopbackName(h)
	}
	if magicDNS != "" && strings.EqualFold(h, magicDNS) {
		return true
	}
	for _, ip := range ips {
		if h == ip {
			return true
		}
	}
	return false
}

// bareHost strips the port, brackets and a trailing dot from a Host header.
func bareHost(host string) string {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.TrimSuffix(h, ".")
	return strings.Trim(h, "[]") // IPv6 literal
}

// isLoopbackName reports whether h names this machine's loopback: the names a
// browser treats as a secure context over plain HTTP.
func isLoopbackName(h string) bool {
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}
