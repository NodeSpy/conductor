package bot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/store"
)

// CONSOLIDATION AND THE ARCHIVE. Raw case logs grow without limit; what a
// session should see of its past is their summaries. A consolidation worker
// is given the oldest log entries not yet consolidated (at most
// consolidateBatch), writes ONE digest summarizing them (and updates
// knowledge where something durable emerged), and finishes. Only when it
// finishes done, having committed a digest, do those entries count as
// consolidated — each at the exact content it was given. Then conductor
// archives them: one reviewable commit removes them from the tree, so they
// leave every session's working copy; git history keeps them, recall still
// finds them, and `conductor bot read` reads them back.
//
// The invariant the archive keeps: an entry leaves the working copy only if
// a finished consolidation covered exactly the content it has now, and its
// case is closed. Anything appended since, anything not yet covered, and an
// open case's log all stay.

// consolidateBatch bounds one consolidation's input.
const consolidateBatch = 60

// Consolidate starts a worker that summarizes the oldest unconsolidated log
// entries into a digest (and knowledge). With none, it starts nothing.
func (s *Service) Consolidate(ctx context.Context, name string) (store.Worker, error) {
	if _, err := s.bot(name); err != nil {
		return store.Worker{}, err
	}
	repo := s.Repo(name)
	logs, err := repo.LogDocs(ctx)
	if err != nil {
		return store.Worker{}, err
	}
	blobs, err := repo.logBlobs(ctx)
	if err != nil {
		return store.Worker{}, err
	}
	rec := s.Ledger.Bot(name)
	pending := map[string]bool{}
	for _, w := range rec.Workers {
		if liveWorker(w) {
			for p := range w.Consolidates {
				pending[p] = true
			}
		}
	}
	var todo []string
	given := map[string]string{}
	for _, e := range logs { // oldest first
		b := blobs[e.Path]
		if b == "" || pending[e.Path] || rec.ConsolidatedDocs[e.Path] == b {
			continue
		}
		if len(todo) == consolidateBatch {
			break
		}
		todo = append(todo, e.Path)
		given[e.Path] = b
	}
	if len(todo) == 0 {
		// A scheduled consolidation with nothing new is a no-op, not a
		// failed run: the zero Worker says nothing was started.
		return store.Worker{}, nil
	}
	remaining := 0
	for _, e := range logs {
		if b := blobs[e.Path]; b != "" && !pending[e.Path] && rec.ConsolidatedDocs[e.Path] != b && given[e.Path] == "" {
			remaining++
		}
	}
	w, err := s.Spawn(ctx, Actor{Bot: name, Role: config.BotRoleHome}, SpawnRequest{Title: "memory consolidation",
		Brief: consolidationBrief(todo, remaining), byConductor: true, consolidates: given})
	if err != nil {
		return store.Worker{}, err
	}
	_ = s.Ledger.UpdateBot(name, func(r *store.BotRecord) error { r.Consolidated = s.now(); return nil })
	return w, nil
}

func consolidationBrief(todo []string, remaining int) string {
	more := ""
	if remaining > 0 {
		more = fmt.Sprintf(" (%d more wait for the next consolidation)", remaining)
	}
	return fmt.Sprintf(`CONSOLIDATION. Summarize these %d raw case logs%s — the oldest not yet consolidated — into memory a future session can use without reading them:

1. Read each with `+"`conductor bot read <path>`"+` (long ones a section at a time with --section; never cat or grep the directory).
2. Write ONE digest summarizing all of them: what happened on each case, the cause, what was done, how it ended.
     conductor bot remember --kind digest --title "Episodes <first date> to <last date>" --text "<document>"
   Its frontmatter: title, summary (one line), period ("<first date> to <last date>"), cases (the case ids). `+"`conductor bot kinds --kind digest`"+` shows the skeleton. Keep it under the digest cap — dense, no copied logs.
3. Where something durable emerged — a fact about a system, a recurring cause, a procedure — update knowledge/ (or runbooks/, watch/): prefer editing an existing document over adding one, one topic per document, never copy secrets.
4. Do not rewrite the logs themselves. Finish with step.done naming the digest and the knowledge files you changed.

Once you finish done with a digest written, conductor archives these logs out of the working copy: recall still finds them, and the digest is what sessions see.

Logs:
- %s`, len(todo), more, strings.Join(todo, "\n- "))
}

