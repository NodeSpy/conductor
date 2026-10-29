package dashboard

// Generic listeners (dashboard.bind): an explicit address, the hostnames it
// answers to, and an optional certificate — the way to reach the dashboard from
// another machine without Tailscale. Every request passes the same chain as the
// other listeners: security headers, the Host allowlist (DNS rebinding), then
// the auth dispatch. With TLS each non-IP hostname is a passkey relying party;
// without it the connection is plain HTTP and passkeys are not offered.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dashboard/auth"
)

// bindListener is the auth listener name for a bind entry; a session is bound
// to it like to the localhost and tailnet listeners.
func bindListener(b config.DashboardBind) auth.Listener {
	return auth.Listener("bind:" + b.Address)
}

// bindTransport is how browsers reach a bind entry.
func bindTransport(b config.DashboardBind) auth.Transport {
	switch {
	case b.TLS():
		return auth.TransportHTTPS
	case b.Loopback():
		return auth.TransportLocalhost
	default:
		return auth.TransportHTTP
	}
}

// bindRPs are the passkey relying parties the bind entries serve: each non-IP
// hostname of a secure-context listener.
func bindRPs(binds []config.DashboardBind) []auth.RP {
	var out []auth.RP
	for _, b := range binds {
		t := bindTransport(b)
		if !t.Secure() {
			continue
		}
		_, port, _ := net.SplitHostPort(b.Address)
		for _, h := range b.Hostnames {
			h = strings.ToLower(strings.TrimSuffix(h, "."))
			if net.ParseIP(strings.Trim(h, "[]")) != nil {
				continue // WebAuthn has no IP relying parties
			}
			origins := []string{fmt.Sprintf("%s://%s:%s", t.Scheme(), h, port)}
			if (t == auth.TransportHTTPS && port == "443") || (t != auth.TransportHTTPS && port == "80") {
				origins = append(origins, fmt.Sprintf("%s://%s", t.Scheme(), h))
			}
			out = append(out, auth.RP{ID: h, Origins: origins, Listener: bindListener(b)})
		}
	}
	return out
}

// bindHostAllowed reports whether a request Host is one of the entry's
// hostnames (the port is not compared: the listener only receives its own).
func bindHostAllowed(host string, b config.DashboardBind) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.ToLower(strings.TrimSuffix(strings.Trim(h, "[]"), "."))
	if h == "" {
		return false
	}
	for _, want := range b.Hostnames {
		if strings.EqualFold(strings.TrimSuffix(strings.Trim(want, "[]"), "."), h) {
			return true
		}
	}
	return false
}

// bindHandler is the handler chain for one bind entry.
func (s *Server) bindHandler(b config.DashboardBind) http.Handler {
	l, t := bindListener(b), bindTransport(b)
	return auth.SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !bindHostAllowed(r.Host, b) {
			http.Error(w, "host not allowed", http.StatusForbidden)
			return
		}
		r = auth.WithConn(r, auth.ConnInfo{Listener: l, Transport: t})
		s.dispatch(w, r)
	}))
}

// startBind binds one entry and serves it until ctx ends. It re-checks the
// plain-HTTP acknowledgment (config validation already refuses it), so a
// non-loopback plain listener never starts unacknowledged.
func (s *Server) startBind(ctx context.Context, b config.DashboardBind) error {
	t := bindTransport(b)
	if t == auth.TransportHTTP && !b.AllowPlainHTTP {
		return fmt.Errorf("dashboard.bind %s: plain HTTP off loopback needs allow_plain_http: true", b.Address)
	}
	var tlsCfg *tls.Config
	if t == auth.TransportHTTPS {
		pair, err := tls.LoadX509KeyPair(b.CertFile, b.KeyFile)
		if err != nil {
			return fmt.Errorf("dashboard.bind %s: %w", b.Address, err)
		}
		tlsCfg = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	}
	ln, err := net.Listen("tcp", b.Address)
	if err != nil {
		return fmt.Errorf("dashboard.bind %s: %w", b.Address, err)
	}
	srv := &http.Server{
		Handler:           s.bindHandler(b),
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	go func() {
		var err error
		if tlsCfg != nil {
			err = srv.ServeTLS(ln, "", "")
		} else {
			err = srv.Serve(ln)
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.deps.Log("dashboard: bind %s stopped: %v", b.Address, err)
		}
	}()
	go func() { <-ctx.Done(); _ = srv.Close() }()
	if t == auth.TransportHTTP {
		s.deps.Log("dashboard: WARNING serving plain HTTP on %s (%s): session cookies and everything else cross the network unencrypted; passkeys are not offered (dashboard.bind allow_plain_http)",
			b.Address, strings.Join(b.Hostnames, ", "))
	} else {
		s.deps.Log("dashboard: serving on %s://%s (%s)", t.Scheme(), b.Address, strings.Join(b.Hostnames, ", "))
	}
	return nil
}
