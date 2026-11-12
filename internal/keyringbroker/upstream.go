package keyringbroker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// errNoBus: conductor has no session bus to find a keyring on.
var errNoBus = errors.New("no session bus")

// sessionBusAddress is conductor's session bus: DBUS_SESSION_BUS_ADDRESS,
// else $XDG_RUNTIME_DIR/bus when it is there. Nothing is launched to make
// one (no autolaunch): a box without a session has no keyring to read.
func sessionBusAddress() (string, error) {
	if a := strings.TrimSpace(os.Getenv("DBUS_SESSION_BUS_ADDRESS")); a != "" {
		if strings.HasPrefix(a, "autolaunch:") {
			return "", fmt.Errorf("%w: DBUS_SESSION_BUS_ADDRESS is %s, and conductor does not launch a bus", errNoBus, a)
		}
		return a, nil
	}
	if x := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(x) {
		p := filepath.Join(x, "bus")
		if fi, err := os.Stat(p); err == nil && fi.Mode()&os.ModeSocket != 0 {
			return "unix:path=" + p, nil
		}
		return "", fmt.Errorf("%w: DBUS_SESSION_BUS_ADDRESS is unset and %s is not a bus", errNoBus, p)
	}
	return "", fmt.Errorf("%w: DBUS_SESSION_BUS_ADDRESS and XDG_RUNTIME_DIR are unset", errNoBus)
}

// SessionUpstream reaches the Secret Service on conductor's session bus.
func SessionUpstream() (Upstream, error) {
	addr, err := sessionBusAddress()
	if err != nil {
		return nil, err
	}
	conn, err := dbus.Dial(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNoBus, err)
	}
	if err := conn.Auth(nil); err != nil {
		conn.Close()
		return nil, fmt.Errorf("%w: %v", errNoBus, err)
	}
	if err := conn.Hello(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("%w: %v", errNoBus, err)
	}
	u := &sessionUpstream{conn: conn, svc: conn.Object(secretsName, servicePath)}
	var out dbus.Variant
	if err := u.svc.Call(ifService+".OpenSession", 0, algPlain, dbus.MakeVariant("")).Store(&out, &u.session); err != nil {
		conn.Close()
		return nil, err
	}
	return u, nil
}

type sessionUpstream struct {
	conn    *dbus.Conn
	svc     dbus.BusObject
	session dbus.ObjectPath
}

func (u *sessionUpstream) Search(attrs map[string]string) (unlocked, locked []dbus.ObjectPath, err error) {
	err = u.svc.Call(ifService+".SearchItems", 0, attrs).Store(&unlocked, &locked)
	return unlocked, locked, err
}

func (u *sessionUpstream) Prop(item dbus.ObjectPath, name string) (dbus.Variant, error) {
	var v dbus.Variant
	err := u.conn.Object(secretsName, item).Call(ifProps+".Get", 0, ifItem, name).Store(&v)
	return v, err
}

func (u *sessionUpstream) Secret(item dbus.ObjectPath) ([]byte, string, error) {
	var s secret
	if err := u.conn.Object(secretsName, item).Call(ifItem+".GetSecret", 0, u.session).Store(&s); err != nil {
		return nil, "", err
	}
	return s.Value, s.ContentType, nil
}

func (u *sessionUpstream) Connected() bool { return u.conn.Connected() }

func (u *sessionUpstream) Close() {
	_ = u.conn.Object(secretsName, u.session).Call(ifSession+".Close", 0).Err
	u.conn.Close()
}

// Count is how many items in the user's keyring carry attrs, locked or not:
// a search, which reads no secret and unlocks nothing.
func Count(attrs map[string]string) (int, error) {
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		up, err := SessionUpstream()
		if err != nil {
			done <- result{0, err}
			return
		}
		defer up.Close()
		u, l, err := up.Search(attrs)
		done <- result{len(u) + len(l), err}
	}()
	select {
	case r := <-done:
		return r.n, r.err
	case <-time.After(5 * time.Second):
		return 0, errors.New("the keyring did not answer")
	}
}
