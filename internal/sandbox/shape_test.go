package sandbox

import (
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// shrinkShapes scales the shape thresholds down for a test.
func shrinkShapes(t *testing.T) {
	g, a, turns, mx, bu := ServerFirstGap, interactiveMinAge, interactiveMinTurns, interactiveMaxBytes, bulkUploadMin
	ServerFirstGap, interactiveMinAge, bulkUploadMin = 60*time.Millisecond, 150*time.Millisecond, 64<<10
	t.Cleanup(func() {
		ServerFirstGap, interactiveMinAge, interactiveMinTurns, interactiveMaxBytes, bulkUploadMin = g, a, turns, mx, bu
	})
}

// serve runs handle on every connection to a fresh loopback listener.
func serve(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); handle(c) }()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

// commander is a remote operator: after each of the client's replies and a
// silence, it sends the next command.
func commander(c net.Conn) {
	buf := make([]byte, 64)
	for i := 0; i < 8; i++ {
		time.Sleep(100 * time.Millisecond)
		if _, err := io.WriteString(c, "id\n"); err != nil {
			return
		}
		if _, err := c.Read(buf); err != nil {
			return
		}
	}
	time.Sleep(time.Second)
}

type shapeLog struct {
	mu  sync.Mutex
	evs []ShapeEvent
}

func (l *shapeLog) add(e ShapeEvent) { l.mu.Lock(); l.evs = append(l.evs, e); l.mu.Unlock() }
func (l *shapeLog) get() []ShapeEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]ShapeEvent(nil), l.evs...)
}

// runShell answers each command, as a shell on the agent's side would.
func runShell(c net.Conn, br io.Reader, d time.Duration) {
	c.SetReadDeadline(time.Now().Add(d))
	buf := make([]byte, 64)
	for {
		if _, err := br.Read(buf); err != nil {
			return
		}
		io.WriteString(c, "uid=1000\n")
	}
}

// A tunnel whose far end keeps speaking first after silences, long-lived and
// small, is flagged interactive while it is open — whatever program holds
// it; alert reports it, close also closes it, an allowance covering the
// target reports it as allowed.
func TestInteractiveShapedTunnelsAreFlagged(t *testing.T) {
	shrinkShapes(t)
	target := serve(t, commander)
	for _, c := range []struct {
		mode, want string
		allowed    []string
	}{
		{"", ShapeAlert, nil},
		{ShapeClose, ShapeClose, nil},
		{ShapeAlert, "allowed", []string{target}},
	} {
		var log shapeLog
		p := &Proxy{Allow: []string{target}}
		addr, rec := startProxy(t, p)
		cred, _ := p.MintSession(SessionOpts{Run: "r1", Shape: c.mode, ShapeAllowed: c.allowed, ShapeAllowedBy: "workflow ops", OnShape: log.add})
		conn, br, code := connectVia(t, addr, cred, target)
		if code != 200 {
			t.Fatalf("CONNECT %d", code)
		}
		start := time.Now()
		runShell(conn, br, 3*time.Second)
		conn.Close()
		r := rec.wait(t, func(r ConnRecord) bool { return r.Target == target })
		evs := log.get()
		if len(evs) != 1 || evs[0].Kind != ShapeInteractive || evs[0].Action != c.want || evs[0].Run != "r1" || evs[0].Target != target || evs[0].ServerFirst < 3 {
			t.Fatalf("%s: events %+v", c.mode, evs)
		}
		if c.want == "allowed" && evs[0].AllowedBy != "workflow ops" {
			t.Fatalf("allowed by %q", evs[0].AllowedBy)
		}
		if r.Shape != ShapeInteractive || r.ServerFirst < 3 {
			t.Fatalf("%s: record %+v", c.mode, r)
		}
		closedEarly := time.Since(start) < 1500*time.Millisecond
		if (c.want == ShapeClose) != closedEarly || (c.want == ShapeClose && !strings.Contains(r.Reason, "interactive")) {
			t.Fatalf("%s: closed early %v, reason %q", c.mode, closedEarly, r.Reason)
		}
	}
}

