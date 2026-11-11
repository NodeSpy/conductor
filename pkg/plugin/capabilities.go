package plugin

import (
	"fmt"
	"maps"
	"net"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Grant is a plugin's declaration (Capabilities) resolved against the
// machine and its configured instances: the concrete destinations, addresses,
// paths, programs and variables conductor's jail allows it, and nothing else.
type Grant struct {
	// Egress are "host:port" destinations; a host may glob.
	Egress []string `json:"egress,omitempty"`
	// Listen are "[host]:port" addresses the plugin serves on.
	Listen []string `json:"listen,omitempty"`
	// Read and Write are absolute paths (Write implies read).
	Read  []string `json:"read,omitempty"`
	Write []string `json:"write,omitempty"`
	// Commands are program names.
	Commands []string `json:"commands,omitempty"`
	// Exec are absolute paths whose programs may run (read-only).
	Exec []string `json:"exec,omitempty"`
	// Env are variable names.
	Env      []string `json:"env,omitempty"`
	Keychain bool     `json:"keychain,omitempty"`
	// Keyring are the keyring items the plugin reads (Capabilities.Keyring).
	Keyring []KeyringItem `json:"keyring,omitempty"`
	// From maps each Read, Write and Exec path to the declared entries it
	// came from ("read:" + path), so a check on the path can name the
	// entry, tell a config: value from a static path, and tell an optional
	// entry ("?…") from a required one.
	From map[string][]string `json:"-"`
}

// Origins are the declared entries kind's path ("read", "write", "exec")
// came from.
func (g Grant) Origins(kind, path string) []string { return g.From[kind+":"+path] }

// Optional reports that every entry kind's path came from is optional
// ("?…"): a check that refuses it skips it instead of failing the launch.
func (g Grant) Optional(kind, path string) bool {
	o := g.Origins(kind, path)
	for _, e := range o {
		if !strings.HasPrefix(e, OptionalPrefix) {
			return false
		}
	}
	return len(o) > 0
}

// FromConfig reports that every entry kind's path came from is a config:
// value: the path is one the user configured, not one the plugin chose.
func (g Grant) FromConfig(kind, path string) bool {
	o := g.Origins(kind, path)
	for _, e := range o {
		if !strings.HasPrefix(strings.TrimPrefix(e, OptionalPrefix), refConfig) {
			return false
		}
	}
	return len(o) > 0
}

// OptionalPrefix marks a Read, Write or Exec entry optional: "?command:tool/..".
// A path it resolves to that the jail refuses (too broad, through a symlink
// it does not follow) is skipped, and logged, rather than failing the
// launch; a required entry fails it. A path that does not exist is skipped
// either way.
const OptionalPrefix = "?"

// ResolveEnv is what a declaration resolves against.
type ResolveEnv struct {
	// Home is the user's home directory ("~/").
	Home string
	// Getenv reads the daemon's environment ("env:<VAR>").
	Getenv func(string) (string, bool)
	// Conns are the configured instances' connection configs ("config:"),
	// one per instance the process serves. None (a type-level describe)
	// resolves every config: entry to nothing.
	Conns []map[string]any
	// Command finds a declared command's resolved file ("command:"); nil or
	// not found resolves the entry to nothing.
	Command func(name string) (string, bool)
}

const (
	refConfig  = "config:"
	refEnv     = "env:"
	refCommand = "command:"
)

// Validate checks a declaration's own shape: every entry parses, a config:
// reference names a field the plugin's Connection schema declares (and not a
// secret one: a secret's value is never a destination or a path), and an
// env: reference names a variable in Env. A declaration that fails is
// refused at install and at launch.
func (c Capabilities) Validate(conn Schema) error {
	env := map[string]bool{}
	for _, n := range c.Env {
		if n == "" || strings.ContainsAny(n, "= \t") {
			return fmt.Errorf("capabilities.env: %q is not a variable name", n)
		}
		env[n] = true
	}
	field := func(where, ref string) error {
		top, _, _ := strings.Cut(ref, ".")
		if top == "" {
			return fmt.Errorf("capabilities.%s: %q names no field", where, refConfig+ref)
		}
		f, ok := conn[top]
		if !ok {
			return fmt.Errorf("capabilities.%s: %q refers to connection field %q, which the plugin does not declare", where, refConfig+ref, top)
		}
		if f.Secret {
			return fmt.Errorf("capabilities.%s: %q refers to connection field %q, which is a secret", where, refConfig+ref, top)
		}
		return nil
	}
	for _, e := range append(append([]string(nil), c.Egress...), c.Listen...) {
		if strings.HasPrefix(e, OptionalPrefix) {
			return fmt.Errorf("capabilities: %q: only a path may be optional (%s)", e, OptionalPrefix)
		}
	}
	for _, e := range c.Egress {
		if ref, ok := strings.CutPrefix(e, refConfig); ok {
			f, port, _ := strings.Cut(ref, ":")
			if port != "" {
				if _, err := parsePort(port); err != nil {
					return fmt.Errorf("capabilities.egress: %q: %w", e, err)
				}
			}
			if err := field("egress", f); err != nil {
				return err
			}
			continue
		}
		if _, err := staticHostPort(e); err != nil {
			return fmt.Errorf("capabilities.egress: %w", err)
		}
	}
	for _, l := range c.Listen {
		if ref, ok := strings.CutPrefix(l, refConfig); ok {
			if err := field("listen", ref); err != nil {
				return err
			}
			continue
		}
		if _, err := listenAddr(l); err != nil {
			return fmt.Errorf("capabilities.listen: %w", err)
		}
	}
	for where, ps := range map[string][]string{"read": c.Read, "write": c.Write, "fs": c.FS, "exec": c.Exec} {
		for _, p := range ps {
			p = strings.TrimPrefix(p, OptionalPrefix)
			switch {
			case strings.HasPrefix(p, refConfig):
				f, _, _ := strings.Cut(strings.TrimPrefix(p, refConfig), "/")
				if err := field(where, f); err != nil {
					return err
				}
			case strings.HasPrefix(p, refEnv):
				v, _, _ := strings.Cut(strings.TrimPrefix(p, refEnv), "/")
				if !env[v] {
					return fmt.Errorf("capabilities.%s: %q refers to %s, which capabilities.env does not declare", where, p, v)
				}
			case strings.HasPrefix(p, refCommand):
				n, _, _ := strings.Cut(strings.TrimPrefix(p, refCommand), "/")
				if !slices.Contains(c.Commands, n) {
					return fmt.Errorf("capabilities.%s: %q refers to command %s, which capabilities.commands does not declare", where, p, n)
				}
			case p == "~" || strings.HasPrefix(p, "~/") || filepath.IsAbs(p):
			default:
				return fmt.Errorf("capabilities.%s: %q is not an absolute path, ~/…, env:VAR, config:field or command:name", where, p)
			}
		}
	}
	for _, cmd := range c.Commands {
		if cmd == "" || strings.ContainsAny(cmd, "/\\ \t") {
			return fmt.Errorf("capabilities.commands: %q is not a program name", cmd)
		}
	}
	for i, it := range c.Keyring {
		if err := it.validate(); err != nil {
			return fmt.Errorf("capabilities.keyring[%d]: %w", i, err)
		}
		refs := 0
		for k, v := range it.Attributes {
			r, ok, err := ParseKeyringValue(v)
			if err != nil {
				return fmt.Errorf("capabilities.keyring[%d] %q: attribute %s: %w", i, it.Label, k, err)
			}
			if !ok {
				continue
			}
			if refs++; refs > 1 {
				return fmt.Errorf("capabilities.keyring[%d] %q: only one attribute may hold a reference", i, it.Label)
			}
			switch r.Kind {
			case "config":
				if err := field("keyring", r.Name); err != nil {
					return err
				}
			case "env":
				if !env[r.Name] {
					return fmt.Errorf("capabilities.keyring[%d] %q: %s refers to %s, which capabilities.env does not declare", i, it.Label, v, r.Name)
				}
			}
		}
	}
	return nil
}

// KeyringRef is a reference in a keyring attribute value: the value is
// Prefix, then what the reference names, then Suffix.
//
//	"${config:<field>}"       the instance's connection field (a list gives
//	                          an item per entry)
//	"${env:<VAR>}"            the daemon's variable (one in Env)
//	"${host:config:<field>}"  the host of that value: a URL's host name, or
//	"${host:env:<VAR>}"       the host of host:port, or the value itself
//
// A field or variable that is not set gives no item.
type KeyringRef struct {
	Prefix, Suffix string
	// Kind is "config" or "env"; Name the field or variable.
	Kind, Name string
	// Host takes the host of the value.
	Host bool
}

// ParseKeyringValue reads a keyring attribute value: ok when it holds a
// reference, an error when it holds a malformed one or more than one.
func ParseKeyringValue(v string) (KeyringRef, bool, error) {
	i := strings.Index(v, "${")
	if i < 0 {
		return KeyringRef{}, false, nil
	}
	j := strings.Index(v[i:], "}")
	if j < 0 {
		return KeyringRef{}, false, fmt.Errorf("%q: an unclosed ${", v)
	}
	r := KeyringRef{Prefix: v[:i], Suffix: v[i+j+1:]}
	if strings.Contains(r.Suffix, "${") {
		return KeyringRef{}, false, fmt.Errorf("%q: a value may hold one reference", v)
	}
	body := v[i+2 : i+j]
	if b, ok := strings.CutPrefix(body, "host:"); ok {
		r.Host, body = true, b
	}
	kind, name, _ := strings.Cut(body, ":")
	if (kind != "config" && kind != "env") || name == "" || strings.ContainsAny(name, " \t${}") {
		return KeyringRef{}, false, fmt.Errorf("%q: a reference is ${config:<field>}, ${env:<VAR>}, ${host:config:<field>} or ${host:env:<VAR>}", v)
	}
	r.Kind, r.Name = kind, name
	return r, true, nil
}

// resolveKeyring is a declared item as the jail grants it: an item with no
// reference as declared; one with a reference once per value it names (none
// when unset). A value that is not plain text, or has no host when one is
// taken, is an error naming the field.
func resolveKeyring(it KeyringItem, e ResolveEnv) ([]KeyringItem, error) {
	var key string
	var ref KeyringRef
	for k, v := range it.Attributes {
		if r, ok, err := ParseKeyringValue(v); err != nil {
			return nil, err
		} else if ok {
			key, ref = k, r
		}
	}
	if key == "" {
		return []KeyringItem{{Label: it.Label, Attributes: maps.Clone(it.Attributes)}}, nil
	}
	var vals []string
	switch ref.Kind {
	case "config":
		vals = configValues(e.Conns, ref.Name)
	case "env":
		if e.Getenv != nil {
			if v, ok := e.Getenv(ref.Name); ok && strings.TrimSpace(v) != "" {
				vals = []string{strings.TrimSpace(v)}
			}
		}
	}
	var out []KeyringItem
	for _, v := range vals {
		if ref.Host {
			h, err := hostOf(v)
			if err != nil {
				return nil, fmt.Errorf("capabilities.keyring %q: %s:%s: %w", it.Label, ref.Kind, ref.Name, err)
			}
			v = h
		}
		if !printable(v) {
			return nil, fmt.Errorf("capabilities.keyring %q: %s:%s: %q has a control character", it.Label, ref.Kind, ref.Name, v)
		}
		attrs := maps.Clone(it.Attributes)
		attrs[key] = ref.Prefix + v + ref.Suffix
		out = append(out, KeyringItem{Label: it.Label, Attributes: attrs})
	}
	return out, nil
}

// hostOf is a value's host: a URL's host name, host:port's host, or the
// value itself when it is a bare host.
func hostOf(v string) (string, error) {
	var h string
	switch {
	case strings.Contains(v, "://"):
		u, err := url.Parse(v)
		if err != nil || u.Hostname() == "" {
			return "", fmt.Errorf("%q is not a URL with a host", v)
		}
		h = u.Hostname()
	default:
		if hh, _, err := net.SplitHostPort(v); err == nil {
			h = hh
		} else {
			h = v
		}
	}
	if h == "" || strings.ContainsAny(h, "/ \t*?[]\\@") {
		return "", fmt.Errorf("%q is not a host", v)
	}
	return strings.ToLower(h), nil
}

// maxKeyringLabel bounds a keyring item's label: a few words for the user.
const maxKeyringLabel = 80

func (it KeyringItem) validate() error {
	if strings.TrimSpace(it.Label) == "" {
		return fmt.Errorf("a label is required (what the item is, in plain words: \"tool login for example.com\")")
	}
	if len([]rune(it.Label)) > maxKeyringLabel {
		return fmt.Errorf("label %q is longer than %d characters", it.Label, maxKeyringLabel)
	}
	if !printable(it.Label) {
		return fmt.Errorf("label %q has a control character", it.Label)
	}
	if len(it.Attributes) == 0 {
		return fmt.Errorf("%q names no attributes: an item is declared by the attributes it is stored under", it.Label)
	}
	for k, v := range it.Attributes {
		if k == "" || v == "" {
			return fmt.Errorf("%q: attribute %q=%q: names and values may not be empty", it.Label, k, v)
		}
		if !printable(k) || !printable(v) {
			return fmt.Errorf("%q: attribute %q=%q has a control character", it.Label, k, v)
		}
	}
	return nil
}

func printable(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}

// Matches reports whether an item stored under attrs is this declared one:
// it carries every declared attribute, with the same value.
func (it KeyringItem) Matches(attrs map[string]string) bool {
	if len(it.Attributes) == 0 {
		return false
	}
	for k, v := range it.Attributes {
		if got, ok := attrs[k]; !ok || got != v {
			return false
		}
	}
	return true
}

// Selector renders the attributes in a stable order: "service=tool:example.com".
func (it KeyringItem) Selector() string {
	keys := make([]string, 0, len(it.Attributes))
	for k := range it.Attributes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+it.Attributes[k])
	}
	return strings.Join(parts, ", ")
}

