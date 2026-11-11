package keyringbroker

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// serveCounted serves the fake keyring and counts how often the upstream is
// dialled (each dial after the first is a dropped connection to the user's
// keyring).
func serveCounted(t *testing.T, up Upstream) (path string, b *Broker, dials *atomic.Int32) {
	t.Helper()
	dir, err := os.MkdirTemp("", "kb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path = filepath.Join(dir, "bus")
	dials = &atomic.Int32{}
	b, err = Serve(path, declared, Options{Dial: func() (Upstream, error) { dials.Add(1); return up, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return path, b, dials
}

// closedSoon reports whether the broker closes c within d.
func closedSoon(c net.Conn, d time.Duration) bool {
	_ = c.SetReadDeadline(time.Now().Add(d))
	buf := make([]byte, 4096)
	for {
		_, err := c.Read(buf)
		if err == nil {
			continue
		}
		var ne net.Error
		return !(errors.As(err, &ne) && ne.Timeout())
	}
}

// rawAuthed is a connection past SASL, before Hello.
func rawAuthed(t *testing.T, path string) net.Conn {
	t.Helper()
	nc, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close() })
	if _, err := nc.Write([]byte("\x00AUTH ANONYMOUS\r\nBEGIN\r\n")); err != nil {
		t.Fatal(err)
	}
	line := make([]byte, 64)
	_ = nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := nc.Read(line); err != nil || !strings.HasPrefix(string(line[:n]), "OK ") {
		t.Fatalf("auth: %q %v", line[:n], err)
	}
	_ = nc.SetReadDeadline(time.Time{})
	return nc
}

// searchMessage encodes a SearchItems call with serial 1.
func searchMessage(t *testing.T, attrs map[string]string) []byte {
	t.Helper()
	m := &dbus.Message{Type: dbus.TypeMethodCall, Headers: map[dbus.HeaderField]dbus.Variant{
		dbus.FieldPath:        dbus.MakeVariant(servicePath),
		dbus.FieldInterface:   dbus.MakeVariant(ifService),
		dbus.FieldMember:      dbus.MakeVariant("SearchItems"),
		dbus.FieldDestination: dbus.MakeVariant(secretsName),
		dbus.FieldSignature:   dbus.MakeVariant(dbus.SignatureOf(attrs)),
	}, Body: []any{attrs}}
	var buf bytes.Buffer
	if err := m.EncodeTo(&buf, binary.LittleEndian); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	binary.LittleEndian.PutUint32(raw[8:12], 1)
	return raw
}

// The SASL exchange is bounded: a line over 16 KiB is refused before it is
// read whole (a client streaming one endless line cannot grow conductor's
// memory), so is the 17th line, and so is a client that stalls.
func TestBrokerAuthIsBounded(t *testing.T) {
	path, _, _ := serveCounted(t, newFake())

	nc, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	nc.Write([]byte{0})
	sent := 0
	chunk := []byte(strings.Repeat("A", 64<<10))
	_ = nc.SetWriteDeadline(time.Now().Add(5 * time.Second))
	for sent < 64<<20 {
		n, err := nc.Write(chunk)
		sent += n
		if err != nil {
			break
		}
	}
	if sent >= 64<<20 {
		t.Fatalf("the broker read %d MiB of one auth line", sent>>20)
	}

	nc2, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer nc2.Close()
	nc2.Write([]byte("\x00" + strings.Repeat("NOPE\r\n", 20)))
	if !closedSoon(nc2, 5*time.Second) {
		t.Fatal("20 auth lines: still open")
	}

	old := authTimeout
	authTimeout = 200 * time.Millisecond
	path, _, _ = serveCounted(t, newFake())
	authTimeout = old
	nc3, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer nc3.Close()
	nc3.Write([]byte{0})
	if !closedSoon(nc3, 3*time.Second) {
		t.Fatal("a stalled auth: still open")
	}
}

// At most maxConns connections are served at once; a further one is closed.
func TestBrokerConnectionLimit(t *testing.T) {
	path, b, _ := serveCounted(t, newFake())
	var cs []net.Conn
	for i := 0; i < 3*maxConns; i++ {
		nc, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		defer nc.Close()
		cs = append(cs, nc)
	}
	time.Sleep(200 * time.Millisecond)
	b.mu.Lock()
	held := len(b.conns)
	b.mu.Unlock()
	if held > maxConns {
		t.Fatalf("%d connections held, limit %d", held, maxConns)
	}
	closed := 0
	for _, c := range cs {
		if closedSoon(c, 100*time.Millisecond) {
			closed++
		}
	}
	if closed < 2*maxConns {
		t.Fatalf("%d of %d connections over the limit closed", closed, 2*maxConns)
	}
}

// A connection that sends nothing for idleTimeout is closed.
func TestBrokerIdleTimeout(t *testing.T) {
	old := idleTimeout
	idleTimeout = 200 * time.Millisecond
	path, _, _ := serveCounted(t, newFake())
	idleTimeout = old
	nc := rawAuthed(t, path)
	if !closedSoon(nc, 3*time.Second) {
		t.Fatal("an idle connection: still open")
	}
}

// A message over 64 KiB is refused from its header, before its body is read;
// a search of too many or too long attributes is refused, and the keyring is
// not searched.
func TestBrokerMessageAndSearchBounds(t *testing.T) {
	up := newFake()
	path, _, _ := serveCounted(t, up)
	nc := rawAuthed(t, path)
	hdr := make([]byte, 16)
	hdr[0], hdr[1], hdr[3] = 'l', byte(dbus.TypeMethodCall), 1
	binary.LittleEndian.PutUint32(hdr[4:8], 512<<20)
	binary.LittleEndian.PutUint32(hdr[8:12], 1)
	binary.LittleEndian.PutUint32(hdr[12:16], 0)
	nc.Write(hdr)
	if !closedSoon(nc, 3*time.Second) {
		t.Fatal("a 512 MiB message: still open")
	}

	c, _ := serveFake(t, up, declared)
	many := map[string]string{}
	for i := 0; i <= maxAttrs; i++ {
		many["k"+strconv.Itoa(i)] = "v"
	}
	long := map[string]string{"k": strings.Repeat("v", maxAttrLen+1)}
	for name, q := range map[string]map[string]string{"too many": many, "too long": long} {
		var u, l []dbus.ObjectPath
		if err := svc(c).Call(ifService+".SearchItems", 0, q).Store(&u, &l); err == nil {
			t.Errorf("%s: searched", name)
		}
	}
	if s, _ := up.seen(); len(s) != 0 {
		t.Fatalf("the keyring was searched: %v", s)
	}
}

// failingKeyring is a keyring that answers every search with an error reply.
type failingKeyring struct{ *fakeKeyring }

func (failingKeyring) Search(map[string]string) (u, l []dbus.ObjectPath, err error) {
	return nil, nil, dbus.Error{Name: "org.freedesktop.DBus.Error.Failed", Body: []any{"no"}}
}

// A string the bus would refuse (not UTF-8, or holding a NUL) is refused
// before it reaches the user's keyring, whose bus would drop the connection
// for it; and an error the keyring replies with does not drop the
// connection either.
func TestBrokerInvalidStringsAndReplyErrors(t *testing.T) {
	up := newFake()
	path, _, dials := serveCounted(t, up)
	nc := rawAuthed(t, path)
	raw := searchMessage(t, map[string]string{"k": "QQ"})
	i := bytes.Index(raw, []byte("QQ"))
	for _, bad := range [][2]byte{{0xff, 0xfe}, {'a', 0}} {
		raw[i], raw[i+1] = bad[0], bad[1]
		if _, err := nc.Write(raw); err != nil {
			t.Fatal(err)
		}
		_ = nc.SetReadDeadline(time.Now().Add(3 * time.Second))
		m, err := dbus.DecodeMessage(nc)
		if err != nil {
			t.Fatalf("%q: no reply: %v", bad, err)
		}
		if m.Type != dbus.TypeError || header[string](m, dbus.FieldErrorName) != "org.freedesktop.DBus.Error.InvalidArgs" {
			t.Fatalf("%q: reply %v %v", bad, m.Type, m.Headers)
		}
	}
	if s, _ := up.seen(); len(s) != 0 {
		t.Fatalf("forwarded to the keyring: %q", s)
	}

	fpath, _, fdials := serveCounted(t, failingKeyring{newFake()})
	fc, err := dbus.Dial(Address(fpath))
	if err != nil {
		t.Fatal(err)
	}
	defer fc.Close()
	if err := fc.Auth(nil); err != nil {
		t.Fatal(err)
	}
	if err := fc.Hello(); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		_ = svc(fc).Call(ifService+".SearchItems", 0, map[string]string{}).Err
	}
	if n := fdials.Load(); n != 1 {
		t.Fatalf("an error reply re-dialled the keyring: %d dials", n)
	}
	_ = dials
}

// Calls to the user's keyring are paced across every broker.
func TestPacer(t *testing.T) {
	p := newPacer(20, 1)
	start := time.Now()
	for range 5 {
		p.wait()
	}
	if d := time.Since(start); d < 150*time.Millisecond {
		t.Fatalf("5 calls at 20/s, burst 1, took %v", d)
	}
}
