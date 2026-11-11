package keyringbroker

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// fakeKeyring is an upstream keyring: items by path.
type fakeKeyring struct {
	mu       sync.Mutex
	items    map[dbus.ObjectPath]fakeItem
	searches []map[string]string
	secrets  []dbus.ObjectPath
}

type fakeItem struct {
	attrs  map[string]string
	value  string
	locked bool
}

func (f *fakeKeyring) Search(attrs map[string]string) (u, l []dbus.ObjectPath, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.searches = append(f.searches, maps.Clone(attrs))
	for p, it := range f.items {
		if (sdk.KeyringItem{Label: "q", Attributes: attrs}).Matches(it.attrs) || len(attrs) == 0 {
			if it.locked {
				l = append(l, p)
			} else {
				u = append(u, p)
			}
		}
	}
	return u, l, nil
}

func (f *fakeKeyring) Prop(item dbus.ObjectPath, name string) (dbus.Variant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[item]
	if !ok {
		return dbus.Variant{}, dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownObject"}
	}
	switch name {
	case "Attributes":
		return dbus.MakeVariant(it.attrs), nil
	case "Locked":
		return dbus.MakeVariant(it.locked), nil
	case "Label":
		return dbus.MakeVariant("label of " + string(item)), nil
	}
	return dbus.MakeVariant(uint64(0)), nil
}

func (f *fakeKeyring) Secret(item dbus.ObjectPath) ([]byte, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secrets = append(f.secrets, item)
	it, ok := f.items[item]
	if !ok {
		return nil, "", dbus.Error{Name: errNoObject}
	}
	if it.locked {
		return nil, "", dbus.Error{Name: errIsLocked}
	}
	return []byte(it.value), "text/plain", nil
}

func (f *fakeKeyring) Close() {}

func (f *fakeKeyring) seen() (searches []map[string]string, secrets []dbus.ObjectPath) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append(searches, f.searches...), append(secrets, f.secrets...)
}

type logs struct {
	mu sync.Mutex
	l  []string
}

func (l *logs) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.l...)
}

const (
	declaredPath = dbus.ObjectPath("/org/freedesktop/secrets/collection/login/1")
	otherPath    = dbus.ObjectPath("/org/freedesktop/secrets/collection/login/2")
	unrelPath    = dbus.ObjectPath("/org/freedesktop/secrets/collection/login/3")
)

func newFake() *fakeKeyring {
	return &fakeKeyring{items: map[dbus.ObjectPath]fakeItem{
		declaredPath: {attrs: map[string]string{"service": "tool:example.com", "username": "me"}, value: "declared-token"},
		otherPath:    {attrs: map[string]string{"service": "tool:other.example", "username": "me"}, value: "other-token"},
		unrelPath:    {attrs: map[string]string{"service": "unrelated"}, value: "unrelated-secret"},
	}}
}

var declared = []sdk.KeyringItem{{Label: "tool login for example.com", Attributes: map[string]string{"service": "tool:example.com"}}}

func serveFake(t *testing.T, up Upstream, items []sdk.KeyringItem) (*dbus.Conn, *logs) {
	t.Helper()
	return serveFakeAllowing(t, up, items, nil)
}

