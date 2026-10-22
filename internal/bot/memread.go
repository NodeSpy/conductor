package bot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/NodeSpy/conductor/internal/config"
)

// READING IN PARTS, AND THE MEMORY INDEX. A session reads memory the way a
// person skims a wiki: the index first (one line a document), then recall,
// then the one section it needs. `conductor bot read` returns a document —
// or one section of it — up to the repo's read_max bytes, and says plainly
// when it cut, where to read on from, and which sections there are.

// Limits is the repo's memory limits (the defaults when unset).
func (r *Repo) Limits() config.MemoryLimits {
	if r.LimitsFn != nil {
		return r.LimitsFn()
	}
	return config.MemoryLimits{}
}

type heading struct {
	level int
	text  string
}

// headingOf parses an ATX Markdown heading ("## Text").
func headingOf(line string) (heading, bool) {
	t := strings.TrimRight(line, " \t\r")
	n := 0
	for n < len(t) && n < 7 && t[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || n >= len(t) || (t[n] != ' ' && t[n] != '\t') {
		return heading{}, false
	}
	return heading{level: n, text: strings.TrimSpace(strings.TrimRight(strings.TrimSpace(t[n:]), "#"))}, true
}

// section is one heading's span of a document: from its line to the next
// heading at its level or above.
type section struct {
	heading
	start, end int
}

// sectionsOf lists a document's headings and their spans, skipping fenced
// code (a "# comment" in a shell block is not a heading).
func sectionsOf(text string) []section {
	var out []section
	off, fenced := 0, false
	for _, l := range strings.SplitAfter(text, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fenced = !fenced
		} else if h, ok := headingOf(strings.TrimRight(l, "\n")); ok && !fenced {
			for i := range out {
				if out[i].end < 0 && out[i].level >= h.level {
					out[i].end = off
				}
			}
			out = append(out, section{heading: h, start: off, end: -1})
		}
		off += len(l)
	}
	for i := range out {
		if out[i].end < 0 {
			out[i].end = len(text)
		}
	}
	return out
}

func sectionNames(secs []section) []string {
	var out []string
	for i, s := range secs {
		if i == 40 {
			out = append(out, fmt.Sprintf("… %d more", len(secs)-40))
			break
		}
		out = append(out, s.text)
	}
	return out
}

// ReadResult is one `conductor bot read`.
type ReadResult struct {
	Path      string   `json:"path"`
	Section   string   `json:"section,omitempty"`
	Text      string   `json:"text"`
	Bytes     int      `json:"bytes"` // of the whole document (or the section)
	Offset    int      `json:"offset,omitempty"`
	Truncated bool     `json:"truncated"`
	Next      int      `json:"next_offset,omitempty"`
	Sections  []string `json:"sections,omitempty"`
	Archived  string   `json:"archived,omitempty"`
	Note      string   `json:"note,omitempty"`
}