// Request and response — the client speaking first after each silence, the
// server answering within the gap — is not interactive; nor is anything
// with shapes off. (A server slower than the gap every time is
// indistinguishable by timing: see the thresholds.)
func TestRequestResponseTunnelsAreNotFlagged(t *testing.T) {
	shrinkShapes(t)
	slowEcho := serve(t, func(c net.Conn) {
		buf := make([]byte, 64)
		for {
			n, err := c.Read(buf)
			if err != nil {
				return
			}
			time.Sleep(20 * time.Millisecond) // an answer, under the gap
			c.Write(buf[:n])
		}
	})
	var log shapeLog
	p := &Proxy{Allow: []string{slowEcho}}
	addr, rec := startProxy(t, p)
	cred, _ := p.MintSession(SessionOpts{Run: "r2", OnShape: log.add})
	conn, br, _ := connectVia(t, addr, cred, slowEcho)
	buf := make([]byte, 8)
	for i := 0; i < 8; i++ {
		io.WriteString(conn, "GET\n")
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := io.ReadFull(br, buf[:4]); err != nil {
			t.Fatal(err)
		}
		time.Sleep(80 * time.Millisecond)
	}
	conn.Close()
	r := rec.wait(t, func(r ConnRecord) bool { return r.Target == slowEcho })
	if evs := log.get(); len(evs) != 0 || r.Shape != "" {
		t.Fatalf("request/response flagged: %+v %+v", evs, r)
	}

	// Shapes off: the commander is not reported, its features still are.
	cmd := serve(t, commander)
	p2 := &Proxy{Allow: []string{cmd}}
	addr2, rec2 := startProxy(t, p2)
	cred2, _ := p2.MintSession(SessionOpts{Run: "r3", Shape: ShapeOff, OnShape: log.add})
	c2, br2, _ := connectVia(t, addr2, cred2, cmd)
	runShell(c2, br2, 2*time.Second)
	c2.Close()
	r2 := rec2.wait(t, func(r ConnRecord) bool { return r.Target == cmd })
	if evs := log.get(); len(evs) != 0 || r2.Shape != "" || r2.ServerFirst < 3 {
		t.Fatalf("shapes off: %+v %+v", evs, r2)
	}
}

// A tunnel that sends far more than it receives is flagged a bulk upload,
// even if it lasts a fraction of the watcher's interval.
func TestBulkUploadIsFlaggedAtOnce(t *testing.T) {
	shrinkShapes(t)
	sink := serve(t, func(c net.Conn) { io.Copy(io.Discard, c) })
	var log shapeLog
	p := &Proxy{Allow: []string{sink}}
	addr, rec := startProxy(t, p)
	cred, _ := p.MintSession(SessionOpts{Run: "r4", OnShape: log.add})
	conn, _, _ := connectVia(t, addr, cred, sink)
	start := time.Now()
	conn.Write(make([]byte, 256<<10))
	conn.Close()
	r := rec.wait(t, func(r ConnRecord) bool { return r.Target == sink })
	if time.Since(start) > 3*time.Second {
		t.Fatal("slow")
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(log.get()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	evs := log.get()
	if len(evs) != 1 || evs[0].Kind != ShapeBulkUpload || evs[0].Action != ShapeAlert || r.Shape != ShapeBulkUpload {
		t.Fatalf("bulk upload: %+v %+v", evs, r)
	}
}

// Not interactive: a young tunnel however many turns it has had; a server
// that streams with no turns from the client (events, a stalled download);
// a large upload that is answered in kind is not a bulk upload.
func TestShapesNeedAgeTurnsAndImbalance(t *testing.T) {
	shrinkShapes(t)
	check := func(name string, target string, drive func(net.Conn, io.Reader)) {
		t.Helper()
		var log shapeLog
		p := &Proxy{Allow: []string{target}}
		addr, rec := startProxy(t, p)
		cred, _ := p.MintSession(SessionOpts{Run: "r", OnShape: log.add})
		conn, br, code := connectVia(t, addr, cred, target)
		if code != 200 {
			t.Fatalf("%s: CONNECT %d", name, code)
		}
		drive(conn, br)
		conn.Close()
		r := rec.wait(t, func(r ConnRecord) bool { return r.Target == target })
		time.Sleep(50 * time.Millisecond)
		if evs := log.get(); len(evs) != 0 || r.Shape != "" {
			t.Errorf("%s flagged: %+v %+v", name, evs, r)
		}
	}
	age := interactiveMinAge
	interactiveMinAge = time.Hour
	check("a young commander", serve(t, commander), func(c net.Conn, br io.Reader) { runShell(c, br, 2*time.Second) })
	interactiveMinAge = age

	streamer := serve(t, func(c net.Conn) {
		for i := 0; i < 8; i++ {
			time.Sleep(100 * time.Millisecond)
			if _, err := io.WriteString(c, "data: tick\n"); err != nil {
				return
			}
		}
	})
	check("a server stream", streamer, func(c net.Conn, br io.Reader) {
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		io.Copy(io.Discard, br)
	})

	// At the real threshold: what is in flight (socket buffers) is small
	// beside it, so an answered upload keeps its ratio near one.
	bulkUploadMin = 16 << 20
	echo := serve(t, func(c net.Conn) { io.Copy(c, c) })
	check("an echoed upload", echo, func(c net.Conn, br io.Reader) {
		go c.Write(make([]byte, 32<<20))
		c.SetReadDeadline(time.Now().Add(20 * time.Second))
		if n, err := io.ReadFull(br, make([]byte, 32<<20)); err != nil {
			t.Errorf("echoed %d: %v", n, err)
		}
	})
}
