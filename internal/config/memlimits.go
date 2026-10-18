package config

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// MEMORY LIMITS (docs/wiki/Bots.md#memory-limits): what a bot's sessions
// read from and write to a memory or knowledge repo, bounded so a session's
// context stays small however large the repo grows. Every limit is ON by
// default; a bigger value loosens one. A bot's sit under bots.<b>.memory, a
// team knowledge repo's beside the rest of teams.<t>.knowledge:
//
//	bots:
//	  platform-sre:
//	    memory:
//	      index_budget: 6k        # the memory index in a session's first prompt
//	      read_max: 12k           # one `conductor bot read`
//	      recall_limit: 8         # documents one `conductor bot recall` returns
//	      max_bytes: { knowledge: 16k, handoff: 6k }
type MemoryLimits struct {
	// IndexBudget caps the memory index a session's prompt carries (bytes).
	IndexBudget ByteSize `yaml:"index_budget,omitempty"`
	// ReadMax caps what one `conductor bot read` returns (bytes); past it the
	// output is truncated, says so, and names the sections and the offset to
	// read on from.
	ReadMax ByteSize `yaml:"read_max,omitempty"`
	// RecallLimit is how many documents a recall returns by default (a
	// session may ask for up to MaxRecallLimit).
	RecallLimit int `yaml:"recall_limit,omitempty"`
	// MaxBytes caps a document of a built-in kind, by kind name, plus
	// "handoff" (a session's handoff text) and "episode" (one `conductor bot
	// episode` entry). A collection sets its own (collections.<c>.max_bytes).
	MaxBytes map[string]ByteSize `yaml:"max_bytes,omitempty"`
	// Archive moves raw log entries out of the sessions' working copies once
	// a consolidation has summarized them (they stay in git history, and
	// recall still searches them). Default true; false keeps them in place.
	Archive *bool `yaml:"archive,omitempty"`
}

// ArchiveOn reports whether consolidated log entries are archived.
func (l MemoryLimits) ArchiveOn() bool { return l.Archive == nil || *l.Archive }

// Memory-limit defaults and ceilings.
const (
	DefaultIndexBudget     = 6000
	DefaultTeamIndexBudget = 2000
	DefaultReadMax         = 12000
	DefaultRecallLimit     = 8
	MaxRecallLimit         = 25
	// MaxDocBytes is the hard ceiling on any one memory write.
	MaxDocBytes = 256 << 10
	// MaxIndexBudget bounds index_budget: a prompt is not a document dump.
	MaxIndexBudget = 64 << 10
	// DefaultCollectionMaxBytes caps a collection's document unless it sets
	// max_bytes.
	DefaultCollectionMaxBytes = 8 << 10
)

// DefaultMaxBytes are the built-in caps: a document of each kind, a
// handoff note, one episode entry.
var DefaultMaxBytes = map[string]int{
	"knowledge": 8 << 10,
	"runbook":   12 << 10,
	"watch":     4 << 10,
	"digest":    8 << 10,
	"handoff":   4 << 10,
	"episode":   4 << 10,
}

// memLimitKeys are the max_bytes keys a bot's memory takes; a team's
// knowledge repo has only the shared kinds.
var (
	memLimitKeys  = []string{"knowledge", "runbook", "watch", "digest", "handoff", "episode"}
	teamLimitKeys = []string{"knowledge", "runbook", "watch"}
)

// IndexBudgetOr is the effective index budget (def when unset).
func (l MemoryLimits) IndexBudgetOr(def int) int {
	if l.IndexBudget > 0 {
		return int(l.IndexBudget)
	}
	return def
}

// ReadMaxOr is the effective per-read cap.
func (l MemoryLimits) ReadMaxOr() int {
	if l.ReadMax > 0 {
		return int(l.ReadMax)
	}
	return DefaultReadMax
}

// RecallLimitOr is the effective default recall size.
func (l MemoryLimits) RecallLimitOr() int {
	if l.RecallLimit > 0 {
		return l.RecallLimit
	}
	return DefaultRecallLimit
}

// MaxBytesOf is the effective cap for a built-in kind (or "handoff" /
// "episode"); 0 for a name with none.
func (l MemoryLimits) MaxBytesOf(name string) int {
	if v, ok := l.MaxBytes[name]; ok && v > 0 {
		return int(v)
	}
	return DefaultMaxBytes[name]
}

// validate checks a limits block: no negative value, nothing past its
// ceiling, only max_bytes keys the repo has.
func (l MemoryLimits) validate(team bool) error {
	if l.IndexBudget < 0 || l.IndexBudget > MaxIndexBudget {
		return fmt.Errorf("index_budget: want 1 to %d bytes, got %d", MaxIndexBudget, l.IndexBudget)
	}
	if l.ReadMax < 0 || l.ReadMax > MaxDocBytes {
		return fmt.Errorf("read_max: want 1 to %d bytes, got %d", MaxDocBytes, l.ReadMax)
	}
	if l.RecallLimit < 0 || l.RecallLimit > MaxRecallLimit {
		return fmt.Errorf("recall_limit: want 1 to %d, got %d", MaxRecallLimit, l.RecallLimit)
	}
	keys := memLimitKeys
	if team {
		keys = teamLimitKeys
		if l.Archive != nil {
			return fmt.Errorf("archive: a team's knowledge repo has no log to archive")
		}
	}
	names := make([]string, 0, len(l.MaxBytes))
	for k := range l.MaxBytes {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		ok := false
		for _, want := range keys {
			ok = ok || k == want
		}
		if !ok {
			return fmt.Errorf("max_bytes.%s: not a kind here (want %s; a collection sets its own max_bytes)", k, strings.Join(keys, ", "))
		}
		if v := l.MaxBytes[k]; v <= 0 || v > MaxDocBytes {
			return fmt.Errorf("max_bytes.%s: want 1 to %d bytes, got %d", k, MaxDocBytes, v)
		}
	}
	return nil
}

// BotIndexPath is where a bot's recall index lives: conductor's state dir,
// never inside the memory repo (sessions cannot read it, and it is rebuilt
// from the repo whenever it is missing or stale).
func (c *Config) BotIndexPath(name string) string {
	return filepath.Join(StateDir(), "bots", name, "recall.db")
}

// TeamIndexPath is a team knowledge repo's recall index.
func (c *Config) TeamIndexPath(team string) string {
	return filepath.Join(StateDir(), "teams", team, "recall.db")
}

// TeamLimits is a team knowledge repo's limits (zero without knowledge:).
func (c *Config) TeamLimits(team string) MemoryLimits {
	if k := c.Teams[team].Knowledge; k != nil {
		return k.MemoryLimits
	}
	return MemoryLimits{}
}
