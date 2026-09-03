package handoff

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
)

func TestRegistryResolveNoneConfigured(t *testing.T) {
	r := NewRegistry(nil, "", nil)
	ch, err := r.Resolve("")
	if err != nil {
		t.Fatalf("no handoffs configured should not error, got %v", err)
	}
	if ch != nil {
		t.Fatalf("no handoffs configured should resolve to nil (paseo-native), got %v", ch)
	}
}

func TestRegistryResolveSoleEntry(t *testing.T) {
	cfgs := map[string]config.HandoffConfig{
		"only": {Web: &config.HandoffWeb{BaseURL: "http://a.test"}},
	}
	r := NewRegistry(cfgs, "", nil)
	ch, err := r.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if ch == nil {
		t.Fatal("the sole configured entry should resolve when none is named/default")
	}
	if w, ok := ch.(*WebChannel); !ok || w != r.channels["only"] {
		t.Fatalf("resolved channel should be the sole entry's *WebChannel, got %T", ch)
	}
}

func TestRegistryResolveDefaultFlagWinsOverSoleAmbiguity(t *testing.T) {
	cfgs := map[string]config.HandoffConfig{
		"a": {Web: &config.HandoffWeb{BaseURL: "http://a.test"}},
		"b": {Web: &config.HandoffWeb{BaseURL: "http://b.test"}, Default: true},
	}
	r := NewRegistry(cfgs, "b", nil)
	ch, err := r.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if ch != r.channels["b"] {
		t.Fatal("empty name with >1 entries should resolve to the default:true entry")
	}
}

func TestRegistryResolveExplicitWinsOverDefault(t *testing.T) {
	cfgs := map[string]config.HandoffConfig{
		"a": {Web: &config.HandoffWeb{BaseURL: "http://a.test"}, Default: true},
		"b": {Web: &config.HandoffWeb{BaseURL: "http://b.test"}},
	}
	r := NewRegistry(cfgs, "a", nil)
	ch, err := r.Resolve("b")
	if err != nil {
		t.Fatal(err)
	}
	if ch != r.channels["b"] {
		t.Fatal("an explicit step handoff: name should win over the default:true entry")
	}
}

func TestRegistryResolveWithMultipleEntriesNoDefaultNoName(t *testing.T) {
	// Ambiguous: more than one entry, none flagged default, none named — resolves
	// to nil, no error (config validation is what should have caught a step
	// referencing an unknown/ambiguous handoff; the registry itself just reports
	// "nothing specific to resolve to").
	cfgs := map[string]config.HandoffConfig{
		"a": {Web: &config.HandoffWeb{BaseURL: "http://a.test"}},
		"b": {Web: &config.HandoffWeb{BaseURL: "http://b.test"}},
	}
	r := NewRegistry(cfgs, "", nil)
	ch, err := r.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if ch != nil {
		t.Fatalf("ambiguous (no default, no name, >1 entries) should resolve to nil, got %v", ch)
	}
}

func TestRegistryResolveUnknownName(t *testing.T) {
	r := NewRegistry(map[string]config.HandoffConfig{
		"a": {Web: &config.HandoffWeb{BaseURL: "http://a.test"}},
	}, "", nil)
	if _, err := r.Resolve("nope"); err == nil {
		t.Fatal("an unknown handoff name should error")
	}
}

func TestRegistryChatChannelsAreNotWiredStubs(t *testing.T) {
	cfgs := map[string]config.HandoffConfig{
		"phone":   {Slack: &config.HandoffChat{To: "dm"}},
		"discord": {Discord: &config.HandoffChat{To: "thread", Channel: "general"}},
	}
	r := NewRegistry(cfgs, "", nil)
	for _, name := range []string{"phone", "discord"} {
		ch, err := r.Resolve(name)
		if err != nil {
			t.Fatalf("%s: resolving a configured (if unwired) handoff should not error, got %v", name, err)
		}
		_, perr := ch.Present(context.Background(), Draft{Title: "t"})
		if !errors.Is(perr, ErrNotWired) {
			t.Fatalf("%s: Present on a slack/discord stub should fail with ErrNotWired, got %v", name, perr)
		}
	}
}

// TestRegistryBuildChannelWiresTunnel confirms buildChannel actually wires a
// configured tunnel into the *WebChannel (not just the schema): a `lan`
// provider's Open should be reachable through the resolved channel's Present,
// producing a link on the configured LAN host rather than base_url.
func TestRegistryBuildChannelWiresTunnel(t *testing.T) {
	cfgs := map[string]config.HandoffConfig{
		"page": {Web: &config.HandoffWeb{
			BaseURL: "https://unused.example",
			Listen:  ":9911",
			Tunnel:  config.TunnelConfig{Provider: "lan", Host: "192.168.1.50"},
		}},
	}
	r := NewRegistry(cfgs, "", nil)
	ch, err := r.Resolve("page")
	if err != nil {
		t.Fatal(err)
	}
	pres, err := ch.Present(context.Background(), Draft{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	want := "http://192.168.1.50:9911/handoff?id="
	if !strings.HasPrefix(pres.Ref(), want) {
		t.Fatalf("expected the lan tunnel origin in the link, got %q", pres.Ref())
	}
}

func TestRegistryWebEntriesDefaultListen(t *testing.T) {
	cfgs := map[string]config.HandoffConfig{
		"a": {Web: &config.HandoffWeb{BaseURL: "http://a.test"}},                  // no listen: → :8099
		"b": {Web: &config.HandoffWeb{BaseURL: "http://b.test", Listen: ":9100"}}, // explicit listen:
		"c": {Slack: &config.HandoffChat{To: "dm"}},                               // not a web entry
	}
	r := NewRegistry(cfgs, "", nil)
	entries := map[string]string{}
	for _, e := range r.WebEntries() {
		entries[e.Name] = e.Listen
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 web entries, got %d: %+v", len(entries), entries)
	}
	if entries["a"] != ":8099" {
		t.Fatalf("entry a should default to :8099, got %q", entries["a"])
	}
	if entries["b"] != ":9100" {
		t.Fatalf("entry b should keep its explicit listen, got %q", entries["b"])
	}
	if _, ok := entries["c"]; ok {
		t.Fatal("a slack entry must not appear in WebEntries")
	}
}
