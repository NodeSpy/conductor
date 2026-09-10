package models

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// No test in this package touches the network or a real credential file: the
// HTTP client, the process runner, and the home directory are all seams.

// --- fixtures --------------------------------------------------------------

// catalogFixture mirrors the real models.dev document shape (verified against
// https://models.dev/api.json), trimmed to three providers.
const catalogFixture = `{
  "anthropic": {
    "id": "anthropic", "name": "Anthropic",
    "models": {
      "claude-opus-5":    {"id":"claude-opus-5","name":"Claude Opus 5","release_date":"2026-05-01","limit":{"context":200000,"output":64000},"cost":{"input":5,"output":25}},
      "claude-sonnet-5":  {"id":"claude-sonnet-5","name":"Claude Sonnet 5","release_date":"2026-02-01","limit":{"context":200000,"output":64000},"cost":{"input":3,"output":15}},
      "claude-opus-4-8":  {"id":"claude-opus-4-8","name":"Claude Opus 4.8","release_date":"2025-11-24","limit":{"context":200000,"output":64000},"cost":{"input":5,"output":25}}
    }
  },
  "openai": {
    "id": "openai", "name": "OpenAI",
    "models": {
      "gpt-5.6-sol":  {"id":"gpt-5.6-sol","name":"GPT-5.6-Sol","release_date":"2026-04-10","limit":{"context":400000,"output":128000},"cost":{"input":1.25,"output":10}},
      "gpt-5.6-mini": {"id":"gpt-5.6-mini","name":"GPT-5.6-Mini","release_date":"2026-04-10","limit":{"context":400000,"output":128000},"cost":{"input":0.25,"output":2}}
    }
  },
  "google": {
    "id": "google", "name": "Google",
    "models": {
      "gemini-3.0-pro": {"id":"gemini-3.0-pro","name":"Gemini 3.0 Pro","release_date":"2026-03-01","limit":{"context":1000000,"output":64000},"cost":{"input":2,"output":12}}
    }
  }
}`

// stubHTTP answers every request from a canned response.
type stubHTTP struct {
	status int
	body   string
	err    error
	calls  int
	seen   []*http.Request
}

func (s *stubHTTP) Do(req *http.Request) (*http.Response, error) {
	s.calls++
	s.seen = append(s.seen, req)
	if s.err != nil {
		return nil, s.err
	}
	st := s.status
	if st == 0 {
		st = http.StatusOK
	}
	return &http.Response{
		StatusCode: st,
		Status:     http.StatusText(st),
		Body:       io.NopCloser(strings.NewReader(s.body)),
		Header:     http.Header{},
	}, nil
}

func testCatalog(t *testing.T, h HTTPDoer) *Catalog {
	t.Helper()
	c := NewCatalog(t.TempDir())
	c.URL = "https://models.test/api.json"
	c.HTTP = h
	return c
}

// --- catalog ---------------------------------------------------------------

func TestCatalogParsesAndOrdersNewestFirst(t *testing.T) {
	cat := testCatalog(t, &stubHTTP{body: catalogFixture})
	got, err := cat.Provider(context.Background(), "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude-opus-5", "claude-sonnet-5", "claude-opus-4-8"}
	if !reflect.DeepEqual(got.IDs(), want) {
		t.Fatalf("roster = %v, want %v", got.IDs(), want)
	}
	m, _ := got.Find("claude-opus-5")
	if m.Context != 200000 || m.InputUSD != 5 || m.OutputUSD != 25 || m.Provider != "anthropic" {
		t.Errorf("metadata not carried: %#v", m)
	}
}

func TestCatalogLoadsOncePerProcess(t *testing.T) {
	h := &stubHTTP{body: catalogFixture}
	cat := testCatalog(t, h)
	for i := 0; i < 3; i++ {
		if _, err := cat.Provider(context.Background(), "openai"); err != nil {
			t.Fatal(err)
		}
	}
	if h.calls != 1 {
		t.Fatalf("fetched %d times, want 1", h.calls)
	}
}

func TestCatalogUnknownProviderIsNoDiscovery(t *testing.T) {
	cat := testCatalog(t, &stubHTTP{body: catalogFixture})
	_, err := cat.Provider(context.Background(), "nope")
	if !errors.Is(err, ErrNoDiscovery) {
		t.Fatalf("want ErrNoDiscovery, got %v", err)
	}
}

