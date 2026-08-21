package rss

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
)

func newTest(t *testing.T, cfg Config) *Integration {
	t.Helper()
	ig, err := newIntegration("test", func(v any) error { *(v.(*Config)) = cfg; return nil })
	if err != nil {
		t.Fatal(err)
	}
	return ig.(*Integration)
}

func collect() (core.EmitFunc, *[]core.Trigger) {
	var got []core.Trigger
	return func(_ context.Context, tr core.Trigger) { got = append(got, tr) }, &got
}

const rssXML = `<?xml version="1.0"?>
<rss version="2.0"><channel>
  <title>Canvas Changelog</title>
  <item><title>Deprecating the old Enrollments param</title><link>https://x/1</link>
    <guid>guid-1</guid><description>The <code>foo</code> param is deprecated.</description>
    <pubDate>Mon, 18 Aug 2026 00:00:00 GMT</pubDate></item>
  <item><title>New GraphQL field</title><link>https://x/2</link><guid>guid-2</guid>
    <description>Added a field.</description></item>
</channel></rss>`

const atomXML = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Releases</title>
  <entry><title>v1.27.0</title><link href="https://x/rel/1.27.0" rel="alternate"/>
    <id>tag:x,2026:rel/1.27.0</id><summary>Bug fixes.</summary>
    <updated>2026-08-20T00:00:00Z</updated></entry>
</feed>`

func TestParseRSS(t *testing.T) {
	items := parseFeed([]byte(rssXML))
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].Title != "Deprecating the old Enrollments param" || items[0].ID != "guid-1" || items[0].Link != "https://x/1" {
		t.Fatalf("rss item 0 wrong: %+v", items[0])
	}
}

func TestParseAtom(t *testing.T) {
	items := parseFeed([]byte(atomXML))
	if len(items) != 1 {
		t.Fatalf("want 1 entry, got %d", len(items))
	}
	if items[0].Title != "v1.27.0" || items[0].Link != "https://x/rel/1.27.0" || items[0].ID != "tag:x,2026:rel/1.27.0" {
		t.Fatalf("atom entry wrong: %+v", items[0])
	}
}

func TestColdStartSeedsThenEmits(t *testing.T) {
	g := newTest(t, Config{})
	f := Feed{Name: "canvas", Actions: config.ActionSet{{Type: "agent", Agent: "assess"}}}
	emit, got := collect()
	seen := map[string]bool{}

	// First poll seeds the two existing items silently.
	g.process(context.Background(), emit, f, nil, seen, true, parseFeed([]byte(rssXML)))
	if len(*got) != 0 {
		t.Fatalf("first poll should emit nothing, got %d", len(*got))
	}
	// A later poll with a brand-new third item emits only that one.
	items := append(parseFeed([]byte(rssXML)), Item{Title: "Brand new", Link: "https://x/3", ID: "guid-3"})
	g.process(context.Background(), emit, f, nil, seen, false, items)
	if len(*got) != 1 || (*got)[0].Title != "Brand new" {
		t.Fatalf("only the new item should emit, got %+v", *got)
	}
	if (*got)[0].Kind != "canvas" || !hasPrefix((*got)[0].Target.Repo, "rss:") {
		t.Fatalf("unexpected trigger shape: %+v", (*got)[0])
	}
	if (*got)[0].Action.(config.Action).Checkout != "none" {
		t.Fatal("no repo → checkout none")
	}
}

func TestMatchRegexFilters(t *testing.T) {
	g := newTest(t, Config{})
	f := Feed{Name: "canvas", Match: "deprecat", Actions: config.ActionSet{{Type: "agent", Agent: "assess"}}}
	re := regexp.MustCompile("(?i)" + f.Match)
	emit, got := collect()
	seen := map[string]bool{}
	// Not first poll: only items matching /deprecat/i emit. Item 1 matches, item 2 doesn't.
	g.process(context.Background(), emit, f, re, seen, false, parseFeed([]byte(rssXML)))
	if len(*got) != 1 || (*got)[0].Title != "Deprecating the old Enrollments param" {
		t.Fatalf("match filter should keep only the deprecation item, got %+v", *got)
	}
}

func TestDedupWithinRun(t *testing.T) {
	g := newTest(t, Config{})
	f := Feed{Name: "canvas", Actions: config.ActionSet{{Type: "agent", Agent: "assess"}}}
	emit, got := collect()
	seen := map[string]bool{}
	items := parseFeed([]byte(rssXML))
	g.process(context.Background(), emit, f, nil, seen, false, items)
	g.process(context.Background(), emit, f, nil, seen, false, items) // same items again
	if len(*got) != 2 {
		t.Fatalf("identical items should each emit once (2 total), got %d", len(*got))
	}
}

func TestFetchAndParseHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rssXML))
	}))
	defer srv.Close()
	g := newTest(t, Config{})
	items, err := g.fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items over HTTP, got %d", len(items))
	}
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }
