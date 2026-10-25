package configedit

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Pipeline is the stateful config-edit surface the dashboard drives: it holds a
// set of staged changes against the live config file, renders the candidate
// text, validates it on a copy, diffs it, and applies it with a backup and a
// reload. One Pipeline serves the whole daemon (the dashboard is single-origin,
// localhost); it serializes staging and apply under its own lock.
//
// The apply contract is backup → validate-on-copy → diff → apply → reload:
//
//	backup           the current file is copied into the history dir
//	validate-on-copy the candidate is written beside the config and run through
//	                 Validate (the daemon's own load+semantic pass) so a bad edit
//	                 never reaches the live file
//	diff             the operator reviews exactly what changes
//	apply            the candidate replaces the live file atomically
//	reload           Reload (SIGHUP) tells the daemon to re-read it
type Pipeline struct {
	mu sync.Mutex

	path     string             // the live config file
	validate func(string) error // run the daemon's load+validate pass on a path
	reload   func() error       // signal the daemon to re-read its config (SIGHUP)
	now      func() time.Time

	staged []Change
	tokens sync.Map // confirm token (string) → candidate sha (string)
}

// Change is one staged edit. Value is nil for a delete.
type Change struct {
	ID      string   `json:"id"`
	Path    []string `json:"path"`
	Value   any      `json:"value,omitempty"`
	Delete  bool     `json:"delete,omitempty"`
	Summary string   `json:"summary"`
	Section string   `json:"section"` // the top-level section, for grouping in the UI
}

// Options configure a Pipeline.
type Options struct {
	Path     string
	Validate func(string) error
	Reload   func() error
	Now      func() time.Time // injectable clock for tests
}

// New builds a Pipeline. Validate and Reload may be nil (validate then accepts
// anything and reload is a no-op) — the dashboard always wires both.
func New(opt Options) *Pipeline {
	p := &Pipeline{path: opt.Path, validate: opt.Validate, reload: opt.Reload, now: opt.Now}
	if p.validate == nil {
		p.validate = func(string) error { return nil }
	}
	if p.reload == nil {
		p.reload = func() error { return nil }
	}
	if p.now == nil {
		p.now = time.Now
	}
	return p
}

// Path is the live config file path.
func (p *Pipeline) Path() string { return p.path }

// current reads the live config bytes.
func (p *Pipeline) current() ([]byte, error) { return os.ReadFile(p.path) }

// Staged returns a copy of the staged changes.
func (p *Pipeline) Staged() []Change {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Change, len(p.staged))
	copy(out, p.staged)
	return out
}

// Stage adds or replaces a staged change. A second edit to the same path
// replaces the first (last write wins), so the staged set never holds two edits
// of one value.
func (p *Pipeline) Stage(c Change) (Change, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(c.Path) == 0 {
		return Change{}, fmt.Errorf("configedit: a change needs a path")
	}
	if c.ID == "" {
		c.ID = randID()
	}
	c.Section = c.Path[0]
	// Replace any existing staged edit of the same path.
	key := pathKey(c.Path)
	for i, ex := range p.staged {
		if pathKey(ex.Path) == key {
			p.staged[i] = c
			return c, nil
		}
	}
	p.staged = append(p.staged, c)
	return c, nil
}

// Unstage drops a staged change by id. Unknown ids are ignored.
func (p *Pipeline) Unstage(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.staged[:0]
	for _, c := range p.staged {
		if c.ID != id {
			out = append(out, c)
		}
	}
	p.staged = out
}

// Clear drops all staged changes.
func (p *Pipeline) Clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.staged = nil
}

// candidateLocked renders the current file with every staged change applied.
func (p *Pipeline) candidateLocked() (before, after []byte, err error) {
	before, err = p.current()
	if err != nil {
		return nil, nil, err
	}
	doc, err := Parse(before)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range p.staged {
		if c.Delete {
			if err := doc.Delete(c.Path); err != nil {
				return nil, nil, fmt.Errorf("stage %s: %w", pathKey(c.Path), err)
			}
			continue
		}
		if err := doc.SetScalar(c.Path, c.Value); err != nil {
			return nil, nil, fmt.Errorf("stage %s: %w", pathKey(c.Path), err)
		}
	}
	return before, doc.Bytes(), nil
}

// Candidate returns the current and candidate bytes.
func (p *Pipeline) Candidate() (before, after []byte, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.candidateLocked()
}

