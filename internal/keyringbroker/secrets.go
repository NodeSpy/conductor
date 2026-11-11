package keyringbroker

import (
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

const (
	servicePath   = dbus.ObjectPath("/org/freedesktop/secrets")
	collection    = dbus.ObjectPath("/org/freedesktop/secrets/collection/login")
	aliasDefault  = dbus.ObjectPath("/org/freedesktop/secrets/aliases/default")
	collectionPfx = "/org/freedesktop/secrets/collection/"
	sessionPfx    = "/org/freedesktop/secrets/session/"

	ifService    = "org.freedesktop.Secret.Service"
	ifCollection = "org.freedesktop.Secret.Collection"
	ifItem       = "org.freedesktop.Secret.Item"
	ifSession    = "org.freedesktop.Secret.Session"
	ifProps      = "org.freedesktop.DBus.Properties"
	ifIntrospect = "org.freedesktop.DBus.Introspectable"

	errNoObject     = "org.freedesktop.Secret.Error.NoSuchObject"
	errNoSession    = "org.freedesktop.Secret.Error.NoSession"
	errIsLocked     = "org.freedesktop.Secret.Error.IsLocked"
	errDenied       = "org.freedesktop.DBus.Error.AccessDenied"
	errNotSupported = "org.freedesktop.DBus.Error.NotSupported"
	errUnknown      = "org.freedesktop.DBus.Error.UnknownMethod"
	errFailed       = "org.freedesktop.DBus.Error.Failed"
)

// readOnly is the refusal of every call that would change the keyring.
const readOnly = "conductor serves this plugin's declared keyring items read-only"

// secret is the Secret Service's Secret struct, (oayays).
type secret struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

// secrets answers calls to the Secret Service.
func (c *conn) secrets(m *dbus.Message, path dbus.ObjectPath, iface, member string) {
	if iface == ifIntrospect && member == "Introspect" {
		c.reply(m, secretsID, `<!DOCTYPE node PUBLIC "-//freedesktop//DTD D-BUS Object Introspection 1.0//EN" "http://www.freedesktop.org/standards/dbus/1.0/introspect.dtd"><node/>`)
		return
	}
	if path == aliasDefault {
		path = collection
	}
	switch {
	case path == servicePath:
		c.service(m, iface, member)
	case path == collection:
		c.collection(m, iface, member)
	case strings.HasPrefix(string(path), sessionPfx):
		c.sessionCall(m, path, iface, member)
	case strings.HasPrefix(string(path), collectionPfx):
		c.item(m, path, iface, member)
	default:
		c.fail(m, secretsID, errNoObject, "no such object "+string(path))
	}
}

func (c *conn) service(m *dbus.Message, iface, member string) {
	if iface == ifProps {
		c.props(m, func(_, name string) (dbus.Variant, bool) {
			if name == "Collections" {
				return dbus.MakeVariant([]dbus.ObjectPath{collection}), true
			}
			return dbus.Variant{}, false
		}, []string{"Collections"})
		return
	}
	switch member {
	case "OpenSession":
		c.openSession(m)
	case "SearchItems":
		attrs, ok := attrsArg(m)
		if !ok {
			c.fail(m, secretsID, "org.freedesktop.DBus.Error.InvalidArgs", "SearchItems takes a{ss}, of at most 32 attributes of at most 1 KiB")
			return
		}
		unlocked, locked, err := c.b.search(attrs)
		if err != nil {
			c.b.unavailable(err)
		}
		c.reply(m, secretsID, nonNil(unlocked), nonNil(locked))
	case "Unlock":
		paths, _ := pathsArg(m)
		var out []dbus.ObjectPath
		for _, p := range paths {
			switch {
			case p == collection || p == aliasDefault:
				// The view of the declared items is open; each item's
				// own lock is the user's keyring's.
				out = append(out, p)
			case strings.HasPrefix(string(p), collectionPfx):
				if attrs, locked, err := c.b.check(p); err == nil && attrs != nil && !locked {
					out = append(out, p)
				}
			}
		}
		c.reply(m, secretsID, nonNil(out), dbus.ObjectPath("/"))
	case "Lock":
		// Locking is the user's: nothing is locked for a plugin.
		c.reply(m, secretsID, []dbus.ObjectPath{}, dbus.ObjectPath("/"))
	case "GetSecrets":
		paths, _ := pathsArg(m)
		var sp dbus.ObjectPath
		if len(m.Body) > 1 {
			sp, _ = m.Body[1].(dbus.ObjectPath)
		}
		s, ok := c.sessions[sp]
		if !ok {
			c.fail(m, secretsID, errNoSession, "no such session "+string(sp))
			return
		}
		out := map[dbus.ObjectPath]secret{}
		for _, p := range paths {
			if v, errName, _ := c.b.read(p); errName == "" {
				enc, err := s.encode(sp, v)
				if err == nil {
					out[p] = enc
				}
			}
		}
		c.reply(m, secretsID, out)
	case "ReadAlias":
		name := ""
		if len(m.Body) > 0 {
			name, _ = m.Body[0].(string)
		}
		if name == "default" || name == "login" {
			c.reply(m, secretsID, collection)
			return
		}
		c.reply(m, secretsID, dbus.ObjectPath("/"))
	case "CreateCollection", "SetAlias", "ChangeLock", "LockService", "CreateWithMasterPassword", "UnlockWithMasterPassword":
		c.fail(m, secretsID, errDenied, readOnly)
	default:
		c.fail(m, secretsID, errUnknown, "no method "+member)
	}
}

func (c *conn) collection(m *dbus.Message, iface, member string) {
	if iface == ifProps {
		c.props(m, func(_, name string) (dbus.Variant, bool) {
			switch name {
			case "Items":
				u, l, err := c.b.search(map[string]string{})
				if err != nil {
					c.b.unavailable(err)
				}
				return dbus.MakeVariant(nonNil(append(u, l...))), true
			case "Label":
				return dbus.MakeVariant("Login"), true
			case "Locked":
				return dbus.MakeVariant(false), true
			case "Created", "Modified":
				return dbus.MakeVariant(uint64(0)), true
			}
			return dbus.Variant{}, false
		}, []string{"Items", "Label", "Locked", "Created", "Modified"})
		return
	}
	switch member {
	case "SearchItems":
		attrs, ok := attrsArg(m)
		if !ok {
			c.fail(m, secretsID, "org.freedesktop.DBus.Error.InvalidArgs", "SearchItems takes a{ss}, of at most 32 attributes of at most 1 KiB")
			return
		}
		u, l, err := c.b.search(attrs)
		if err != nil {
			c.b.unavailable(err)
		}
		c.reply(m, secretsID, nonNil(append(u, l...)))
	case "CreateItem", "Delete":
		c.fail(m, secretsID, errDenied, readOnly)
	default:
		c.fail(m, secretsID, errUnknown, "no method "+member)
	}
}

func (c *conn) item(m *dbus.Message, path dbus.ObjectPath, iface, member string) {
	attrs, locked, err := c.b.check(path)
	if err != nil {
		c.b.unavailable(err)
		c.fail(m, secretsID, errFailed, "the keyring cannot be read")
		return
	}
	if attrs == nil {
		// Not an item, or not a declared one: as far as the jail can
		// tell, it does not exist.
		c.fail(m, secretsID, errNoObject, "no such item "+string(path))
		return
	}
	if iface == ifProps {
		if member == "Set" {
			c.fail(m, secretsID, errDenied, readOnly)
			return
		}
		c.props(m, func(_, name string) (dbus.Variant, bool) {
			switch name {
			case "Attributes":
				return dbus.MakeVariant(attrs), true
			case "Locked":
				return dbus.MakeVariant(locked), true
			case "Label", "Created", "Modified":
				up, err := c.b.upstream()
				if err != nil {
					return dbus.Variant{}, false
				}
				v, err := up.Prop(path, name)
				if err != nil {
					return dbus.Variant{}, false
				}
				return v, true
			}
			return dbus.Variant{}, false
		}, []string{"Attributes", "Locked", "Label", "Created", "Modified"})
		return
	}
	switch member {
	case "GetSecret":
		var sp dbus.ObjectPath
		if len(m.Body) > 0 {
			sp, _ = m.Body[0].(dbus.ObjectPath)
		}
		s, ok := c.sessions[sp]
		if !ok {
			c.fail(m, secretsID, errNoSession, "no such session "+string(sp))
			return
		}
		v, errName, text := c.b.read(path)
		if errName != "" {
			c.fail(m, secretsID, errName, text)
			return
		}
		enc, err := s.encode(sp, v)
		if err != nil {
			c.fail(m, secretsID, errFailed, err.Error())
			return
		}
		c.reply(m, secretsID, enc)
	case "SetSecret", "Delete":
		c.fail(m, secretsID, errDenied, readOnly)
	default:
		c.fail(m, secretsID, errUnknown, "no method "+member)
	}
}

func (c *conn) sessionCall(m *dbus.Message, path dbus.ObjectPath, iface, member string) {
	if _, ok := c.sessions[path]; !ok {
		c.fail(m, secretsID, errNoObject, "no such session "+string(path))
		return
	}
	if iface == ifSession && member == "Close" || iface == "" && member == "Close" {
		delete(c.sessions, path)
		c.reply(m, secretsID)
		return
	}
	c.fail(m, secretsID, errUnknown, "no method "+member)
}

func (c *conn) openSession(m *dbus.Message) {
	if len(m.Body) < 2 {
		c.fail(m, secretsID, "org.freedesktop.DBus.Error.InvalidArgs", "OpenSession takes (s, v)")
		return
	}
	alg, _ := m.Body[0].(string)
	in, _ := m.Body[1].(dbus.Variant)
	s, out, err := newSession(alg, in)
	if err != nil {
		c.fail(m, secretsID, errNotSupported, err.Error())
		return
	}
	if len(c.sessions) >= 64 {
		c.fail(m, secretsID, errFailed, "too many open sessions")
		return
	}
	c.nextSess++
	p := dbus.ObjectPath(sessionPfx + "s" + strconv.Itoa(c.nextSess))
	c.sessions[p] = s
	c.reply(m, secretsID, out, p)
}

// props serves org.freedesktop.DBus.Properties Get and GetAll from get;
// Set is refused.
func (c *conn) props(m *dbus.Message, get func(iface, name string) (dbus.Variant, bool), names []string) {
	member := header[string](m, dbus.FieldMember)
	switch member {
	case "Get":
		if len(m.Body) < 2 {
			c.fail(m, secretsID, "org.freedesktop.DBus.Error.InvalidArgs", "Get takes (s, s)")
			return
		}
		iface, _ := m.Body[0].(string)
		name, _ := m.Body[1].(string)
		v, ok := get(iface, name)
		if !ok {
			c.fail(m, secretsID, "org.freedesktop.DBus.Error.UnknownProperty", "no property "+name)
			return
		}
		c.reply(m, secretsID, v)
	case "GetAll":
		iface := ""
		if len(m.Body) > 0 {
			iface, _ = m.Body[0].(string)
		}
		out := map[string]dbus.Variant{}
		for _, n := range names {
			if v, ok := get(iface, n); ok {
				out[n] = v
			}
		}
		c.reply(m, secretsID, out)
	case "Set":
		c.fail(m, secretsID, errDenied, readOnly)
	default:
		c.fail(m, secretsID, errUnknown, "no method "+member)
	}
}

// attrsArg is a search's attributes, within maxAttrs and maxAttrLen.
func attrsArg(m *dbus.Message) (map[string]string, bool) {
	if len(m.Body) < 1 {
		return nil, false
	}
	a, ok := m.Body[0].(map[string]string)
	if !ok || len(a) > maxAttrs {
		return nil, false
	}
	for k, v := range a {
		if len(k) > maxAttrLen || len(v) > maxAttrLen {
			return nil, false
		}
	}
	return a, true
}

func pathsArg(m *dbus.Message) ([]dbus.ObjectPath, bool) {
	if len(m.Body) < 1 {
		return nil, false
	}
	p, ok := m.Body[0].([]dbus.ObjectPath)
	return p, ok
}

func nonNil(p []dbus.ObjectPath) []dbus.ObjectPath {
	if p == nil {
		return []dbus.ObjectPath{}
	}
	return p
}

// --- the declared view of the keyring ---------------------------------------

// search finds the declared items that carry every attribute in attrs: for
// each declared item whose attributes do not contradict the query, the
// keyring is searched for both together, so it is never searched for an
// item that is not declared.
func (b *Broker) search(attrs map[string]string) (unlocked, locked []dbus.ObjectPath, err error) {
	var queries []map[string]string
	var matched []int
	for i, it := range b.items {
		q := maps.Clone(attrs)
		if q == nil {
			q = map[string]string{}
		}
		ok := true
		for k, v := range it.Attributes {
			if have, set := q[k]; set && have != v {
				ok = false
				break
			}
			q[k] = v
		}
		if ok {
			queries = append(queries, q)
			matched = append(matched, i)
		}
	}
	if len(queries) == 0 {
		return nil, nil, nil
	}
	up, err := b.upstream()
	if err != nil {
		return nil, nil, err
	}
	for i, q := range queries {
		u, l, err := up.Search(q)
		if err != nil {
			b.drop(up, err)
			return nil, nil, err
		}
		if len(l) > 0 {
			b.reportLocked(b.items[matched[i]].Label)
		}
		for _, p := range u {
			if !slices.Contains(unlocked, p) {
				unlocked = append(unlocked, p)
			}
		}
		for _, p := range l {
			if !slices.Contains(locked, p) {
				locked = append(locked, p)
			}
		}
	}
	return unlocked, locked, nil
}

// check reads an item's attributes and lock from the keyring: nil
// attributes when the path is not an item, or not a declared one.
func (b *Broker) check(path dbus.ObjectPath) (attrs map[string]string, locked bool, err error) {
	if !path.IsValid() || !strings.HasPrefix(string(path), collectionPfx) {
		return nil, false, nil
	}
	up, err := b.upstream()
	if err != nil {
		return nil, false, err
	}
	v, err := up.Prop(path, "Attributes")
	if err != nil {
		var de dbus.Error
		if errors.As(err, &de) && (de.Name == "org.freedesktop.DBus.Error.UnknownMethod" || de.Name == "org.freedesktop.DBus.Error.UnknownObject" ||
			de.Name == "org.freedesktop.DBus.Error.UnknownInterface" || de.Name == "org.freedesktop.DBus.Error.InvalidArgs" ||
			de.Name == "org.freedesktop.DBus.Error.UnknownProperty" || de.Name == errNoObject) {
			return nil, false, nil
		}
		b.drop(up, err)
		return nil, false, err
	}
	a, ok := v.Value().(map[string]string)
	if !ok || !b.declared(a) {
		return nil, false, nil
	}
	if lv, err := up.Prop(path, "Locked"); err == nil {
		locked, _ = lv.Value().(bool)
	}
	return a, locked, nil
}

// read reads a declared item's secret: errName is set when it cannot be.
func (b *Broker) read(path dbus.ObjectPath) (v plain, errName, text string) {
	attrs, locked, err := b.check(path)
	if err != nil {
		b.unavailable(err)
		return plain{}, errFailed, "the keyring cannot be read"
	}
	if attrs == nil {
		return plain{}, errNoObject, "no such item " + string(path)
	}
	if locked {
		b.reportLocked(b.labelFor(attrs))
		return plain{}, errIsLocked, "the keyring is locked: unlock it in your desktop session"
	}
	up, err := b.upstream()
	if err != nil {
		b.unavailable(err)
		return plain{}, errFailed, "the keyring cannot be read"
	}
	val, ct, err := up.Secret(path)
	if err != nil {
		var de dbus.Error
		if errors.As(err, &de) && de.Name == errIsLocked {
			b.reportLocked(b.labelFor(attrs))
			return plain{}, errIsLocked, "the keyring is locked: unlock it in your desktop session"
		}
		b.unavailable(err)
		return plain{}, errFailed, "the keyring cannot be read"
	}
	return plain{value: val, contentType: ct}, "", ""
}

func (b *Broker) labelFor(attrs map[string]string) string {
	for _, it := range b.items {
		if it.Matches(attrs) {
			return it.Label
		}
	}
	return "(undeclared)"
}

// reportLocked says, once per item, that a declared item is locked.
func (b *Broker) reportLocked(label string) {
	b.report("locked:"+label, "its keyring item %q is locked: unlock your keyring (log in to your desktop session, or unlock it in your keyring manager) — conductor never unlocks it for a plugin", label)
}

// plain is a secret in the clear, before a session encodes it.
type plain struct {
	value       []byte
	contentType string
}
