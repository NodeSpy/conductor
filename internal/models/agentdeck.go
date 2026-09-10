package models

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// agent-deck discovery (docs/design/runtimes-models-packs.md §3.1).
//
// agent-deck has no model registry of its own: it is a session manager that
// drives OTHER tools (claude, codex, gemini, opencode) and its `profiles:` are
// ACCOUNT SLOTS for those tools, not providers. So discovery resolves which
// tools this installation is configured for — the default tool, every
// per-tool section, and every profile's per-tool override — maps each onto its
// models.dev provider, and unions the catalog rosters.
//
// The account behind a profile narrows ENTITLEMENT, not the catalog, so it
// does not change the roster; the live-API filter in cli.go is where an
// account-scoped narrowing would belong if agent-deck ever exposed which
// credential a profile resolves to.
//
// If the config cannot be read at all, the default tool alone is assumed —
// agent-deck ships with `default_tool = "claude"` — and failing that the
// runtime simply cannot enumerate (bare launch).

// liveProbeTimeout bounds one live provider call.
const liveProbeTimeout = 20 * time.Second

func init() {
	Register("agent-deck", func(rt Runtime, cat *Catalog) Lister {
		return &deckLister{cat: cat, configPath: deckConfigPath}
	})
}

type deckLister struct {
	cat        *Catalog
	configPath func() (string, error)
}

// deckConfigPath is agent-deck's config file. AGENT_DECK_HOME overrides the
// default location, matching agent-deck's own resolution.
func deckConfigPath() (string, error) {
	if h := strings.TrimSpace(os.Getenv("AGENT_DECK_HOME")); h != "" {
		return filepath.Join(h, "config.toml"), nil
	}
	h, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".agent-deck", "config.toml"), nil
}

func (l *deckLister) List(ctx context.Context) (Roster, error) {
	tools := l.tools()
	if len(tools) == 0 {
		return nil, fmt.Errorf("%w: agent-deck names no tool to enumerate", ErrNoDiscovery)
	}
	var roster Roster
	var lastErr error
	for _, tool := range tools {
		provider, ok := toolProvider[tool]
		if !ok {
			continue
		}
		r, err := l.cat.Provider(ctx, provider)
		if err != nil {
			lastErr = err
			continue
		}
		roster = append(roster, r...)
	}
	if len(roster) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("%w: no models.dev provider matched agent-deck's tools (%s)", ErrNoDiscovery, strings.Join(tools, ", "))
	}
	return roster.Dedupe(), nil
}

// tools returns the configured tools, default first then the rest sorted so
// the roster order is stable across runs.
func (l *deckLister) tools() []string {
	path, err := l.configPath()
	if err != nil {
		return []string{"claude"}
	}
	def, others := scanDeckTools(path)
	if def == "" && len(others) == 0 {
		return []string{"claude"} // agent-deck's own shipped default
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(others)+1)
	if def != "" {
		seen[def] = true
		out = append(out, def)
	}
	rest := make([]string, 0, len(others))
	for t := range others {
		if !seen[t] {
			rest = append(rest, t)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// scanDeckTools reads agent-deck's config.toml for `default_tool` and every
// tool a section names.
//
// This is a deliberately narrow scanner, not a TOML parser: the only things
// discovery needs are the top-level `default_tool = "…"` assignment and the
// tool component of section headers —
//
//	[claude]                        -> claude
//	[profiles.tapresearch.claude]   -> claude
//	[groups.Ednition.codex]         -> codex
//
// Anything else in the file is ignored. Pulling in a TOML dependency to read
// two facts out of another tool's config would be a poor trade, and a
// mis-scanned file degrades to the default tool rather than failing.
func scanDeckTools(path string) (defaultTool string, tools map[string]bool) {
	tools = map[string]bool{}
	f, err := os.Open(path)
	if err != nil {
		return "", tools
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if end := strings.IndexByte(line, ']'); end > 1 {
				if tool := deckSectionTool(line[1:end]); tool != "" {
					tools[tool] = true
				}
			}
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "default_tool" {
			defaultTool = strings.ToLower(strings.Trim(strings.TrimSpace(v), `"'`))
		}
	}
	if defaultTool != "" {
		tools[defaultTool] = true
	}
	return defaultTool, tools
}

// deckSectionTool extracts the tool a section header names: the whole header
// for a top-level `[claude]`, or the last component of a
// `[profiles.<name>.<tool>]` / `[groups.<name>.<tool>]` header. A header that
// names no known tool contributes nothing.
func deckSectionTool(header string) string {
	header = strings.TrimSpace(strings.Trim(header, "[]"))
	if header == "" {
		return ""
	}
	parts := strings.Split(header, ".")
	cand := strings.ToLower(strings.Trim(strings.TrimSpace(parts[len(parts)-1]), `"'`))
	if _, ok := toolProvider[cand]; ok {
		return cand
	}
	return ""
}
