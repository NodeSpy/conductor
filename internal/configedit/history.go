package configedit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// historyDir is the directory, beside the config file, where pre-apply backups
// are kept. One file per apply, named <RFC3339-ish>-<sha>.yaml, so history is
// just a directory listing (no index to corrupt).
const historyDir = ".conductor-config-history"

// HistoryEntry is one restorable backup.
type HistoryEntry struct {
	ID    string `json:"id"`    // the backup filename stem
	TS    string `json:"ts"`    // RFC3339 time the backup was taken
	SHA   string `json:"sha"`   // short sha256 of the backed-up content
	Bytes int64  `json:"bytes"` // size of the backed-up file
}

// backup copies the current config content into the history dir and returns its
// id. Best-effort pruning follows (keepBackup).
func (p *Pipeline) backup(content []byte) (string, error) {
	dir := filepath.Join(filepath.Dir(p.path), historyDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])[:12]
	ts := p.now().UTC()
	id := fmt.Sprintf("%s-%s", ts.Format("20060102T150405Z"), sha)
	name := filepath.Join(dir, id+".yaml")
	if err := os.WriteFile(name, content, 0o600); err != nil {
		return "", err
	}
	p.pruneBackups(dir)
	return id, nil
}

// RETENTION keeps the newest maxBackups, and the oldest backup of each of the
// last keepDays UTC days. A backup holds the config as it was before an apply,
// so a day's oldest is how it stood when that day's changes began: a burst of
// applies (a stolen session in config_stepup: guarded can make some) prunes
// only within the newest 50 and that day, never the earlier days' starting
// points.
const (
	maxBackups = 50
	keepDays   = 30
)

func (p *Pipeline) pruneBackups(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var files []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			files = append(files, e.Name())
		}
	}
	if len(files) <= maxBackups {
		return
	}
	sort.Strings(files) // timestamp-prefixed → chronological
	keep := map[string]bool{}
	for _, f := range files[len(files)-maxBackups:] {
		keep[f] = true
	}
	oldest := p.now().UTC().AddDate(0, 0, -keepDays).Format("20060102")
	seenDay := map[string]bool{}
	for _, f := range files {
		day := f[:min(8, len(f))]
		if day >= oldest && !seenDay[day] {
			seenDay[day] = true
			keep[f] = true
		}
	}
	for _, f := range files {
		if !keep[f] {
			_ = os.Remove(filepath.Join(dir, f))
		}
	}
}

// History lists the backups, newest first.
func (p *Pipeline) History() ([]HistoryEntry, error) {
	dir := filepath.Join(filepath.Dir(p.path), historyDir)
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []HistoryEntry
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".yaml")
		he := HistoryEntry{ID: id}
		if i := strings.LastIndex(id, "-"); i >= 0 {
			he.SHA = id[i+1:]
			if t, err := time.Parse("20060102T150405Z", id[:i]); err == nil {
				he.TS = t.UTC().Format(time.RFC3339)
			}
		}
		if fi, err := e.Info(); err == nil {
			he.Bytes = fi.Size()
		}
		out = append(out, he)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// backupContent returns a backup's bytes.
func (p *Pipeline) backupContent(id string) ([]byte, error) {
	if strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return nil, fmt.Errorf("configedit: invalid backup id %q", id)
	}
	name := filepath.Join(filepath.Dir(p.path), historyDir, id+".yaml")
	return os.ReadFile(name)
}

// RestoreReview is the unowned RestoreReview.
func (p *Pipeline) RestoreReview(id string) (Review, []byte, error) {
	return p.As("").RestoreReview(id)
}

// Restore is the unowned Restore.
func (p *Pipeline) Restore(id, token string) (Applied, error) { return p.As("").Restore(id, token) }

// RestoreReview renders the diff from restoring a backup over the current file,
// and mints a confirm token bound to the owner and that candidate — the same
// review shape as a staged edit, so restore flows through the identical apply
// gate.
func (e Edits) RestoreReview(id string) (Review, []byte, error) {
	p := e.p
	p.mu.Lock()
	defer p.mu.Unlock()
	before, err := p.current()
	if err != nil {
		return Review{}, nil, err
	}
	after, err := p.backupContent(id)
	if err != nil {
		return Review{}, nil, err
	}
	diff := ComputeDiff(before, after)
	sum := sha256.Sum256(after)
	sha := hex.EncodeToString(sum[:])
	tok := randID()
	p.tokens.Store(tok, tokenBind{owner: e.owner, sha: sha})
	widening, guarded, leaves := p.classifyLocked(before, after)
	rev := Review{
		Changes: leaves,
		Diff:    diff,
		SHA:     sha[:12],
		Token:   tok,
		Effects: []Effect{{
			Path: "(restore)",
			Text: fmt.Sprintf("restores the config to backup %s", id),
		}},
		Widening: widening,
		Guarded:  guarded,
		Stepup:   true, // a restore rewrites the whole file: always confirmed
	}
	return rev, after, nil
}

// Restore validates a backup, backs up the current file, writes the backup over
// the live config, and reloads — reusing the whole apply contract. token must be
// the one minted by the matching RestoreReview.
func (e Edits) Restore(id, token string) (Applied, error) {
	p := e.p
	p.mu.Lock()
	defer p.mu.Unlock()

	before, err := p.current()
	if err != nil {
		return Applied{}, err
	}
	after, err := p.backupContent(id)
	if err != nil {
		return Applied{}, err
	}
	sum := sha256.Sum256(after)
	sha := hex.EncodeToString(sum[:])
	if !p.tokenFor(token, e.owner, sha) {
		return Applied{}, ErrStale
	}
	if !ComputeDiff(before, after).Changed {
		p.tokens.Delete(token)
		return Applied{SHA: sha[:12], Unchanged: true}, nil
	}
	if err := p.validateCandidate(after); err != nil {
		return Applied{}, fmt.Errorf("the backup does not validate against this binary: %w", err)
	}
	backupID, err := p.backup(before)
	if err != nil {
		return Applied{}, fmt.Errorf("backup before restore: %w", err)
	}
	if err := writeAtomic(p.path, after); err != nil {
		return Applied{}, err
	}
	p.tokens.Delete(token)
	delete(p.staged, e.owner)
	reloadErr := ""
	if err := p.reload(); err != nil {
		reloadErr = err.Error()
	}
	return Applied{SHA: sha[:12], BackupID: backupID, ReloadError: reloadErr}, nil
}
