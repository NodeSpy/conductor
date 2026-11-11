package plugin

import (
	"context"
	"encoding/json"
	"fmt"
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

// oldHostSemantics is HostInfo.Semantics as conductor v0.61 through v0.89.x
// sent it: everything this SDK knows but SemanticReferences and
// SemanticExposeLocal.
func oldHostSemantics() []string {
	return slices.DeleteFunc(KnownSemantics(), func(s string) bool {
		return s == SemanticReferences || s == SemanticExposeLocal || s == SemanticListenerDefaultPath
	})
}

// v090Semantics is HostInfo.Semantics as conductor v0.90 sent it: it
// resolves references, but has never heard of expose_local.
func v090Semantics() []string {
	return slices.DeleteFunc(KnownSemantics(), func(s string) bool { return s == SemanticExposeLocal || s == SemanticListenerDefaultPath })
}

// exposeHandler declares expose_local.
type exposeHandler struct{}

func (exposeHandler) Describe() Decl {
	return Decl{Type: "t", Capabilities: Capabilities{Egress: []string{"relay.example:443", "config:channel"}, ExposeLocal: true}}
}
func (exposeHandler) Invoke(InvokeRequest) (InvokeResult, error) { return InvokeResult{}, nil }

// Serve sends expose_local only to a host that advertises it grants it;
// every older conductor (which ignores the field) gets the declaration
// without it, and the rest of the declaration as before.
func TestServeWithholdsExposeLocalFromOldHosts(t *testing.T) {
	if !slices.Contains(KnownSemantics(), SemanticExposeLocal) {
		t.Fatalf("KnownSemantics() = %v: a host built with this SDK must advertise %s", KnownSemantics(), SemanticExposeLocal)
	}
	hosts := []*HostInfo{
		nil, // before v0.61
		{Version: "dev", Semantics: oldHostSemantics()}, // v0.61 – v0.89.x
		{Version: "dev", Semantics: v090Semantics()},    // v0.90
		{Version: "dev", Semantics: KnownSemantics()},   // this SDK
	}
	var reqs []string
	for i, h := range hosts {
		b, err := json.Marshal(DescribeRequest{Host: h})
		if err != nil {
			t.Fatal(err)
		}
		reqs = append(reqs, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"plugin.describe","params":%s}`, i+1, b))
	}
	var out strings.Builder
	if err := serve(strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out, exposeHandler{}); err != nil {
		t.Fatalf("serve: %v", err)
	}
	byID := decodeResponses(t, out.String())
	for i, want := range []struct {
		expose bool
		egress []string
	}{
		{false, []string{"relay.example:443"}},
		{false, []string{"relay.example:443"}},
		{false, []string{"relay.example:443", "config:channel"}},
		{true, []string{"relay.example:443", "config:channel"}},
	} {
		m := byID[fmt.Sprint(i+1)]
		if m.Error != nil {
			t.Fatalf("host %d: %+v", i, m.Error)
		}
		var d struct {
			Capabilities map[string]json.RawMessage `json:"capabilities"`
		}
		if err := json.Unmarshal(m.Result, &d); err != nil {
			t.Fatal(err)
		}
		_, sent := d.Capabilities["expose_local"]
		var egress []string
		_ = json.Unmarshal(d.Capabilities["egress"], &egress)
		if sent != want.expose || !slices.Equal(egress, want.egress) {
			t.Errorf("host %d: expose_local sent=%v egress=%v, want %v %v", i, sent, egress, want.expose, want.egress)
		}
	}
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

// v094Semantics is HostInfo.Semantics as conductor v0.91 through v0.94 sent
// it: everything but SemanticListenerDefaultPath.
func v094Semantics() []string {
	return slices.DeleteFunc(KnownSemantics(), func(s string) bool { return s == SemanticListenerDefaultPath })
}

// defaultPathHandler declares a listener with a default path.
type defaultPathHandler struct{}

func (defaultPathHandler) Describe() Decl {
	return Decl{Type: "t",
		Connection: Schema{"webhook": {Type: "map"}},
		Semantics:  &ConnSemantics{Listeners: []Listener{{Listen: "webhook.listen", Expose: "webhook.expose", Path: "webhook.path", DefaultPath: "/hook"}}}}
}
func (defaultPathHandler) Invoke(InvokeRequest) (InvokeResult, error) { return InvokeResult{}, nil }

// Serve sends a listener's default_path only to a host that advertises it
// reads it. An older host decodes listeners strictly and would refuse the
// plugin over the field; it gets the declaration it got before the field
// existed, which its own must-understand check accepts.
func TestServeWithholdsListenerDefaultPathFromOldHosts(t *testing.T) {
	if !slices.Contains(KnownSemantics(), SemanticListenerDefaultPath) {
		t.Fatalf("KnownSemantics() = %v: a host built with this SDK must advertise %s", KnownSemantics(), SemanticListenerDefaultPath)
	}
	hosts := []*HostInfo{
		nil,
		{Version: "dev", Semantics: oldHostSemantics()},
		{Version: "dev", Semantics: v090Semantics()},
		{Version: "dev", Semantics: v094Semantics()},
		{Version: "dev", Semantics: KnownSemantics()},
	}
	var reqs []string
	for i, h := range hosts {
		b, err := json.Marshal(DescribeRequest{Host: h})
		if err != nil {
			t.Fatal(err)
		}
		reqs = append(reqs, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"plugin.describe","params":%s}`, i+1, b))
	}
	var out strings.Builder
	if err := serve(strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out, defaultPathHandler{}); err != nil {
		t.Fatalf("serve: %v", err)
	}
	byID := decodeResponses(t, out.String())
	// What a host before the field decodes listeners into.
	type oldListener struct {
		Listen string `json:"listen"`
		Expose string `json:"expose,omitempty"`
		URLTo  string `json:"url_to,omitempty"`
		Path   string `json:"path,omitempty"`
	}
	for i := range hosts {
		m := byID[fmt.Sprint(i+1)]
		if m.Error != nil {
			t.Fatalf("host %d: %+v", i, m.Error)
		}
		var d struct {
			Semantics struct {
				Listeners []json.RawMessage `json:"listeners"`
			} `json:"semantics"`
		}
		if err := json.Unmarshal(m.Result, &d); err != nil || len(d.Semantics.Listeners) != 1 {
			t.Fatalf("host %d: %s (%v)", i, m.Result, err)
		}
		raw := d.Semantics.Listeners[0]
		newHost := i == len(hosts)-1
		if sent := strings.Contains(string(raw), "default_path"); sent != newHost {
			t.Errorf("host %d: default_path sent=%v: %s", i, sent, raw)
		}
		if !newHost {
			var ol oldListener
			if err := strictDecode(raw, &ol); err != nil || ol.Path != "webhook.path" {
				t.Errorf("host %d: an older host's strict decode: %v (%+v)", i, err, ol)
			}
		}
	}
}
