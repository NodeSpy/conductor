// Package attention keeps the set of things that need an operator, and records
// every change to it on the journal's "attention" topic (design:
// event-journal).
//
// Items come from two kinds of producer. Sources report what is open right
// now (pending approvals, say); the service reconciles them on an interval and
// on a nudge, so the source stays the truth and a restart or a missed nudge
// can't leave an item stuck. Raised items exist only as an event (a run
// failed); they stay open until a producer resolves them or the operator
// dismisses them.
//
// The open set is persisted beside the journal, so it survives a restart. The
// service knows nothing about what its items are about: kinds, titles, links
// and subjects are the producers'.
package attention

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/journal"
)

// Topic is the journal topic every transition is recorded on.
const Topic = "attention"

// Transition types on the attention topic.
const (
	Opened   = "opened"
	Updated  = "updated" // an open item's title, detail or link changed
	Resolved = "resolved"
)

// Severities.
const (
	Info   = "info"
	Warn   = "warn"
	Urgent = "urgent"
)

// Item is one thing that needs an operator.
type Item struct {
	// Key identifies the item; a producer that raises the same key again
	// updates it rather than opening a second one.
	Key   string `json:"key"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	// Detail is a one-line elaboration; Link is where to deal with it (a
	// dashboard route such as "#/runs/<id>").
	Detail string `json:"detail,omitempty"`
	Link   string `json:"link,omitempty"`
	// Subject groups items a later event may resolve together
	// (ResolveSubject).
	Subject  string    `json:"subject,omitempty"`
	Severity string    `json:"severity"`
	Opened   time.Time `json:"opened"`
	// Source is the source that reports the item; empty for a raised item.
	// Only raised items can be dismissed.
	Source string `json:"source,omitempty"`
}

// Source reports the items of one kind that are open right now. A List error
// leaves that source's items as they were.
type Source struct {
	Name string
	List func(ctx context.Context) ([]Item, error)
}

// Options configure a Service.
type Options struct {
	Journal *journal.Journal
	// StatePath is where the open set is persisted (JSON). Empty: memory only.
	StatePath string
	// Interval between source reconciles (default 10s).
	Interval time.Duration
	Now      func() time.Time
	Log      func(string, ...any)
}

// Service is the attention view.
type Service struct {
	opts    Options
	mu      sync.Mutex
	open    map[string]Item
	sources []Source
	nudge   chan struct{}
}

// ErrNotDismissable is returned when dismissing an item a source reports: it
// closes when its source says so (the approval is decided, say).
var ErrNotDismissable = errors.New("attention: this item closes when it is dealt with, not by dismissing it")

// ErrUnknown is returned for a key that isn't open.
var ErrUnknown = errors.New("attention: no such open item")

// New builds a Service and loads the persisted open set.
func New(opts Options) *Service {
	if opts.Interval <= 0 {
		opts.Interval = 10 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = func(string, ...any) {}
	}
	s := &Service{opts: opts, open: map[string]Item{}, nudge: make(chan struct{}, 1)}
	if opts.StatePath != "" {
		if b, err := os.ReadFile(opts.StatePath); err == nil {
			var items []Item
			if json.Unmarshal(b, &items) == nil {
				for _, it := range items {
					s.open[it.Key] = it
				}
			}
		}
	}
	return s
}

// AddSource registers a source. Call before Run.
func (s *Service) AddSource(src Source) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sources = append(s.sources, src)
}

// Run reconciles every source now, then on each interval and nudge, until ctx
// ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(s.opts.Interval)
	defer t.Stop()
	for {
		s.Reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.nudge:
		}
	}
}

// Nudge asks for a reconcile soon (a producer just changed what a source
// reports). Never blocks.
func (s *Service) Nudge() {
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

// Reconcile asks every source what is open and records the difference.
func (s *Service) Reconcile(ctx context.Context) {
	s.mu.Lock()
	srcs := append([]Source(nil), s.sources...)
	s.mu.Unlock()
	for _, src := range srcs {
		items, err := src.List(ctx)
		if err != nil {
			s.opts.Log("attention: source %s: %v", src.Name, err)
			continue
		}
		s.apply(src.Name, items)
	}
}

// apply makes the open items of one source exactly items.
func (s *Service) apply(source string, items []Item) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	dirty := false
	for _, it := range items {
		it.Source = source
		seen[it.Key] = true
		if s.put(it) {
			dirty = true
		}
	}
	for _, k := range s.sortedKeys() {
		if it := s.open[k]; it.Source == source && !seen[k] {
			s.drop(it)
			dirty = true
		}
	}
	if dirty {
		s.persist()
	}
}

// Raise opens (or updates) an item that exists only as an event.
func (s *Service) Raise(it Item) {
	it.Source = ""
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.put(it) {
		s.persist()
	}
}

// Resolve closes one raised item. Unknown keys are ignored.
func (s *Service) Resolve(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if it, ok := s.open[key]; ok && it.Source == "" {
		s.drop(it)
		s.persist()
	}
}

// ResolveSubject closes every raised item with the given subject.
func (s *Service) ResolveSubject(subject string) {
	if subject == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dirty := false
	for _, k := range s.sortedKeys() {
		if it := s.open[k]; it.Source == "" && it.Subject == subject {
			s.drop(it)
			dirty = true
		}
	}
	if dirty {
		s.persist()
	}
}

// Dismiss closes a raised item at the operator's request.
func (s *Service) Dismiss(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.open[key]
	if !ok {
		return ErrUnknown
	}
	if it.Source != "" {
		return ErrNotDismissable
	}
	s.drop(it)
	s.persist()
	return nil
}

// Snapshot returns the open items, oldest first, and the journal cursor they
// reflect: following the attention topic from that cursor misses no
// transition.
func (s *Service) Snapshot() ([]Item, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Item, 0, len(s.open))
	for _, it := range s.open {
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Opened.Equal(out[j].Opened) {
			return out[i].Opened.Before(out[j].Opened)
		}
		return out[i].Key < out[j].Key
	})
	cur := ""
	if s.opts.Journal != nil {
		cur = s.opts.Journal.Cursor()
	}
	return out, cur
}

// put opens or updates an item and records the transition; it reports whether
// anything changed. Called with mu held, so every attention event is appended
// under mu and Snapshot's cursor matches its items.
func (s *Service) put(it Item) bool {
	if it.Severity == "" {
		it.Severity = Info
	}
	prev, ok := s.open[it.Key]
	if ok {
		it.Opened = prev.Opened
		if prev == it {
			return false
		}
		s.open[it.Key] = it
		s.record(Updated, it)
		return true
	}
	if it.Opened.IsZero() {
		it.Opened = s.opts.Now().UTC()
	}
	s.open[it.Key] = it
	s.record(Opened, it)
	return true
}

func (s *Service) drop(it Item) {
	delete(s.open, it.Key)
	s.record(Resolved, it)
}

func (s *Service) record(typ string, it Item) {
	if s.opts.Journal == nil {
		return
	}
	if _, err := s.opts.Journal.Append(Topic, typ, it.Key, it); err != nil {
		s.opts.Log("attention: journal %s %s: %v", typ, it.Key, err)
	}
}

func (s *Service) sortedKeys() []string {
	ks := make([]string, 0, len(s.open))
	for k := range s.open {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// persist writes the open set. Called with mu held.
func (s *Service) persist() {
	if s.opts.StatePath == "" {
		return
	}
	items := make([]Item, 0, len(s.open))
	for _, k := range s.sortedKeys() {
		items = append(items, s.open[k])
	}
	b, err := json.Marshal(items)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.opts.StatePath), 0o700); err != nil {
		s.opts.Log("attention: persist: %v", err)
		return
	}
	tmp := s.opts.StatePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		s.opts.Log("attention: persist: %v", err)
		return
	}
	if err := os.Rename(tmp, s.opts.StatePath); err != nil {
		s.opts.Log("attention: persist: %v", err)
	}
}
