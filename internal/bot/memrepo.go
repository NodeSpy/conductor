package bot

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// THE MEMORY REPO. A bot's long-term memory is a plain git repository:
// knowledge (durable facts, the infra map, runbooks), an episodic log (what
// happened on each case and what was done), and handoff notes between home
// sessions. Every write goes through conductor and lands as ONE commit with
// the bot as author and the case/session as trailers — so memory is
// reviewable (`git log`), attributable, and correctable (an operator's own
// commit, or a revert). The bot's agents never commit; they ask
// conductor to.
//
// Sessions read memory through their own working directory: every live
// session (home or worker) gets a DETACHED worktree of this repo at its own
// path, so no two live agents ever share a cwd (the sandbox keys and archives by
// cwd). Conductor fast-forwards clean session worktrees after each commit.

// Layout of a memory repo: each built-in kind's home (kinds.go), and
// episodes/, the log kind's legacy home (read, never written).
const (
	DirKnowledge = "knowledge"
	DirRunbooks  = "runbooks"
	DirWatch     = "watch"
	DirLog       = "log"
	DirEpisodes  = "episodes"
	DirHandoff   = "handoff"
)

// Repo is one bot's memory repo.
type Repo struct {
	Dir string
	Bot string
	Now func() time.Time
	// Seed is copied into a NEW repo's first commit (knowledge/ and
	// runbooks/ Markdown only); "" seeds nothing.
	Seed string
	// Team marks a TEAM knowledge repo (Bot is then the team's name): only
	// knowledge/ and runbooks/, written only by promotion, each commit
	// authored by the bot whose memory it came from.
	Team bool
	// KindsFn is the repo's content kinds (built-ins plus its collections);
	// nil: the built-ins alone.
	KindsFn func() *Kinds
	// SeedSkipped lists seed files Init did not copy (not a valid document
	// of any kind here), with why — reported, never silently dropped.
	SeedSkipped []string
	// IndexPath is the recall index (memindex.go), in conductor's state dir;
	// "" keeps it in memory for this handle's life. LimitsFn is the repo's
	// memory limits (nil: the defaults).
	IndexPath string
	LimitsFn  func() config.MemoryLimits
	// TemplateFn is a template bot's provenance (nil otherwise): a NEW
	// repo's first commit carries the template's charter and names it.
	TemplateFn func() *config.BotOrigin

	mu    sync.Mutex
	idxMu sync.Mutex // the index: its sync and its queries
	idx   *sql.DB
	// rebuilds counts full index rebuilds (catch-ups are incremental).
	rebuilds int
}

// Kinds is the repo's content kinds.
func (r *Repo) Kinds() *Kinds {
	if r.KindsFn != nil {
		if ks := r.KindsFn(); ks != nil {
			return ks
		}
	}
	return NewKinds(r.Team, nil)
}

func (r *Repo) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// git runs one git command in the repo, isolated from the operator's own git
// configuration's side effects (signing prompts, hooks). Git's automatic
// maintenance after a write runs inside the command rather than detached, so
// nothing is still writing to the repo once the command returns.
func (r *Repo) git(ctx context.Context, dir string, args ...string) (string, error) {
	return r.gitAs(ctx, r.Bot, dir, args...)
}

// Ident is a commit author.
type Ident struct{ Name, Email string }

// Commit-author email domains: how history tells who wrote a commit.
const (
	botDomain      = "@bots.conductor.invalid"
	teamDomain     = "@teams.conductor.invalid"
	operatorDomain = "@operators.conductor.invalid"
)

// BotIdent / OperatorIdent are the authors of a bot's and an operator's
// commits.
func BotIdent(bot string) Ident     { return Ident{"bot " + bot, bot + botDomain} }
func OperatorIdent(op string) Ident { return Ident{"operator " + op, op + operatorDomain} }