// logBlobs is every live log entry's git blob at head.
func (r *Repo) logBlobs(ctx context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.logBlobsLocked(ctx)
}

func (r *Repo) logBlobsLocked(ctx context.Context) (map[string]string, error) {
	args := []string{"ls-tree", "-r", "HEAD", "--"}
	for _, k := range r.Kinds().List() {
		if k.Name == KindLog {
			args = append(args, k.Dir)
			args = append(args, k.Legacy...)
		}
	}
	out, err := r.git(ctx, r.Dir, args...)
	if err != nil {
		return nil, err
	}
	blobs := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		meta, p, ok := strings.Cut(l, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 || f[1] != "blob" || filepath.Base(p) == ".gitkeep" {
			continue
		}
		blobs[p] = f[2]
	}
	return blobs, nil
}

// consolidationDone is a consolidation worker's finish: done with a digest
// committed by that worker, its entries become consolidated (each at the
// content it was given) and conductor archives what it may.
func (s *Service) consolidationDone(ctx context.Context, name string, w store.Worker, status string) {
	if len(w.Consolidates) == 0 {
		return
	}
	repo := s.Repo(name)
	if status != "done" {
		s.audit(map[string]any{"event": "bot_consolidation", "bot": name, "worker": w.ID, "outcome": "not done (" + status + "): nothing consolidated"})
		return
	}
	if !repo.wroteDigest(ctx, w.Dispatch) {
		s.logf("%s: consolidation worker %s finished without writing a digest; its %d log entries stay unconsolidated", name, w.ID, len(w.Consolidates))
		s.audit(map[string]any{"event": "bot_consolidation", "bot": name, "worker": w.ID, "outcome": "no digest written: nothing consolidated"})
		return
	}
	// The consolidation's own case log (its brief and result) is covered by
	// the digest it produced: it is not one more entry for the next run.
	own := ""
	if cs := s.Ledger.Bot(name).Cases[w.CaseID]; cs != nil {
		own = repo.EpisodePath(cs.ID, cs.Title, cs.Created)
	}
	blobs, _ := repo.logBlobs(ctx)
	_ = s.Ledger.UpdateBot(name, func(r *store.BotRecord) error {
		if r.ConsolidatedDocs == nil {
			r.ConsolidatedDocs = map[string]string{}
		}
		for p, b := range w.Consolidates {
			r.ConsolidatedDocs[p] = b
		}
		if b := blobs[own]; b != "" {
			r.ConsolidatedDocs[own] = b
		}
		return nil
	})
	s.audit(map[string]any{"event": "bot_consolidation", "bot": name, "worker": w.ID, "outcome": "consolidated", "entries": len(w.Consolidates)})
	if _, err := s.Archive(ctx, name); err != nil {
		s.logf("%s: archive after consolidation %s: %v", name, w.ID, err)
	}
}

// wroteDigest reports whether a commit by the given worker dispatch added
// or changed a digest.
func (r *Repo) wroteDigest(ctx context.Context, dispatch string) bool {
	if dispatch == "" {
		return false
	}
	var dir string
	for _, k := range r.Kinds().List() {
		if k.Name == KindDigest {
			dir = k.Dir
		}
	}
	if dir == "" {
		return false
	}
	out, err := r.git(ctx, r.Dir, "log", "--fixed-strings", "--grep=Bot-Dispatch: "+dispatch, "--format=%x01%B", "--", dir)
	if err != nil {
		return false
	}
	for _, msg := range strings.Split(out, "\x01") {
		for _, l := range strings.Split(msg, "\n") {
			if strings.TrimSpace(l) == "Bot-Dispatch: "+dispatch {
				return true
			}
		}
	}
	return false
}

