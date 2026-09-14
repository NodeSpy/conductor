package models

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Bare-CLI discovery (docs/design/runtimes-models-packs.md §3.1).
//
// A bare CLI runtime — `use: cli` with a tool:, or `use: acp` with an agent: —
// drives one vendor's tool. Strategy, in order:
//
//  1. the PROVIDER'S OWN API with the tool's stored credentials. This is
//     account-scoped truth: it is what this box can actually run, not what
//     exists in the world. Validated on the maintainer's box:
//     - claude: GET api.anthropic.com/v1/models with the OAuth token from
//       ~/.claude/.credentials.json — WORKS (200, live list).
//     - codex:  ~/.codex/auth.json is auth_mode: chatgpt; /v1/models returns
//       403 "missing scopes: api.model.read". There is no live call.
//     - gemini: Google's models API needs an API key / registered identity;
//       the CLI's OAuth token does not authorize it.
//  2. models.dev, the public catalog, for the provider behind the tool.
//  3. ErrNoDiscovery → bare launch.
//
// Only claude has a live path today, so only claude implements one. codex and
// gemini go straight to step 2 rather than burning a request on a call that is
// known to fail. When either vendor ships a listable scope, it gets a prober
// here and nothing else changes.

func init() {
	f := func(rt Runtime, cat *Catalog) Lister {
		return &cliLister{tool: rt.ToolName(), cat: cat, http: defaultHTTP()}
	}
	Register("cli", f)
	Register("acp", f)
	Register("opencode", func(rt Runtime, cat *Catalog) Lister {
		return &cliLister{tool: "opencode", cat: cat, http: defaultHTTP()}
	})
}

// toolProvider maps a CLI tool onto its models.dev provider id. A tool with no
// entry cannot be enumerated through the catalog.
var toolProvider = map[string]string{
	"claude":       "anthropic",
	"claude-code":  "anthropic",
	"codex":        "openai",
	"gemini":       "google",
	"opencode":     "opencode",
	"copilot":      "github-copilot",
	"cursor-agent": "openai",
}

// ToolProvider exposes the tool→catalog-provider mapping for diagnostics.
func ToolProvider(tool string) (string, bool) {
	p, ok := toolProvider[strings.ToLower(strings.TrimSpace(tool))]
	return p, ok
}

type cliLister struct {
	tool string
	cat  *Catalog
	http HTTPDoer
}

// List runs the live probe (where one exists) and otherwise the catalog. A
// successful live probe is used as an ENTITLEMENT FILTER over the catalog: the
// live ids decide membership and order, the catalog supplies the metadata the
// live response omits (context, pricing).
func (l *cliLister) List(ctx context.Context) (Roster, error) {
	tool := strings.ToLower(strings.TrimSpace(l.tool))
	if tool == "" {
		return nil, fmt.Errorf("%w: runtime names no tool to enumerate", ErrNoDiscovery)
	}
	provider, known := toolProvider[tool]

	if probe := liveProbes[tool]; probe != nil {
		if live, err := probe(ctx, l.http); err == nil && len(live) > 0 {
			return l.enrich(ctx, live, provider), nil
		}
		// A live probe that fails (no credentials, expired token, offline)
		// falls through to the catalog. It is an optional filter, never a gate.
	}
	if !known {
		return nil, fmt.Errorf("%w: no models.dev provider is known for the tool %q", ErrNoDiscovery, tool)
	}
	return l.cat.Provider(ctx, provider)
}

// enrich fills a live roster's metadata from the catalog, keeping the live
// order and the live membership. A catalog that is unavailable simply leaves
// the metadata empty — the ids are what selection needs.
func (l *cliLister) enrich(ctx context.Context, live Roster, provider string) Roster {
	if provider == "" {
		return live.Dedupe()
	}
	cat, err := l.cat.Provider(ctx, provider)
	if err != nil {
		return live.Dedupe()
	}
	out := make(Roster, 0, len(live))
	for _, m := range live {
		if c, ok := cat.Find(m.ID); ok {
			if m.Name == "" {
				m.Name = c.Name
			}
			if m.Context == 0 {
				m.Context = c.Context
			}
			if m.MaxOutput == 0 {
				m.MaxOutput = c.MaxOutput
			}
			m.InputUSD, m.OutputUSD = c.InputUSD, c.OutputUSD
			if m.Released == "" {
				m.Released = c.Released
			}
		}
		m.Provider = provider
		out = append(out, m)
	}
	return out.Dedupe()
}

// ---------------------------------------------------------------------------
// Live provider probes
// ---------------------------------------------------------------------------

// liveProbe asks one vendor's API what THIS ACCOUNT may run.
type liveProbe func(ctx context.Context, h HTTPDoer) (Roster, error)

// liveProbes holds a probe per tool. Only tools with a working live path get
// one — see the file header for why codex and gemini do not.
var liveProbes = map[string]liveProbe{
	"claude":      claudeLiveModels,
	"claude-code": claudeLiveModels,
}

// anthropicModelsURL is the live model list.
const anthropicModelsURL = "https://api.anthropic.com/v1/models?limit=200"

// claudeLiveModels lists what the claude CLI's own account is entitled to.
// The stored OAuth token is read, turned straight into a header, and dropped;
// it never reaches a log, an error, or the cache.
func claudeLiveModels(ctx context.Context, h HTTPDoer) (Roster, error) {
	tok, err := claudeOAuthToken()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, anthropicModelsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("authorization", "Bearer "+tok)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("accept", "application/json")

	resp, err := h.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic /v1/models (credential %s): %w", redactToken(tok), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The status, never the body: an auth error can echo the request.
		return nil, fmt.Errorf("anthropic /v1/models (credential %s): %s", redactToken(tok), resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var doc struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			CreatedAt   string `json:"created_at"`
			MaxInput    int    `json:"max_input_tokens"`
			MaxTokens   int    `json:"max_tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse anthropic /v1/models: %w", err)
	}
	out := make(Roster, 0, len(doc.Data))
	for _, m := range doc.Data {
		if m.ID == "" {
			continue
		}
		out = append(out, Model{
			ID: m.ID, Name: m.DisplayName, Provider: "anthropic",
			Context: m.MaxInput, MaxOutput: m.MaxTokens, Released: m.CreatedAt,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("anthropic /v1/models returned no models")
	}
	return out, nil
}

func defaultHTTP() HTTPDoer {
	return &http.Client{Timeout: liveProbeTimeout}
}