// gitAs is git with the given bot as the author.
func (r *Repo) gitAs(ctx context.Context, author, dir string, args ...string) (string, error) {
	id := BotIdent(author)
	if r.Team && author == r.Bot {
		id = Ident{"team " + r.Bot, r.Bot + teamDomain}
	}
	return r.gitIdent(ctx, id, dir, args...)
}

// gitIdent is git with id as the author.
func (r *Repo) gitIdent(ctx context.Context, id Ident, dir string, args ...string) (string, error) {
	full := append([]string{"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "-c", "core.hooksPath=/dev/null", "-c", "init.defaultBranch=main", "-c", "maintenance.autoDetach=false", "-c", "gc.autoDetach=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	name, email := id.Name, id.Email
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+email,
		"GIT_COMMITTER_NAME=conductor", "GIT_COMMITTER_EMAIL=conductor@bots.conductor.invalid",
		"GIT_TERMINAL_PROMPT=0")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out.String(), nil
}

// Init creates the repo (and its first commit) if it does not exist yet. An
// existing repo is left as it is — it may be one the operator seeded.
func (r *Repo) Init(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := os.Stat(filepath.Join(r.Dir, ".git")); err == nil {
		return nil
	}
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return err
	}
	if _, err := r.git(ctx, r.Dir, "init", "-q", "-b", "main"); err != nil {
		return err
	}
	readme := fmt.Sprintf(`# Memory of bot %s

Written by conductor on the bot's behalf; every change is one commit.
Correct anything by committing over it — the bot reads what is here.

Every file is a typed document with YAML frontmatter, checked by conductor
(`+"`conductor bot kinds`"+` lists the schemas):

- knowledge/  durable facts (title, applies_to, evidence, confidence)
- runbooks/   procedures (title, when_to_use, prerequisites, steps, verification, rollback, owner)
- watch/      what to keep an eye on (what, signal, threshold, why)
- log/        what happened on each case (conductor writes it; operators annotate)
- handoff/    notes one home session leaves for the next (conductor writes it)
`, r.Bot)
	if r.Team {
		readme = fmt.Sprintf(`# Knowledge of team %s

Shared by every bot on the team: each reads it, none writes it directly. A
bot PROPOSES promoting something from its own memory; it lands here as one
commit authored by that bot, once an operator of the team approves it (or
at once, with knowledge.promotion: direct). Correct anything by committing
over it.

- knowledge/  durable facts the whole team relies on
- runbooks/   procedures the whole team follows
- watch/      what the team keeps an eye on
`, r.Bot)
	}
	if err := os.WriteFile(filepath.Join(r.Dir, "README.md"), []byte(readme), 0o600); err != nil {
		return err
	}
	for _, k := range r.Kinds().List() {
		d := k.Dir
		if err := os.MkdirAll(filepath.Join(r.Dir, d), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(r.Dir, d, ".gitkeep"), nil, 0o600); err != nil {
			return err
		}
	}
	if err := r.seed(); err != nil {
		return fmt.Errorf("seed %s: %w", r.Seed, err)
	}
	var o *config.BotOrigin
	if r.TemplateFn != nil && !r.Team {
		o = r.TemplateFn()
	}
	if o != nil && o.Charter != "" {
		if err := r.writeCharterLocked(o, o.Charter); err != nil {
			return err
		}
	}
	if _, err := r.git(ctx, r.Dir, "add", "-A"); err != nil {
		return err
	}
	subject := "memory: initialize"
	if r.Team {
		subject = "knowledge: initialize"
	}
	if o != nil {
		subject = commitMessage(fmt.Sprintf("memory: initialize from template %s (%s %s)", o.Ref(), o.Pack, o.Version),
			map[string]string{"Bot-Template": o.Ref() + "@" + o.Version, "Template-Digest": o.Digest})
	}
	_, err := r.git(ctx, r.Dir, "commit", "-q", "-m", subject)
	return err
}

// seed copies the seed directory's documents into a new repo: every file
// under a kind's home that is a valid document of that kind — the same rule
// as any write (WriterSystem aside, which only conductor's own kinds take).
// Regular files only (no symlinks — a seed cannot point the repo at files
// outside it), each under the write-size cap. A file that is not a valid
// document is skipped and listed in SeedSkipped, never silently dropped.
func (r *Repo) seed() error {
	if r.Seed == "" {
		return nil
	}
	ks := r.Kinds()
	return filepath.WalkDir(r.Seed, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(r.Seed, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.Contains(rel, "/") {
			return nil // top-level files (the pack's README, its manifest) are not memory
		}
		k, ok := ks.Of(rel)
		if !ok {
			return nil // not under any kind's home: not memory
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if len(b) > 256<<10 {
			return fmt.Errorf("%s is larger than 256KiB", rel)
		}
		if err := k.CheckPath(rel); err != nil {
			r.SeedSkipped = append(r.SeedSkipped, err.Error())
			return nil
		}
		if err := k.Validate(string(b)); err != nil {
			r.SeedSkipped = append(r.SeedSkipped, rel+": "+err.Error())
			return nil
		}
		if err := k.CheckSize(string(b)); err != nil {
			r.SeedSkipped = append(r.SeedSkipped, rel+": "+err.Error())
			return nil
		}
		dst := filepath.Join(r.Dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o600)
	})
}

// CharterDoc renders a template bot's charter document.
func CharterDoc(ks *Kinds, o *config.BotOrigin, charter string) string {
	k, err := ks.Get(KindCharter)
	if err != nil {
		return charter
	}
	return k.JoinFrontmatter(map[string]any{"template": o.Ref()}, strings.TrimSpace(charter)+"\n")
}

func (r *Repo) writeCharterLocked(o *config.BotOrigin, charter string) error {
	p := filepath.Join(r.Dir, config.CharterDocPath)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(CharterDoc(r.Kinds(), o, charter)), 0o600)
}

// CleanPath validates the shape of a memory path an agent or operator
// names: relative, no traversal, no hidden segment, a Markdown or text file.
// Where it may be is the kinds' business (Repo.cleanPath, Kinds).
func CleanPath(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "\\") {
		return "", fmt.Errorf("memory path %q must be relative", rel)
	}
	c := filepath.Clean(rel)
	if c != rel || c == "." || strings.HasPrefix(c, "..") || strings.Contains(c, "/../") {
		return "", fmt.Errorf("memory path %q is not clean", rel)
	}
	for _, seg := range strings.Split(c, "/") {
		if strings.HasPrefix(seg, ".") {
			return "", fmt.Errorf("memory path %q has a hidden segment", rel)
		}
	}
	if ext := filepath.Ext(c); ext != ".md" && ext != ".txt" {
		return "", fmt.Errorf("memory path %q must be a .md or .txt file", rel)
	}
	return c, nil
}

// Write records one document and commits it, as written by w — through
// the kinds' chokepoint (Kinds.CheckWrite). mode "replace" overwrites,
// "append" appends to the body (with a separating newline). trailers
// become commit trailers.
func (r *Repo) Write(ctx context.Context, w Writer, rel, text, mode, subject string, trailers map[string]string) (string, error) {
	return r.WriteAs(ctx, r.Bot, w, rel, text, mode, subject, trailers)
}

// WriteAs is Write with author as the commit's author (a team repo's
// promotion is authored by the promoting bot).
func (r *Repo) WriteAs(ctx context.Context, author string, w Writer, rel, text, mode, subject string, trailers map[string]string) (string, error) {
	id := BotIdent(author)
	if r.Team && author == r.Bot {
		id = Ident{"team " + r.Bot, r.Bot + teamDomain}
	}
	return r.WriteIdent(ctx, id, w, rel, text, mode, subject, trailers)
}

