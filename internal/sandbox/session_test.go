package sandbox

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recorder collects a proxy's records.
type recorder struct {
	mu   sync.Mutex
	recs []ConnRecord
}

func (r *recorder) add(c ConnRecord) { r.mu.Lock(); r.recs = append(r.recs, c); r.mu.Unlock() }

func (r *recorder) wait(t *testing.T, pred func(ConnRecord) bool) ConnRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		for _, c := range r.recs {
			if pred(c) {
				r.mu.Unlock()
				return c
			}
		}
		r.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Fatalf("no matching record in %+v", r.recs)
	return ConnRecord{}
}

// upstreamListener counts the connections a target receives and echoes.
type upstreamListener struct {
	ln       net.Listener
	accepted atomic.Int32
}

func newUpstream(t *testing.T) *upstreamListener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u := &upstreamListener{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			u.accepted.Add(1)
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return u
}

// connectVia opens a CONNECT tunnel to target through the proxy at addr.
func connectVia(t *testing.T, addr, cred, target string) (net.Conn, *bufio.Reader, int) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	auth := "Basic " + b64(proxyUser+":"+cred)
	_, _ = io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\nProxy-Authorization: "+auth+"\r\n\r\n")
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c, br, resp.StatusCode
}

func b64(s string) string {
	const enc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var out strings.Builder
	b := []byte(s)
	for i := 0; i < len(b); i += 3 {
		var n uint32
		k := 0
		for j := 0; j < 3; j++ {
			n <<= 8
			if i+j < len(b) {
				n |= uint32(b[i+j])
				k++
			}
		}
		for j := 0; j < 4; j++ {
			if j <= k {
				out.WriteByte(enc[(n>>(18-6*j))&63])
			} else {
				out.WriteByte('=')
			}
		}
	}
	return out.String()
}

func startProxy(t *testing.T, p *Proxy) (string, *recorder) {
	t.Helper()
	rec := &recorder{}
	p.OnConn = rec.add
	addr, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return addr, rec
}

// Plain ssh over CONNECT: refused at the first bytes, and the allowlisted
// host never receives a connection.
func TestRequireTLSRefusesNonTLSBeforeDialing(t *testing.T) {
	up := newUpstream(t)
	target := up.ln.Addr().String()
	p := &Proxy{Allow: []string{target}}
	addr, rec := startProxy(t, p)
	cred, _ := p.MintSession(SessionOpts{Run: "run-1", RequireTLS: true})
	c, br, code := connectVia(t, addr, cred, target)
	defer c.Close()
	if code != 200 {
		t.Fatalf("CONNECT: %d", code)
	}
	_, _ = io.WriteString(c, "SSH-2.0-OpenSSH_9.6\r\n")
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := br.ReadByte(); err == nil {
		t.Fatal("a non-TLS tunnel stayed open")
	}
	r := rec.wait(t, func(c ConnRecord) bool { return c.Outcome == "refused" })
	if r.Run != "run-1" || !strings.Contains(r.Reason, "SSH-2.0") {
		t.Fatalf("record: %+v", r)
	}
	if n := up.accepted.Load(); n != 0 {
		t.Fatalf("the upstream received %d connection(s) for a refused tunnel", n)
	}
}

// A real TLS client naming the CONNECT host passes; a different name, no
// name, or an encrypted ClientHello is refused — all before dialing.
func TestRequireTLSChecksTheServerName(t *testing.T) {
	up := newUpstream(t)
	_, port, _ := net.SplitHostPort(up.ln.Addr().String())
	target := "localhost:" + port
	p := &Proxy{Allow: []string{target}}
	addr, rec := startProxy(t, p)
	cred, _ := p.MintSession(SessionOpts{Run: "run-2", RequireTLS: true})
	hello := func(sni string) []byte {
		var buf safeBuf
		cl := tls.Client(&captureConn{w: &buf}, &tls.Config{ServerName: sni, InsecureSkipVerify: sni == ""})
		cl.SetDeadline(time.Now().Add(200 * time.Millisecond))
		_ = cl.Handshake()
		return buf.bytes()
	}
	try := func(b []byte) (passed bool) {
		c, br, code := connectVia(t, addr, cred, target)
		defer c.Close()
		if code != 200 {
			t.Fatalf("CONNECT %d", code)
		}
		c.Write(b)
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		got := make([]byte, len(b))
		_, err := io.ReadFull(br, got) // the echo upstream returns the hello
		return err == nil
	}
	if !try(hello("localhost")) {
		t.Fatal("a ClientHello naming the CONNECT host was refused")
	}
	if up.accepted.Load() != 1 {
		t.Fatal("the allowed tunnel did not reach the upstream")
	}
	// A plugin's or code step's session (MintCred) has no first-bytes check.
	legacy, _ := p.MintCred()
	c, br, _ := connectVia(t, addr, legacy, target)
	c.Write([]byte("PING"))
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if b := make([]byte, 4); func() error { _, err := io.ReadFull(br, b); return err }() != nil {
		t.Fatal("a non-agent session's plain tunnel was refused")
	}
	c.Close()
	up.accepted.Store(1)
	if try(hello("evil.example")) || try(hello("")) || try(withECH(hello("localhost"))) {
		t.Fatal("a wrong, absent or encrypted server name passed")
	}
	if up.accepted.Load() != 1 {
		t.Fatalf("refused tunnels reached the upstream (%d connections)", up.accepted.Load())
	}
	rec.wait(t, func(c ConnRecord) bool { return strings.Contains(c.Reason, "evil.example") })
	rec.wait(t, func(c ConnRecord) bool { return strings.Contains(c.Reason, "no TLS server name") })
	rec.wait(t, func(c ConnRecord) bool { return strings.Contains(c.Reason, "encrypted ClientHello") })
}

