package connector

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// resolveTo builds a fake resolver that maps every host to the given IP,
// modeling a caller-supplied url whose DNS points at an internal address.
func resolveTo(ip string) func(context.Context, string) ([]net.IPAddr, error) {
	return func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
	}
}

// TestWebhookPostEgressGuard proves webhook.post refuses to reach loopback,
// cloud-metadata, and private addresses by default (SSRF), and that an operator
// opt-in (allow_private) re-permits them.
func TestWebhookPostEgressGuard(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "169.254.169.254", "10.1.2.3"} {
		w := &webhookImpl{name: "wh", conn: webhookConn{}, resolve: resolveTo(ip)}
		_, err := w.Invoke(context.Background(), "post", map[string]any{
			"url":  "https://evil.example.com/x",
			"json": map[string]any{"a": 1},
		})
		if err == nil {
			t.Fatalf("post to %s: expected refusal, got nil error", ip)
		}
		if !strings.Contains(err.Error(), "blocked address") {
			t.Fatalf("post to %s: want blocked-address error, got %v", ip, err)
		}
	}

	// Opt-in via allow_private: the same loopback target now connects.
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		hit = true
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	w := &webhookImpl{name: "wh", conn: webhookConn{AllowPrivate: true}, resolve: resolveTo("127.0.0.1")}
	out, err := w.Invoke(context.Background(), "post", map[string]any{
		"url":    "http://" + host + "/hook",
		"method": http.MethodPost,
		"body":   "hi",
	})
	if err != nil {
		t.Fatalf("post with allow_private: unexpected error %v", err)
	}
	if !hit {
		t.Fatalf("post with allow_private: server was never reached")
	}
	if out["status"] != http.StatusOK {
		t.Fatalf("post with allow_private: status = %v", out["status"])
	}
}

// TestWebhookPostAllowHostsByName proves an operator can opt a single host back
// in by name without opening all private space.
func TestWebhookPostAllowHostsByName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	_ = host

	// The url uses a name that resolves to loopback; only "internal.svc" is
	// allow-listed, so it is permitted while any other name would be refused.
	w := &webhookImpl{
		name:    "wh",
		conn:    webhookConn{AllowHosts: []string{"internal.svc"}},
		resolve: resolveTo("127.0.0.1"),
	}
	out, err := w.Invoke(context.Background(), "post", map[string]any{
		"url":  "http://internal.svc:" + port + "/hook",
		"body": "hi",
	})
	if err != nil {
		t.Fatalf("allow_hosts by name: unexpected error %v", err)
	}
	if out["status"] != http.StatusOK {
		t.Fatalf("allow_hosts by name: status = %v", out["status"])
	}
}