func TestCatalogWritesAndServesFreshCache(t *testing.T) {
	dir := t.TempDir()
	h := &stubHTTP{body: catalogFixture}
	c1 := NewCatalog(dir)
	c1.URL, c1.HTTP = "https://models.test/api.json", h
	if _, err := c1.Provider(context.Background(), "google"); err != nil {
		t.Fatal(err)
	}
	cached := filepath.Join(dir, "models", "models.dev.json")
	if _, err := os.Stat(cached); err != nil {
		t.Fatalf("cache not written: %v", err)
	}
	// A second catalog with a DEAD network must still resolve from the cache.
	dead := &stubHTTP{err: errors.New("offline")}
	c2 := NewCatalog(dir)
	c2.URL, c2.HTTP = "https://models.test/api.json", dead
	got, err := c2.Provider(context.Background(), "google")
	if err != nil {
		t.Fatalf("fresh cache should serve offline: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("roster = %v", got.IDs())
	}
	if dead.calls != 0 {
		t.Errorf("a fresh cache must not refetch (%d calls)", dead.calls)
	}
}

func TestCatalogRefetchesPastTTLAndDegradesToStaleCache(t *testing.T) {
	dir := t.TempDir()
	seed := NewCatalog(dir)
	seed.URL, seed.HTTP = "https://models.test/api.json", &stubHTTP{body: catalogFixture}
	if _, err := seed.Provider(context.Background(), "openai"); err != nil {
		t.Fatal(err)
	}

	// Same cache, now well past its TTL, and the network is down: the STALE
	// copy must still answer rather than blanking the roster.
	dead := &stubHTTP{err: errors.New("offline")}
	c := NewCatalog(dir)
	c.URL, c.HTTP = "https://models.test/api.json", dead
	c.Now = func() time.Time { return time.Now().Add(48 * time.Hour) }
	got, err := c.Provider(context.Background(), "openai")
	if err != nil {
		t.Fatalf("stale cache should degrade-serve: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("roster = %v", got.IDs())
	}
	if dead.calls != 1 {
		t.Errorf("a stale cache must attempt one refetch (%d calls)", dead.calls)
	}
}

func TestCatalogNoNetworkNoCacheIsNoDiscovery(t *testing.T) {
	cat := testCatalog(t, &stubHTTP{err: errors.New("offline")})
	_, err := cat.Provider(context.Background(), "anthropic")
	if !errors.Is(err, ErrNoDiscovery) {
		t.Fatalf("want ErrNoDiscovery, got %v", err)
	}
	// And it must not re-attempt the 4MB fetch on every call.
	_, _ = cat.Provider(context.Background(), "anthropic")
	if h := cat.HTTP.(*stubHTTP); h.calls != 1 {
		t.Errorf("fetched %d times, want 1", h.calls)
	}
}

func TestCatalogCorruptCacheRefetches(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "models"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models", "models.dev.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &stubHTTP{body: catalogFixture}
	c := NewCatalog(dir)
	c.URL, c.HTTP = "https://models.test/api.json", h
	if _, err := c.Provider(context.Background(), "anthropic"); err != nil {
		t.Fatalf("corrupt cache should refetch: %v", err)
	}
	if h.calls != 1 {
		t.Errorf("fetched %d times, want 1", h.calls)
	}
}

func TestCatalogHTTPErrorStatus(t *testing.T) {
	cat := testCatalog(t, &stubHTTP{status: http.StatusServiceUnavailable, body: ""})
	_, err := cat.Provider(context.Background(), "anthropic")
	if !errors.Is(err, ErrNoDiscovery) {
		t.Fatalf("want ErrNoDiscovery, got %v", err)
	}
}

func TestNilCatalogIsNoDiscovery(t *testing.T) {
	var cat *Catalog
	if _, err := cat.Provider(context.Background(), "anthropic"); !errors.Is(err, ErrNoDiscovery) {
		t.Fatalf("want ErrNoDiscovery, got %v", err)
	}
}

// --- paseo (native) --------------------------------------------------------

// paseoRunner replays the real `paseo provider ls|models --json` shapes.
func paseoRunner(t *testing.T, fail map[string]bool) (Runner, *[]string) {
	t.Helper()
	var calls []string
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if fail[strings.Join(args, " ")] {
			return nil, errors.New("boom")
		}
		switch {
		case len(args) >= 2 && args[1] == "ls":
			return []byte(`[
              {"provider":"claude","label":"Claude","status":"available","enabled":"Enabled"},
              {"provider":"codex","label":"Codex","status":"available","enabled":"Enabled"},
              {"provider":"copilot","label":"Copilot","status":"unavailable","enabled":"Disabled"}
            ]`), nil
		case len(args) >= 3 && args[1] == "models" && args[2] == "claude":
			return []byte(`[
              {"model":"Opus 5","id":"claude-opus-5"},
              {"model":"Fable 5.1","id":"claude-fable-5-1"}
            ]`), nil
		case len(args) >= 3 && args[1] == "models" && args[2] == "codex":
			return []byte(`[{"model":"GPT-5.6-Sol","id":"gpt-5.6-sol"}]`), nil
		}
		return nil, errors.New("unexpected call")
	}, &calls
}

func TestPaseoListerIsNativeAndSkipsUnavailableProviders(t *testing.T) {
	run, calls := paseoRunner(t, nil)
	l := &paseoLister{bin: "paseo", run: run}
	got, err := l.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude-opus-5", "claude-fable-5-1", "gpt-5.6-sol"}
	if !reflect.DeepEqual(got.IDs(), want) {
		t.Fatalf("roster = %v, want %v", got.IDs(), want)
	}
	for _, c := range *calls {
		if strings.Contains(c, "copilot") {
			t.Errorf("unavailable provider was queried: %s", c)
		}
	}
}

func TestPaseoListerOneBadProviderDoesNotBlankTheRoster(t *testing.T) {
	run, _ := paseoRunner(t, map[string]bool{"provider models codex --json": true})
	l := &paseoLister{bin: "paseo", run: run}
	got, err := l.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.IDs(), []string{"claude-opus-5", "claude-fable-5-1"}) {
		t.Fatalf("roster = %v", got.IDs())
	}
}