// Resolve turns a declaration into the Grant conductor enforces. It never
// adds anything the declaration does not name: a config: or env: entry
// becomes the value it points at, or nothing when that value is unset. A
// value that cannot be read as what the entry needs (a URL with no host, a
// relative path) is an error naming the field, so the launch fails loudly.
func (c Capabilities) Resolve(e ResolveEnv) (Grant, error) {
	var g Grant
	for _, ent := range c.Egress {
		ref, ok := strings.CutPrefix(ent, refConfig)
		if !ok {
			hp, err := staticHostPort(ent)
			if err != nil {
				return Grant{}, fmt.Errorf("capabilities.egress: %w", err)
			}
			g.Egress = append(g.Egress, hp)
			continue
		}
		f, port, _ := strings.Cut(ref, ":")
		for _, v := range configValues(e.Conns, f) {
			hp, err := valueHostPort(v, port)
			if err != nil {
				return Grant{}, fmt.Errorf("capabilities.egress %q: connection field %s: %w", ent, f, err)
			}
			g.Egress = append(g.Egress, hp)
		}
	}
	for _, ent := range c.Listen {
		ref, ok := strings.CutPrefix(ent, refConfig)
		vals := []string{ent}
		if ok {
			vals = configValues(e.Conns, ref)
		}
		for _, v := range vals {
			a, err := listenAddr(v)
			if err != nil {
				return Grant{}, fmt.Errorf("capabilities.listen %q: %w", ent, err)
			}
			g.Listen = append(g.Listen, a)
		}
	}
	var err error
	g.From = map[string][]string{}
	if g.Read, err = resolvePaths("read", c.Read, e, g.From); err != nil {
		return Grant{}, err
	}
	if g.Write, err = resolvePaths("write", append(append([]string(nil), c.Write...), c.FS...), e, g.From); err != nil {
		return Grant{}, err
	}
	if g.Exec, err = resolvePaths("exec", c.Exec, e, g.From); err != nil {
		return Grant{}, err
	}
	g.Commands = append([]string(nil), c.Commands...)
	g.Env = append([]string(nil), c.Env...)
	g.Keychain = c.Keychain
	seen := map[string]bool{}
	for _, it := range c.Keyring {
		items, err := resolveKeyring(it, e)
		if err != nil {
			return Grant{}, err
		}
		for _, r := range items {
			if k := r.Selector(); !seen[k] {
				seen[k] = true
				g.Keyring = append(g.Keyring, r)
			}
		}
	}
	for _, l := range []*[]string{&g.Egress, &g.Listen, &g.Read, &g.Write, &g.Exec, &g.Commands, &g.Env} {
		*l = uniqSorted(*l)
	}
	return g, nil
}

