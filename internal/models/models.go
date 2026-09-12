// Package models is conductor's model-discovery layer: it answers "what can
// this runtime actually run?" per runtime, and nothing more.
//
// Discovery is NOT one mechanism (docs/design/runtimes-models-packs.md §3).
// It is a per-runtime adapter capability. paseo knows its own providers and is
// asked directly; agent-deck's configured tools are resolved through the
// public models.dev catalog; a bare CLI tries the provider's own API with the
// tool's stored credentials first (account-scoped truth) and falls back to the
// catalog. A runtime that cannot enumerate contributes nothing to fleets and
// wildcards, and simply runs its own built-in default — a BARE LAUNCH, which
// is a first-class outcome, not an error.
//
// Discovery is deliberately never funnelled through one runtime: conductor is
// a platform, not a paseo wrapper. A new runtime implements List() its own way
// or leaves it unimplemented.
//
// Nothing here is on a hot path. The catalog is cached in the state dir with a
// TTL and every failure degrades to the last good copy, then to "cannot
// enumerate".
package models

import (
	"context"
	"errors"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// Model is one entry in a runtime's roster. Everything past ID is best-effort
// metadata: the live provider APIs publish far less than the catalog does, and
// a roster is useful with ID alone.
type Model struct {
	// ID is the identifier a dispatch passes as --model.
	ID string
	// Name is the human label ("Claude Opus 5"), when a source publishes one.
	Name string
	// Provider is the catalog provider this model belongs to ("anthropic",
	// "openai", "google") — empty when the source does not say.
	Provider string
	// Context and MaxOutput are the token limits, 0 when unknown.
	Context   int
	MaxOutput int
	// InputUSD/OutputUSD are $ per 1M tokens, 0 when unknown. They feed the
	// cost layer's estimates; they are never a selection criterion here.
	InputUSD  float64
	OutputUSD float64
	// Released is the release date as the source reports it (RFC3339 or
	// YYYY-MM-DD), used to order a roster newest-first. Empty sorts last.
	Released string
}

// Roster is an ordered model list, newest/best first as its source reports it.
// Order is load-bearing: a wildcard in a fleet's `any:` expands in roster
// order (docs/design/runtimes-models-packs.md §2.1).
type Roster []Model

// IDs returns the model ids in roster order — what the config layer globs
// patterns against.
func (r Roster) IDs() []string {
	out := make([]string, 0, len(r))
	for _, m := range r {
		out = append(out, m.ID)
	}
	return out
}

// Find returns the entry for an id (and whether it was there).
func (r Roster) Find(id string) (Model, bool) {
	for _, m := range r {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

// SortNewestFirst orders by release date descending, ties broken by id so the
// result is deterministic. Entries with no date sort last — a source that does
// not publish dates keeps whatever order it gave us, which for a live API is
// already the provider's own ranking.
func (r Roster) SortNewestFirst() Roster {
	out := append(Roster(nil), r...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Released, out[j].Released
		switch {
		case a == b:
			return out[i].ID < out[j].ID
		case a == "":
			return false
		case b == "":
			return true
		}
		return a > b
	})
	return out
}

// Dedupe drops repeated ids, keeping the first (richest, highest-priority)
// occurrence — used when several sources union into one roster.
func (r Roster) Dedupe() Roster {
	seen := make(map[string]bool, len(r))
	out := make(Roster, 0, len(r))
	for _, m := range r {
		if seen[m.ID] || m.ID == "" {
			continue
		}
		seen[m.ID] = true
		out = append(out, m)
	}
	return out
}

// ErrNoDiscovery is what an adapter returns when it cannot enumerate at all —
// no credentials, no catalog entry, no CLI. It is NOT a failure: the caller
// falls back to bare launch. Distinguish it from a real error (a malformed
// response, a broken cache write), which is worth surfacing.
var ErrNoDiscovery = errors.New("models: this runtime cannot enumerate its models")

// Runtime is the slice of a `runtimes:` entry discovery needs. It is a plain
// struct rather than config.RuntimeConfig so this package stays independent of
// the config schema (and trivially table-testable).
type Runtime struct {
	// Name is the `runtimes:` map key.
	Name string
	// Impl is the resolved `use:` leaf — paseo | agent-deck | cli | acp |
	// opencode, or a plugin's name.
	Impl string
	// Bin overrides the runtime binary (a paseo/agent-deck `bin:`).
	Bin string
	// Agent is the ACP-driven agent (`use: acp`, agent: gemini).
	Agent string
	// Tool is the bare-CLI recipe's tool name (`use: cli`, tool: claude).
	Tool string
	// Command is the bare-CLI recipe's argv; Command[0] stands in for Tool
	// when Tool is unset.
	Command []string
}

// ToolName is the concrete CLI this runtime drives: an ACP agent, an explicit
// `tool:`, or the head of a `command:` recipe. Empty when the runtime is not
// tool-shaped (paseo, a plugin).
func (r Runtime) ToolName() string {
	switch {
	case r.Tool != "":
		return r.Tool
	case r.Agent != "":
		return r.Agent
	case len(r.Command) > 0:
		return baseName(r.Command[0])
	}
	return ""
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// Lister is the per-runtime discovery capability — the whole adapter contract.
// An implementation reports what its runtime can run, or ErrNoDiscovery.
type Lister interface {
	List(ctx context.Context) (Roster, error)
}

// Factory builds a Lister for one runtime. The catalog is shared across every
// adapter in a process so the 4MB models.dev document is parsed at most once.
type Factory func(rt Runtime, cat *Catalog) Lister

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register installs the discovery adapter for a runtime implementation. Called
// from this package's init functions; a runtime plugin that wants discovery
// registers its own.
func Register(impl string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[impl] = f
}

// ListerFor returns the adapter for a runtime, or false when the
// implementation has none (which means bare launch, not an error).
func ListerFor(rt Runtime, cat *Catalog) (Lister, bool) {
	registryMu.RLock()
	f, ok := registry[rt.Impl]
	registryMu.RUnlock()
	if !ok {
		return nil, false
	}
	return f(rt, cat), true
}

// Implementations lists the runtime implementations that can enumerate,
// sorted — for `conductor validate` output and diagnostics.
func Implementations() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Process seam
// ---------------------------------------------------------------------------

// Runner executes one discovery subcommand and returns its stdout. It is a
// seam so the adapters are table-testable without shelling out; unit tests
// never exec anything.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// execRunner is the default Runner: run the binary, capture stdout, bound the
// wait. Discovery is off the hot path but must never wedge a boot.
func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

// execTimeout bounds one discovery subprocess.
const execTimeout = 20 * time.Second

// binOr returns the configured binary or the default name.
func binOr(bin, def string) string {
	if strings.TrimSpace(bin) != "" {
		return bin
	}
	return def
}
