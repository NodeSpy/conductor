package member

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// THE MEMORY REPO. A member's long-term memory is a plain git repository:
// knowledge (durable facts, the infra map, runbooks), an episodic log (what
// happened on each case and what was done), and handoff notes between home
// sessions. Every write goes through conductor and lands as ONE commit with
// the member as author and the case/session as trailers — so memory is
// reviewable (`git log`), attributable, and correctable (an operator's own
// commit, or a revert). The member's agents never commit; they ask
// conductor to.
//
// Sessions read memory through their own working directory: every live
// session (home or worker) gets a DETACHED worktree of this repo at its own
// path, so no two live agents ever share a cwd (the jail keys and archives by
// cwd). Conductor fast-forwards clean session worktrees after each commit.

// Layout of a memory repo.
const (
	DirKnowledge = "knowledge"
	DirRunbooks  = "runbooks"
	DirEpisodes  = "episodes"
	DirHandoff   = "handoff"
)

var writableDirs = []string{DirKnowledge, DirRunbooks, DirEpisodes, DirHandoff}

// Repo is one member's memory repo.
type Repo struct {
	Dir    string
	Member string
	Now    func() time.Time

	mu sync.Mutex
}

func (r *Repo) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// git runs one git command in the repo, isolated from the operator's own git
// configuration's side effects (signing prompts, hooks).
func (r *Repo) git(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "-c", "core.hooksPath=/dev/null", "-c", "init.defaultBranch=main"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	name := "member " + r.Member
	email := r.Member + "@members.conductor.invalid"
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+email,
		"GIT_COMMITTER_NAME=conductor", "GIT_COMMITTER_EMAIL=conductor@members.conductor.invalid",
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
	readme := fmt.Sprintf(`# Memory of member %s

Written by conductor on the member's behalf; every change is one commit.
Correct anything by committing over it — the member reads what is here.

- knowledge/  durable facts: the infra map, how things work, learned lessons
- runbooks/   procedures the member follows
- episodes/   what happened on each case and what was done (episodic log)
- handoff/    notes one home session leaves for the next
`, r.Member)
	if err := os.WriteFile(filepath.Join(r.Dir, "README.md"), []byte(readme), 0o600); err != nil {
		return err
	}
	for _, d := range writableDirs {
		if err := os.MkdirAll(filepath.Join(r.Dir, d), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(r.Dir, d, ".gitkeep"), nil, 0o600); err != nil {
			return err
		}
	}
	if _, err := r.git(ctx, r.Dir, "add", "-A"); err != nil {
		return err
	}
	_, err := r.git(ctx, r.Dir, "commit", "-q", "-m", "memory: initialize")
	return err
}

// CleanPath validates a memory path an agent names: relative, inside one of
// the writable directories, no traversal, a Markdown or text file.
func CleanPath(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "\\") {
		return "", fmt.Errorf("memory path %q must be relative", rel)
	}
	c := filepath.Clean(rel)
	if c != rel || strings.HasPrefix(c, "..") || strings.Contains(c, "/../") {
		return "", fmt.Errorf("memory path %q is not clean", rel)
	}
	top := strings.SplitN(c, "/", 2)[0]
	ok := false
	for _, d := range writableDirs {
		if top == d && c != d {
			ok = true
		}
	}
	if !ok {
		return "", fmt.Errorf("memory path %q must be under one of %s", rel, strings.Join(writableDirs, "/, ")+"/")
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

// Write records one file and commits it. mode "replace" overwrites, "append"
// appends (with a separating newline). trailers become commit trailers.
func (r *Repo) Write(ctx context.Context, rel, text, mode, subject string, trailers map[string]string) (string, error) {
	c, err := CleanPath(rel)
	if err != nil {
		return "", err
	}
	if len(text) > 256<<10 {
		return "", fmt.Errorf("memory write too large (%d bytes, max 256KiB)", len(text))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	abs := filepath.Join(r.Dir, c)
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return "", err
	}
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
	msg = firstLine(msg)
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
	if _, err := r.git(ctx, r.Dir, "commit", "-q", "-m", msg, "--", c); err != nil {
		return "", err
	}
	head := r.headLocked(ctx)
	r.refreshLocked(ctx)
	return head, nil
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

// EpisodePath is where a case's episode lives.
func (r *Repo) EpisodePath(caseID, title string, created time.Time) string {
	return filepath.Join(DirEpisodes, created.Format("2006"), created.Format("01"),
		created.Format("2006-01-02")+"-"+caseID+"-"+Slug(title)+".md")
}

// Read returns a memory file's text.
func (r *Repo) Read(rel string) (string, error) {
	c, err := CleanPath(rel)
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

// Search returns (path, line) hits for a case-insensitive substring across
// knowledge, runbooks and episodes, newest episodes first, capped.
func (r *Repo) Search(query string, limit int) []map[string]string {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	if limit <= 0 {
		limit = 20
	}
	var out []map[string]string
	for _, d := range []string{DirKnowledge, DirRunbooks, DirEpisodes, DirHandoff} {
		files := r.List(d)
		if d == DirEpisodes || d == DirHandoff {
			sort.Sort(sort.Reverse(sort.StringSlice(files)))
		}
		for _, f := range files {
			b, err := os.ReadFile(filepath.Join(r.Dir, f))
			if err != nil {
				continue
			}
			for _, line := range strings.Split(string(b), "\n") {
				if strings.Contains(strings.ToLower(line), q) {
					out = append(out, map[string]string{"path": f, "line": strings.TrimSpace(line)})
					if len(out) >= limit {
						return out
					}
				}
			}
		}
	}
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

// EpisodesSince lists episode files committed after t.
func (r *Repo) EpisodesSince(ctx context.Context, t time.Time) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	args := []string{"log", "--name-only", "--pretty=format:", "--", DirEpisodes}
	if !t.IsZero() {
		args = append(args[:1], append([]string{"--since=" + t.UTC().Format(time.RFC3339)}, args[1:]...)...)
	}
	out, err := r.git(ctx, r.Dir, args...)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var files []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !seen[l] && !strings.HasSuffix(l, ".gitkeep") {
			if _, err := os.Stat(filepath.Join(r.Dir, l)); err == nil {
				seen[l] = true
				files = append(files, l)
			}
		}
	}
	sort.Strings(files)
	return files
}

// Log returns recent commit subjects (for `conductor member show`).
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