func TestPaseoListerUnavailableBinaryIsNoDiscovery(t *testing.T) {
	l := &paseoLister{bin: "paseo", run: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("exec: not found")
	}}
	if _, err := l.List(context.Background()); !errors.Is(err, ErrNoDiscovery) {
		t.Fatalf("want ErrNoDiscovery, got %v", err)
	}
}

func TestPaseoListerHonorsBinOverride(t *testing.T) {
	var seen string
	l := &paseoLister{bin: "/opt/paseo", run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
		seen = name
		return nil, errors.New("stop")
	}}
	_, _ = l.List(context.Background())
	if seen != "/opt/paseo" {
		t.Fatalf("bin = %q, want /opt/paseo", seen)
	}
}

// Discovery must never be funnelled through paseo: no other adapter may shell
// out to it.
func TestOnlyPaseoAdapterInvokesPaseo(t *testing.T) {
	cat := testCatalog(t, &stubHTTP{body: catalogFixture})
	for _, impl := range []string{"cli", "acp", "agent-deck", "opencode"} {
		l, ok := ListerFor(Runtime{Name: "r", Impl: impl, Tool: "claude"}, cat)
		if !ok {
			t.Fatalf("%s: no adapter registered", impl)
		}
		if _, isPaseo := l.(*paseoLister); isPaseo {
			t.Fatalf("%s resolved to the paseo adapter", impl)
		}
	}
}

// --- bare CLI --------------------------------------------------------------

const claudeLiveFixture = `{"data":[
  {"type":"model","id":"claude-fable-5-1","display_name":"Claude Fable 5.1","created_at":"2026-08-28T00:00:00Z","max_input_tokens":1000000,"max_tokens":128000},
  {"type":"model","id":"claude-opus-5","display_name":"Claude Opus 5","created_at":"2026-05-01T00:00:00Z","max_input_tokens":200000,"max_tokens":64000}
],"has_more":false}`

// withStubClaudeCreds points the credential reader at a temp file.
func withStubClaudeCreds(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prev := homeDir
	homeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { homeDir = prev })
}