// configValues reads a dotted connection field from every instance: a
// string, a number, or a list of them. Missing or empty yields nothing.
func configValues(conns []map[string]any, path string) []string {
	var out []string
	for _, conn := range conns {
		var cur any = conn
		for _, seg := range strings.Split(path, ".") {
			m, ok := cur.(map[string]any)
			if !ok {
				cur = nil
				break
			}
			cur = m[seg]
		}
		out = append(out, scalarStrings(cur)...)
	}
	return out
}

func scalarStrings(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		if s := strings.TrimSpace(t); s != "" {
			return []string{s}
		}
		return nil
	case int, int64, float64, uint64:
		return []string{fmt.Sprint(t)}
	case []any:
		var out []string
		for _, x := range t {
			out = append(out, scalarStrings(x)...)
		}
		return out
	case []string:
		var out []string
		for _, x := range t {
			out = append(out, scalarStrings(x)...)
		}
		return out
	}
	return nil
}

// staticHostPort reads a declared "host[:port]" (host may glob; bare host is
// :443).
func staticHostPort(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("empty destination")
	}
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		if strings.Contains(s, ":") && !strings.HasPrefix(s, "[") {
			return "", fmt.Errorf("%q is not host:port", s)
		}
		h, p = strings.Trim(s, "[]"), "443"
	}
	if h == "" || strings.ContainsAny(h, "/ \t") {
		return "", fmt.Errorf("%q has no host", s)
	}
	if _, err := parsePort(p); err != nil {
		return "", fmt.Errorf("%q: %w", s, err)
	}
	return net.JoinHostPort(h, p), nil
}

