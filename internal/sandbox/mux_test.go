package sandbox

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// muxPair connects a host mux to a container mux over in-memory pipes.
func muxPair(t *testing.T, sockDir string) (*Mux, chan error) {
	t.Helper()
	h2cR, h2cW := io.Pipe()
	c2hR, c2hW := io.Pipe()
	served := make(chan error, 1)
	go func() {
		served <- ServeContainerMux(h2cR, c2hW, sockDir)
		c2hW.Close()
	}()
	m, err := NewHostMux(c2hR, h2cW, h2cW)
	if err != nil {
		t.Fatalf("NewHostMux: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	return m, served
}

// echoServer is a loopback server echoing every byte back.
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

func roundTrip(t *testing.T, c net.Conn, size int) {
	t.Helper()
	want := make([]byte, size)
	_, _ = rand.Read(want)
	go func() {
		_, _ = c.Write(want)
		if cw, ok := c.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	_ = c.SetReadDeadline(time.Now().Add(20 * time.Second))
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("echoed %d bytes, want %d identical", len(got), len(want))
	}
}

// A connection to a container listener reaches the host target the host
// registered for it, both ways, past several windows.
func TestMuxContainerListenerReachesHostTarget(t *testing.T) {
	m, _ := muxPair(t, "")
	target := echoServer(t)
	addr, stop, err := m.Listen("tcp", "127.0.0.1:0", func() (net.Conn, error) { return net.Dial("tcp", target) })
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if !strings.HasPrefix(addr, "127.0.0.1:") || strings.HasSuffix(addr, ":0") {
		t.Fatalf("container listens at %q", addr)
	}
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	roundTrip(t, c, 3*muxWindow+12345)
}

// The host dials a server inside the container (opencode's), loopback only.
func TestMuxHostDialsIntoContainer(t *testing.T) {
	m, _ := muxPair(t, "")
	srv := echoServer(t)
	c, err := m.Dial(srv)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	roundTrip(t, c, 700000)
	if _, err := m.Dial("10.1.2.3:80"); err == nil {
		t.Fatal("dialed a non-loopback address into the container")
	}
}

// A dial the container cannot complete is refused, not hung.
func TestMuxDialRefused(t *testing.T) {
	m, _ := muxPair(t, "")
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	if _, err := m.Dial(addr); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("dial to a closed port: %v", err)
	}
}

// The container side refuses listeners the host should never ask for: a
// non-loopback address, a unix socket outside its socket dir.
func TestMuxContainerListenLimits(t *testing.T) {
	dir := t.TempDir()
	m, _ := muxPair(t, dir)
	none := func() (net.Conn, error) { return nil, io.EOF }
	if _, _, err := m.Listen("tcp", "0.0.0.0:0", none); err == nil {
		t.Fatal("listened on every interface")
	}
	if _, _, err := m.Listen("unix", filepath.Join(t.TempDir(), "x.sock"), none); err == nil {
		t.Fatal("listened on a unix socket outside the socket dir")
	}
	if _, _, err := m.Listen("unix", filepath.Join(dir, "sub", "..", "..", "x.sock"), none); err == nil {
		t.Fatal("listened on a unix socket that escapes the socket dir")
	}
	if _, _, err := m.Listen("udp", "127.0.0.1:0", none); err == nil {
		t.Fatal("listened on udp")
	}
	sock := filepath.Join(dir, "conductor.sock")
	target := echoServer(t)
	got, stop, err := m.Listen("unix", sock, func() (net.Conn, error) { return net.Dial("tcp", target) })
	if err != nil || got != sock {
		t.Fatalf("unix listen in the socket dir: %q %v", got, err)
	}
	defer stop()
	fi, err := os.Stat(sock)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode: %v %v", fi, err)
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, c, 1000)
	c.Close()
}

