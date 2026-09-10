// Package memory is the durable memory agents share across runs — what one
// run learns is available to the next. Entries are structured, with
// provenance (which step/run/trigger/repo wrote it) and a SCOPE KEY.
//
// A scope is either GLOBAL — no key, the shared set everything can see — or
// an arbitrary OPAQUE STRING this package never interprets
// (docs/design/agents-removal.md §2). There are no privileged scope TYPES:
// `repo:` would bake a GitHub concept into the memory core and `agent:` would
// bake in an identity that no longer exists. The engine supplies concrete
// keys from run context as a CONVENTION — the repo string, the workflow name,
// the step identity — and they are just keys.
//
// Three backends hold the entries — a `stores:` KV entry, a directory of
// Markdown-with-frontmatter files, or an in-process map — behind one Backend
// contract; the verbs, prompt injection, code bindings, and recall are
// identical across them.
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

// Source is an entry's provenance: what learned it and from where. Step is
// the step identity that wrote it (docs/design/agents-removal.md §5); the
// JSON/YAML tag stays "agent" so entries written by earlier versions keep
// their attribution rather than silently losing it on the first read.
type Source struct {
	Step    string `json:"agent,omitempty" yaml:"agent,omitempty"`
	Run     string `json:"run,omitempty" yaml:"run,omitempty"`
	Trigger string `json:"trigger,omitempty" yaml:"trigger,omitempty"`
	Repo    string `json:"repo,omitempty" yaml:"repo,omitempty"`
}

// Entry is one memory. Scope is the opaque key it was filed under, or
// GlobalScope for the shared set.
type Entry struct {
	ID      string    `json:"id"`
	Text    string    `json:"text"`
	Tags    []string  `json:"tags,omitempty"`
	Scope   string    `json:"scope"`
	Source  Source    `json:"source,omitempty"`
	Created time.Time `json:"created"`
}

// GlobalScope is how the no-key (shared) set is spelled on disk. It is a
// STORAGE detail, not a keyword: callers pass "" for global, and Recall
// treats "" and GlobalScope alike. Persisting a concrete token keeps every
// backend's on-disk shape unchanged and keeps entries written by earlier
// versions readable.
const GlobalScope = "global"

// NormalizeScope canonicalizes a scope key: empty (or the global token) is
// the shared set; anything else is passed through verbatim, trimmed. It never
// rejects a key — the memory core does not interpret keys, so there is
// nothing to be invalid.
func NormalizeScope(scope string) string {
	s := strings.TrimSpace(scope)
	if s == "" {
		return GlobalScope
	}
	return s
}

// ErrReservedScope rejects an agent-supplied scope key that would land in
// the shared bucket.
var ErrReservedScope = fmt.Errorf("memory: %q is a reserved scope — it is the SHARED set, injected into every agent's prompt on this daemon. Name the thing you mean (a repo, a service, a team) so the note reaches the agents it is for", GlobalScope)

// CheckAgentScope guards the scope key on an AGENT-supplied write.
//
// "" and "global" both normalize to the shared set, which is injected into
// every opted-in agent's prompt regardless of repo or tenant. That is
// correct for config- and engine-authored context, and wrong for anything
// an agent chose: on a shared daemon it turns one repo's note into every
// repo's context, which is a cross-tenant leak dressed up as a feature.
//
// Empty stays allowed — it means "the caller did not scope this", and the
// callers here supply their own default. Only the explicit reserved token
// is refused, so an agent has to name what it means.
func CheckAgentScope(scope string) error {
	if strings.EqualFold(strings.TrimSpace(scope), GlobalScope) {
		return ErrReservedScope
	}
	return nil
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
	// Scopes the entry's scope key must be one of; empty = every scope. An
	// entry in the global set matches the empty key or GlobalScope.
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
	guard      atomic.Pointer[WriteGuard]
	redactor   atomic.Pointer[Redactor]
	scopeGuard atomic.Pointer[ScopeGuard]
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

// Remember persists one memory: the text, normalized tags, the opaque scope
// key ("" = global), and provenance.
func (m *Manager) Remember(text string, tags []string, scope string, src Source) (Entry, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Entry{}, fmt.Errorf("memory: text is required")
	}
	resolved := NormalizeScope(scope)
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
		if s = strings.TrimSpace(s); s != "" {
			scopes[NormalizeScope(s)] = true
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
	// Redact HERE, not at the call sites. Memories written before the write
	// guard existed (or by a conductor-authored path the guard does not
	// cover) can hold tracked secret values, and every read face — the
	// prompt injection, the IPC recall, the code binding, the connector verb
	// — had to remember to scrub them. The prompt and IPC paths did; the
	// code binding and the connector's recall/list verbs did not. Doing it
	// inside the one function they all funnel through means a new read face
	// cannot forget.
	for i := range out {
		out[i].Text = m.redactText(out[i].Text)
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
