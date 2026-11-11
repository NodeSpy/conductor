// Package keyringbroker serves a jailed plugin the keyring items it declares
// (plugin.Capabilities.Keyring), and nothing else.
//
// A CLI that keeps its login in the OS keyring reads it on Linux
// over the D-Bus session bus, from the Secret Service (GNOME Keyring,
// KWallet, KeePassXC). A jail cannot be handed that bus: it reaches every
// program in the desktop session, and the Secret Service has no per-item
// access control, so even a filtered bus that only talks to the Secret
// Service exposes every secret in the keyring.
//
// The broker is instead the jail's only session bus: a unix socket conductor
// serves outside the jail, bound into it, named by DBUS_SESSION_BUS_ADDRESS.
// It speaks just enough of the bus protocol for a client to connect, and a
// read-only Secret Service whose only items are the user's keyring items
// that carry every attribute of a declared one. Each request is answered
// from the user's real keyring at that moment: a search adds the declared
// attributes to the client's (so the keyring is never searched for anything
// else), and an item is re-checked against the declaration before its
// secret is read. Nothing can be written, deleted, unlocked or locked, and
// no other name on the bus exists.
package keyringbroker

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// The bounds on what a jailed client can make the broker (and through it the
// user's keyring daemon, which other programs share) do.
const (
	// maxConns is the most connections one broker serves at once.
	maxConns = 8
	// maxAuthLine and maxAuthLines bound the SASL exchange, and authTimeout
	// its duration.
	maxAuthLine  = 16 << 10
	maxAuthLines = 16
	// maxMessage is the largest message a client may send.
	maxMessage = 64 << 10
	// maxAttrs, maxAttrLen bound a client's search.
	maxAttrs   = 32
	maxAttrLen = 1 << 10
)

// Variables so tests can shorten them.
var (
	authTimeout = 10 * time.Second
	// idleTimeout closes a connection that sends nothing for this long.
	idleTimeout = 5 * time.Minute
)

// Upstream is the user's real keyring, as the broker reads it.
type Upstream interface {
	// Search finds the items that carry every attribute in attrs.
	Search(attrs map[string]string) (unlocked, locked []dbus.ObjectPath, err error)
	// Prop reads one org.freedesktop.Secret.Item property of an item.
	Prop(item dbus.ObjectPath, name string) (dbus.Variant, error)
	// Secret reads an item's secret in the clear.
	Secret(item dbus.ObjectPath) (value []byte, contentType string, err error)
	Close()
}

// Broker serves one plugin launch's declared keyring items on a socket.
type Broker struct {
	items []sdk.KeyringItem
	logf  func(string, ...any)
	dial  func() (Upstream, error)

	ln     net.Listener
	mu     sync.Mutex
	up     Upstream
	conns  map[net.Conn]bool
	logged map[string]bool
	closed bool
	// authTimeout, idleTimeout: the package's, when served.
	authTimeout, idleTimeout time.Duration
	nextID                   atomic.Uint32
	wg                       sync.WaitGroup
}

// Options configure Serve.
type Options struct {
	// Logf reports what the user should know: a keyring that cannot be
	// read (locked, absent), and what to do. Each distinct report once.
	Logf func(string, ...any)
	// Dial reaches the user's keyring; nil is the session bus's Secret
	// Service (SessionUpstream).
	Dial func() (Upstream, error)
}

// Serve listens on a unix socket at path and serves items there until the
// returned stop is called. The socket is the user's only (0600).
func Serve(path string, items []sdk.KeyringItem, o Options) (*Broker, error) {
	if len(items) == 0 {
		return nil, errors.New("keyring broker: no items declared")
	}
	// Each item is checked once more where it is served: the declared
	// attributes are the whole boundary, so one that would match other
	// programs' items is refused here too.
	for _, it := range items {
		if err := it.Check(); err != nil {
			return nil, fmt.Errorf("keyring broker: %w", err)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	b := &Broker{items: items, logf: o.Logf, dial: o.Dial, ln: ln, conns: map[net.Conn]bool{}, logged: map[string]bool{},
		authTimeout: authTimeout, idleTimeout: idleTimeout}
	if b.logf == nil {
		b.logf = func(string, ...any) {}
	}
	if b.dial == nil {
		b.dial = SessionUpstream
	}
	b.wg.Add(1)
	go b.accept()
	return b, nil
}

// Address is the D-Bus address of a broker serving at path.
func Address(path string) string { return "unix:path=" + path }

// Close stops serving: the socket, every connection, the upstream.
func (b *Broker) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	b.ln.Close()
	for c := range b.conns {
		c.Close()
	}
	b.mu.Unlock()
	b.wg.Wait()
	b.mu.Lock()
	if b.up != nil {
		b.up.Close()
		b.up = nil
	}
	b.mu.Unlock()
}

func (b *Broker) accept() {
	defer b.wg.Done()
	for {
		c, err := b.ln.Accept()
		if err != nil {
			return
		}
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			c.Close()
			return
		}
		if len(b.conns) >= maxConns {
			b.mu.Unlock()
			c.Close()
			b.report("conns", "its keyring connections are over the limit of %d at once: a further one is refused", maxConns)
			continue
		}
		b.conns[c] = true
		b.mu.Unlock()
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			defer func() {
				b.mu.Lock()
				delete(b.conns, c)
				b.mu.Unlock()
				c.Close()
			}()
			b.serveConn(c)
		}()
	}
}

