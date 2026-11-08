package plugin

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func refCaps() Capabilities {
	return Capabilities{
		Egress:        []string{"api.example.com:443", "config:base_url", "config:relay:8080"},
		Listen:        []string{"config:listen"},
		Read:          []string{"/etc/tool", "env:TOOL_HOME/conf", "?config:ca_file"},
		Write:         []string{"?env:TOOL_CACHE"},
		FS:            []string{"config:data_dir"},
		Exec:          []string{"command:tool/..", "?env:TOOL_HOME/bin"},
		Commands:      []string{"tool"},
		Env:           []string{"TOOL_HOME", "TOOL_CACHE"},
		Keychain:      true,
		SingleProcess: true,
	}
}

func TestWithoutReferences(t *testing.T) {
	in := refCaps()
	got := in.WithoutReferences()
	want := Capabilities{
		Egress:        []string{"api.example.com:443"},
		Read:          []string{"/etc/tool"},
		Exec:          []string{"command:tool/.."},
		Commands:      []string{"tool"},
		Env:           []string{"TOOL_HOME", "TOOL_CACHE"},
		Keychain:      true,
		SingleProcess: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WithoutReferences =\n %+v\nwant\n %+v", got, want)
	}
	// A list left empty is unspecified, so it is absent on the wire, not [].
	if got.Listen != nil || got.Write != nil || got.FS != nil {
		t.Fatalf("emptied lists must be nil: listen %#v write %#v fs %#v", got.Listen, got.Write, got.FS)
	}
	if !reflect.DeepEqual(in, refCaps()) {
		t.Fatalf("WithoutReferences changed its receiver: %+v", in)
	}
}

func TestResolvesReferences(t *testing.T) {
	var none *HostInfo
	for name, tc := range map[string]struct {
		h    *HostInfo
		want bool
	}{
		"no host info (before v0.61)":           {none, false},
		"dev, no semantics":                     {&HostInfo{Version: "dev"}, false},
		"semantics without it (v0.61 to v0.89)": {&HostInfo{Version: "dev", Semantics: oldHostSemantics()}, false},
		"a release version is not enough":       {&HostInfo{Version: "v0.89.0", Semantics: oldHostSemantics()}, false},
		"advertised":                            {&HostInfo{Version: "dev", Semantics: KnownSemantics()}, true},
		"advertised, whatever the version says": {&HostInfo{Version: "v0.80.0", Semantics: []string{SemanticReferences}}, true},
	} {
		if got := tc.h.ResolvesReferences(); got != tc.want {
			t.Errorf("%s: ResolvesReferences = %v, want %v", name, got, tc.want)
		}
	}
}

func TestKnownSemanticsAdvertisesReferences(t *testing.T) {
	if !slices.Contains(KnownSemantics(), SemanticReferences) {
		t.Fatalf("KnownSemantics() = %v: a host built with this SDK must advertise %s", KnownSemantics(), SemanticReferences)
	}
}

// oldHostSemantics is HostInfo.Semantics as conductor v0.61 through v0.89.0
// sent it: everything this SDK knows but SemanticReferences.
func oldHostSemantics() []string {
	return slices.DeleteFunc(KnownSemantics(), func(s string) bool { return s == SemanticReferences })
}

// refHandler declares references at type and instance level.
type refHandler struct{}

func (refHandler) Describe() Decl { return Decl{Type: "t", Capabilities: refCaps()} }
func (refHandler) Invoke(InvokeRequest) (InvokeResult, error) {
	return InvokeResult{}, nil
}
func (refHandler) DescribeInstance(context.Context, string, map[string]any) (Decl, error) {
	return Decl{Type: "t", Capabilities: Capabilities{Egress: []string{"config:base_url"}}}, nil
}

// TestServeWithholdsReferencesFromOldHosts drives plugin.describe over the
// wire the way each conductor generation asks: a host that does not
// advertise SemanticReferences gets the declaration without its config: and
// env: entries (a list left empty absent, so it does not confine), and one
// that does gets all of it, at type and instance level alike.
func TestServeWithholdsReferencesFromOldHosts(t *testing.T) {
	params := func(h *HostInfo, instance string) string {
		b, err := json.Marshal(DescribeRequest{Host: h, Instance: instance})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	old := &HostInfo{Version: "dev", Semantics: oldHostSemantics()}
	now := &HostInfo{Version: "dev", Semantics: KnownSemantics()}
	reqs := []string{
		`{"jsonrpc":"2.0","id":1,"method":"plugin.describe"}`,
		`{"jsonrpc":"2.0","id":2,"method":"plugin.describe","params":` + params(old, "") + `}`,
		`{"jsonrpc":"2.0","id":3,"method":"plugin.describe","params":` + params(now, "") + `}`,
		`{"jsonrpc":"2.0","id":4,"method":"plugin.describe","params":` + params(old, "a") + `}`,
		`{"jsonrpc":"2.0","id":5,"method":"plugin.describe","params":` + params(now, "a") + `}`,
	}
	var out strings.Builder
	if err := serve(strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out, refHandler{}); err != nil {
		t.Fatalf("serve: %v", err)
	}
	byID := decodeResponses(t, out.String())
	caps := func(id string) (Capabilities, map[string]json.RawMessage) {
		t.Helper()
		m, ok := byID[id]
		if !ok || m.Error != nil {
			t.Fatalf("describe %s: %+v", id, m.Error)
		}
		var d struct {
			Capabilities json.RawMessage `json:"capabilities"`
		}
		if err := json.Unmarshal(m.Result, &d); err != nil {
			t.Fatal(err)
		}
		var c Capabilities
		var raw map[string]json.RawMessage
		if len(d.Capabilities) > 0 {
			if err := json.Unmarshal(d.Capabilities, &c); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(d.Capabilities, &raw); err != nil {
				t.Fatal(err)
			}
		}
		return c, raw
	}
	stripped := refCaps().WithoutReferences()
	for _, id := range []string{"1", "2"} {
		c, raw := caps(id)
		if !reflect.DeepEqual(c, stripped) {
			t.Errorf("describe %s (old host): capabilities = %+v, want %+v", id, c, stripped)
		}
		for _, k := range []string{"listen", "write", "fs"} {
			if _, ok := raw[k]; ok {
				t.Errorf("describe %s (old host): %q is on the wire (%s); an emptied list must be absent", id, k, raw[k])
			}
		}
	}
	if c, _ := caps("3"); !reflect.DeepEqual(c, refCaps()) {
		t.Errorf("describe 3 (current host): capabilities = %+v, want the full declaration %+v", c, refCaps())
	}
	// An instance declaring only config: egress: unspecified for the old
	// host, exactly as declared for the current one.
	if c, raw := caps("4"); c.Egress != nil || raw["egress"] != nil {
		t.Errorf("describe 4 (old host, instance): egress = %v (%s), want it absent", c.Egress, raw["egress"])
	}
	if c, _ := caps("5"); !reflect.DeepEqual(c.Egress, []string{"config:base_url"}) {
		t.Errorf("describe 5 (current host, instance): egress = %v, want [config:base_url]", c.Egress)
	}
}