// valueHostPort reads a configured value as a destination: a URL or
// host:port, or a bare host given defPort. A value may not glob (no * ? [ ]
// or \, which the egress proxy's matcher would read as a pattern): the
// declaration said "this field's host", not "any host".
func valueHostPort(v, defPort string) (string, error) {
	var h, p string
	if strings.Contains(v, "://") {
		u, err := url.Parse(v)
		if err != nil || u.Hostname() == "" {
			return "", fmt.Errorf("%q is not a URL with a host", v)
		}
		h, p = u.Hostname(), u.Port()
		if p == "" {
			switch strings.ToLower(u.Scheme) {
			case "http", "ws":
				p = "80"
			case "https", "wss":
				p = "443"
			default:
				p = defPort
			}
		}
	} else if hh, pp, err := net.SplitHostPort(v); err == nil {
		h, p = hh, pp
	} else {
		h, p = v, defPort
	}
	if h == "" || strings.ContainsAny(h, "*?[]\\/ \t") {
		return "", fmt.Errorf("%q is not a host", v)
	}
	if p == "" {
		return "", fmt.Errorf("%q names no port, and the declaration gives none", v)
	}
	if _, err := parsePort(p); err != nil {
		return "", fmt.Errorf("%q: %w", v, err)
	}
	return net.JoinHostPort(h, p), nil
}