// report logs a keyring problem once per broker.
func (b *Broker) report(key, format string, args ...any) {
	b.mu.Lock()
	seen := b.logged[key]
	b.logged[key] = true
	b.mu.Unlock()
	if !seen {
		b.logf(format, args...)
	}
}

// upstream is the connection to the user's keyring, dialled on first use
// and again after it fails. Every call through it is paced (upstreamPace).
func (b *Broker) upstream() (Upstream, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, errors.New("closed")
	}
	if b.up != nil {
		return b.up, nil
	}
	up, err := b.dial()
	if err != nil {
		return nil, err
	}
	b.up = paced{up}
	return b.up, nil
}

// drop forgets an upstream after err, so the next request dials again —
// only when the connection itself failed: an error the keyring replied
// with, or a request that could not be sent, leaves it up.
func (b *Broker) drop(up Upstream, err error) {
	var de dbus.Error
	if errors.As(err, &de) {
		return
	}
	if c, ok := up.(interface{ Connected() bool }); ok && c.Connected() {
		return
	}
	b.mu.Lock()
	if b.up == up {
		b.up = nil
		up.Close()
	}
	b.mu.Unlock()
}

// upstreamPace is shared by every broker in the process: the user's keyring
// daemon serves the whole desktop, and no plugin may crowd it.
var upstreamPace = newPacer(50, 50)

// pacer is a token bucket: rate calls a second, up to burst at once.
type pacer struct {
	mu    sync.Mutex
	rate  float64
	burst float64
	have  float64
	last  time.Time
}

func newPacer(rate, burst float64) *pacer {
	return &pacer{rate: rate, burst: burst, have: burst, last: time.Now()}
}

// wait blocks until a call may go.
func (p *pacer) wait() {
	p.mu.Lock()
	now := time.Now()
	p.have = min(p.burst, p.have+now.Sub(p.last).Seconds()*p.rate)
	p.last = now
	p.have--
	d := time.Duration(0)
	if p.have < 0 {
		d = time.Duration(-p.have / p.rate * float64(time.Second))
	}
	p.mu.Unlock()
	time.Sleep(d)
}

// paced is an Upstream whose every call waits for upstreamPace.
type paced struct{ Upstream }

func (p paced) Search(attrs map[string]string) ([]dbus.ObjectPath, []dbus.ObjectPath, error) {
	upstreamPace.wait()
	return p.Upstream.Search(attrs)
}

func (p paced) Prop(item dbus.ObjectPath, name string) (dbus.Variant, error) {
	upstreamPace.wait()
	return p.Upstream.Prop(item, name)
}

func (p paced) Secret(item dbus.ObjectPath) ([]byte, string, error) {
	upstreamPace.wait()
	return p.Upstream.Secret(item)
}

func (p paced) Connected() bool {
	if c, ok := p.Upstream.(interface{ Connected() bool }); ok {
		return c.Connected()
	}
	return false
}

// declared reports whether an item stored under attrs is one of the
// declared items.
func (b *Broker) declared(attrs map[string]string) bool {
	for _, it := range b.items {
		if it.Matches(attrs) {
			return true
		}
	}
	return false
}

func (b *Broker) labels() string {
	var ls []string
	for _, it := range b.items {
		ls = append(ls, it.Label)
	}
	return strings.Join(ls, ", ")
}

