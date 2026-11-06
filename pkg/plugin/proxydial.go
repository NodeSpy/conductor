package plugin

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

// DialContext connects to addr ("host:port") the way a jailed plugin's
// network allows: through conductor's egress proxy (HTTPS_PROXY, an HTTP
// CONNECT tunnel) when one is set, directly otherwise. Use it for any TCP
// protocol that is not HTTP (a database, a message broker, SSH): an HTTP
// client already honors the proxy variables. The proxy refuses a destination
// the plugin did not declare in Capabilities.Egress.
func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	pu := proxyURL()
	if pu == nil || (network != "tcp" && network != "tcp4" && network != "tcp6") {
		return d.DialContext(ctx, network, addr)
	}
	c, err := d.DialContext(ctx, "tcp", pu.Host)
	if err != nil {
		return nil, fmt.Errorf("egress proxy: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: addr}, Host: addr, Header: http.Header{}}
	if pu.User != nil {
		pass, _ := pu.User.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(pu.User.Username()+":"+pass)))
	}
	if err := req.Write(c); err != nil {
		c.Close()
		return nil, fmt.Errorf("egress proxy: %w", err)
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("egress proxy: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.Close()
		return nil, fmt.Errorf("egress proxy refused %s: %s (declare it in capabilities.egress)", addr, resp.Status)
	}
	_ = c.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: c, r: br}, nil
	}
	return c, nil
}

func proxyURL() *url.URL {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if v := os.Getenv(k); v != "" {
			if u, err := url.Parse(v); err == nil && u.Host != "" {
				return u
			}
		}
	}
	return nil
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }
