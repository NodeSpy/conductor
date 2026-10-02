// Package rss is the `rss` connector as a contract handler
// (docs/design/plugin-contract.md §1.10): it polls each configured feed and
// emits new items, routed to the triggers whose `match:` they satisfy,
// served in-process over the same protocol a spawned plugin speaks.
package rss

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/inbound"
	feedparse "github.com/NodeSpy/conductor/internal/integrations/rss"
	"github.com/NodeSpy/conductor/pkg/plugin"
	"github.com/NodeSpy/conductor/pkg/sourcekit"
)

const (
	defaultInterval = 30 * time.Minute
	maxBody         = 8 << 20
)

// RSS is the rss handler. HTTP and Stagger are seams for tests.
type RSS struct {
	HTTP    *http.Client
	Stagger time.Duration
}

// New is the rss handler.
func New() *RSS { return &RSS{HTTP: &http.Client{Timeout: 30 * time.Second}, Stagger: 3 * time.Second} }

// semantics: the item's target is synthetic — the feed's own name, the
// operator's word — so it is assigned, and there is nothing to check out.
var semantics = &plugin.EventSemantics{Target: &plugin.TargetSemantics{Label: "feed item", Assigned: json.RawMessage(`true`)}}

// Describe declares rss: one dynamic event per configured feed.
func (*RSS) Describe() plugin.Decl {
	return plugin.Decl{
		Type: "rss", Kind: plugin.KindConnector,
		Desc: "RSS/Atom: polls feeds and fires on new items; no verbs.",
		Connection: plugin.Schema{
			"feeds": {Type: "map", Required: true, Desc: "name -> { url, interval }"},
		},
		Events: []plugin.Event{{
			Name: "<feed>", Dynamic: true, Desc: "a configured feed produced a new item",
			Filters: plugin.Schema{"match": {Type: "string", Desc: "case-insensitive regex over the item's title+summary"}},
			Context: plugin.Schema{
				"item.title": {Type: "string"}, "item.link": {Type: "string"}, "item.id": {Type: "string"},
				"item.summary": {Type: "string"}, "item.published": {Type: "string"},
				"url": {Type: "string"}, "kind": {Type: "string"}, "title": {Type: "string"},
			},
			Semantics: semantics,
		}},
	}
}

// Invoke: rss has no verbs.
func (*RSS) Invoke(plugin.InvokeRequest) (plugin.InvokeResult, error) {
	return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInvalid, "rss: no verbs", nil)
}

type feed struct {
	name, url string
	interval  time.Duration
}

func feeds(cfg map[string]any) (map[string]feed, []plugin.Problem) {
	raw, _ := cfg["feeds"].(map[string]any)
	if len(raw) == 0 {
		return nil, []plugin.Problem{{Path: "feeds", Message: "no feeds"}}
	}
	out := map[string]feed{}
	var problems []plugin.Problem
	for name, v := range raw {
		m, _ := v.(map[string]any)
		f := feed{name: name, interval: defaultInterval}
		f.url, _ = m["url"].(string)
		if f.url == "" {
			problems = append(problems, plugin.Problem{Path: "feeds." + name + ".url", Message: "required"})
			continue
		}
		if s, ok := m["interval"].(string); ok && s != "" {
			d, err := time.ParseDuration(s)
			if err != nil || d <= 0 {
				problems = append(problems, plugin.Problem{Path: "feeds." + name + ".interval", Message: "not a positive duration"})
				continue
			}
			f.interval = d
		}
		out[name] = f
	}
	return out, problems
}

func names(m map[string]feed) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// matcher evaluates rss's one match key: a case-insensitive regex over the
// item's title and summary.
func matcher(key string, val any, facts map[string]any) (bool, error) {
	if key != "match" {
		return false, fmt.Errorf("rss: unknown filter key %q", key)
	}
	pat := fmt.Sprint(val)
	if l, ok := val.([]any); ok && len(l) == 1 {
		pat = fmt.Sprint(l[0])
	}
	re, err := regexp.Compile("(?i)" + pat)
	if err != nil {
		return false, fmt.Errorf("filters.match: bad regex: %w", err)
	}
	item, _ := facts["item"].(map[string]any)
	title, _ := item["title"].(string)
	summary, _ := item["summary"].(string)
	return re.MatchString(title + "\n" + summary), nil
}