// unavailable explains, once, why the keyring could not be read.
func (b *Broker) unavailable(err error) {
	var de dbus.Error
	switch {
	case errors.Is(err, errNoBus):
		b.report("nobus", "its keyring items (%s) cannot be read: %v. A CLI that also keeps its login in a file still uses that; otherwise run conductor in a desktop session with an unlocked keyring, or give the CLI its token another way", b.labels(), err)
	case errors.As(err, &de) && (de.Name == "org.freedesktop.DBus.Error.ServiceUnknown" || de.Name == "org.freedesktop.DBus.Error.NameHasNoOwner"):
		b.report("nosvc", "its keyring items (%s) cannot be read: no keyring (Secret Service) is running in conductor's session — start one (GNOME Keyring, KWallet, KeePassXC) and log in to it", b.labels())
	default:
		b.report("err:"+err.Error(), "its keyring items (%s) cannot be read: %v", b.labels(), err)
	}
}

// --- connection ------------------------------------------------------------

const (
	busName     = "org.freedesktop.DBus"
	secretsName = "org.freedesktop.secrets"
	secretsID   = ":1.0" // the Secret Service's unique name on this bus
)

// conn is one client connection.
type conn struct {
	b      *Broker
	rw     net.Conn
	in     *bufio.Reader
	wmu    sync.Mutex
	serial uint32
	name   string // the client's unique name
	// sessions are the client's open Secret Service sessions.
	sessions map[dbus.ObjectPath]*session
	nextSess int
}

func (b *Broker) serveConn(rw net.Conn) {
	c := &conn{b: b, rw: rw, in: bufio.NewReader(rw), sessions: map[dbus.ObjectPath]*session{}}
	_ = rw.SetDeadline(time.Now().Add(b.authTimeout))
	if err := c.auth(); err != nil {
		return
	}
	_ = rw.SetDeadline(time.Time{})
	c.name = ":1." + strconv.FormatUint(uint64(b.nextID.Add(1)), 10)
	for {
		_ = rw.SetReadDeadline(time.Now().Add(b.idleTimeout))
		raw, err := c.readMessage()
		if err != nil {
			return
		}
		msg, err := dbus.DecodeMessage(bytes.NewReader(raw))
		if err != nil {
			return
		}
		if msg.Type != dbus.TypeMethodCall {
			continue
		}
		if !validBody(msg.Body) {
			c.fail(msg, senderFor(header[string](msg, dbus.FieldDestination)), "org.freedesktop.DBus.Error.InvalidArgs", "a string is not valid UTF-8, or holds a NUL")
			continue
		}
		c.handle(msg)
	}
}

// readMessage reads one whole message, refusing one over maxMessage before
// reading it: the fixed header gives the lengths of the header fields and
// the body.
func (c *conn) readMessage() ([]byte, error) {
	var fixed [16]byte
	if _, err := io.ReadFull(c.in, fixed[:]); err != nil {
		return nil, err
	}
	var order binary.ByteOrder
	switch fixed[0] {
	case 'l':
		order = binary.LittleEndian
	case 'B':
		order = binary.BigEndian
	default:
		return nil, errors.New("bad byte order")
	}
	body := uint64(order.Uint32(fixed[4:8]))
	fields := uint64(order.Uint32(fixed[12:16]))
	total := 16 + (fields+7)&^7 + body
	if total > maxMessage {
		return nil, fmt.Errorf("message of %d bytes is over %d", total, maxMessage)
	}
	raw := make([]byte, total)
	copy(raw, fixed[:])
	if _, err := io.ReadFull(c.in, raw[16:]); err != nil {
		return nil, err
	}
	return raw, nil
}

// validBody reports whether every string in a message body is one the bus
// carries: valid UTF-8, without NUL. The user's bus would refuse it, so it
// is refused here.
func validBody(vs []any) bool {
	for _, v := range vs {
		if !validValue(v) {
			return false
		}
	}
	return true
}

func validValue(v any) bool {
	switch t := v.(type) {
	case string:
		return validString(t)
	case dbus.ObjectPath:
		return t.IsValid()
	case dbus.Signature:
		return validString(t.String())
	case dbus.Variant:
		return validValue(t.Value())
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.String:
		return validString(r.String())
	case reflect.Slice, reflect.Array:
		if r.Type().Elem().Kind() == reflect.Uint8 {
			return true // bytes
		}
		for i := range r.Len() {
			if !validValue(r.Index(i).Interface()) {
				return false
			}
		}
	case reflect.Map:
		for it := r.MapRange(); it.Next(); {
			if !validValue(it.Key().Interface()) || !validValue(it.Value().Interface()) {
				return false
			}
		}
	case reflect.Struct:
		for i := range r.NumField() {
			if r.Type().Field(i).IsExported() && !validValue(r.Field(i).Interface()) {
				return false
			}
		}
	case reflect.Pointer, reflect.Interface:
		if !r.IsNil() {
			return validValue(r.Elem().Interface())
		}
	}
	return true
}

