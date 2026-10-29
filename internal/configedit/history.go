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
// id. Best-effort pruning keeps the most recent maxBackups.
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

const maxBackups = 50

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
	for _, f := range files[:len(files)-maxBackups] {
		_ = os.Remove(filepath.Join(dir, f))
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

// RestoreReview renders the diff from restoring a backup over the current file,
// and mints a confirm token bound to that candidate — the same review shape as a
// staged edit, so restore flows through the identical apply gate.
func (p *Pipeline) RestoreReview(id string) (Review, []byte, error) {
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
	p.tokens.Store(tok, sha)
	widening, guarded := p.classifyLocked(before, after)
	rev := Review{
		Diff:  diff,
		SHA:   sha[:12],
		Token: tok,
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
func (p *Pipeline) Restore(id, token string) (Applied, error) {
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
	v, ok := p.tokens.Load(token)
	if !ok || v.(string) != sha {
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
	p.staged = nil
	reloadErr := ""
	if err := p.reload(); err != nil {
		reloadErr = err.Error()
	}
	return Applied{SHA: sha[:12], BackupID: backupID, ReloadError: reloadErr}, nil
}