// WriteIdent is Write with id as the commit's author (an approved promotion
// the operator edited is authored by the operator).
func (r *Repo) WriteIdent(ctx context.Context, id Ident, w Writer, rel, text, mode, subject string, trailers map[string]string) (string, error) {
	c, err := r.cleanPath(rel)
	if err != nil {
		return "", err
	}
	if len(text) > 256<<10 {
		return "", fmt.Errorf("memory write too large (%d bytes, max 256KiB)", len(text))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	abs := filepath.Join(r.Dir, c)
	if mode == "append" {
		prev, _ := os.ReadFile(abs)
		if len(prev) > 0 && !bytes.HasSuffix(prev, []byte("\n")) {
			prev = append(prev, '\n')
		}
		if len(prev) > 0 {
			prev = append(prev, '\n')
		}
		text = string(prev) + text
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	// The chokepoint: what the file will hold, where, by whom.
	if _, err := r.Kinds().CheckWrite(c, text, w, ""); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, []byte(text), 0o600); err != nil {
		return "", err
	}
	if _, err := r.git(ctx, r.Dir, "add", "--", c); err != nil {
		return "", err
	}
	if out, _ := r.git(ctx, r.Dir, "status", "--porcelain", "--", c); strings.TrimSpace(out) == "" {
		return r.headLocked(ctx), nil // nothing changed
	}
	msg := subject
	if strings.TrimSpace(msg) == "" {
		msg = "memory: update " + c
	}
	msg = commitMessage(msg, trailers)
	if _, err := r.gitIdent(ctx, id, r.Dir, "commit", "-q", "-m", msg, "--", c); err != nil {
		return "", err
	}
	head := r.headLocked(ctx)
	r.refreshLocked(ctx)
	r.indexCommitLocked(ctx)
	return head, nil
}

// indexCommitLocked brings the recall index up to a commit conductor just
// made. A failure is not the write's: the next query syncs again, and
// reports it.
func (r *Repo) indexCommitLocked(ctx context.Context) {
	_ = r.SyncIndex(ctx)
}

// AppendLog appends an entry to a conductor-composed document (a case's log,
// a sweep's day): a new file starts with fm as its frontmatter. Written as
// WriterSystem, through the same chokepoint.
func (r *Repo) AppendLog(ctx context.Context, rel string, fm map[string]any, entry, subject string, trailers map[string]string) (string, error) {
	k, ok := r.Kinds().Of(rel)
	if !ok {
		return "", fmt.Errorf("%s is not in any kind's home", rel)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, rel)); os.IsNotExist(err) {
		return r.Write(ctx, WriterSystem, rel, k.JoinFrontmatter(fm, entry), "replace", subject, trailers)
	}
	return r.Write(ctx, WriterSystem, rel, entry, "append", subject, trailers)
}

// commitMessage is a one-line subject and its trailers, sorted.
func commitMessage(subject string, trailers map[string]string) string {
	msg := firstLine(subject)
	if len(trailers) > 0 {
		keys := make([]string, 0, len(trailers))
		for k := range trailers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		msg += "\n"
		for _, k := range keys {
			if v := strings.TrimSpace(trailers[k]); v != "" {
				msg += "\n" + k + ": " + firstLine(v)
			}
		}
	}
	return msg
}

func (r *Repo) headLocked(ctx context.Context) string {
	out, _ := r.git(ctx, r.Dir, "rev-parse", "--short", "HEAD")
	return strings.TrimSpace(out)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slug renders a title into a path-safe slug.
func Slug(s string) string {
	s = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 48 {
		s = strings.Trim(s[:48], "-")
	}
	if s == "" {
		s = "note"
	}
	return s
}

// EpisodePath is where a case's log entry lives (the log kind).
func (r *Repo) EpisodePath(caseID, title string, created time.Time) string {
	return filepath.Join(DirLog, created.Format("2006"), created.Format("01"),
		created.Format("2006-01-02")+"-"+caseID+"-"+Slug(title)+".md")
}

// cleanPath is CleanPath, held to the repo's kinds: in some kind's home,
// or a legacy home (read only — writes are checked again by CheckWrite).
func (r *Repo) cleanPath(rel string) (string, error) {
	c, err := CleanPath(rel)
	if err != nil {
		return "", err
	}
	if _, ok := r.Kinds().Of(c); !ok {
		return "", fmt.Errorf("memory path %q is not in any kind's home (%s)", rel, r.Kinds().homes())
	}
	return c, nil
}

// Head is the repo's current commit (short).
func (r *Repo) Head(ctx context.Context) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.headLocked(ctx)
}

