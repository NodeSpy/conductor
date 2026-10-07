package rss

import "testing"

// These cover the one part of this package still alive once the old
// core.Register-based Integration was deleted as dead code (internal/builtins/
// rss is the only caller now, through the exported ParseFeed/DedupID wrappers
// below) — the feed parsing itself, moved here from the deleted rss_test.go/
// rss_more_test.go.

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

func TestItemDedupIDFallbacks(t *testing.T) {
	if got := (Item{ID: "id", Link: "l", Title: "t"}).dedupID(); got != "id" {
		t.Fatalf("id wins: %q", got)
	}
	if got := (Item{Link: "l", Title: "t"}).dedupID(); got != "l" {
		t.Fatalf("link next: %q", got)
	}
	if got := (Item{Title: "t"}).dedupID(); got != "t" {
		t.Fatalf("title last: %q", got)
	}
	// The exported wrapper internal/builtins/rss actually calls.
	if got := (Item{ID: "id"}).DedupID(); got != "id" {
		t.Fatalf("DedupID: %q", got)
	}
}

func TestAtomLinkRelPreference(t *testing.T) {
	// A rel=self link is skipped in favor of the alternate/unmarked one.
	doc := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
	  <entry><title>v1</title>
	    <link href="https://x/self" rel="self"/>
	    <link href="https://x/alt"/>
	    <id>e1</id><content>full body</content><published>2026-01-01</published></entry>
	</feed>`
	items := parseFeed([]byte(doc))
	if len(items) != 1 || items[0].Link != "https://x/alt" {
		t.Fatalf("alternate link preferred: %+v", items)
	}
	// content fills summary when summary is empty; published preferred over updated.
	if items[0].Summary != "full body" || items[0].Published != "2026-01-01" {
		t.Fatalf("fallback fields: %+v", items[0])
	}
	// Only a self link → falls back to it (firstNonEmpty exercised the other way).
	onlySelf := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
	  <entry><title>v2</title><link href="https://x/self" rel="self"/><id>e2</id></entry></feed>`
	items = parseFeed([]byte(onlySelf))
	if len(items) != 1 || items[0].Link != "https://x/self" {
		t.Fatalf("self-only fallback: %+v", items)
	}
}

func TestParseFeedExportedWrapper(t *testing.T) {
	// internal/builtins/rss calls only the exported name.
	if got := ParseFeed([]byte(rssXML)); len(got) != 2 {
		t.Fatalf("ParseFeed: want 2, got %d", len(got))
	}
}
