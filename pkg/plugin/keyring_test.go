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