func validString(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }

// auth runs the SASL exchange: EXTERNAL or ANONYMOUS, then BEGIN. The
// socket is reachable only from the jail it is bound into.
func (c *conn) auth() error {
	nul := make([]byte, 1)
	if _, err := io.ReadFull(c.in, nul); err != nil || nul[0] != 0 {
		return errors.New("no nul byte")
	}
	guid := make([]byte, 16)
	if _, err := io.ReadFull(randReader, guid); err != nil {
		return err
	}
	ok := "OK " + hex.EncodeToString(guid)
	authed := false
	for lines := 0; ; lines++ {
		if lines >= maxAuthLines {
			return errors.New("too many auth lines")
		}
		line, err := c.authLine()
		if err != nil {
			return err
		}
		f := strings.Fields(strings.TrimRight(line, "\r\n"))
		if len(f) == 0 {
			c.writeLine("ERROR")
			continue
		}
		switch f[0] {
		case "AUTH":
			switch {
			case len(f) >= 2 && (f[1] == "EXTERNAL" || f[1] == "ANONYMOUS"):
				if f[1] == "EXTERNAL" && len(f) == 2 {
					c.writeLine("DATA")
					continue
				}
				authed = true
				c.writeLine(ok)
			default:
				c.writeLine("REJECTED EXTERNAL ANONYMOUS")
			}
		case "DATA":
			authed = true
			c.writeLine(ok)
		case "NEGOTIATE_UNIX_FD":
			c.writeLine("ERROR file descriptors are not passed")
		case "BEGIN":
			if !authed {
				return errors.New("BEGIN before auth")
			}
			return nil
		case "CANCEL", "ERROR":
			authed = false
			c.writeLine("REJECTED EXTERNAL ANONYMOUS")
		default:
			c.writeLine("ERROR")
		}
	}
}

// authLine reads one SASL line, refusing one over maxAuthLine before it has
// been read whole.
func (c *conn) authLine() (string, error) {
	var line []byte
	for {
		part, err := c.in.ReadSlice('\n')
		line = append(line, part...)
		if len(line) > maxAuthLine {
			return "", errors.New("auth line too long")
		}
		if err == nil {
			return string(line), nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return "", err
		}
	}
}

func (c *conn) writeLine(s string) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, _ = c.rw.Write([]byte(s + "\r\n"))
}