// Validate checks the feeds and that every trigger names one.
func (*RSS) Validate(_ context.Context, req plugin.ValidateRequest) (plugin.ValidateResult, error) {
	fs, problems := feeds(req.Config)
	for i, t := range req.Triggers {
		if _, ok := fs[t.Event]; !ok {
			problems = append(problems, plugin.Problem{Path: fmt.Sprintf("triggers[%d]", i),
				Message: fmt.Sprintf("unknown rss feed %q (declared: %s)", t.Event, names(fs))})
		}
	}
	return plugin.ValidateResult{Problems: problems}, nil
}

type route struct {
	id     string
	filter *sourcekit.Filter
}

// StartSource polls every triggered feed until ctx ends.
func (r *RSS) StartSource(ctx context.Context, req plugin.StartSourceRequest, emit func(any) error) error {
	fs, problems := feeds(req.Config)
	if len(problems) > 0 {
		return fmt.Errorf("rss[%s]: %s: %s", req.Instance, problems[0].Path, problems[0].Message)
	}
	byFeed := map[string][]route{}
	for _, t := range req.Triggers {
		if t.Enabled != nil && !*t.Enabled {
			continue
		}
		var f *sourcekit.Filter
		if len(t.Filter) > 0 && string(t.Filter) != "null" {
			f = new(sourcekit.Filter)
			if err := json.Unmarshal(t.Filter, f); err != nil {
				return fmt.Errorf("rss[%s]: trigger %s: filter: %w", req.Instance, t.ID, err)
			}
		}
		byFeed[t.Event] = append(byFeed[t.Event], route{id: t.ID, filter: f})
	}
	i := 0
	for name, routes := range byFeed {
		f, ok := fs[name]
		if !ok {
			continue
		}
		go r.poll(ctx, req.Instance, f, routes, time.Duration(i)*r.Stagger, emit)
		i++
	}
	<-ctx.Done()
	return nil
}

func (r *RSS) poll(ctx context.Context, instance string, f feed, routes []route, stagger time.Duration, emit func(any) error) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(stagger):
	}
	seen := map[string]bool{}
	primed := false
	t := time.NewTicker(f.interval)
	defer t.Stop()
	for {
		if items, err := r.fetch(ctx, f.url); err == nil {
			for _, it := range items {
				id := it.DedupID()
				if id == "" || seen[id] {
					continue
				}
				seen[id] = true
				if primed {
					r.emitItem(instance, f, it, routes, emit)
				}
			}
			primed = true // the first poll only seeds the backlog
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (r *RSS) emitItem(instance string, f feed, it feedparse.Item, routes []route, emit func(any) error) {
	dedup := f.name + "\x00" + it.DedupID()
	tgt := inbound.SyntheticTarget("rss:"+f.name, dedup)
	item := map[string]any{"title": it.Title, "link": it.Link, "id": it.ID, "summary": it.Summary, "published": it.Published}
	facts := map[string]any{"item": item, "url": it.Link}
	for _, rt := range routes {
		if rt.filter != nil {
			if keep, err := rt.filter.Eval(facts, matcher); err != nil || !keep {
				continue
			}
		}
		_ = emit(plugin.SourceEvent{
			Event: f.name, Trigger: rt.id, Instance: instance, Title: it.Title, Dedup: dedup,
			Target:  plugin.Target{Repo: tgt.Repo, Number: tgt.Number, HTMLURL: it.Link, Assigned: true},
			Context: facts,
		})
	}
}

func (r *RSS) fetch(ctx context.Context, url string) ([]feedparse.Item, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "conductor")
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, */*;q=0.8")
	resp, err := r.HTTP.Do(req)
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
	return feedparse.ParseFeed(body), nil
}