// Read returns a memory file's text.
func (r *Repo) Read(rel string) (string, error) {
	c, err := r.cleanPath(rel)
	if err != nil {
		if rel != "README.md" {
			return "", err
		}
		c = rel
	}
	b, err := os.ReadFile(filepath.Join(r.Dir, c))
	return string(b), err
}

// List returns the memory files under a directory (relative paths, sorted).
func (r *Repo) List(dir string) []string {
	var out []string
	root := filepath.Join(r.Dir, dir)
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || strings.HasPrefix(info.Name(), ".") {
			return nil
		}
		rel, _ := filepath.Rel(r.Dir, p)
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out
}

// LatestHandoff returns the newest handoff note ("" when there is none).
func (r *Repo) LatestHandoff() (string, string) {
	files := r.List(DirHandoff)
	if len(files) == 0 {
		return "", ""
	}
	// Names start with a sortable timestamp.
	p := files[len(files)-1]
	b, _ := os.ReadFile(filepath.Join(r.Dir, p))
	return p, string(b)
}

// HandoffPath names a new handoff note for a session.
func (r *Repo) HandoffPath(session string) string {
	return filepath.Join(DirHandoff, r.now().UTC().Format("20060102T150405Z")+"-"+session+".md")
}

// Log returns recent commit subjects (for `conductor bot show`).
func (r *Repo) Log(ctx context.Context, n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out, err := r.git(ctx, r.Dir, "log", fmt.Sprintf("-%d", n), "--pretty=format:%h %ad %s", "--date=short")
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(out), "\n")
}

// AddWorktree creates a detached worktree of main at path — one live
// session's own cwd.
func (r *Repo) AddWorktree(ctx context.Context, path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("session dir %s already exists — refusing to share a cwd", path)
	}
	_, err := r.git(ctx, r.Dir, "worktree", "add", "-q", "--detach", path, "main")
	return err
}

// RemoveWorktree deletes a session worktree. Missing is not an error.
func (r *Repo) RemoveWorktree(ctx context.Context, path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		_, _ = r.git(ctx, r.Dir, "worktree", "prune")
		return nil
	}
	if _, err := r.git(ctx, r.Dir, "worktree", "remove", "--force", path); err != nil {
		if rerr := os.RemoveAll(path); rerr != nil {
			return err
		}
		_, _ = r.git(ctx, r.Dir, "worktree", "prune")
	}
	return nil
}

// Worktrees lists this repo's session worktrees (excluding the main one).
func (r *Repo) Worktrees(ctx context.Context) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.worktreesLocked(ctx)
}

func (r *Repo) worktreesLocked(ctx context.Context) []string {
	out, err := r.git(ctx, r.Dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}
	main, _ := filepath.EvalSymlinks(r.Dir)
	var wts []string
	for _, l := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(l, "worktree "); ok {
			ep, _ := filepath.EvalSymlinks(p)
			if ep != main && p != r.Dir {
				wts = append(wts, p)
			}
		}
	}
	return wts
}

// refreshLocked moves every CLEAN session worktree to the new main, so a live
// session reads what was just committed. A worktree with local edits is left
// alone (the session is working in it).
func (r *Repo) refreshLocked(ctx context.Context) {
	for _, wt := range r.worktreesLocked(ctx) {
		if out, err := r.git(ctx, wt, "status", "--porcelain"); err != nil || strings.TrimSpace(out) != "" {
			continue
		}
		_, _ = r.git(ctx, wt, "checkout", "-q", "--detach", "main")
	}
}
