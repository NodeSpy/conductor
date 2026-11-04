package sandbox

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2/hpack"
)

// h2Opening is an HTTP/2 prior-knowledge client's opening: the preface, an
// empty SETTINGS frame and a HEADERS frame naming authority.
func h2Opening(authority string) []byte {
	var hb bytes.Buffer
	enc := hpack.NewEncoder(&hb)
	for _, f := range []hpack.HeaderField{{Name: ":method", Value: "GET"}, {Name: ":scheme", Value: "http"}, {Name: ":authority", Value: authority}, {Name: ":path", Value: "/"}} {
		_ = enc.WriteField(f)
	}
	frame := func(typ, flags byte, payload []byte) []byte {
		h := make([]byte, 9)
		h[0], h[1], h[2] = byte(len(payload)>>16), byte(len(payload)>>8), byte(len(payload))
		h[3], h[4] = typ, flags
		binary.BigEndian.PutUint32(h[5:], 1)
		if typ == 0x4 {
			binary.BigEndian.PutUint32(h[5:], 0)
		}
		return append(h, payload...)
	}
	out := []byte(h2Preface)
	out = append(out, frame(0x4, 0, nil)...)
	return append(out, frame(0x1, 0x4|0x1, hb.Bytes())...)
}

// The protocol check passes the protocols it recognizes naming the CONNECT
// host — TLS, HTTP/1.x, HTTP/2 — and refuses ssh (named), a silent client
// and unknown bytes, none of them reaching the upstream. A raw allowance
// lets anything through to its target only.
func TestProtocolCheckRecognizesAndRefuses(t *testing.T) {
	old := firstBytesWait
	firstBytesWait = 300 * time.Millisecond
	t.Cleanup(func() { firstBytesWait = old })
	up, rawUp := newUpstream(t), newUpstream(t)
	target, rawTarget := up.ln.Addr().String(), rawUp.ln.Addr().String()
	host, _, _ := net.SplitHostPort(target)
	p := &Proxy{Allow: []string{target, rawTarget}}
	addr, rec := startProxy(t, p)
	cred, _ := p.MintSession(SessionOpts{Run: "run-p", RequireTLS: true, NonTLS: []string{rawTarget}, NonTLSBy: "workflow mirror"})

	// try sends opening (nil: nothing) and reports whether the echo came back.
	try := func(target string, opening []byte) bool {
		c, br, code := connectVia(t, addr, cred, target)
		defer c.Close()
		if code != 200 {
			t.Fatalf("CONNECT %d", code)
		}
		if opening == nil {
			c.SetReadDeadline(time.Now().Add(3 * time.Second))
			_, err := br.ReadByte()
			return err == nil
		}
		c.Write(opening)
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		got := make([]byte, len(opening))
		_, err := io.ReadFull(br, got)
		return err == nil && bytes.Equal(got, opening)
	}
	for name, opening := range map[string][]byte{
		"http/1.1":         []byte("GET /mod/@v/list HTTP/1.1\r\nHost: " + target + "\r\nUser-Agent: go\r\n\r\n"),
		"http/1.0 no port": []byte("GET / HTTP/1.0\r\nHost: " + host + "\r\n\r\n"),
		"http/1.1 + body":  []byte("POST /x HTTP/1.1\r\nHost: " + host + "\r\nContent-Length: 4\r\n\r\nbody"),
		"h2c":              h2Opening(target),
	} {
		if !try(target, opening) {
			t.Errorf("%s: a recognized protocol naming the CONNECT host was refused", name)
		}
	}
	passed := up.accepted.Load()
	rec.wait(t, func(c ConnRecord) bool { return c.Outcome == "closed" && c.Proto == ProtoHTTP })
	rec.wait(t, func(c ConnRecord) bool { return c.Outcome == "closed" && c.Proto == ProtoHTTP2 })

	for name, c := range map[string]struct {
		opening []byte
		reason  string
	}{
		"ssh":              {[]byte("SSH-2.0-OpenSSH_9.6\r\n"), "ssh to " + target + " refused — allow it with allow.raw for that host"},
		"silent client":    {nil, "unrecognized protocol to " + target + ": the client sent nothing first"},
		"unknown bytes":    {[]byte("\x00\x01hello there"), "unrecognized protocol to " + target},
		"a lowercase verb": {[]byte("get / HTTP/1.1\r\nHost: " + host + "\r\n\r\n"), "unrecognized protocol"},
		"another host":     {[]byte("GET / HTTP/1.1\r\nHost: evil.example\r\n\r\n"), `HTTP Host "evil.example" is not the CONNECT host`},
		"two hosts":        {[]byte("GET / HTTP/1.1\r\nHost: " + host + "\r\nHost: evil.example\r\n\r\n"), "2 Host headers"},
		"h2 elsewhere":     {h2Opening("evil.example"), `HTTP/2 :authority "evil.example" is not the CONNECT host`},
	} {
		if try(target, c.opening) {
			t.Errorf("%s: passed", name)
			continue
		}
		reason := c.reason
		rec.wait(t, func(r ConnRecord) bool { return r.Outcome == "refused" && strings.Contains(r.Reason, reason) })
	}
	if n := up.accepted.Load(); n != passed {
		t.Fatalf("refused tunnels reached the upstream: %d connections, want %d", n, passed)
	}
	rec.wait(t, func(r ConnRecord) bool { return r.Outcome == "refused" && r.Proto == ProtoSSH })

	// The raw allowance: ssh, and a silent client, reach its target only.
	if !try(rawTarget, []byte("SSH-2.0-OpenSSH_9.6\r\n")) {
		t.Fatal("ssh to the raw-allowed target was refused")
	}
	rec.wait(t, func(r ConnRecord) bool {
		return r.Target == rawTarget && r.Outcome == "closed" && r.AllowedBy == "workflow mirror"
	})
}

func TestAuthorityMatches(t *testing.T) {
	for _, c := range []struct {
		auth, host, port string
		want             bool
	}{
		{"mirror.lan", "mirror.lan", "3000", true},
		{"MIRROR.lan.:3000", "mirror.lan", "3000", true},
		{"mirror.lan:4000", "mirror.lan", "3000", false},
		{"[::1]:3000", "::1", "3000", true},
		{"::1", "::1", "3000", true},
		{"evil.example", "mirror.lan", "3000", false},
		{"", "mirror.lan", "3000", false},
	} {
		if got := authorityMatches(c.auth, c.host, c.port); got != c.want {
			t.Errorf("%q vs %s:%s = %v", c.auth, c.host, c.port, got)
		}
	}
	_ = bufio.NewReader
}
