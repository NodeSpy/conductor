package bot

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // pure Go: the build stays CGO-free

	"github.com/NodeSpy/conductor/internal/config"
)

// THE RECALL INDEX. The memory repo stays the storage — typed Markdown in
// git, human-editable and reviewable. What a session should not do is read
// it wholesale: it finds what it needs by ranked recall and reads it in
// parts. This is the index behind both: a SQLite FTS5 table (BM25 ranking,
// frontmatter columns weighted above the body) plus one row per document
// with what the memory index shows.
//
// It lives in conductor's state dir, never in the repo, and is DERIVED: it
// records the commit it reflects and catches up from git — incrementally
// (the files a diff names) after every commit conductor makes, and before
// every query, so an operator's own commit is never missed. A missing,
// corrupt or outdated index is rebuilt from the repo. Content is read from
// git objects at the indexed commit, never the working tree, so a write in
// flight cannot tear it. Archived log entries (archive.go) stay indexed:
// recall still finds them after they left every session's snapshot.

// indexSchema versions the layout; a change rebuilds every index.
const indexSchema = "1"

// Column weights for BM25: path (unindexed), title, scope (applies_to /
// when_to_use / …), summary, other frontmatter fields, body.
const bm25Weights = "0.0, 20.0, 10.0, 6.0, 2.0, 1.0"

// indexDoc is one document as the index holds it. Archived is the commit
// that archived it ("" for a live document).
// Summary is what the memory index shows (the summary field, else the
// body's first sentence); only an explicit summary field is searched with
// the summary's weight (SummaryField) — a fallback is body text already.
type indexDoc struct {
	Path, Kind, Title, Scope, Summary, SummaryField, Fields, Body, Archived string
	Bytes                                                                   int
	Updated                                                                 int64
}

func (r *Repo) indexPath() string { return r.IndexPath }

// openIndex opens (creating) the index database. A file that does not open
// as one is removed and recreated: it is derived data.
func (r *Repo) openIndex() (*sql.DB, error) {
	if r.idx != nil {
		return r.idx, nil
	}
	open := func() (*sql.DB, error) {
		dsn := ":memory:" // no IndexPath (a test, a throwaway repo): an index for this handle's life
		if p := r.indexPath(); p != "" {
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				return nil, err
			}
			dsn = "file:" + p + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
		}
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, err
		}
		// One connection: the index is per repo, used under idxMu, and an
		// in-memory database lives only as long as its connection.
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		db.SetConnMaxLifetime(0)
		db.SetConnMaxIdleTime(0)
		for _, q := range []string{
			`CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT NOT NULL)`,
			`CREATE TABLE IF NOT EXISTS docs (id INTEGER PRIMARY KEY, path TEXT NOT NULL, archived TEXT NOT NULL DEFAULT '',
				kind TEXT NOT NULL, title TEXT NOT NULL, scope TEXT NOT NULL, summary TEXT NOT NULL,
				bytes INTEGER NOT NULL, updated INTEGER NOT NULL, UNIQUE (path, archived))`,
			`CREATE VIRTUAL TABLE IF NOT EXISTS fts USING fts5(path UNINDEXED, title, scope, summary, fields, body, tokenize='porter unicode61')`,
		} {
			if _, err := db.Exec(q); err != nil {
				_ = db.Close()
				return nil, err
			}
		}
		return db, nil
	}
	db, err := open()
	if err != nil && r.indexPath() != "" {
		for _, sfx := range []string{"", "-wal", "-shm"} {
			_ = os.Remove(r.indexPath() + sfx)
		}
		db, err = open()
	}
	if err != nil {
		return nil, fmt.Errorf("recall index: %w", err)
	}
	r.idx = db
	return db, nil
}