// Revoking a session closes its open tunnels — a launch that ended keeps
// no channel — and the idle limit and byte caps close them too.
func TestSessionRevokeIdleAndCapsCloseTunnels(t *testing.T) {
	up := newUpstream(t)
	target := up.ln.Addr().String()
	p := &Proxy{Allow: []string{target}}
	addr, rec := startProxy(t, p)
	open := func(cred string) (net.Conn, *bufio.Reader) {
		c, br, code := connectVia(t, addr, cred, target)
		if code != 200 {
			t.Fatalf("CONNECT %d", code)
		}
		c.Write([]byte("ping"))
		buf := make([]byte, 4)
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := io.ReadFull(br, buf); err != nil {
			t.Fatalf("tunnel: %v", err)
		}
		return c, br
	}
	closed := func(c net.Conn, br *bufio.Reader) bool {
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, err := br.ReadByte()
		var ne net.Error
		return err != nil && !(errors.As(err, &ne) && ne.Timeout())
	}

	cred, _ := p.MintSession(SessionOpts{Run: "revoked-run"})
	c, br := open(cred)
	p.Revoke(cred)
	if !closed(c, br) {
		t.Fatal("revoke left the tunnel open")
	}
	rec.wait(t, func(c ConnRecord) bool { return c.Run == "revoked-run" && c.Reason == "revoked" })

	cred, _ = p.MintSession(SessionOpts{Run: "idle-run", Limits: EgressLimits{Idle: 150 * time.Millisecond}})
	c, br = open(cred)
	if !closed(c, br) {
		t.Fatal("an idle tunnel stayed open")
	}
	rec.wait(t, func(c ConnRecord) bool { return c.Run == "idle-run" && c.Reason == "idle timeout" })

	cred, _ = p.MintSession(SessionOpts{Run: "cap-run", Limits: EgressLimits{Upload: 1000}})
	c, br = open(cred)
	c.Write(make([]byte, 2000))
	if !closed(c, br) {
		t.Fatal("a tunnel over the upload cap stayed open")
	}
	if _, _, code := connectVia(t, addr, cred, target); code != http.StatusForbidden {
		t.Fatalf("a capped session's next CONNECT: %d", code)
	}
	rec.wait(t, func(c ConnRecord) bool { return c.Run == "cap-run" && c.Reason == "upload cap reached" })
}

// Every request is recorded with its run: an allowlist miss too.
func TestProxyRecordsDenials(t *testing.T) {
	p := &Proxy{Allow: []string{"allowed.example"}}
	addr, rec := startProxy(t, p)
	cred, _ := p.MintSession(SessionOpts{Run: "run-d"})
	c, _, code := connectVia(t, addr, cred, "evil.example:443")
	c.Close()
	if code != http.StatusForbidden {
		t.Fatalf("%d", code)
	}
	rec.wait(t, func(c ConnRecord) bool {
		return c.Run == "run-d" && c.Outcome == "denied" && c.Target == "evil.example:443"
	})
}

func TestParseClientHelloRejectsGarbage(t *testing.T) {
	for _, b := range [][]byte{{}, []byte("GET / HTTP/1.1\r\n\r\n"), {0x16, 0x03, 0x01, 0x00, 0x05, 0x02, 0, 0, 1, 0}} {
		if _, _, err := readClientHello(bufio.NewReader(strings.NewReader(string(b)))); !errors.Is(err, errNotTLS) {
			t.Errorf("%q: %v", b, err)
		}
	}
}

// captureConn records what a TLS client writes and never answers.
type captureConn struct {
	net.Conn
	w *safeBuf
}

func (c *captureConn) Write(b []byte) (int, error)      { return c.w.Write(b) }
func (c *captureConn) Read([]byte) (int, error)         { time.Sleep(250 * time.Millisecond); return 0, io.EOF }
func (c *captureConn) Close() error                     { return nil }
func (c *captureConn) SetDeadline(time.Time) error      { return nil }
func (c *captureConn) SetReadDeadline(time.Time) error  { return nil }
func (c *captureConn) SetWriteDeadline(time.Time) error { return nil }
func (c *captureConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *captureConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }

type safeBuf struct {
	mu sync.Mutex
	b  []byte
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.b = append(s.b, p...)
	s.mu.Unlock()
	return len(p), nil
}

func (s *safeBuf) bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.b...)
}

// withECH appends an encrypted_client_hello extension to a one-record
// ClientHello, fixing up the lengths.
func withECH(rec []byte) []byte {
	ext := []byte{0xfe, 0x0d, 0x00, 0x01, 0x00}
	out := append(append([]byte(nil), rec...), ext...)
	binary.BigEndian.PutUint16(out[3:5], binary.BigEndian.Uint16(out[3:5])+uint16(len(ext)))
	hl := int(out[6])<<16 | int(out[7])<<8 | int(out[8])
	hl += len(ext)
	out[6], out[7], out[8] = byte(hl>>16), byte(hl>>8), byte(hl)
	// the extensions block length: after version(2) random(32) sid cs comp
	b := out[9:]
	i := 2 + 32
	i += 1 + int(b[i])
	i += 2 + int(binary.BigEndian.Uint16(b[i:]))
	i += 1 + int(b[i])
	binary.BigEndian.PutUint16(b[i:], binary.BigEndian.Uint16(b[i:])+uint16(len(ext)))
	return out
}

var _ = httptest.NewServer

// The download cap closes a tunnel too, symmetric with the upload cap.
func TestSessionDownloadCapClosesTunnel(t *testing.T) {
	up := newUpstream(t)
	target := up.ln.Addr().String()
	p := &Proxy{Allow: []string{target}}
	addr, rec := startProxy(t, p)

	cred, _ := p.MintSession(SessionOpts{Run: "dl-cap-run", Limits: EgressLimits{Download: 1000}})
	c, br, code := connectVia(t, addr, cred, target)
	if code != 200 {
		t.Fatalf("CONNECT %d", code)
	}
	c.Write(make([]byte, 2000)) // echoed back: 2000 bytes downstream, over the cap
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, err := br.ReadByte()
	var ne net.Error
	if err == nil || (errors.As(err, &ne) && ne.Timeout()) {
		t.Fatal("a tunnel over the download cap stayed open")
	}
	if _, _, code := connectVia(t, addr, cred, target); code != http.StatusForbidden {
		t.Fatalf("a capped session's next CONNECT: %d", code)
	}
	rec.wait(t, func(c ConnRecord) bool { return c.Run == "dl-cap-run" && c.Reason == "download cap reached" })
}

// A launch's non_tls allowance lets raw bytes through to the target it
// names, audited as allowed by its scope; another allowed target of the
// same session still gets the TLS check.
func TestNonTLSAllowanceExemptsOnlyItsTarget(t *testing.T) {
	bastion, other := newUpstream(t), newUpstream(t)
	bt, ot := bastion.ln.Addr().String(), other.ln.Addr().String()
	p := &Proxy{Allow: []string{bt, ot}}
	addr, rec := startProxy(t, p)
	cred, _ := p.MintSession(SessionOpts{Run: "run-9", RequireTLS: true, NonTLS: []string{bt}, NonTLSBy: "workflow deploy"})

	c, br, code := connectVia(t, addr, cred, bt)
	if code != 200 {
		t.Fatalf("CONNECT: %d", code)
	}
	banner := "SSH-2.0-OpenSSH_9.6\r\n"
	_, _ = io.WriteString(c, banner)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len(banner))
	if _, err := io.ReadFull(br, got); err != nil || string(got) != banner {
		t.Fatalf("raw bytes to the allowed host: %q, %v", got, err)
	}
	c.Close()
	r := rec.wait(t, func(c ConnRecord) bool { return c.Target == bt && c.Outcome != "refused" })
	if r.AllowedBy != "workflow deploy" || r.Run != "run-9" {
		t.Fatalf("the allowed tunnel's record: %+v", r)
	}

	c2, br2, code := connectVia(t, addr, cred, ot)
	defer c2.Close()
	if code != 200 {
		t.Fatalf("CONNECT: %d", code)
	}
	_, _ = io.WriteString(c2, banner)
	c2.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := br2.ReadByte(); err == nil {
		t.Fatal("raw bytes passed to a host the allowance does not name")
	}
	if r := rec.wait(t, func(c ConnRecord) bool { return c.Target == ot }); r.Outcome != "refused" || r.AllowedBy != "" {
		t.Fatalf("the other host's record: %+v", r)
	}
	if n := other.accepted.Load(); n != 0 {
		t.Fatalf("the other host was dialed %d time(s)", n)
	}
}
