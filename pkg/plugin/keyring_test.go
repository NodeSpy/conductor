package plugin

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestKeyringItemValidate(t *testing.T) {
	ok := KeyringItem{Label: "tool login for example.com", Attributes: map[string]string{"service": "tool:example.com"}}
	if err := (Capabilities{Keyring: []KeyringItem{ok}}).Validate(nil); err != nil {
		t.Fatalf("a well-formed item: %v", err)
	}
	for name, it := range map[string]KeyringItem{
		"no label":           {Attributes: map[string]string{"service": "x"}},
		"blank label":        {Label: "  ", Attributes: map[string]string{"service": "x"}},
		"long label":         {Label: strings.Repeat("a", maxKeyringLabel+1), Attributes: map[string]string{"service": "x"}},
		"newline in label":   {Label: "login\nreads every secret", Attributes: map[string]string{"service": "x"}},
		"no attributes":      {Label: "login"},
		"empty value":        {Label: "login", Attributes: map[string]string{"service": ""}},
		"empty name":         {Label: "login", Attributes: map[string]string{"": "x"}},
		"control in a value": {Label: "login", Attributes: map[string]string{"service": "x\x1b[2J"}},
	} {
		if err := (Capabilities{Keyring: []KeyringItem{it}}).Validate(nil); err == nil {
			t.Errorf("%s: validated", name)
		}
	}
}

func TestKeyringItemMatches(t *testing.T) {
	it := KeyringItem{Label: "l", Attributes: map[string]string{"service": "tool:example.com"}}
	for attrs, want := range map[string]bool{
		"service=tool:example.com":               true,
		"service=tool:example.com,username=me":   true,
		"service=tool:other.example,username=me": false,
		"username=me":                            false,
		"service=tool:example.com.evil":          false,
		"service=TOOL:example.com":               false,
	} {
		m := map[string]string{}
		for _, kv := range strings.Split(attrs, ",") {
			k, v, _ := strings.Cut(kv, "=")
			m[k] = v
		}
		if got := it.Matches(m); got != want {
			t.Errorf("%s: matches=%v, want %v", attrs, got, want)
		}
	}
	if (KeyringItem{Label: "l"}).Matches(map[string]string{"a": "b"}) {
		t.Error("an item with no attributes matches everything")
	}
}

func TestResolveCopiesKeyring(t *testing.T) {
	attrs := map[string]string{"service": "tool:example.com"}
	g, err := Capabilities{Keyring: []KeyringItem{{Label: "l", Attributes: attrs}}}.Resolve(ResolveEnv{})
	if err != nil {
		t.Fatal(err)
	}
	attrs["service"] = "changed"
	if len(g.Keyring) != 1 || g.Keyring[0].Attributes["service"] != "tool:example.com" {
		t.Fatalf("grant: %+v", g.Keyring)
	}
}

type keyringHandler struct{}

func (keyringHandler) Describe() Decl {
	return Decl{Type: "t", Capabilities: Capabilities{Commands: []string{"tool"},
		Keyring: []KeyringItem{{Label: "tool login", Attributes: map[string]string{"service": "tool"}}}}}
}
func (keyringHandler) Invoke(InvokeRequest) (InvokeResult, error) { return InvokeResult{}, nil }