// serveFakeAllowing is serveFake with the user's allow_keyring.
func serveFakeAllowing(t *testing.T, up Upstream, items []sdk.KeyringItem, allow []string) (*dbus.Conn, *logs) {
	t.Helper()
	dir, err := os.MkdirTemp("", "kb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "bus")
	lg := &logs{}
	b, err := Serve(path, items, Options{
		Logf:  func(f string, a ...any) { lg.mu.Lock(); lg.l = append(lg.l, fmt.Sprintf(f, a...)); lg.mu.Unlock() },
		Allow: allow,
		Dial: func() (Upstream, error) {
			if up == nil {
				return nil, errNoBus
			}
			return up, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	conn, err := dbus.Dial(Address(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.Auth(nil); err != nil {
		t.Fatal(err)
	}
	if err := conn.Hello(); err != nil {
		t.Fatal(err)
	}
	return conn, lg
}

func svc(c *dbus.Conn) dbus.BusObject { return c.Object(secretsName, servicePath) }

func TestBrokerServesOnlyDeclaredItems(t *testing.T) {
	up := newFake()
	c, _ := serveFake(t, up, declared)
	var out dbus.Variant
	var sess dbus.ObjectPath
	if err := svc(c).Call(ifService+".OpenSession", 0, "plain", dbus.MakeVariant("")).Store(&out, &sess); err != nil {
		t.Fatal(err)
	}
	// Every search is the client's attributes with a declared item's.
	var u, l []dbus.ObjectPath
	if err := svc(c).Call(ifService+".SearchItems", 0, map[string]string{"username": "me"}).Store(&u, &l); err != nil {
		t.Fatal(err)
	}
	if len(u) != 1 || u[0] != declaredPath || len(l) != 0 {
		t.Fatalf("search by username: %v %v", u, l)
	}
	for _, q := range []map[string]string{{"service": "unrelated"}, {"service": "tool:other.example"}} {
		var res []dbus.ObjectPath
		if err := c.Object(secretsName, collection).Call(ifCollection+".SearchItems", 0, q).Store(&res); err != nil || len(res) != 0 {
			t.Fatalf("search %v: %v %v", q, res, err)
		}
	}
	searches, _ := up.seen()
	for _, q := range searches {
		if q["service"] != "tool:example.com" {
			t.Fatalf("the keyring was searched for an undeclared item: %v", q)
		}
	}
	var s secret
	if err := c.Object(secretsName, declaredPath).Call(ifItem+".GetSecret", 0, sess).Store(&s); err != nil || string(s.Value) != "declared-token" {
		t.Fatalf("declared item: %q %v", s.Value, err)
	}
	// An undeclared item, addressed by its path, does not exist.
	for _, p := range []dbus.ObjectPath{otherPath, unrelPath, "/org/freedesktop/secrets/collection/login/99"} {
		err := c.Object(secretsName, p).Call(ifItem+".GetSecret", 0, sess).Store(&s)
		var de dbus.Error
		if !errors.As(err, &de) || de.Name != errNoObject {
			t.Errorf("GetSecret %s: %v", p, err)
		}
		var v dbus.Variant
		if err := c.Object(secretsName, p).Call(ifProps+".Get", 0, ifItem, "Attributes").Store(&v); err == nil {
			t.Errorf("attributes of %s: %v", p, v)
		}
	}
	var many map[dbus.ObjectPath]secret
	if err := svc(c).Call(ifService+".GetSecrets", 0, []dbus.ObjectPath{declaredPath, otherPath, unrelPath}, sess).Store(&many); err != nil {
		t.Fatal(err)
	}
	if len(many) != 1 || string(many[declaredPath].Value) != "declared-token" {
		t.Fatalf("GetSecrets: %v", many)
	}
	_, read := up.seen()
	for _, p := range read {
		if p != declaredPath {
			t.Fatalf("the keyring was read for an undeclared item: %s", p)
		}
	}
	// The collection lists the declared items alone.
	var items dbus.Variant
	if err := c.Object(secretsName, aliasDefault).Call(ifProps+".Get", 0, ifCollection, "Items").Store(&items); err != nil {
		t.Fatal(err)
	}
	if got := items.Value().([]dbus.ObjectPath); len(got) != 1 || got[0] != declaredPath {
		t.Fatalf("Items: %v", got)
	}
}

func TestBrokerIsReadOnlyAndAlone(t *testing.T) {
	c, _ := serveFake(t, newFake(), declared)
	denied := func(what string, call *dbus.Call) {
		t.Helper()
		var de dbus.Error
		if !errors.As(call.Err, &de) || (de.Name != errDenied && de.Name != "org.freedesktop.DBus.Error.ServiceUnknown") {
			t.Errorf("%s: %v", what, call.Err)
		}
	}
	props := map[string]dbus.Variant{ifItem + ".Label": dbus.MakeVariant("x"), ifItem + ".Attributes": dbus.MakeVariant(map[string]string{"service": "tool:example.com"})}
	denied("CreateItem", c.Object(secretsName, collection).Call(ifCollection+".CreateItem", 0, props, secret{Session: "/", Parameters: []byte{}, Value: []byte("x"), ContentType: "text/plain"}, true))
	denied("Delete", c.Object(secretsName, declaredPath).Call(ifItem+".Delete", 0))
	denied("SetSecret", c.Object(secretsName, declaredPath).Call(ifItem+".SetSecret", 0, secret{Session: "/", Parameters: []byte{}, Value: []byte("x"), ContentType: "text/plain"}))
	denied("set attributes", c.Object(secretsName, declaredPath).Call(ifProps+".Set", 0, ifItem, "Attributes", dbus.MakeVariant(map[string]string{"service": "x"})))
	denied("CreateCollection", svc(c).Call(ifService+".CreateCollection", 0, map[string]dbus.Variant{}, ""))
	denied("another service", c.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications").Call("org.freedesktop.Notifications.GetServerInformation", 0))
	denied("systemd", c.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1").Call("org.freedesktop.systemd1.Manager.StartUnit", 0, "x.service", "replace"))
	denied("become a service", c.BusObject().Call("org.freedesktop.DBus.RequestName", 0, "org.freedesktop.secrets", uint32(0)))
}

// The DH session libsecret prefers: the secret arrives encrypted under a key
// the client derives on its own (RFC 2409 group 2, HKDF-SHA256, AES-128-CBC).
func TestBrokerDHSession(t *testing.T) {
	c, _ := serveFake(t, newFake(), declared)
	x, _ := rand.Int(rand.Reader, ietf1024)
	pub := new(big.Int).Exp(big.NewInt(2), x, ietf1024)
	var out dbus.Variant
	var sess dbus.ObjectPath
	if err := svc(c).Call(ifService+".OpenSession", 0, algDH, dbus.MakeVariant(pub.Bytes())).Store(&out, &sess); err != nil {
		t.Fatal(err)
	}
	peer := new(big.Int).SetBytes(out.Value().([]byte))
	shared := new(big.Int).Exp(peer, x, ietf1024).FillBytes(make([]byte, 128))
	key, _ := hkdf.Key(sha256.New, shared, nil, "", 16)
	var s secret
	if err := c.Object(secretsName, declaredPath).Call(ifItem+".GetSecret", 0, sess).Store(&s); err != nil {
		t.Fatal(err)
	}
	if string(s.Value) == "declared-token" || len(s.Parameters) != 16 {
		t.Fatalf("not encrypted: %+v", s)
	}
	block, _ := aes.NewCipher(key)
	plain := make([]byte, len(s.Value))
	cipher.NewCBCDecrypter(block, s.Parameters).CryptBlocks(plain, s.Value)
	plain = plain[:len(plain)-int(plain[len(plain)-1])]
	if string(plain) != "declared-token" {
		t.Fatalf("decrypted %q", plain)
	}
	if err := svc(c).Call(ifService+".OpenSession", 0, algDH, dbus.MakeVariant([]byte{1})).Store(&out, &sess); err == nil {
		t.Fatal("a degenerate public key was accepted")
	}
	if err := svc(c).Call(ifService+".OpenSession", 0, "dh-other", dbus.MakeVariant("")).Store(&out, &sess); err == nil {
		t.Fatal("an unknown algorithm was accepted")
	}
}

func TestBrokerLockedAndAbsent(t *testing.T) {
	up := newFake()
	it := up.items[declaredPath]
	it.locked = true
	up.items[declaredPath] = it
	c, logs := serveFake(t, up, declared)
	var out dbus.Variant
	var sess dbus.ObjectPath
	_ = svc(c).Call(ifService+".OpenSession", 0, "plain", dbus.MakeVariant("")).Store(&out, &sess)
	var unlocked []dbus.ObjectPath
	var prompt dbus.ObjectPath
	if err := svc(c).Call(ifService+".Unlock", 0, []dbus.ObjectPath{declaredPath}).Store(&unlocked, &prompt); err != nil || len(unlocked) != 0 || prompt != "/" {
		t.Fatalf("Unlock of a locked item: %v %v %v", unlocked, prompt, err)
	}
	var s secret
	err := c.Object(secretsName, declaredPath).Call(ifItem+".GetSecret", 0, sess).Store(&s)
	var de dbus.Error
	if !errors.As(err, &de) || de.Name != errIsLocked {
		t.Fatalf("GetSecret of a locked item: %v", err)
	}
	if l := logs.get(); len(l) != 1 || !strings.Contains(l[0], "is locked: unlock your keyring") {
		t.Fatalf("logged: %q", l)
	}

	c2, logs2 := serveFake(t, nil, declared)
	var u, l []dbus.ObjectPath
	if err := svc(c2).Call(ifService+".SearchItems", 0, map[string]string{}).Store(&u, &l); err != nil || len(u)+len(l) != 0 {
		t.Fatalf("search without a keyring: %v %v %v", u, l, err)
	}
	_ = svc(c2).Call(ifService+".SearchItems", 0, map[string]string{}).Store(&u, &l)
	if l := logs2.get(); len(l) != 1 || !strings.Contains(l[0], "cannot be read") {
		t.Fatalf("logged once: %q", l)
	}
}