// kindsSignature changes whenever what is indexed, or how, would: the
// schema, and every kind's name, homes and fields.
func (r *Repo) kindsSignature() string {
	h := sha256.New()
	fmt.Fprintf(h, "schema %s\n", indexSchema)
	for _, k := range r.Kinds().List() {
		fmt.Fprintf(h, "%s %s %s", k.Name, k.Dir, strings.Join(k.Legacy, ","))
		for _, f := range k.Fields {
			fmt.Fprintf(h, " %s:%s", f.Name, f.Type)
		}
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// indexed reports whether a repo path is indexed at all: a document under
// a kind's home (current or legacy), handoff notes aside (they reach a
// session whole, in its first prompt).
func (r *Repo) indexed(ks *Kinds, p string) (*ContentKind, bool) {
	ext := filepath.Ext(p)
	if ext != ".md" && ext != ".txt" {
		return nil, false
	}
	k, ok := ks.Of(p)
	if !ok || k.Name == KindHandoff || k.Name == KindCharter {
		return nil, false
	}
	return k, true
}

// SyncIndex brings the index up to the repo's head: incrementally from the
// commit it last reflected, or rebuilt from scratch when that is not an
// ancestor, the kinds changed, or the index is new.
func (r *Repo) SyncIndex(ctx context.Context) error {
	r.idxMu.Lock()
	defer r.idxMu.Unlock()
	return r.syncIndexLocked(ctx)
}

func (r *Repo) syncIndexLocked(ctx context.Context) error {
	db, err := r.openIndex()
	if err != nil {
		return err
	}
	head, err := r.git(ctx, r.Dir, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	head = strings.TrimSpace(head)
	if err != nil || head == "" {
		return nil // no commit yet: nothing to index
	}
	meta := map[string]string{}
	rows, err := db.Query(`SELECT k, v FROM meta`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			rows.Close()
			return err
		}
		meta[k] = v
	}
	rows.Close()
	sig := r.kindsSignature()
	if meta["head"] == head && meta["kinds"] == sig {
		return nil
	}
	full := meta["kinds"] != sig || meta["head"] == ""
	if !full {
		if _, err := r.git(ctx, r.Dir, "merge-base", "--is-ancestor", meta["head"], head); err != nil {
			full = true // history was rewritten under the index
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if full {
		err = r.rebuildLocked(ctx, tx, head)
	} else {
		err = r.catchUpLocked(ctx, tx, meta["head"], head)
	}
	if err != nil {
		return err
	}
	for k, v := range map[string]string{"head": head, "kinds": sig} {
		if _, err := tx.Exec(`INSERT INTO meta (k, v) VALUES (?, ?) ON CONFLICT (k) DO UPDATE SET v = excluded.v`, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// rebuildLocked indexes the repo from nothing: every document at head, then
// every log entry an archive commit removed.
func (r *Repo) rebuildLocked(ctx context.Context, tx *sql.Tx, head string) error {
	r.rebuilds++
	for _, q := range []string{`DELETE FROM docs`, `DELETE FROM fts`} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	ks := r.Kinds()
	out, err := r.git(ctx, r.Dir, "ls-tree", "-r", "--name-only", "--full-tree", head)
	if err != nil {
		return err
	}
	var paths []string
	for _, p := range strings.Split(strings.TrimSpace(out), "\n") {
		if _, ok := r.indexed(ks, p); ok {
			paths = append(paths, p)
		}
	}
	times, err := r.touchTimes(ctx, head)
	if err != nil {
		return err
	}
	if err := r.indexAt(ctx, tx, ks, head, "", paths, times); err != nil {
		return err
	}
	return r.indexArchivedRange(ctx, tx, ks, head)
}

// catchUpLocked applies what changed between two commits: documents added
// or modified are re-read at head; a deleted log entry was archived (only
// conductor removes one, archive.go) and stays searchable; anything else
// deleted leaves the index.
func (r *Repo) catchUpLocked(ctx context.Context, tx *sql.Tx, from, head string) error {
	ks := r.Kinds()
	out, err := r.git(ctx, r.Dir, "diff-tree", "-r", "--no-renames", "--name-status", from, head)
	if err != nil {
		return err
	}
	var changed []string
	archivedAny := false
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		st, p, ok := strings.Cut(l, "\t")
		if !ok {
			continue
		}
		k, ok := r.indexed(ks, p)
		if !ok {
			continue
		}
		if st == "D" {
			if k.Name == KindLog {
				archivedAny = true
			}
			if err := deleteDoc(tx, p, ""); err != nil {
				return err
			}
			continue
		}
		changed = append(changed, p)
	}
	times, err := r.touchTimes(ctx, from+".."+head)
	if err != nil {
		return err
	}
	if err := r.indexAt(ctx, tx, ks, head, "", changed, times); err != nil {
		return err
	}
	if archivedAny {
		return r.indexArchivedRange(ctx, tx, ks, from+".."+head)
	}
	return nil
}

// indexArchivedRange indexes every log entry removed from the tree in rng
// — the archive — at its content just before removal.
func (r *Repo) indexArchivedRange(ctx context.Context, tx *sql.Tx, ks *Kinds, rng string) error {
	args := []string{"log", "--diff-filter=D", "--no-renames", "--name-only", "--format=%x01%H %ct", rng, "--"}
	for _, k := range ks.List() {
		if k.Name == KindLog {
			args = append(args, k.Dir)
			args = append(args, k.Legacy...)
		}
	}
	out, err := r.git(ctx, r.Dir, args...)
	if err != nil {
		return err
	}
	type del struct {
		commit string
		at     int64
		paths  []string
	}
	var dels []del
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case l == "":
		case strings.HasPrefix(l, "\x01"):
			c, t, _ := strings.Cut(strings.TrimPrefix(l, "\x01"), " ")
			at, _ := strconv.ParseInt(t, 10, 64)
			dels = append(dels, del{commit: c, at: at})
		case len(dels) > 0:
			if _, ok := r.indexed(ks, l); ok {
				dels[len(dels)-1].paths = append(dels[len(dels)-1].paths, l)
			}
		}
	}
	for _, d := range dels {
		times := map[string]int64{}
		for _, p := range d.paths {
			times[p] = d.at
		}
		if err := r.indexAt(ctx, tx, ks, d.commit+"^", d.commit, d.paths, times); err != nil {
			return err
		}
	}
	return nil
}

// touchTimes is, per path, the commit time of the newest commit in rng
// that touched it.
func (r *Repo) touchTimes(ctx context.Context, rng string) (map[string]int64, error) {
	out, err := r.git(ctx, r.Dir, "log", "--no-renames", "--name-only", "--format=%x01%ct", rng)
	if err != nil {
		return nil, err
	}
	times := map[string]int64{}
	var cur int64
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if t, ok := strings.CutPrefix(l, "\x01"); ok {
			cur, _ = strconv.ParseInt(t, 10, 64)
			continue
		}
		if _, seen := times[l]; !seen {
			times[l] = cur
		}
	}
	return times, nil
}

// indexAt (re)indexes paths as they are at rev; archived is the archive
// commit for archived entries ("" for live documents).
func (r *Repo) indexAt(ctx context.Context, tx *sql.Tx, ks *Kinds, rev, archived string, paths []string, times map[string]int64) error {
	if len(paths) == 0 {
		return nil
	}
	blobs, err := r.catFiles(ctx, rev, paths)
	if err != nil {
		return err
	}
	for _, p := range paths {
		b, ok := blobs[p]
		if !ok {
			continue
		}
		k, _ := r.indexed(ks, p)
		d := docFor(k, p, string(b))
		d.Archived, d.Updated = archived, times[p]
		if err := putDoc(tx, d); err != nil {
			return err
		}
	}
	return nil
}

// catFiles reads paths at rev in one git process.
func (r *Repo) catFiles(ctx context.Context, rev string, paths []string) (map[string][]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch")
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var in bytes.Buffer
	for _, p := range paths {
		in.WriteString(rev + ":" + p + "\n")
	}
	cmd.Stdin = &in
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	br := bufio.NewReader(stdout)
	var readErr error
	for _, p := range paths {
		hdr, err := br.ReadString('\n')
		if err != nil {
			readErr = err
			break
		}
		f := strings.Fields(hdr)
		if len(f) != 3 || f[1] != "blob" {
			continue // missing, or not a file
		}
		n, err := strconv.Atoi(f[2])
		if err != nil {
			readErr = err
			break
		}
		buf := make([]byte, n+1) // content and its trailing newline
		if _, err := io.ReadFull(br, buf); err != nil {
			readErr = err
			break
		}
		out[p] = buf[:n]
	}
	_, _ = io.Copy(io.Discard, br)
	if err := cmd.Wait(); err != nil && readErr == nil {
		readErr = fmt.Errorf("git cat-file: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, readErr
}

func deleteDoc(tx *sql.Tx, path, archived string) error {
	var id int64
	err := tx.QueryRow(`SELECT id FROM docs WHERE path = ? AND archived = ?`, path, archived).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM fts WHERE rowid = ?`, id); err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM docs WHERE id = ?`, id)
	return err
}

func putDoc(tx *sql.Tx, d indexDoc) error {
	if err := deleteDoc(tx, d.Path, d.Archived); err != nil {
		return err
	}
	res, err := tx.Exec(`INSERT INTO docs (path, archived, kind, title, scope, summary, bytes, updated) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		d.Path, d.Archived, d.Kind, d.Title, d.Scope, d.Summary, d.Bytes, d.Updated)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO fts (rowid, path, title, scope, summary, fields, body) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, d.Path, d.Title, d.Scope, d.SummaryField, d.Fields, d.Body)
	return err
}

// scopeFields are the frontmatter fields that say where a document
// applies, weighted between the title and the summary.
var scopeFields = map[string]bool{"applies_to": true, "when_to_use": true, "case": true, "sweep": true, "period": true, "cases": true}

// docFor extracts what the index holds from a document's text: its title,
// scope and one-line summary (the summary field, else the body's first
// sentence), its other frontmatter flattened, and the body. A document
// whose frontmatter does not parse is indexed by its raw text.
func docFor(k *ContentKind, p, text string) indexDoc {
	d := indexDoc{Path: p, Kind: k.Name, Bytes: len(text)}
	fm, body, ok, err := SplitFrontmatter(text)
	if !ok || err != nil {
		fm, body = map[string]any{}, text
	}
	d.Body = body
	d.Title = fmString(fm["title"])
	if d.Title == "" {
		d.Title = fmString(fm["what"])
	}
	if d.Title == "" {
		d.Title = firstHeading(body)
	}
	if d.Title == "" {
		d.Title = strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
	}
	var scope, fields []string
	names := make([]string, 0, len(fm))
	for n := range fm {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		v := fmString(fm[n])
		switch {
		case v == "" || n == "title" || n == "summary" || (n == "what" && d.Title == v):
		case scopeFields[n]:
			scope = append(scope, v)
		default:
			fields = append(fields, v)
		}
	}
	d.Scope = strings.Join(scope, "; ")
	d.Fields = strings.Join(fields, "\n")
	d.SummaryField = fmString(fm["summary"])
	d.Summary = d.SummaryField
	if d.Summary == "" {
		d.Summary = firstSentence(body)
	}
	return d
}

// fmString renders a frontmatter value as text: a list joined by commas.
func fmString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case []any:
		var parts []string
		for _, e := range x {
			if s := fmString(e); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprint(x)
	}
}

func firstHeading(body string) string {
	for _, l := range strings.Split(body, "\n") {
		if h, ok := headingOf(l); ok {
			return h.text
		}
	}
	return ""
}

var sentenceEnd = regexp.MustCompile(`[.!?](\s|$)`)

// firstSentence is a body's first prose sentence: headings, rules and list
// markers skipped, markdown emphasis dropped, at most 200 bytes.
func firstSentence(body string) string {
	var para []string
	for _, l := range strings.Split(body, "\n") {
		t := strings.TrimSpace(l)
		if _, isHeading := headingOf(l); isHeading || t == "---" || strings.HasPrefix(t, "```") {
			if len(para) > 0 {
				break
			}
			continue
		}
		if t == "" {
			if len(para) > 0 {
				break
			}
			continue
		}
		t = strings.TrimLeft(t, "-*+> ")
		para = append(para, t)
	}
	s := strings.Join(para, " ")
	s = strings.NewReplacer("**", "", "__", "", "`", "").Replace(s)
	if loc := sentenceEnd.FindStringIndex(s); loc != nil {
		s = s[:loc[0]+1]
	}
	return oneLine(s, 200)
}

// ---- queries ----

// Hit is one recall result.
type Hit struct {
	Path     string  `json:"path"`
	Kind     string  `json:"kind"`
	Title    string  `json:"title"`
	Snippet  string  `json:"snippet"`
	Archived string  `json:"archived,omitempty"` // the archive commit, for an archived log entry
	Score    float64 `json:"score"`
}

var queryWord = regexp.MustCompile(`[\p{L}\p{N}_]+`)

// queryTerms are a query's searchable words, lowercased, deduplicated, at
// most 16 — each later quoted, so no FTS syntax can be injected.
func queryTerms(q string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range queryWord.FindAllString(strings.ToLower(q), -1) {
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
		if len(out) == 16 {
			break
		}
	}
	return out
}

func ftsQuery(terms []string, op string) string {
	q := make([]string, len(terms))
	for i, t := range terms {
		q[i] = `"` + t + `"`
	}
	return strings.Join(q, " "+op+" ")
}

// Recall ranks the repo's documents against a query: BM25 over the title,
// scope, summary and other frontmatter fields (weighted in that order)
// and the body. Documents matching every word come first, then those
// matching any. kind narrows it to one kind ("" all).
func (r *Repo) Recall(ctx context.Context, query string, limit int, kind string) ([]Hit, error) {
	terms := queryTerms(query)
	if len(terms) == 0 {
		return nil, errors.New("recall needs at least one word to search for")
	}
	if limit <= 0 {
		limit = config.DefaultRecallLimit
	}
	r.idxMu.Lock()
	defer r.idxMu.Unlock()
	if err := r.syncIndexLocked(ctx); err != nil {
		return nil, err
	}
	var hits []Hit
	seen := map[string]bool{}
	ops := []string{"AND"}
	if len(terms) > 1 {
		ops = append(ops, "OR")
	}
	for _, op := range ops {
		if len(hits) >= limit {
			break
		}
		more, err := r.match(ctx, ftsQuery(terms, op), kind, limit+len(hits))
		if err != nil {
			return nil, err
		}
		for _, h := range more {
			key := h.Path + "@" + h.Archived
			if !seen[key] && len(hits) < limit {
				seen[key] = true
				hits = append(hits, h)
			}
		}
	}
	return hits, nil
}

func (r *Repo) match(ctx context.Context, q, kind string, limit int) ([]Hit, error) {
	db, err := r.openIndex()
	if err != nil {
		return nil, err
	}
	sqlq := `SELECT d.path, d.kind, d.title, d.archived, snippet(fts, -1, '«', '»', '…', 12), bm25(fts, ` + bm25Weights + `) AS score
		FROM fts JOIN docs d ON d.id = fts.rowid WHERE fts MATCH ?`
	args := []any{q}
	if kind != "" {
		sqlq += ` AND d.kind = ?`
		args = append(args, kind)
	}
	sqlq += ` ORDER BY score, d.path, d.archived DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.Path, &h.Kind, &h.Title, &h.Archived, &h.Snippet, &h.Score); err != nil {
			return nil, err
		}
		h.Snippet = oneLine(h.Snippet, 240)
		out = append(out, h)
	}
	return out, rows.Err()
}

// IndexEntry is one line of the memory index.
type IndexEntry struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	Scope   string `json:"scope,omitempty"`
	Summary string `json:"summary,omitempty"`
	Bytes   int    `json:"bytes"`
	Updated int64  `json:"updated"`
}

// IndexStats counts what the index holds beyond its entries: the raw log
// entries still in the snapshot, and the archived ones.
type IndexStats struct {
	Logs, Archived int
}

// Entries is every live document the memory index lists — everything but
// the log (raw case logs are found by recall, summarized by digests) —
// newest first, with the log's counts.
func (r *Repo) Entries(ctx context.Context, kind string) ([]IndexEntry, IndexStats, error) {
	r.idxMu.Lock()
	defer r.idxMu.Unlock()
	var st IndexStats
	if err := r.syncIndexLocked(ctx); err != nil {
		return nil, st, err
	}
	db, err := r.openIndex()
	if err != nil {
		return nil, st, err
	}
	q := `SELECT path, kind, title, scope, summary, bytes, updated FROM docs WHERE archived = '' AND kind != ?`
	args := []any{KindLog}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	rows, err := db.QueryContext(ctx, q+` ORDER BY updated DESC, path`, args...)
	if err != nil {
		return nil, st, err
	}
	defer rows.Close()
	var out []IndexEntry
	for rows.Next() {
		var e IndexEntry
		if err := rows.Scan(&e.Path, &e.Kind, &e.Title, &e.Scope, &e.Summary, &e.Bytes, &e.Updated); err != nil {
			return nil, st, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, st, err
	}
	err = db.QueryRowContext(ctx, `SELECT COUNT(CASE WHEN archived = '' THEN 1 END), COUNT(CASE WHEN archived != '' THEN 1 END) FROM docs WHERE kind = ?`, KindLog).Scan(&st.Logs, &st.Archived)
	return out, st, err
}

// relevant ranks the index's entries against a query (a worker's brief,
// the open cases): matching documents in BM25 order, best first.
func (r *Repo) relevant(ctx context.Context, query string) []string {
	terms := queryTerms(query)
	if len(terms) == 0 {
		return nil
	}
	r.idxMu.Lock()
	defer r.idxMu.Unlock()
	db, err := r.openIndex()
	if err != nil {
		return nil
	}
	rows, err := db.QueryContext(ctx, `SELECT d.path FROM fts JOIN docs d ON d.id = fts.rowid
		WHERE fts MATCH ? AND d.archived = '' AND d.kind != ? ORDER BY bm25(fts, `+bm25Weights+`), d.path LIMIT 200`, ftsQuery(terms, "OR"), KindLog)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			out = append(out, p)
		}
	}
	return out
}

// LogDocs is the archive's view of the log: every live log entry with its
// last commit time, oldest first.
func (r *Repo) LogDocs(ctx context.Context) ([]IndexEntry, error) {
	r.idxMu.Lock()
	defer r.idxMu.Unlock()
	if err := r.syncIndexLocked(ctx); err != nil {
		return nil, err
	}
	db, err := r.openIndex()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT path, title, bytes, updated FROM docs WHERE archived = '' AND kind = ? ORDER BY updated, path`, KindLog)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IndexEntry
	for rows.Next() {
		e := IndexEntry{Kind: KindLog}
		if err := rows.Scan(&e.Path, &e.Title, &e.Bytes, &e.Updated); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