// send writes msg with the next serial. godbus keeps a message's serial
// private, so it is written into the encoded header (bytes 8–11).
func (c *conn) send(msg *dbus.Message) {
	var buf bytes.Buffer
	if err := msg.EncodeTo(&buf, binary.LittleEndian); err != nil {
		return
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.serial++
	raw := buf.Bytes()
	binary.LittleEndian.PutUint32(raw[8:12], c.serial)
	_, _ = c.rw.Write(raw)
}

func (c *conn) reply(call *dbus.Message, sender string, body ...any) {
	if call.Flags&dbus.FlagNoReplyExpected != 0 {
		return
	}
	h := map[dbus.HeaderField]dbus.Variant{
		dbus.FieldReplySerial: dbus.MakeVariant(call.Serial()),
		dbus.FieldDestination: dbus.MakeVariant(c.name),
		dbus.FieldSender:      dbus.MakeVariant(sender),
	}
	if len(body) > 0 {
		h[dbus.FieldSignature] = dbus.MakeVariant(dbus.SignatureOf(body...))
	}
	c.send(&dbus.Message{Type: dbus.TypeMethodReply, Flags: dbus.FlagNoAutoStart, Headers: h, Body: body})
}

func (c *conn) fail(call *dbus.Message, sender, name, text string) {
	if call.Flags&dbus.FlagNoReplyExpected != 0 {
		return
	}
	h := map[dbus.HeaderField]dbus.Variant{
		dbus.FieldReplySerial: dbus.MakeVariant(call.Serial()),
		dbus.FieldDestination: dbus.MakeVariant(c.name),
		dbus.FieldSender:      dbus.MakeVariant(sender),
		dbus.FieldErrorName:   dbus.MakeVariant(name),
		dbus.FieldSignature:   dbus.MakeVariant(dbus.SignatureOf(text)),
	}
	c.send(&dbus.Message{Type: dbus.TypeError, Flags: dbus.FlagNoAutoStart, Headers: h, Body: []any{text}})
}

func (c *conn) signal(path dbus.ObjectPath, iface, member string, body ...any) {
	h := map[dbus.HeaderField]dbus.Variant{
		dbus.FieldPath:        dbus.MakeVariant(path),
		dbus.FieldInterface:   dbus.MakeVariant(iface),
		dbus.FieldMember:      dbus.MakeVariant(member),
		dbus.FieldDestination: dbus.MakeVariant(c.name),
		dbus.FieldSender:      dbus.MakeVariant(busName),
	}
	if len(body) > 0 {
		h[dbus.FieldSignature] = dbus.MakeVariant(dbus.SignatureOf(body...))
	}
	c.send(&dbus.Message{Type: dbus.TypeSignal, Headers: h, Body: body})
}

func header[T any](m *dbus.Message, f dbus.HeaderField) T {
	var zero T
	v, ok := m.Headers[f]
	if !ok {
		return zero
	}
	t, _ := v.Value().(T)
	return t
}

func (c *conn) handle(m *dbus.Message) {
	dest := header[string](m, dbus.FieldDestination)
	path := header[dbus.ObjectPath](m, dbus.FieldPath)
	iface := header[string](m, dbus.FieldInterface)
	member := header[string](m, dbus.FieldMember)
	switch {
	case iface == "org.freedesktop.DBus.Peer":
		switch member {
		case "Ping":
			c.reply(m, senderFor(dest))
		case "GetMachineId":
			c.reply(m, senderFor(dest), strings.Repeat("0", 32))
		default:
			c.fail(m, senderFor(dest), "org.freedesktop.DBus.Error.UnknownMethod", member)
		}
	case dest == busName || dest == "":
		c.bus(m, member)
	case dest == secretsName || dest == secretsID:
		c.secrets(m, path, iface, member)
	default:
		c.fail(m, busName, "org.freedesktop.DBus.Error.ServiceUnknown", "The name "+dest+" was not provided by any .service files")
	}
}

func senderFor(dest string) string {
	if dest == secretsName || dest == secretsID {
		return secretsID
	}
	return busName
}

// bus answers the org.freedesktop.DBus calls a client makes on connecting
// and while it looks for the Secret Service.
func (c *conn) bus(m *dbus.Message, member string) {
	arg := func() string {
		if len(m.Body) > 0 {
			s, _ := m.Body[0].(string)
			return s
		}
		return ""
	}
	switch member {
	case "Hello":
		c.reply(m, busName, c.name)
		c.signal("/org/freedesktop/DBus", busName, "NameAcquired", c.name)
	case "AddMatch", "RemoveMatch":
		c.reply(m, busName)
	case "GetNameOwner":
		switch arg() {
		case busName:
			c.reply(m, busName, busName)
		case secretsName, secretsID:
			c.reply(m, busName, secretsID)
		case c.name:
			c.reply(m, busName, c.name)
		default:
			c.fail(m, busName, "org.freedesktop.DBus.Error.NameHasNoOwner", "Could not get owner of name '"+arg()+"': no such name")
		}
	case "NameHasOwner":
		n := arg()
		c.reply(m, busName, n == busName || n == secretsName || n == secretsID || n == c.name)
	case "StartServiceByName":
		if arg() == secretsName {
			c.reply(m, busName, uint32(2)) // already running
			return
		}
		c.fail(m, busName, "org.freedesktop.DBus.Error.ServiceUnknown", "The name "+arg()+" was not provided by any .service files")
	case "ListNames":
		c.reply(m, busName, []string{busName, secretsName, secretsID, c.name})
	case "ListActivatableNames":
		c.reply(m, busName, []string{busName})
	case "GetId":
		c.reply(m, busName, strings.Repeat("0", 32))
	case "Introspect":
		c.reply(m, busName, `<!DOCTYPE node PUBLIC "-//freedesktop//DTD D-BUS Object Introspection 1.0//EN" "http://www.freedesktop.org/standards/dbus/1.0/introspect.dtd"><node/>`)
	default:
		c.fail(m, busName, "org.freedesktop.DBus.Error.AccessDenied", "not available in this plugin's jail")
	}
}
