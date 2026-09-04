package handoff

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
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

// TestRegistryBuildsRealDiscordChannel confirms buildChannel builds a real
// *DiscordChannel for a `discord:` entry (not the notWiredChannel stub used
// for a malformed `to`): Present actually posts through the (stubbed)
// Discord REST API and Ref reflects the mode.
func TestRegistryBuildsRealDiscordChannel(t *testing.T) {
	var posted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posted = append(posted, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/messages"):
			fmt.Fprint(w, `{"id":"111222"}`)
		case r.URL.Path == "/users/@me/channels":
			fmt.Fprint(w, `{"id":"D999"}`)
		default:
			t.Fatalf("unexpected discord API call: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	defer setDiscordAPIURL(srv.URL)()

	cfgs := map[string]config.HandoffConfig{
		"warroom": {Discord: &config.HandoffChat{To: "thread", Channel: "C0456", BotToken: "bot-x"}},
		"phone":   {Discord: &config.HandoffChat{To: "dm", User: "U123", BotToken: "bot-x"}},
	}
	r := NewRegistry(cfgs, "", nil)

	warroom, err := r.Resolve("warroom")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := warroom.(*DiscordChannel); !ok {
		t.Fatalf("warroom should resolve to a real *DiscordChannel, got %T", warroom)
	}
	pres, err := warroom.Present(context.Background(), Draft{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if pres.Ref() != "discord:C0456:thread" {
		t.Fatalf("unexpected thread ref %q", pres.Ref())
	}

	phone, err := r.Resolve("phone")
	if err != nil {
		t.Fatal(err)
	}
	pres, err = phone.Present(context.Background(), Draft{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if pres.Ref() != "discord:D999:dm" {
		t.Fatalf("unexpected dm ref %q", pres.Ref())
	}
	if len(posted) != 3 { // thread post + dm open + dm post
		t.Fatalf("unexpected API calls: %v", posted)
	}
}

// TestRegistryDiscordInboxSharedAcrossEntries confirms every configured
// discord entry's replies route through the SAME Inbox, and that
// DiscordInbox/DiscordBotTokens are empty/nil when no discord handoff is
// configured.
func TestRegistryDiscordInboxSharedAcrossEntries(t *testing.T) {
	defer setDiscordAPIURL(fakeDiscordServer(t))()

	cfgs := map[string]config.HandoffConfig{
		"a": {Discord: &config.HandoffChat{To: "thread", Channel: "C1", BotToken: "bot-x"}},
		"b": {Discord: &config.HandoffChat{To: "thread", Channel: "C2", BotToken: "bot-x"}},
	}
	r := NewRegistry(cfgs, "", nil)
	if r.DiscordInbox() == nil {
		t.Fatal("DiscordInbox should be non-nil when a discord handoff is configured")
	}
	if got := r.DiscordBotTokens(); len(got) != 1 || got[0] != "bot-x" {
		t.Fatalf("expected a single distinct bot token, got %v", got)
	}

	a, _ := r.Resolve("a")
	b, _ := r.Resolve("b")
	pa, err := a.Present(context.Background(), Draft{Title: "a"})
	if err != nil {
		t.Fatal(err)
	}
	pb, err := b.Present(context.Background(), Draft{Title: "b"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan Decision, 2)
	go func() { d, _ := pa.Await(context.Background()); done <- d }()
	go func() { d, _ := pb.Await(context.Background()); done <- d }()
	time.Sleep(10 * time.Millisecond)
	if !r.DiscordInbox().Deliver("C1", "", "approve") {
		t.Fatal("reply on entry a's channel should be consumed via the shared inbox")
	}
	if !r.DiscordInbox().Deliver("C2", "", "discard") {
		t.Fatal("reply on entry b's channel should be consumed via the shared inbox")
	}
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Await never resolved")
		}
	}
}

// TestRegistryDiscordBotTokensDistinct confirms two discord entries with
// different bot_tokens produce two distinct tokens (so main.go starts a
// gateway per token), and a shared token collapses to one.
func TestRegistryDiscordBotTokensDistinct(t *testing.T) {
	cfgs := map[string]config.HandoffConfig{
		"a": {Discord: &config.HandoffChat{To: "thread", Channel: "C1", BotToken: "bot-1"}},
		"b": {Discord: &config.HandoffChat{To: "thread", Channel: "C2", BotToken: "bot-2"}},
		"c": {Discord: &config.HandoffChat{To: "thread", Channel: "C3", BotToken: "bot-1"}},
	}
	r := NewRegistry(cfgs, "", nil)
	toks := r.DiscordBotTokens()
	seen := map[string]bool{}
	for _, t := range toks {
		seen[t] = true
	}
	if len(toks) != 2 || !seen["bot-1"] || !seen["bot-2"] {
		t.Fatalf("expected exactly [bot-1 bot-2] (order-independent), got %v", toks)
	}
}

func TestRegistryDiscordInboxNilWithoutDiscord(t *testing.T) {
	cfgs := map[string]config.HandoffConfig{
		"page": {Web: &config.HandoffWeb{BaseURL: "https://a.test"}},
	}
	r := NewRegistry(cfgs, "", nil)
	if r.DiscordInbox() != nil {
		t.Fatal("DiscordInbox should be nil when no discord handoff is configured")
	}
	if len(r.DiscordBotTokens()) != 0 {
		t.Fatal("DiscordBotTokens should be empty when no discord handoff is configured")
	}
}

// fakeDiscordServer starts an httptest server answering the Discord REST
// calls a hand-off channel makes, closed on test cleanup, and returns its URL.
func fakeDiscordServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/messages"):
			fmt.Fprint(w, `{"id":"1"}`)
		case r.URL.Path == "/users/@me/channels":
			fmt.Fprint(w, `{"id":"D999"}`)
		default:
			t.Fatalf("unexpected discord API call: %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestRegistryBuildsRealSlackChannel confirms buildChannel builds a real
// *SlackChannel for a `slack:` entry (not the notWiredChannel stub other chat
// channels still use): Present actually posts through the (stubbed) Slack Web
// API and Ref reflects the mode.
func TestRegistryBuildsRealSlackChannel(t *testing.T) {
	var posted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posted = append(posted, r.URL.Path)
		switch r.URL.Path {
		case "/chat.postMessage":
			fmt.Fprint(w, `{"ok":true,"ts":"111.222"}`)
		case "/conversations.open":
			fmt.Fprint(w, `{"ok":true,"channel":{"id":"D999"}}`)
		default:
			t.Fatalf("unexpected slack API call: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	restore := setSlackAPIURL(srv.URL)
	defer restore()

	cfgs := map[string]config.HandoffConfig{
		"warroom": {Slack: &config.HandoffChat{To: "thread", Channel: "C0456", BotToken: "xoxb-x"}},
		"phone":   {Slack: &config.HandoffChat{To: "dm", User: "U123", BotToken: "xoxb-x"}},
	}
	r := NewRegistry(cfgs, "", nil)

	warroom, err := r.Resolve("warroom")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := warroom.(*SlackChannel); !ok {
		t.Fatalf("warroom should resolve to a real *SlackChannel, got %T", warroom)
	}
	pres, err := warroom.Present(context.Background(), Draft{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if pres.Ref() != "slack:C0456:111.222" {
		t.Fatalf("unexpected thread ref %q", pres.Ref())
	}

	phone, err := r.Resolve("phone")
	if err != nil {
		t.Fatal(err)
	}
	pres, err = phone.Present(context.Background(), Draft{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if pres.Ref() != "slack:D999:dm" {
		t.Fatalf("unexpected dm ref %q", pres.Ref())
	}
}

// TestRegistrySlackInboxSharedAcrossEntries confirms every configured slack
// entry's replies route through the SAME Inbox (so one slack.SetReplyHook
// wiring in main.go covers every entry), and that SlackInbox is nil when no
// slack handoff is configured.
func TestRegistrySlackInboxSharedAcrossEntries(t *testing.T) {
	restore := setSlackAPIURL(fakeSlackServer(t))
	defer restore()

	cfgs := map[string]config.HandoffConfig{
		"a": {Slack: &config.HandoffChat{To: "thread", Channel: "C1", BotToken: "xoxb-x"}},
		"b": {Slack: &config.HandoffChat{To: "thread", Channel: "C2", BotToken: "xoxb-x"}},
	}
	r := NewRegistry(cfgs, "", nil)
	if r.SlackInbox() == nil {
		t.Fatal("SlackInbox should be non-nil when a slack handoff is configured")
	}

	a, _ := r.Resolve("a")
	b, _ := r.Resolve("b")
	pa, err := a.Present(context.Background(), Draft{Title: "a"})
	if err != nil {
		t.Fatal(err)
	}
	pb, err := b.Present(context.Background(), Draft{Title: "b"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan Decision, 2)
	go func() { d, _ := pa.Await(context.Background()); done <- d }()
	go func() { d, _ := pb.Await(context.Background()); done <- d }()
	time.Sleep(10 * time.Millisecond)
	if !r.SlackInbox().Deliver("C1", "111.222", "approve") {
		t.Fatal("reply to entry a's thread should be consumed via the shared inbox")
	}
	if !r.SlackInbox().Deliver("C2", "111.222", "discard") {
		t.Fatal("reply to entry b's thread should be consumed via the shared inbox")
	}
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Await never resolved")
		}
	}
}

func TestRegistrySlackInboxNilWithoutSlack(t *testing.T) {
	cfgs := map[string]config.HandoffConfig{
		"page": {Web: &config.HandoffWeb{BaseURL: "https://a.test"}},
	}
	r := NewRegistry(cfgs, "", nil)
	if r.SlackInbox() != nil {
		t.Fatal("SlackInbox should be nil when no slack handoff is configured")
	}
}

// fakeSlackServer starts an httptest server answering chat.postMessage with a
// fixed ts, closed on test cleanup, and returns its URL.
func fakeSlackServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			fmt.Fprint(w, `{"ok":true,"ts":"111.222"}`)
		case "/conversations.open":
			fmt.Fprint(w, `{"ok":true,"channel":{"id":"D999"}}`)
		default:
			t.Fatalf("unexpected slack API call: %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
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