// Serve sends keyring only to a host that advertises it grants it.
func TestServeWithholdsKeyringFromOldHosts(t *testing.T) {
	if !slices.Contains(KnownSemantics(), SemanticKeyring) {
		t.Fatalf("KnownSemantics() = %v: a host built with this SDK must advertise %s", KnownSemantics(), SemanticKeyring)
	}
	hosts := []*HostInfo{nil, {Version: "dev", Semantics: oldHostSemantics()}, {Version: "dev", Semantics: v090Semantics()}, {Version: "dev", Semantics: KnownSemantics()}}
	var reqs []string
	for i, h := range hosts {
		b, _ := json.Marshal(DescribeRequest{Host: h})
		reqs = append(reqs, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"plugin.describe","params":%s}`, i+1, b))
	}
	var out strings.Builder
	if err := serve(strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out, keyringHandler{}); err != nil {
		t.Fatal(err)
	}
	byID := decodeResponses(t, out.String())
	for i, want := range []bool{false, false, false, true} {
		var d struct {
			Capabilities map[string]json.RawMessage `json:"capabilities"`
		}
		if err := json.Unmarshal(byID[fmt.Sprint(i+1)].Result, &d); err != nil {
			t.Fatal(err)
		}
		if _, sent := d.Capabilities["keyring"]; sent != want {
			t.Errorf("host %d: keyring sent=%v, want %v", i, sent, want)
		}
		if _, sent := d.Capabilities["commands"]; !sent {
			t.Errorf("host %d: the rest of the declaration was withheld too", i)
		}
	}
}

func TestKeyringReferences(t *testing.T) {
	conn := Schema{"api_base": {Type: "string"}, "hosts": {Type: "list"}, "token": {Type: "string", Secret: true}}
	item := func(v string) KeyringItem {
		return KeyringItem{Label: "tool login", Attributes: map[string]string{"service": v, "kind": "tool"}}
	}
	for v, wantErr := range map[string]bool{
		"tool:${host:config:api_base}":    false,
		"${config:hosts}":                 false,
		"tool:${env:TOOL_HOST}":           false,
		"tool:${host:env:TOOL_HOST}/x":    false,
		"tool:${config:nope}":             true, // not a field
		"tool:${config:token}":            true, // a secret field
		"tool:${env:UNDECLARED}":          true, // not in env
		"tool:${config:api_base":          true, // unclosed
		"${config:hosts}${env:TOOL_HOST}": true,
		"tool:${file:/etc/passwd}":        true,
	} {
		err := Capabilities{Env: []string{"TOOL_HOST"}, Keyring: []KeyringItem{item(v)}}.Validate(conn)
		if (err != nil) != wantErr {
			t.Errorf("%q: err=%v, want error=%v", v, err, wantErr)
		}
	}
	two := KeyringItem{Label: "l", Attributes: map[string]string{"a": "${config:api_base}", "b": "${env:TOOL_HOST}"}}
	if err := (Capabilities{Env: []string{"TOOL_HOST"}, Keyring: []KeyringItem{two}}).Validate(conn); err == nil {
		t.Error("two references in one item validated")
	}

	c := Capabilities{Env: []string{"TOOL_HOST"}, Keyring: []KeyringItem{
		item("tool:example.com"),
		item("tool:${host:config:api_base}"),
		item("${config:hosts}"),
		item("tool:${host:env:TOOL_HOST}"),
	}}
	env := ResolveEnv{
		Conns: []map[string]any{
			{"api_base": "https://GHE.example.test/api/v3", "hosts": []any{"registry.example.test", "other.example.test:5000"}},
			{"api_base": "https://ghe.example.test/api/v3"}, // the same host twice: one item
		},
		Getenv: func(n string) (string, bool) {
			return map[string]string{"TOOL_HOST": "env.example.test:8443"}[n], n == "TOOL_HOST"
		},
	}
	g, err := c.Resolve(env)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range g.Keyring {
		got = append(got, it.Selector())
	}
	slices.Sort(got)
	want := []string{
		"kind=tool, service=other.example.test:5000",
		"kind=tool, service=registry.example.test",
		"kind=tool, service=tool:env.example.test",
		"kind=tool, service=tool:example.com",
		"kind=tool, service=tool:ghe.example.test",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("resolved:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Unset: the referencing items are not granted at all.
	g, err = c.Resolve(ResolveEnv{})
	if err != nil || len(g.Keyring) != 1 || g.Keyring[0].Attributes["service"] != "tool:example.com" {
		t.Fatalf("unset references: %+v %v", g.Keyring, err)
	}
	// A value with no host is an error naming the field, not a guess.
	if _, err := c.Resolve(ResolveEnv{Conns: []map[string]any{{"api_base": "https:///nohost"}}}); err == nil || !strings.Contains(err.Error(), "api_base") {
		t.Fatalf("hostless value: %v", err)
	}
	if _, err := c.Resolve(ResolveEnv{Conns: []map[string]any{{"hosts": "a\x1bb"}}}); err == nil {
		t.Fatal("a control character in a configured value resolved")
	}
	// A conductor that does not resolve references is not sent them.
	if w := c.WithoutReferences(); len(w.Keyring) != 1 || w.Keyring[0].Attributes["service"] != "tool:example.com" {
		t.Fatalf("WithoutReferences kept: %+v", w.Keyring)
	}
}

// An item made only of attributes many programs' items share (a schema, a
// protocol, a user) would read all of them: it is refused, as is a label a
// format character (a bidi override) could make read as something else.
func TestKeyringBroadOrDisguisedRefused(t *testing.T) {
	for _, it := range []KeyringItem{
		{Label: "tool login", Attributes: map[string]string{"xdg:schema": "org.freedesktop.Secret.Generic"}},
		{Label: "tool login", Attributes: map[string]string{"xdg:schema": "org.gnome.keyring.NetworkPassword", "protocol": "https"}},
		{Label: "tool login", Attributes: map[string]string{"username": "me", "port": "443"}},
		{Label: "tool login‮", Attributes: map[string]string{"service": "tool"}},
		{Label: "tool​login", Attributes: map[string]string{"service": "tool"}},
		{Label: "tool login", Attributes: map[string]string{"service": "tool‮"}},
	} {
		if err := (Capabilities{Keyring: []KeyringItem{it}}).Validate(Schema{}); err == nil {
			t.Errorf("%q %v: validated", it.Label, it.Attributes)
		}
	}
	for _, it := range []KeyringItem{
		{Label: "tool login", Attributes: map[string]string{"service": "tool"}},
		{Label: "tool login", Attributes: map[string]string{"xdg:schema": "org.example.Password", "server": "example.com"}},
	} {
		if err := (Capabilities{Keyring: []KeyringItem{it}}).Validate(Schema{}); err != nil {
			t.Errorf("%v: %v", it.Attributes, err)
		}
	}
}

// An env: reference may not name a variable that looks like a credential,
// as a config: reference may not name a secret field.
func TestKeyringEnvRefNotSecret(t *testing.T) {
	for _, v := range []string{"TOOL_TOKEN", "TOOL_PASSWORD", "MY_API_KEY", "SSH_AUTH_SOCK"} {
		c := Capabilities{Env: []string{v}, Keyring: []KeyringItem{{Label: "l", Attributes: map[string]string{"service": "tool:${env:" + v + "}"}}}}
		if err := c.Validate(Schema{}); err == nil {
			t.Errorf("%s: validated", v)
		}
	}
}