// ReadPart reads a document, or one section of it, from offset, at most
// max bytes (the repo's read_max when 0). An archived log entry is read
// from the commit before it was archived.
func (r *Repo) ReadPart(ctx context.Context, rel, sectionName string, offset, max int) (ReadResult, error) {
	if max <= 0 {
		max = r.Limits().ReadMaxOr()
	}
	if offset < 0 {
		return ReadResult{}, fmt.Errorf("--offset must not be negative, got %d", offset)
	}
	res := ReadResult{Path: rel}
	text, err := r.Read(rel)
	if errors.Is(err, os.ErrNotExist) {
		text, res.Archived, err = r.readArchived(ctx, rel)
	}
	if err != nil {
		return ReadResult{}, err
	}
	secs := sectionsOf(text)
	if want := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(sectionName), "#")); want != "" {
		var hit *section
		for i := range secs {
			if strings.EqualFold(secs[i].text, want) {
				hit = &secs[i]
				break
			}
		}
		if hit == nil {
			if len(secs) == 0 {
				return ReadResult{}, fmt.Errorf("%s has no sections (no Markdown headings) — read it without --section", rel)
			}
			return ReadResult{}, fmt.Errorf("no section %q in %s; its sections: %s", want, rel, strings.Join(sectionNames(secs), " | "))
		}
		res.Section = hit.text
		text = text[hit.start:hit.end]
	}
	res.Bytes = len(text)
	if offset > len(text) {
		return ReadResult{}, fmt.Errorf("--offset %d is past the end (%d bytes)", offset, len(text))
	}
	part := text[offset:]
	if len(part) > max {
		cut := len(cutBytes(part, max))
		if nl := strings.LastIndexByte(part[:cut], '\n'); nl >= max/2 {
			cut = nl + 1 // end on a line when that keeps at least half
		}
		part = part[:cut]
		res.Truncated, res.Next = true, offset+cut
	}
	res.Text, res.Offset = part, offset
	if len(secs) > 1 || res.Truncated {
		res.Sections = sectionNames(secs)
	}
	if res.Truncated {
		res.Note = fmt.Sprintf("TRUNCATED: showing bytes %d–%d of %d (read_max %d). Read on with --offset %d", offset, res.Next, res.Bytes, max, res.Next)
		if res.Section == "" && len(secs) > 0 {
			res.Note += `, or read one section with --section "<heading>"`
		}
		res.Note += "."
	}
	if res.Archived != "" {
		res.Note = strings.TrimSpace(fmt.Sprintf("Archived log entry (removed from your working copy by commit %s after it was consolidated). %s", res.Archived, res.Note))
	}
	return res, nil
}

// readArchived reads a log entry that is no longer in the tree: its text
// as of the commit before the one that removed it.
func (r *Repo) readArchived(ctx context.Context, rel string) (string, string, error) {
	c, err := r.cleanPath(rel)
	if err != nil {
		return "", "", err
	}
	if k, _ := r.Kinds().Of(c); k == nil || k.Name != KindLog {
		return "", "", fmt.Errorf("no file %s", c)
	}
	out, err := r.git(ctx, r.Dir, "rev-list", "-1", "HEAD", "--", c)
	commit := strings.TrimSpace(out)
	if err != nil || commit == "" {
		return "", "", fmt.Errorf("no file %s (live or archived)", c)
	}
	text, err := r.git(ctx, r.Dir, "show", commit+"^:"+c)
	if err != nil {
		return "", "", fmt.Errorf("no file %s (live or archived)", c)
	}
	return text, short(commit), nil
}

// cutBytes is s cut to at most n bytes, on a rune boundary.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// ---- the memory index, as a session's prompt shows it ----

// IndexView is how much of the index to render, and for what.
type IndexView struct {
	// Budget caps the whole rendered section, header and footer included.
	Budget int
	// Query ranks matching entries first (a worker's brief, the open cases'
	// titles); the rest follow newest first.
	Query string
	// Heading is the section's title line; Recall is the recall command to
	// point to ("conductor bot recall", "conductor bot team recall").
	Heading, Recall, IndexCmd string
}

// RenderIndex renders a repo's memory index within a byte budget: one line
// per document — path, kind, title, where it applies, a one-line summary —
// the most relevant first. Past the budget it stops and says how many
// documents (of which kinds) it left out, and how to find them.
func (r *Repo) RenderIndex(ctx context.Context, v IndexView) (string, error) {
	entries, st, err := r.Entries(ctx, "")
	if err != nil {
		return "", err
	}
	if v.Recall == "" {
		v.Recall = "conductor bot recall"
	}
	if v.IndexCmd == "" {
		v.IndexCmd = "conductor bot index"
	}
	ranked := r.relevant(ctx, v.Query)
	return renderIndex(entries, st, ranked, v), nil
}