func TestCLIClaudeUsesLiveAPIAndEnrichesFromCatalog(t *testing.T) {
	withStubClaudeCreds(t, `{"claudeAiOauth":{"accessToken":"sk-ant-oat-TESTTOKENVALUE"}}`)
	live := &stubHTTP{body: claudeLiveFixture}
	l := &cliLister{tool: "claude", cat: testCatalog(t, &stubHTTP{body: catalogFixture}), http: live}

	got, err := l.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Live membership and live order win: fable-5-1 is not in the catalog
	// fixture but IS entitled, and opus-4-8 is in the catalog but not
	// entitled — the live list is the filter.
	if !reflect.DeepEqual(got.IDs(), []string{"claude-fable-5-1", "claude-opus-5"}) {
		t.Fatalf("roster = %v", got.IDs())
	}
	// Catalog metadata fills what the live response omits.
	m, _ := got.Find("claude-opus-5")
	if m.InputUSD != 5 || m.OutputUSD != 25 {
		t.Errorf("pricing not enriched: %#v", m)
	}
	if m.Context != 200000 {
		t.Errorf("context = %d", m.Context)
	}
}

func TestCLIClaudeSendsTheDocumentedHeadersAndNeverLeaksTheToken(t *testing.T) {
	withStubClaudeCreds(t, `{"claudeAiOauth":{"accessToken":"sk-ant-oat-SUPERSECRETVALUE"}}`)
	live := &stubHTTP{status: http.StatusUnauthorized}
	l := &cliLister{tool: "claude", cat: testCatalog(t, &stubHTTP{body: catalogFixture}), http: live}

	_, err := l.List(context.Background())
	if err != nil {
		t.Fatalf("a failed live probe must fall through to the catalog, got %v", err)
	}
	if len(live.seen) != 1 {
		t.Fatalf("live probe not attempted")
	}
	req := live.seen[0]
	if got := req.Header.Get("authorization"); got != "Bearer sk-ant-oat-SUPERSECRETVALUE" {
		t.Errorf("authorization header = %q", got)
	}
	if got := req.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q", got)
	}
	if got := req.Header.Get("anthropic-beta"); got != "oauth-2025-04-20" {
		t.Errorf("anthropic-beta = %q", got)
	}
	// The probe's own error text must not carry the token.
	_, perr := claudeLiveModels(context.Background(), live)
	if perr == nil || strings.Contains(perr.Error(), "SUPERSECRETVALUE") {
		t.Fatalf("probe error leaks the token: %v", perr)
	}
}

func TestCLIClaudeFallsBackToCatalogWithoutCredentials(t *testing.T) {
	withStubClaudeCreds(t, "") // no credentials file at all
	live := &stubHTTP{body: claudeLiveFixture}
	l := &cliLister{tool: "claude", cat: testCatalog(t, &stubHTTP{body: catalogFixture}), http: live}

	got, err := l.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if live.calls != 0 {
		t.Errorf("no credentials means no live request (%d made)", live.calls)
	}
	if !reflect.DeepEqual(got.IDs(), []string{"claude-opus-5", "claude-sonnet-5", "claude-opus-4-8"}) {
		t.Fatalf("roster = %v", got.IDs())
	}
}

// codex cannot list live (auth_mode: chatgpt -> 403 missing api.model.read),
// so it must go straight to the catalog and burn no request.
func TestCLICodexGoesStraightToCatalog(t *testing.T) {
	live := &stubHTTP{status: http.StatusForbidden, body: `{"error":"Missing scopes: api.model.read"}`}
	l := &cliLister{tool: "codex", cat: testCatalog(t, &stubHTTP{body: catalogFixture}), http: live}
	got, err := l.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if live.calls != 0 {
		t.Errorf("codex has no live path; %d requests made", live.calls)
	}
	// Same release_date, so the catalog's tiebreak (id, ascending) decides —
	// deterministic, since a JSON object has no order to preserve.
	if !reflect.DeepEqual(got.IDs(), []string{"gpt-5.6-mini", "gpt-5.6-sol"}) {
		t.Fatalf("roster = %v", got.IDs())
	}
}

