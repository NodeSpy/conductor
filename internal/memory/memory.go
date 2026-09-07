// Package memory is the durable memory agents share across runs — what one
// agent learns is available to the next. Entries are structured, with
// provenance (which agent/run/trigger/repo wrote it) and a scope (global /
// per-repo / per-agent). Three backends hold the entries — a `stores:` KV
// entry, a directory of Markdown-with-frontmatter files, or an in-process
// map — behind one Backend contract; the verbs, prompt injection, code
// bindings, and recall are identical across them.
//
// Recall (v1) is tags + scope + recency: filter by tags/scope/substring,
// newest first, limited — pure Go and deterministic. Ranking can layer on
// later without changing this surface.
package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Source is an entry's provenance: who learned it and from where.
type Source struct {
	Agent   string `json:"agent,omitempty" yaml:"agent,omitempty"`
	Run     string `json:"run,omitempty" yaml:"run,omitempty"`
	Trigger string `json:"trigger,omitempty" yaml:"trigger,omitempty"`
	Repo    string `json:"repo,omitempty" yaml:"repo,omitempty"`
}

// Entry is one memory.
type Entry struct {
	ID      string    `json:"id"`
	Text    string    `json:"text"`
	Tags    []string  `json:"tags,omitempty"`
	Scope   string    `json:"scope"`
	Source  Source    `json:"source,omitempty"`
	Created time.Time `json:"created"`
}

// Map returns the entry as a JSON-shaped map (verb outputs, code bindings).
func (e Entry) Map() map[string]any {
	b, _ := json.Marshal(e)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// Backend is the storage contract all three implementations satisfy. Put
// upserts by ID; Delete reports whether the ID existed; List returns every
// entry in no particular order (the Manager sorts).
type Backend interface {
	Put(e Entry) error
	Delete(id string) (bool, error)
	List() ([]Entry, error)
	Close() error
}

// Query is one recall: every set field narrows the result.
type Query struct {
	// Tags an entry must ALL carry.
	Tags []string
	// Scopes the entry's scope must be one of (canonical form); empty = all.
	Scopes []string
	// Substring is a case-insensitive text filter.
	Substring string
	// Limit caps the result (newest first); 0 = no cap.
	Limit int
}

// Manager binds one backend to the memory surface. The clock and ID minting
// are injectable for tests.
type Manager struct {
	backend Backend
	now     func() time.Time
	newID   func() string
	// guard vets the agent-facing write paths (the harvest output contract
	// and the memory IPC tool) before anything persists — wired by main to
	// refuse tracked secret material. The verb/code write paths carry the
	// plan write barrier separately. Atomic: set once at boot, read from
	// engine and flow goroutines.
	guard    atomic.Pointer[WriteGuard]
	redactor atomic.Pointer[Redactor]
}

// WriteGuard vets one to-be-remembered text; a non-nil error refuses it.
type WriteGuard func(text string) error

// Redactor scrubs tracked secret values from text served back to agents.
type Redactor func(string) string

// SetRedactor installs the read-side redactor: memories written before the
// write guard existed (or through a trusted path) may carry secrets, and
// recalled/injected content reaches plaintext agent prompts.
func (m *Manager) SetRedactor(r Redactor) {
	if r == nil {
		return
	}
	m.redactor.Store(&r)
}

// redactText applies the read-side redactor (nil = passthrough).
func (m *Manager) redactText(s string) string {
	if rp := m.redactor.Load(); rp != nil {
		return (*rp)(s)
	}
	return s
}

// SetWriteGuard installs the agent-facing write guard.
func (m *Manager) SetWriteGuard(g WriteGuard) {
	if g == nil {
		return
	}
	m.guard.Store(&g)
}

// checkGuard applies the write guard (nil = allowed).
func (m *Manager) checkGuard(text string) error {
	if gp := m.guard.Load(); gp != nil {
		return (*gp)(text)
	}
	return nil
}

// NewManager wraps a backend.
func NewManager(b Backend) *Manager {
	return &Manager{backend: b, now: time.Now, newID: mintID}
}

func mintID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "m" + hex.EncodeToString(b[:])
}

// SetClock overrides the clock and ID mint (tests).
func (m *Manager) SetClock(now func() time.Time, newID func() string) {
	if now != nil {
		m.now = now
	}
	if newID != nil {
		m.newID = newID
	}
}

// Remember persists one memory: the text, normalized tags, the scope resolved
// against the source (see ResolveScope), and provenance.
func (m *Manager) Remember(text string, tags []string, scope string, src Source) (Entry, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Entry{}, fmt.Errorf("memory: text is required")
	}
	resolved, err := ResolveScope(scope, src)
	if err != nil {
		return Entry{}, err
	}
	e := Entry{
		ID:      m.newID(),
		Text:    text,
		Tags:    normTags(tags),
		Scope:   resolved,
		Source:  src,
		Created: m.now().UTC(),
	}
	if err := m.backend.Put(e); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// Recall filters, orders (newest first, then ID), and limits.