// listenAddr reads "[host]:port" or a bare port.
func listenAddr(v string) (string, error) {
	v = strings.TrimSpace(v)
	if _, err := parsePort(v); err == nil {
		return ":" + v, nil
	}
	h, p, err := net.SplitHostPort(v)
	if err != nil {
		return "", fmt.Errorf("%q is not [host]:port", v)
	}
	if _, err := parsePort(p); err != nil {
		return "", fmt.Errorf("%q: %w", v, err)
	}
	return net.JoinHostPort(h, p), nil
}

func parsePort(p string) (int, error) {
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("%q is not a port", p)
	}
	return n, nil
}

func resolvePaths(where string, entries []string, e ResolveEnv, from map[string][]string) ([]string, error) {
	var out []string
	var declared string
	add := func(base, rest, ent string) error {
		if base == "" {
			return nil
		}
		if strings.HasPrefix(base, "~/") || base == "~" {
			if e.Home == "" {
				return nil
			}
			base = filepath.Join(e.Home, strings.TrimPrefix(base, "~"))
		}
		if !filepath.IsAbs(base) {
			return fmt.Errorf("capabilities.%s %q: %q is not an absolute path", where, ent, base)
		}
		p := filepath.Clean(filepath.Join(base, rest))
		out = append(out, p)
		if k := where + ":" + p; !slices.Contains(from[k], declared) {
			from[k] = append(from[k], declared)
		}
		return nil
	}
	for _, ent := range entries {
		declared = ent
		ent = strings.TrimPrefix(ent, OptionalPrefix)
		switch {
		case strings.HasPrefix(ent, refConfig):
			f, rest, _ := strings.Cut(strings.TrimPrefix(ent, refConfig), "/")
			for _, v := range configValues(e.Conns, f) {
				if err := add(v, rest, ent); err != nil {
					return nil, err
				}
			}
		case strings.HasPrefix(ent, refCommand):
			name, rest, _ := strings.Cut(strings.TrimPrefix(ent, refCommand), "/")
			if e.Command == nil {
				continue
			}
			if v, ok := e.Command(name); ok {
				// Relative to the command, a path stays in its install
				// tree: the directory it is in, or the prefix above a
				// bin/sbin directory (/usr for /usr/bin/git).
				tree := filepath.Dir(v)
				if b := filepath.Base(tree); b == "bin" || b == "sbin" {
					tree = filepath.Dir(tree)
				}
				if p := filepath.Join(v, rest); !within(tree, p) {
					return nil, fmt.Errorf("capabilities.%s %q: resolves to %s, outside %s's install tree %s", where, ent, p, name, tree)
				}
				if err := add(v, rest, ent); err != nil {
					return nil, err
				}
			}
		case strings.HasPrefix(ent, refEnv):
			name, rest, _ := strings.Cut(strings.TrimPrefix(ent, refEnv), "/")
			if e.Getenv == nil {
				continue
			}
			if v, ok := e.Getenv(name); ok {
				if err := add(strings.TrimSpace(v), rest, ent); err != nil {
					return nil, err
				}
			}
		default:
			if err := add(ent, "", ent); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func uniqSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	s := append([]string(nil), in...)
	sort.Strings(s)
	out := s[:1]
	for _, v := range s[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

// within: p is dir or inside it.
func within(dir, p string) bool {
	dir, p = filepath.Clean(dir), filepath.Clean(p)
	if dir == "/" {
		return true
	}
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}
