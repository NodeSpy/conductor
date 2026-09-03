// Package rss polls RSS/Atom feeds and turns new items into triggers — watch an
// upstream changelog (Canvas API, LTI spec, a key dependency's releases) and have
// an agent assess "does this affect us?" It uses only the stdlib for feed parsing.
//
// Cold-start: the first poll of each feed silently seeds a seen-set (so an existing
// backlog isn't back-emitted); later polls emit only genuinely new items. Each item
// carries a stable Trigger.Dedup (its GUID/id), so the engine store suppresses
// re-acting across restarts too.
package rss

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/inbound"
)

func init() { core.Register("rss", newIntegration) }

const (
	defaultInterval = 30 * time.Minute
	maxBody         = 8 << 20
)

// Config is one rss instance.
type Config struct {
	Feeds []Feed `yaml:"feeds"`
}

// Feed is one polled source.
type Feed struct {
	Name     string           `yaml:"name"`
	URL      string           `yaml:"url"`
	Interval config.Duration  `yaml:"interval"` // default 30m
	Match    string           `yaml:"match"`    // optional case-insensitive regex over "title\nsummary"
	Repo     string           `yaml:"repo"`     // optional real repo (enables checkout); else scratch
	Actions  config.ActionSet `yaml:"actions"`
}

func (f Feed) interval() time.Duration {
	if d := f.Interval.D(); d > 0 {
		return d
	}
	return defaultInterval
}

// Integration implements core.Integration for one rss instance.
type Integration struct {
	name string
	cfg  Config
	http *http.Client
}

func newIntegration(name string, decode func(any) error) (core.Integration, error) {
	var cfg Config
	if err := decode(&cfg); err != nil {
		return nil, wrap(name, "decode config", err)
	}
	return &Integration{name: name, cfg: cfg, http: &http.Client{Timeout: 20 * time.Second}}, nil
}

func (g *Integration) Name() string { return g.name }

func (g *Integration) Validate() error {
	if len(g.cfg.Feeds) == 0 {
		return wrap(g.name, "no feeds", nil)
	}
	seen := map[string]bool{}
	for i, f := range g.cfg.Feeds {
		if f.Name == "" {
			return wrapf(g.name, "feeds[%d]: missing name", i)
		}
		if seen[f.Name] {
			return wrapf(g.name, "duplicate feed name %q", f.Name)
		}
		seen[f.Name] = true
		if !strings.HasPrefix(f.URL, "http://") && !strings.HasPrefix(f.URL, "https://") {
			return wrapf(g.name, "feed %q: url must be http(s)", f.Name)
		}
		if f.Match != "" {
			if _, err := regexp.Compile("(?i)" + f.Match); err != nil {
				return wrapf(g.name, "feed %q: bad match regex: %v", f.Name, err)
			}
		}
		if len(f.Actions) == 0 {
			return wrapf(g.name, "feed %q: no actions", f.Name)
		}
		for _, a := range f.Actions {
			if a.Type == "" {
				return wrapf(g.name, "feed %q: action.type is required", f.Name)
			}
		}
	}
	return nil
}

// Actions enumerates every feed's actions with their location, for the CLI's
// cross-config checks.
func (g *Integration) Actions() []config.ActionRef {
	var refs []config.ActionRef
	for _, f := range g.cfg.Feeds {
		refs = append(refs, f.Actions.Refs(fmt.Sprintf("rss[%s] feed %q", g.name, f.Name))...)
	}
	return refs
}

func (g *Integration) Start(ctx context.Context, emit core.EmitFunc) error {
	for i, f := range g.cfg.Feeds {
		i, f := i, f
		go g.pollFeed(ctx, emit, i, f)
	}
	log.Printf("rss[%s]: %d feed(s) polling", g.name, len(g.cfg.Feeds))
	<-ctx.Done()
	return ctx.Err()
}

func (g *Integration) pollFeed(ctx context.Context, emit core.EmitFunc, idx int, f Feed) {
	var re *regexp.Regexp
	if f.Match != "" {
		re = regexp.MustCompile("(?i)" + f.Match) // validated already
	}
	seen := map[string]bool{}
	primed := false

	// Stagger feeds so they don't all fetch on the same tick (deterministic, no RNG).
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Duration(idx) * 3 * time.Second):
	}

	t := time.NewTicker(f.interval())
	defer t.Stop()
	for {
		g.poll(ctx, emit, f, re, seen, &primed)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (g *Integration) poll(ctx context.Context, emit core.EmitFunc, f Feed, re *regexp.Regexp, seen map[string]bool, primed *bool) {
	items, err := g.fetch(ctx, f.URL)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("rss[%s]: feed %q fetch: %v", g.name, f.Name, err)
		}
		return
	}
	firstPoll := !*primed
	*primed = true
	g.process(ctx, emit, f, re, seen, firstPoll, items)
}

// process emits new items (not in seen, passing the match filter). On the first
// poll it only seeds the seen-set, so an existing backlog isn't back-emitted.
func (g *Integration) process(ctx context.Context, emit core.EmitFunc, f Feed, re *regexp.Regexp, seen map[string]bool, firstPoll bool, items []Item) {
	for _, it := range items {
		id := it.dedupID()
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if firstPoll {
			continue // seed the backlog silently on the first poll
		}
		if re != nil && !re.MatchString(it.Title+"\n"+it.Summary) {
			continue
		}
		g.emitItem(ctx, emit, f, it)
	}
}

func (g *Integration) emitItem(ctx context.Context, emit core.EmitFunc, f Feed, it Item) {
	dedup := f.Name + "\x00" + it.dedupID()
	var target core.Target
	synthetic := f.Repo == ""
	if synthetic {
		target = inbound.SyntheticTarget("rss:"+f.Name, dedup)
		target.HTMLURL = it.Link
	} else {
		owner, name, _ := strings.Cut(f.Repo, "/")
		target = core.Target{Repo: f.Repo, Owner: owner, Name: name,
			Number: inbound.SyntheticTarget("", dedup).Number, HTMLURL: it.Link}
	}
	item := map[string]any{
		"title": it.Title, "link": it.Link, "id": it.ID,
		"summary": it.Summary, "published": it.Published,
	}
	for _, act := range f.Actions {
		if !act.IsEnabled() {
			continue
		}
		if synthetic {
			act = inbound.ForceNoCheckout(act)
		}
		emit(ctx, core.Trigger{
			Source:   "rss",
			Instance: g.name,
			Kind:     f.Name,
			Variant:  act.Name,
			Target:   target,
			Title:    it.Title,
			Dedup:    dedup,
			Context:  map[string]any{"item": item, "url": it.Link},
			Action:   act,
		})
	}
}

func (g *Integration) fetch(ctx context.Context, url string) ([]Item, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "conductor")
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, */*;q=0.8")
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	return parseFeed(body), nil
}

func wrap(name, msg string, err error) error {
	if err != nil {
		return fmt.Errorf("rss[%s]: %s: %w", name, msg, err)
	}
	return fmt.Errorf("rss[%s]: %s", name, msg)
}

func wrapf(name, format string, a ...any) error {
	return fmt.Errorf("rss[%s]: "+format, append([]any{name}, a...)...)
}
