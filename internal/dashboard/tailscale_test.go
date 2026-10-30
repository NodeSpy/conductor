package dashboard

import (
	"context"
	"crypto/tls"
	"testing"
	"time"
)

func TestPeerAllowedTaggedRefused(t *testing.T) {
	// A tagged node (server/CI) is refused by default (design §6.2).
	if ok, _ := peerAllowed(tsWho{Node: "n1", Tagged: true}, false, nil); ok {
		t.Fatal("a tagged node must be refused when allow_tagged is false")
	}
	if ok, _ := peerAllowed(tsWho{Node: "n1", Tagged: true}, true, nil); !ok {
		t.Fatal("a tagged node must be allowed when allow_tagged is true")
	}
}

func TestPeerAllowedAllowUsers(t *testing.T) {
	who := tsWho{Node: "n1", Login: "you@example.com"}
	if ok, _ := peerAllowed(who, false, []string{"someone@else.com"}); ok {
		t.Fatal("a user not in allow_users must be refused")
	}
	if ok, _ := peerAllowed(who, false, []string{"YOU@example.com"}); !ok {
		t.Fatal("allow_users should match case-insensitively")
	}
	if ok, _ := peerAllowed(who, false, nil); !ok {
		t.Fatal("an empty allow_users should allow any (non-tagged) user")
	}
}

func TestHostAllowedRebinding(t *testing.T) {
	magic := "box.tailnet.ts.net"
	ips := []string{"100.64.0.1", "fd7a:115c:a1e0::1"}
	ok := func(h string) bool { return hostAllowed(h, magic, ips, 7788) }
	for _, good := range []string{"box.tailnet.ts.net", "box.tailnet.ts.net:7788", "100.64.0.1:7788", "[fd7a:115c:a1e0::1]:7788"} {
		if !ok(good) {
			t.Errorf("host %q should be allowed", good)
		}
	}
	// The loopback names belong to the localhost listener only.
	for _, bad := range []string{"evil.example.com", "attacker.com:7788", "box.tailnet.ts.net.evil.com", "localhost:7788", "127.0.0.1"} {
		if ok(bad) {
			t.Errorf("host %q should be refused on the tailnet", bad)
		}
	}
	local := func(h string) bool { return hostAllowed(h, "", nil, 0) }
	for _, good := range []string{"localhost", "localhost:7788", "127.0.0.1", "[::1]:7788"} {
		if !local(good) {
			t.Errorf("host %q should be allowed on the localhost listener", good)
		}
	}
	for _, bad := range []string{"box.tailnet.ts.net", "evil.example.com", "localhost.evil.com"} {
		if local(bad) {
			t.Errorf("host %q should be refused on the localhost listener", bad)
		}
	}
}

func TestSplitPEMPair(t *testing.T) {
	blob := []byte("-----BEGIN PRIVATE KEY-----\nKKK\n-----END PRIVATE KEY-----\n-----BEGIN CERTIFICATE-----\nCCC\n-----END CERTIFICATE-----\n")
	key, cert := splitPEMPair(blob)
	if len(key) == 0 || len(cert) == 0 {
		t.Fatalf("both halves should parse: key=%d cert=%d", len(key), len(cert))
	}
	if string(key[:len("-----BEGIN PRIVATE KEY-----")]) != "-----BEGIN PRIVATE KEY-----" {
		t.Error("key half should start at the key header")
	}
}

// fakeLocalAPI is a faked tailscaled LocalAPI for the cert-cache test.
type fakeLocalAPI struct {
	calls int
	err   error
}

func (f *fakeLocalAPI) Status(context.Context) (tsStatus, error)     { return tsStatus{}, nil }
func (f *fakeLocalAPI) WhoIs(context.Context, string) (tsWho, error) { return tsWho{}, nil }
func (f *fakeLocalAPI) CertPair(context.Context, string) (tls.Certificate, error) {
	f.calls++
	if f.err != nil {
		return tls.Certificate{}, f.err
	}
	return tls.Certificate{Certificate: [][]byte{{1, 2, 3}}}, nil
}

func TestCertCacheRefreshesDaily(t *testing.T) {
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	api := &fakeLocalAPI{}
	cc := newCertCache(api, "box.tailnet.ts.net", func() time.Time { return now })
	if _, err := cc.GetCertificate(nil); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if _, err := cc.GetCertificate(nil); err != nil {
		t.Fatalf("cached fetch: %v", err)
	}
	if api.calls != 1 {
		t.Fatalf("within a day the cert should be cached, got %d fetches", api.calls)
	}
	now = now.Add(25 * time.Hour)
	if _, err := cc.GetCertificate(nil); err != nil {
		t.Fatalf("refetch: %v", err)
	}
	if api.calls != 2 {
		t.Fatalf("after a day it should refetch, got %d fetches", api.calls)
	}
}