// Diff is the staged set's diff plus its effects (the least-privilege wideners).
type Review struct {
	Diff     Diff     `json:"diff"`
	Effects  []Effect `json:"effects"`  // every change, in words
	Widening []Effect `json:"widening"` // the subset that loosens a guardrail
	SHA      string   `json:"sha"`      // sha256 of the candidate (the apply confirm binds to this)
	Token    string   `json:"token"`    // the step-up confirm token apply must echo
}

// Effect describes one change in words, with its YAML path.
type Effect struct {
	Path   string `json:"path"`
	Text   string `json:"text"`
	Widens bool   `json:"widens"`
}

// Review renders the diff and effects for the staged set and mints a confirm
// token bound to the candidate's sha.
func (p *Pipeline) Review() (Review, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	before, after, err := p.candidateLocked()
	if err != nil {
		return Review{}, err
	}
	diff := ComputeDiff(before, after)
	effects := p.effectsLocked(before)
	var widening []Effect
	for _, e := range effects {
		if e.Widens {
			widening = append(widening, e)
		}
	}
	sum := sha256.Sum256(after)
	sha := hex.EncodeToString(sum[:])
	tok := randID()
	p.tokens.Store(tok, sha)
	return Review{Diff: diff, Effects: effects, Widening: widening, SHA: sha[:12], Token: tok}, nil
}

// effectsLocked describes every staged change in words, flagging wideners.
func (p *Pipeline) effectsLocked(before []byte) []Effect {
	doc, _ := Parse(before)
	var out []Effect
	for _, c := range p.staged {
		var cur any
		if doc != nil {
			cur, _ = doc.Get(c.Path)
		}
		text, widens := describe(c, cur)
		out = append(out, Effect{Path: pathKey(c.Path), Text: text, Widens: widens})
	}
	return out
}

// Apply validates the candidate on a copy, backs up the live file, writes the
// candidate atomically, clears the staged set, records history, and reloads.
// token must be the one minted by the matching Review and must still bind to the
// candidate being applied.
func (p *Pipeline) Apply(token string) (Applied, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	before, after, err := p.candidateLocked()
	if err != nil {
		return Applied{}, err
	}
	sum := sha256.Sum256(after)
	sha := hex.EncodeToString(sum[:])

	v, ok := p.tokens.Load(token)
	if !ok || v.(string) != sha {
		return Applied{}, ErrStale
	}

	if ComputeDiff(before, after).Changed == false {
		// Nothing to do; still a clean result.
		p.tokens.Delete(token)
		p.staged = nil
		return Applied{SHA: sha[:12], Unchanged: true}, nil
	}

	// validate-on-copy: write the candidate beside the config so relative
	// imports and the sibling conductor.env resolve identically, then run the
	// daemon's own load+validate pass against it.
	if err := p.validateCandidate(after); err != nil {
		return Applied{}, fmt.Errorf("the candidate config does not validate: %w", err)
	}

	// backup
	backupID, err := p.backup(before)
	if err != nil {
		return Applied{}, fmt.Errorf("backup before apply: %w", err)
	}

	// apply atomically
	if err := writeAtomic(p.path, after); err != nil {
		return Applied{}, fmt.Errorf("write config: %w", err)
	}

	p.tokens.Delete(token)
	p.staged = nil

	// reload
	reloadErr := ""
	if err := p.reload(); err != nil {
		reloadErr = err.Error()
	}
	return Applied{SHA: sha[:12], BackupID: backupID, ReloadError: reloadErr}, nil
}

// Applied is the result of a successful apply.
type Applied struct {
	SHA         string `json:"sha"`
	BackupID    string `json:"backup_id,omitempty"`
	ReloadError string `json:"reload_error,omitempty"` // non-fatal: the file is written, reload failed
	Unchanged   bool   `json:"unchanged,omitempty"`
}

// validateCandidate writes b to a scratch file in the config's directory and
// runs the validate pass against it.
func (p *Pipeline) validateCandidate(b []byte) error {
	dir := filepath.Dir(p.path)
	tmp, err := os.CreateTemp(dir, ".conductor-candidate-*.yaml")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return p.validate(name)
}

func randID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func pathKey(path []string) string {
	s := ""
	for i, seg := range path {
		if i > 0 {
			s += "."
		}
		s += seg
	}
	return s
}

// writeAtomic writes b to path via a temp file + rename, preserving the
// existing file's mode when present.
func writeAtomic(path string, b []byte) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".conductor-write-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}