// gemini's CLI token does not authorize Google's models API either.
func TestCLIGeminiGoesStraightToCatalog(t *testing.T) {
	live := &stubHTTP{err: errors.New("should not be called")}
	l := &cliLister{tool: "gemini", cat: testCatalog(t, &stubHTTP{body: catalogFixture}), http: live}
	got, err := l.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if live.calls != 0 {
		t.Errorf("gemini has no live path; %d requests made", live.calls)
	}
	if !reflect.DeepEqual(got.IDs(), []string{"gemini-3.0-pro"}) {
		t.Fatalf("roster = %v", got.IDs())
	}
}

func TestCLIUnknownToolIsNoDiscovery(t *testing.T) {
	l := &cliLister{tool: "some-new-agent", cat: testCatalog(t, &stubHTTP{body: catalogFixture}), http: &stubHTTP{}}
	if _, err := l.List(context.Background()); !errors.Is(err, ErrNoDiscovery) {
		t.Fatalf("want ErrNoDiscovery, got %v", err)
	}
}

func TestCLINoToolIsNoDiscovery(t *testing.T) {
	l := &cliLister{cat: testCatalog(t, &stubHTTP{body: catalogFixture}), http: &stubHTTP{}}
	if _, err := l.List(context.Background()); !errors.Is(err, ErrNoDiscovery) {
		t.Fatalf("want ErrNoDiscovery, got %v", err)
	}
}

func TestRuntimeToolName(t *testing.T) {
	tests := []struct {
		rt   Runtime
		want string
	}{
		{Runtime{Tool: "claude"}, "claude"},
		{Runtime{Agent: "gemini"}, "gemini"},
		{Runtime{Command: []string{"/usr/bin/codex", "exec"}}, "codex"},
		{Runtime{Tool: "claude", Agent: "gemini"}, "claude"},
		{Runtime{}, ""},
	}
	for _, tc := range tests {
		if got := tc.rt.ToolName(); got != tc.want {
			t.Errorf("%#v: tool = %q, want %q", tc.rt, got, tc.want)
		}
	}
}

// --- agent-deck ------------------------------------------------------------

func writeDeckConfig(t *testing.T, body string) func() (string, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return func() (string, error) { return path, nil }
}

func TestAgentDeckResolvesConfiguredTools(t *testing.T) {
	// The real shape from a working install: a default_tool, a per-tool
	// section, a profile account slot, and group overrides.
	cfg := `
default_tool = "claude"
theme = "dark"

[claude]
  command = "claude --model claude-opus-4-8"
  default_model = "claude-opus-4-8"

    [profiles.tapresearch.claude]
      config_dir = "/x/.claude-account"

[codex]
  command = "codex"

[worktree]
  auto_cleanup = false
`
	l := &deckLister{cat: testCatalog(t, &stubHTTP{body: catalogFixture}), configPath: writeDeckConfig(t, cfg)}
	if got := l.tools(); !reflect.DeepEqual(got, []string{"claude", "codex"}) {
		t.Fatalf("tools = %v, want [claude codex]", got)
	}
	roster, err := l.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude-opus-5", "claude-sonnet-5", "claude-opus-4-8", "gpt-5.6-mini", "gpt-5.6-sol"}
	if !reflect.DeepEqual(roster.IDs(), want) {
		t.Fatalf("roster = %v, want %v", roster.IDs(), want)
	}
}

func TestAgentDeckDefaultToolLeadsTheRoster(t *testing.T) {
	l := &deckLister{
		cat:        testCatalog(t, &stubHTTP{body: catalogFixture}),
		configPath: writeDeckConfig(t, "default_tool = \"gemini\"\n\n[claude]\n  command = \"claude\"\n"),
	}
	if got := l.tools(); !reflect.DeepEqual(got, []string{"gemini", "claude"}) {
		t.Fatalf("tools = %v, want [gemini claude]", got)
	}
}

func TestAgentDeckMissingConfigAssumesShippedDefault(t *testing.T) {
	l := &deckLister{
		cat:        testCatalog(t, &stubHTTP{body: catalogFixture}),
		configPath: func() (string, error) { return filepath.Join(t.TempDir(), "absent.toml"), nil },
	}
	if got := l.tools(); !reflect.DeepEqual(got, []string{"claude"}) {
		t.Fatalf("tools = %v, want [claude]", got)
	}
	if _, err := l.List(context.Background()); err != nil {
		t.Fatalf("a missing config should still enumerate the default tool: %v", err)
	}
}