func (m *Manager) Recall(q Query) ([]Entry, error) {
	all, err := m.backend.List()
	if err != nil {
		return nil, err
	}
	scopes := map[string]bool{}
	for _, s := range q.Scopes {
		if s != "" {
			scopes[s] = true
		}
	}
	sub := strings.ToLower(strings.TrimSpace(q.Substring))
	var out []Entry
	for _, e := range all {
		if len(scopes) > 0 && !scopes[e.Scope] {
			continue
		}
		if !hasAllTags(e.Tags, q.Tags) {
			continue
		}
		if sub != "" && !strings.Contains(strings.ToLower(e.Text), sub) {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.After(out[j].Created)
		}
		return out[i].ID < out[j].ID
	})
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// Forget removes one memory by ID; found reports whether it existed.
func (m *Manager) Forget(id string) (bool, error) {
	if strings.TrimSpace(id) == "" {
		return false, fmt.Errorf("memory: id is required")
	}
	return m.backend.Delete(id)
}

// List returns every memory, newest first.
func (m *Manager) List() ([]Entry, error) { return m.Recall(Query{}) }

// Close releases the backend.
func (m *Manager) Close() error { return m.backend.Close() }

// ResolveScope canonicalizes a scope against an entry's source: "" or
// "global" → global; "repo" → repo:<source repo>; "agent" → agent:<source
// agent>; an explicit "repo:<name>"/"agent:<name>" passes through.
func ResolveScope(scope string, src Source) (string, error) {
	switch s := strings.TrimSpace(scope); {
	case s == "" || s == "global":
		return "global", nil
	case s == "repo":
		if src.Repo == "" {
			return "", fmt.Errorf("memory: scope \"repo\" needs a repo in the run context — use an explicit repo:<owner/repo>")
		}
		return "repo:" + src.Repo, nil
	case s == "agent":
		if src.Agent == "" {
			return "", fmt.Errorf("memory: scope \"agent\" needs an agent in the run context — use an explicit agent:<name>")
		}
		return "agent:" + src.Agent, nil
	case strings.HasPrefix(s, "repo:") && len(s) > len("repo:"):
		return s, nil
	case strings.HasPrefix(s, "agent:") && len(s) > len("agent:"):
		return s, nil
	default:
		return "", fmt.Errorf("memory: unknown scope %q (global, repo, agent, repo:<owner/repo>, agent:<name>)", scope)
	}
}

func normTags(tags []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func hasAllTags(have, want []string) bool {
	if len(want) == 0 {
		return true
	}
	set := map[string]bool{}
	for _, t := range have {
		set[t] = true
	}
	for _, t := range want {
		if t = strings.TrimSpace(t); t != "" && !set[t] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// The active manager. Like the kv store registry, the configured memory is a
// process-wide singleton: the daemon configures it at boot from the `memory:`
// section, and the verbs / bindings / template funcs / prompt injection all
// read it here. Nil means memory is not configured — every surface then
// reports that plainly instead of inventing a default.
// ---------------------------------------------------------------------------

var (
	regMu   sync.RWMutex
	active  *Manager
	toolCmd []string
)

// Configure installs the manager built from the memory: section (closing any
// previous one — config reload, tests).
func Configure(m *Manager) {
	regMu.Lock()
	prev := active
	active = m
	regMu.Unlock()
	if prev != nil && prev != m {
		_ = prev.Close()
	}
}

// Active returns the configured manager, or nil when there is no memory:
// section.
func Active() *Manager {
	regMu.RLock()
	defer regMu.RUnlock()
	return active
}

// Reset clears the manager, tool command, and live ops (tests, shutdown).
func Reset() {
	regMu.Lock()
	prev := active
	active = nil
	toolCmd = nil
	regMu.Unlock()
	SetLiveOps(LiveOps{})
	if prev != nil {
		_ = prev.Close()
	}
}

// SetToolCommand records the argv prefix that launches the live memory tool
// (the `conductor mcp memory --socket <path>` subprocess). Runtimes that can
// attach MCP servers to an agent session (ACP) read it via ToolCommand and
// append per-dispatch provenance flags.
func SetToolCommand(argv []string) {
	regMu.Lock()
	toolCmd = append([]string(nil), argv...)
	regMu.Unlock()
}

// ToolCommand returns the live-tool launch argv, or nil when memory (or its
// socket) is not configured.
func ToolCommand() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	return append([]string(nil), toolCmd...)
}

// ---------------------------------------------------------------------------
// Run-context provenance. The flow runner stamps the trigger's facts on the
// context; the memory verbs read them back so a `uses: memory.remember` step
// records where the memory came from without any step-level plumbing.
// ---------------------------------------------------------------------------

type sourceKey struct{}

// WithSource stamps provenance on a context.
func WithSource(ctx context.Context, src Source) context.Context {
	return context.WithValue(ctx, sourceKey{}, src)
}

// SourceFrom reads the stamped provenance ({} when absent).
func SourceFrom(ctx context.Context) Source {
	src, _ := ctx.Value(sourceKey{}).(Source)
	return src
}