// renderIndex is RenderIndex's pure half.
func renderIndex(entries []IndexEntry, st IndexStats, ranked []string, v IndexView) string {
	order := make([]IndexEntry, 0, len(entries))
	byPath := map[string]int{}
	for i, e := range entries {
		byPath[e.Path] = i
	}
	used := map[string]bool{}
	for _, p := range ranked {
		if i, ok := byPath[p]; ok && !used[p] {
			used[p] = true
			order = append(order, entries[i])
		}
	}
	for _, e := range entries {
		if !used[e.Path] {
			order = append(order, e)
		}
	}
	var b strings.Builder
	head := v.Heading
	if head == "" {
		head = "## Your memory index"
	}
	fmt.Fprintf(&b, "%s (%d documents)\n\n", head, len(entries))
	tail := ""
	if st.Logs+st.Archived > 0 {
		tail = fmt.Sprintf("\nlog/: %d raw case logs in your working copy, %d archived after consolidation — digests/ summarize them; `%s` searches all of them.\n", st.Logs, st.Archived, v.Recall)
	}
	shown := 0
	omitted := map[string]int{}
	for i, e := range order {
		line := indexLine(e)
		// What a cut here would still have to say: the footer for the rest.
		rest := omittedFooter(order[i+1:], v)
		if b.Len()+len(line)+len(rest)+len(tail) > v.Budget {
			for _, x := range order[i:] {
				omitted[x.Kind]++
			}
			break
		}
		b.WriteString(line)
		shown++
	}
	if len(entries) == 0 {
		b.WriteString("- (empty: nothing remembered yet)\n")
	}
	if shown < len(order) {
		b.WriteString(omittedFooter(order[shown:], v))
	}
	b.WriteString(tail)
	// The budget is a hard cap, whatever the footers say.
	return cutBytes(b.String(), v.Budget)
}

func indexLine(e IndexEntry) string {
	parts := []string{e.Path, e.Kind, oneLine(e.Title, 80)}
	if e.Scope != "" {
		parts = append(parts, "for: "+oneLine(e.Scope, 80))
	}
	if e.Summary != "" && e.Summary != e.Title {
		parts = append(parts, oneLine(e.Summary, 140))
	}
	return "- " + strings.Join(parts, " · ") + "\n"
}

func omittedFooter(rest []IndexEntry, v IndexView) string {
	if len(rest) == 0 {
		return ""
	}
	counts := map[string]int{}
	var kinds []string
	for _, e := range rest {
		if counts[e.Kind] == 0 {
			kinds = append(kinds, e.Kind)
		}
		counts[e.Kind]++
	}
	var parts []string
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
	}
	return fmt.Sprintf("- … %d more not shown (%s): find them with `%s <words>`, list them with `%s --kind <kind>`\n", len(rest), strings.Join(parts, ", "), v.Recall, v.IndexCmd)
}

// IndexPage is the full listing, a page at a time (`conductor bot index
// --all` / `--kind`): entries from offset until max bytes.
func (r *Repo) IndexPage(ctx context.Context, kind string, offset, max int) (map[string]any, error) {
	entries, st, err := r.Entries(ctx, kind)
	if err != nil {
		return nil, err
	}
	if max <= 0 {
		max = r.Limits().ReadMaxOr()
	}
	if offset < 0 || (offset > 0 && offset >= len(entries)) {
		return nil, fmt.Errorf("--offset %d: the listing has %d entries", offset, len(entries))
	}
	var b strings.Builder
	i := offset
	for ; i < len(entries); i++ {
		line := indexLine(entries[i])
		if b.Len()+len(line) > max && i > offset {
			break
		}
		b.WriteString(line)
	}
	out := map[string]any{"text": b.String(), "entries": len(entries), "from": offset, "to": i,
		"logs": st.Logs, "archived_logs": st.Archived}
	if i < len(entries) {
		out["truncated"] = true
		out["next_offset"] = i
		out["note"] = fmt.Sprintf("TRUNCATED: entries %d–%d of %d. Read on with --offset %d.", offset, i, len(entries), i)
	}
	return out, nil
}
