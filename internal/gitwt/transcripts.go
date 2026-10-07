package gitwt

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultTranscriptRetention is how long an agent transcript outlives the
// worktree it was recorded in. Long enough to post-mortem a run from the
// previous day ("why did conductor do X on PR N"), short enough that a daemon
// handling dozens of dispatches a day doesn't grow the directory without bound.
const DefaultTranscriptRetention = 72 * time.Hour

// ClaudeProjectsDir is where Claude Code keeps its per-cwd session transcripts:
// $CLAUDE_CONFIG_DIR/projects, else ~/.claude/projects. "" when neither
// resolves (no home dir), which leaves transcript reaping off.
func ClaudeProjectsDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "projects")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// claudeSlug is the directory name Claude Code files a cwd's sessions under:
// the absolute path with every non-alphanumeric rune folded to '-'
// ("/home/u/.local/state/conductor/worktrees/x" → "-home-u--local-state-
// conductor-worktrees-x"). Claude truncates very long paths and appends a hash;
// the leading part — all the reaper's prefix match needs — survives that.
func claudeSlug(path string) string {
	var b strings.Builder
	for _, r := range path {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// reapTranscripts removes the transcript dirs agents recorded in worktrees
// that no longer exist. A `claude -p` fixer runs with its cwd in
// <state>/worktrees/<id>, so every dispatch leaves a
// <TranscriptsDir>/<slug of that path> behind after RemoveWorktree has taken
// the checkout itself — hundreds a week on a busy daemon.
//
// Only dirs whose name starts with the slug of THIS provisioner's worktrees dir
// are considered, a dir whose worktree is still on disk is skipped, and nothing
// is removed until its newest entry is older than the retention window.
func (p *Provisioner) reapTranscripts() {
	if p.TranscriptsDir == "" {
		return
	}
	ents, err := os.ReadDir(p.TranscriptsDir)
	if err != nil {
		return
	}
	prefix := claudeSlug(filepath.Clean(p.WorktreesDir())) + "-"
	present := map[string]bool{}
	if wts, err := os.ReadDir(p.WorktreesDir()); err == nil {
		for _, w := range wts {
			present[claudeSlug(filepath.Join(p.WorktreesDir(), w.Name()))] = true
		}
	}
	cutoff := p.now().Add(-p.transcriptRetention())
	removed := 0
	for _, e := range ents {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, prefix) || present[name] {
			continue
		}
		dir := filepath.Join(p.TranscriptsDir, name)
		if newestMod(dir).After(cutoff) {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			p.logf("gitwt: reap transcript %s: %v", dir, err)
			continue
		}
		removed++
	}
	if removed > 0 {
		p.logf("gitwt: reaped %d agent transcript dir(s) from %s", removed, p.TranscriptsDir)
	}
}

// newestMod is the latest mtime of dir and its direct children. A transcript
// is appended to in place, which bumps the file but not the dir holding it.
func newestMod(dir string) time.Time {
	var newest time.Time
	if fi, err := os.Stat(dir); err == nil {
		newest = fi.ModTime()
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	return newest
}

func (p *Provisioner) transcriptRetention() time.Duration {
	if p.TranscriptRetention > 0 {
		return p.TranscriptRetention
	}
	return DefaultTranscriptRetention
}