// Unlisten closes the container's listener.
func TestMuxUnlisten(t *testing.T) {
	m, _ := muxPair(t, "")
	target := echoServer(t)
	addr, stop, err := m.Listen("tcp", "127.0.0.1:0", func() (net.Conn, error) { return net.Dial("tcp", target) })
	if err != nil {
		t.Fatal(err)
	}
	stop()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			return
		}
		c.Close()
		if time.Now().After(deadline) {
			t.Fatal("the listener still accepts after unlisten")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A stalled reader on one stream does not hold up another.
func TestMuxStalledStreamDoesNotBlockOthers(t *testing.T) {
	m, _ := muxPair(t, "")
	// A sink that never reads.
	stall, _ := net.Listen("tcp", "127.0.0.1:0")
	defer stall.Close()
	var held []net.Conn
	var hmu sync.Mutex
	go func() {
		for {
			c, err := stall.Accept()
			if err != nil {
				return
			}
			hmu.Lock()
			held = append(held, c)
			hmu.Unlock()
		}
	}()
	defer func() {
		hmu.Lock()
		for _, c := range held {
			c.Close()
		}
		hmu.Unlock()
	}()
	s1, err := m.Dial(stall.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer s1.Close()
	go func() {
		big := make([]byte, 8<<20)
		_, _ = s1.Write(big) // blocks once the window and the socket buffers fill
	}()
	time.Sleep(200 * time.Millisecond)
	c, err := m.Dial(echoServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	roundTrip(t, c, 2*muxWindow)
}

// The container can open streams only to listeners the host registered:
// a forged open is refused, and it cannot ask the host to listen.
func TestMuxHostRefusesForgedFrames(t *testing.T) {
	h2cR, h2cW := io.Pipe()
	c2hR, c2hW := io.Pipe()
	defer h2cW.Close()
	defer c2hW.Close()
	go func() { _, _ = io.Copy(io.Discard, h2cR) }()
	frame := func(typ byte, id uint32, p []byte) {
		var hdr [muxHeader]byte
		hdr[0] = typ
		binary.BigEndian.PutUint32(hdr[1:5], id)
		binary.BigEndian.PutUint32(hdr[5:9], uint32(len(p)))
		_, _ = c2hW.Write(append(hdr[:], p...))
	}
	go frame(frHello, 0, []byte(muxVersion))
	m, err := NewHostMux(c2hR, h2cW, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	// An open naming a listener the host never registered: refused, the
	// mux lives on.
	var lid [4]byte
	binary.BigEndian.PutUint32(lid[:], 77)
	frame(frOpen, 2, lid[:])
	time.Sleep(50 * time.Millisecond)
	if m.Err() != nil {
		t.Fatalf("a refused open ended the mux: %v", m.Err())
	}
	if len(m.streams) != 0 {
		t.Fatal("a forged open made a stream")
	}
	// The container asking the host to listen ends the mux.
	b, _ := json.Marshal(muxListenReq{ID: 1, Net: "tcp", Addr: "127.0.0.1:0"})
	frame(frListen, 0, b)
	select {
	case <-m.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the host obeyed a listen request from the container")
	}
}

// A host-numbered stream id from the container, and an oversized frame,
// end the mux.
func TestMuxHostRejectsProtocolViolations(t *testing.T) {
	for name, send := range map[string]func(w io.Writer){
		"odd id": func(w io.Writer) {
			var hdr [muxHeader]byte
			hdr[0] = frOpen
			binary.BigEndian.PutUint32(hdr[1:5], 3)
			binary.BigEndian.PutUint32(hdr[5:9], 4)
			_, _ = w.Write(append(hdr[:], 0, 0, 0, 1))
		},
		"oversized": func(w io.Writer) {
			var hdr [muxHeader]byte
			hdr[0] = frData
			binary.BigEndian.PutUint32(hdr[5:9], muxMaxFrame+1)
			_, _ = w.Write(hdr[:])
		},
		"unknown type": func(w io.Writer) {
			var hdr [muxHeader]byte
			hdr[0] = 99
			_, _ = w.Write(hdr[:])
		},
	} {
		t.Run(name, func(t *testing.T) {
			h2cR, h2cW := io.Pipe()
			c2hR, c2hW := io.Pipe()
			defer c2hW.Close()
			go func() { _, _ = io.Copy(io.Discard, h2cR) }()
			go func() {
				var hdr [muxHeader]byte
				hdr[0] = frHello
				binary.BigEndian.PutUint32(hdr[5:9], uint32(len(muxVersion)))
				_, _ = c2hW.Write(append(hdr[:], muxVersion...))
				send(c2hW)
			}()
			m, err := NewHostMux(c2hR, h2cW, h2cW)
			if err != nil {
				return // ended before it was handed out: as good
			}
			select {
			case <-m.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("the mux survived a protocol violation")
			}
		})
	}
}

// Data past the granted window resets that stream only.
func TestMuxWindowOverrunResetsStream(t *testing.T) {
	m := newMux(true, strings.NewReader(""), io.Discard, nil)
	s := m.newStream(2)
	if err := m.handle(frData, 2, make([]byte, muxWindow)); err != nil {
		t.Fatal(err)
	}
	if err := m.handle(frData, 2, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if m.stream(2) != nil {
		t.Fatal("an overrun stream is still open")
	}
	s.mu.Lock()
	reset := s.reset
	s.mu.Unlock()
	if !reset {
		t.Fatal("the overrun stream was not reset")
	}
}

// A container side speaking another version is refused.
func TestMuxVersionMismatch(t *testing.T) {
	h2cR, h2cW := io.Pipe()
	c2hR, c2hW := io.Pipe()
	defer c2hW.Close()
	go func() { _, _ = io.Copy(io.Discard, h2cR) }()
	go func() {
		v := "conductor-mux/0"
		var hdr [muxHeader]byte
		hdr[0] = frHello
		binary.BigEndian.PutUint32(hdr[5:9], uint32(len(v)))
		_, _ = c2hW.Write(append(hdr[:], v...))
	}()
	if _, err := NewHostMux(c2hR, h2cW, h2cW); err == nil || !strings.Contains(err.Error(), "conductor-mux/0") {
		t.Fatalf("version mismatch: %v", err)
	}
}

// The container side ends cleanly when the host goes away, closing its
// listeners.
func TestMuxContainerEndsWithHost(t *testing.T) {
	m, served := muxPair(t, "")
	addr, _, err := m.Listen("tcp", "127.0.0.1:0", func() (net.Conn, error) { return nil, io.EOF })
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("container side: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the container side outlived the host")
	}
	if c, err := net.Dial("tcp", addr); err == nil {
		c.Close()
		t.Fatal("a listener outlived the mux")
	}
}

// Read deadlines fire on a quiet stream.
func TestMuxReadDeadline(t *testing.T) {
	m, _ := muxPair(t, "")
	quiet, _ := net.Listen("tcp", "127.0.0.1:0")
	defer quiet.Close()
	go func() {
		c, err := quiet.Accept()
		if err == nil {
			time.Sleep(3 * time.Second)
			c.Close()
		}
	}()
	c, err := m.Dial(quiet.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	start := time.Now()
	_, err = c.Read(make([]byte, 1))
	if !os.IsTimeout(err) && err != os.ErrDeadlineExceeded {
		t.Fatalf("read past the deadline: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("the deadline fired late")
	}
}