// Archive removes from the working tree every log entry a finished
// consolidation covered, at the content it covered, whose case is closed —
// one commit. It returns the archived paths.
func (s *Service) Archive(ctx context.Context, name string) ([]string, error) {
	m, err := s.bot(name)
	if err != nil {
		return nil, err
	}
	if !m.Memory.ArchiveOn() {
		return nil, nil
	}
	rec := s.Ledger.Bot(name)
	repo := s.Repo(name)
	want := archivable(rec, repo, s.now())
	if len(want) == 0 {
		return nil, nil
	}
	paths, err := repo.ArchiveIf(ctx, want, map[string]string{"Bot-Archive": fmt.Sprintf("%d consolidated log entries", len(want))})
	if err != nil {
		return nil, err
	}
	_ = s.Ledger.UpdateBot(name, func(r *store.BotRecord) error {
		for _, p := range paths {
			delete(r.ConsolidatedDocs, p)
		}
		return nil
	})
	if len(paths) > 0 {
		s.audit(map[string]any{"event": "bot_memory_archive", "bot": name, "entries": len(paths), "commit": repo.Head(ctx)})
		s.logf("%s: archived %d consolidated log entries", name, len(paths))
	}
	return paths, nil
}

// archivable is what the ledger allows to leave the working copy: a
// consolidated entry (path → the blob it was consolidated at) whose case is
// closed, or a sweep log of a past day. ArchiveIf re-checks each blob under
// the repo's lock.
func archivable(rec store.BotRecord, repo *Repo, now time.Time) map[string]string {
	want := map[string]string{}
	today := now.Format("2006-01-02")
	for p, b := range rec.ConsolidatedDocs {
		text, err := repo.Read(p)
		if err != nil {
			continue
		}
		fm, _, _, _ := SplitFrontmatter(text)
		if c := fmString(fm["case"]); c != "" {
			if cs := rec.Cases[c]; cs != nil && !store.Closed(cs.Status) {
				continue // its case may still append to it
			}
		}
		if fmString(fm["sweep"]) != "" && strings.HasPrefix(filepath.Base(p), today) {
			continue // today's sweep log still grows
		}
		want[p] = b
	}
	return want
}

// ArchiveIf removes, in one commit, each path whose blob at head is still
// exactly want[path] — checked under the repo's lock, so nothing appended
// meanwhile is archived. It returns what it removed.
func (r *Repo) ArchiveIf(ctx context.Context, want map[string]string, trailers map[string]string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	blobs, err := r.logBlobsLocked(ctx)
	if err != nil {
		return nil, err
	}
	var paths []string
	for p, b := range want {
		if b != "" && blobs[p] == b {
			if _, err := os.Stat(filepath.Join(r.Dir, p)); err == nil {
				paths = append(paths, p)
			}
		}
	}
	if len(paths) == 0 {
		return nil, nil
	}
	sort.Strings(paths)
	if _, err := r.git(ctx, r.Dir, append([]string{"rm", "-q", "--"}, paths...)...); err != nil {
		return nil, err
	}
	msg := commitMessage(fmt.Sprintf("memory: archive %d consolidated log entries", len(paths)), trailers)
	if _, err := r.git(ctx, r.Dir, append([]string{"commit", "-q", "-m", msg, "--"}, paths...)...); err != nil {
		_, _ = r.git(ctx, r.Dir, append([]string{"reset", "-q", "HEAD", "--"}, paths...)...)
		_, _ = r.git(ctx, r.Dir, append([]string{"checkout", "-q", "HEAD", "--"}, paths...)...)
		return nil, err
	}
	r.refreshLocked(ctx)
	r.indexCommitLocked(ctx)
	return paths, nil
}