func TestAgentDeckDegradesWhenTheCatalogIsUnavailable(t *testing.T) {
	l := &deckLister{
		cat:        testCatalog(t, &stubHTTP{err: errors.New("offline")}),
		configPath: writeDeckConfig(t, `default_tool = "claude"`),
	}
	if _, err := l.List(context.Background()); !errors.Is(err, ErrNoDiscovery) {
		t.Fatalf("want ErrNoDiscovery, got %v", err)
	}
}

func TestDeckSectionTool(t *testing.T) {
	tests := map[string]string{
		"claude":                     "claude",
		"profiles.tapresearch.codex": "codex",
		"groups.Ednition.gemini":     "gemini",
		"worktree":                   "",
		"profiles.x.config_dir":      "",
		"":                           "",
	}
	for header, want := range tests {
		if got := deckSectionTool(header); got != want {
			t.Errorf("[%s] -> %q, want %q", header, got, want)
		}
	}
}

func TestScanDeckToolsIgnoresCommentsAndJunk(t *testing.T) {
	cfg := "# default_tool = \"codex\"\ndefault_tool = \"claude\"\n[claude]\nnot a section\n[[weird]]\n"
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	def, tools := scanDeckTools(path)
	if def != "claude" {
		t.Errorf("default = %q", def)
	}
	if len(tools) != 1 || !tools["claude"] {
		t.Errorf("tools = %v", tools)
	}
}

// --- credentials and redaction --------------------------------------------

func TestClaudeOAuthToken(t *testing.T) {
	withStubClaudeCreds(t, `{"claudeAiOauth":{"accessToken":"  sk-ant-oat-VALUE  "}}`)
	got, err := claudeOAuthToken()
	if err != nil {
		t.Fatal(err)
	}
	if got != "sk-ant-oat-VALUE" {
		t.Fatalf("token = %q", got)
	}
}

func TestClaudeOAuthTokenErrorsNeverCarryTheFileBody(t *testing.T) {
	withStubClaudeCreds(t, `{"claudeAiOauth":{"accessToken":""},"other":"SECRETISH"}`)
	_, err := claudeOAuthToken()
	if err == nil {
		t.Fatal("want an error for an empty token")
	}
	if strings.Contains(err.Error(), "SECRETISH") {
		t.Fatalf("error leaks file contents: %v", err)
	}
}

func TestRedactToken(t *testing.T) {
	tests := map[string]string{
		"":                       "(none)",
		"short":                  "(redacted)",
		"sk-ant-oat-LONGVALUE12": "sk-ant…(redacted)",
	}
	for in, want := range tests {
		if got := redactToken(in); got != want {
			t.Errorf("redact(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- registry --------------------------------------------------------------

func TestListerForKnownAndUnknownImplementations(t *testing.T) {
	cat := testCatalog(t, &stubHTTP{body: catalogFixture})
	for _, impl := range Implementations() {
		if _, ok := ListerFor(Runtime{Name: "r", Impl: impl}, cat); !ok {
			t.Errorf("%s: registered but ListerFor said no", impl)
		}
	}
	// An implementation with no adapter is not an error — it means bare
	// launch.
	if _, ok := ListerFor(Runtime{Name: "r", Impl: "modal"}, cat); ok {
		t.Error("an unregistered implementation should have no lister")
	}
}

func TestImplementationsCoversTheDesignedAdapters(t *testing.T) {
	got := strings.Join(Implementations(), " ")
	for _, want := range []string{"paseo", "agent-deck", "cli", "acp"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing adapter %q (have %s)", want, got)
		}
	}
}

func TestRosterHelpers(t *testing.T) {
	r := Roster{
		{ID: "b", Released: "2026-01-01"},
		{ID: "a", Released: "2026-05-01"},
		{ID: "z"},
		{ID: "a", Released: "2026-05-01"},
	}
	if got := r.SortNewestFirst().IDs(); !reflect.DeepEqual(got, []string{"a", "a", "b", "z"}) {
		t.Errorf("sorted = %v", got)
	}
	if got := r.Dedupe().IDs(); !reflect.DeepEqual(got, []string{"b", "a", "z"}) {
		t.Errorf("deduped = %v", got)
	}
	if _, ok := r.Find("nope"); ok {
		t.Error("Find should miss")
	}
}
