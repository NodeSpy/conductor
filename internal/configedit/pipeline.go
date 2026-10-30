package configedit

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	guarded  func(string) (any, error)
	reload   func() error // signal the daemon to re-read its config (SIGHUP)
	now      func() time.Time

	staged map[string]*stagedSet // owner → its staged edits
	tokens sync.Map              // confirm token (string) → tokenBind
}

// STAGED SETS ARE PER OWNER. The dashboard stages under the session's id, so
// one session's edits never ride into another's review, confirmation or apply:
// a review covers its owner's set, its token binds to that owner, and an apply
// writes and clears that set only. Another owner's set stays staged against
// the new file, and its earlier review token goes stale with it. The
// Pipeline's own Stage, Review, Apply… use the unowned set "" (sign-in off,
// and tests).
type stagedSet struct {
	changes []Change
	touched time.Time
}

// tokenBind is what a confirm token binds to: the owner whose review minted
// it and the candidate's sha.
type tokenBind struct {
	owner, sha string
}

const (
	maxOwners       = 256  // staged sets kept; the least recently touched goes first
	maxStagedPerSet = 1000 // edits in one set
)

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
	// Guarded loads the config at path and returns its guarded part (all but
	// the inert paths) in comparable form. Edits are compared on it as well as
	// on the text, so a change that reaches a guarded setting through imports
	// or packs still needs a step-up. Optional.
	Guarded func(path string) (any, error)
}

// New builds a Pipeline. Validate and Reload may be nil (validate then accepts
// anything and reload is a no-op) — the dashboard always wires both.
func New(opt Options) *Pipeline {
	p := &Pipeline{path: opt.Path, validate: opt.Validate, reload: opt.Reload, now: opt.Now, guarded: opt.Guarded}
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

// Edits is one owner's view of the pipeline: its staged set, and the review
// and apply of that set.
type Edits struct {
	p     *Pipeline
	owner string
}

// As is owner's view of the pipeline.
func (p *Pipeline) As(owner string) Edits { return Edits{p: p, owner: owner} }

// setLocked is owner's staged set, created (and the least recently touched set
// evicted past maxOwners) when create is set.
func (p *Pipeline) setLocked(owner string, create bool) *stagedSet {
	if p.staged == nil {
		p.staged = map[string]*stagedSet{}
	}
	set := p.staged[owner]
	if set == nil && create {
		if len(p.staged) >= maxOwners {
			oldest := ""
			for o, s := range p.staged {
				if oldest == "" || s.touched.Before(p.staged[oldest].touched) {
					oldest = o
				}
			}
			delete(p.staged, oldest)
		}
		set = &stagedSet{}
		p.staged[owner] = set
	}
	if set != nil {
		set.touched = p.now()
	}
	return set
}

func (p *Pipeline) changesLocked(owner string) []Change {
	if set := p.staged[owner]; set != nil {
		return set.changes
	}
	return nil
}

// Staged returns a copy of the owner's staged changes.
func (e Edits) Staged() []Change {
	e.p.mu.Lock()
	defer e.p.mu.Unlock()
	cs := e.p.changesLocked(e.owner)
	out := make([]Change, len(cs))
	copy(out, cs)
	return out
}

// Stage adds or replaces a staged change. A second edit to the same path
// replaces the first (last write wins), so the staged set never holds two edits
// of one value.
func (e Edits) Stage(c Change) (Change, error) {
	e.p.mu.Lock()
	defer e.p.mu.Unlock()
	if len(c.Path) == 0 {
		return Change{}, fmt.Errorf("configedit: a change needs a path")
	}
	if c.ID == "" {
		c.ID = randID()
	}
	c.Section = c.Path[0]
	set := e.p.setLocked(e.owner, true)
	// Replace any existing staged edit of the same path.
	key := pathKey(c.Path)
	for i, ex := range set.changes {
		if pathKey(ex.Path) == key {
			set.changes[i] = c
			return c, nil
		}
	}
	if len(set.changes) >= maxStagedPerSet {
		return Change{}, fmt.Errorf("configedit: %d edits are staged already; apply or clear them first", maxStagedPerSet)
	}
	set.changes = append(set.changes, c)
	return c, nil
}

// Unstage drops a staged change by id. Unknown ids are ignored.
func (e Edits) Unstage(id string) {
	e.p.mu.Lock()
	defer e.p.mu.Unlock()
	set := e.p.setLocked(e.owner, false)
	if set == nil {
		return
	}
	out := set.changes[:0]
	for _, c := range set.changes {
		if c.ID != id {
			out = append(out, c)
		}
	}
	set.changes = out
}

// Clear drops all of the owner's staged changes.
func (e Edits) Clear() {
	e.p.mu.Lock()
	defer e.p.mu.Unlock()
	delete(e.p.staged, e.owner)
}

// Staged is the unowned set's Staged.
func (p *Pipeline) Staged() []Change { return p.As("").Staged() }

// Stage is the unowned set's Stage.
func (p *Pipeline) Stage(c Change) (Change, error) { return p.As("").Stage(c) }

// Unstage is the unowned set's Unstage.
func (p *Pipeline) Unstage(id string) { p.As("").Unstage(id) }

// Clear is the unowned set's Clear.
func (p *Pipeline) Clear() { p.As("").Clear() }

// candidateLocked renders the current file with every one of owner's staged
// changes applied.
func (p *Pipeline) candidateLocked(owner string) (before, after []byte, err error) {
	before, err = p.current()
	if err != nil {
		return nil, nil, err
	}
	doc, err := Parse(before)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range p.changesLocked(owner) {
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
func (e Edits) Candidate() (before, after []byte, err error) {
	e.p.mu.Lock()
	defer e.p.mu.Unlock()
	return e.p.candidateLocked(e.owner)
}

// Candidate is the unowned set's Candidate.
func (p *Pipeline) Candidate() (before, after []byte, err error) { return p.As("").Candidate() }

// Diff is the staged set's diff plus its effects (the least-privilege wideners).
type Review struct {
	Diff     Diff     `json:"diff"`
	Effects  []Effect `json:"effects"`  // every change, in words
	Widening []Effect `json:"widening"` // the subset that loosens a guardrail
	SHA      string   `json:"sha"`      // sha256 of the candidate (the apply confirm binds to this)
	Token    string   `json:"token"`    // the step-up confirm token apply must echo
	// Guarded names the changed parts that decide who can sign in to the
	// dashboard and how (operators, dashboard auth/listeners/session).
	Guarded []Effect `json:"guarded"`
	// Stepup is whether applying needs a step-up: something widens, or
	// something guarded changes.
	Stepup bool `json:"stepup"`
	// Changes is every changed leaf, before and after, from the whole
	// candidate (however it was staged): what a confirmation describes.
	Changes []LeafChange `json:"changes"`
}

// LeafChange is one scalar (or empty map or list) that differs between the
// live config and the candidate.
type LeafChange struct {
	Path   string `json:"path"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
	Had    bool   `json:"had"` // the live config has the leaf
	Has    bool   `json:"has"` // the candidate has it
	// Guarded: outside the inert paths. Widens: it loosens a guardrail.
	Guarded bool `json:"guarded"`
	Widens  bool `json:"widens"`
}

// Effect describes one change in words, with its YAML path.
type Effect struct {
	Path   string `json:"path"`
	Text   string `json:"text"`
	Widens bool   `json:"widens"`
}

// Review renders the diff and effects for the owner's staged set and mints a
// confirm token bound to the owner and the candidate's sha.
func (e Edits) Review() (Review, error) {
	p := e.p
	p.mu.Lock()
	defer p.mu.Unlock()
	before, after, err := p.candidateLocked(e.owner)
	if err != nil {
		return Review{}, err
	}
	diff := ComputeDiff(before, after)
	widening, guarded, leaves := p.classifyLocked(before, after)
	effects := p.effectsLocked(e.owner, before)
	for i := range effects {
		// A staged change widens when any loosened leaf sits under its path.
		for _, w := range widening {
			if w.Path == effects[i].Path || strings.HasPrefix(w.Path, effects[i].Path+".") {
				effects[i].Widens = true
			}
		}
	}
	sum := sha256.Sum256(after)
	sha := hex.EncodeToString(sum[:])
	tok := randID()
	p.tokens.Store(tok, tokenBind{owner: e.owner, sha: sha})
	return Review{Diff: diff, Effects: effects, Widening: widening, Guarded: guarded, Changes: leaves,
		Stepup: len(widening) > 0 || len(guarded) > 0, SHA: sha[:12], Token: tok}, nil
}

// Review is the unowned set's Review.
func (p *Pipeline) Review() (Review, error) { return p.As("").Review() }

// tokenFor reports whether token was minted for owner over sha.
func (p *Pipeline) tokenFor(token, owner, sha string) bool {
	v, ok := p.tokens.Load(token)
	if !ok {
		return false
	}
	b := v.(tokenBind)
	return b.owner == owner && b.sha == sha
}

// classifyLocked is classify over the text, plus — with a Guarded loader — a
// comparison of what the loaded config ends up with. A config that can't be
// loaded for the comparison counts as changed, so the answer errs towards
// asking for a step-up.
func (p *Pipeline) classifyLocked(before, after []byte) (widening, guarded []Effect, leaves []LeafChange) {
	widening, guarded, leaves = classify(before, after)
	if p.guarded == nil || len(guarded) > 0 {
		return widening, guarded, leaves
	}
	was, errWas := p.guarded(p.path)
	now, errNow := p.loadCandidate(after)
	if errWas != nil || errNow != nil || !reflect.DeepEqual(was, now) {
		guarded = append(guarded, Effect{Path: "(loaded config)",
			Text: "changes what the loaded config ends up with (through imports, packs or YAML references)"})
	}
	return widening, guarded, leaves
}

// loadCandidate runs the Guarded loader on b, written beside the config so
// relative imports resolve as they will after apply.
func (p *Pipeline) loadCandidate(b []byte) (any, error) {
	var out any
	err := p.withCandidateFile(b, func(name string) error {
		var err error
		out, err = p.guarded(name)
		return err
	})
	return out, err
}

// effectsLocked describes every one of owner's staged changes in words,
// flagging wideners.
func (p *Pipeline) effectsLocked(owner string, before []byte) []Effect {
	doc, _ := Parse(before)
	var out []Effect
	for _, c := range p.changesLocked(owner) {
		var cur any
		if doc != nil {
			cur, _ = doc.Get(c.Path)
		}
		text, widens := describe(c, cur)
		out = append(out, Effect{Path: pathKey(c.Path), Text: text, Widens: widens})
	}
	return out
}

// Validate renders the owner's candidate and runs the daemon's load+semantic
// pass over a copy of it, returning the validation error (nil when it
// validates). It does not change the live file.
func (e Edits) Validate() error {
	e.p.mu.Lock()
	defer e.p.mu.Unlock()
	_, after, err := e.p.candidateLocked(e.owner)
	if err != nil {
		return err
	}
	return e.p.validateCandidate(after)
}

// Validate is the unowned set's Validate.
func (p *Pipeline) Validate() error { return p.As("").Validate() }

// Apply validates the candidate on a copy, backs up the live file, writes the
// candidate atomically, clears the staged set, records history, and reloads.
// token must be the one minted by the matching Review and must still bind to the
// candidate being applied.
func (p *Pipeline) Apply(token string) (Applied, error) {
	return p.As("").ApplyWith(token, nil)
}

// ApplyWith is the unowned set's ApplyWith.
func (p *Pipeline) ApplyWith(token string, gate func(needsStepup bool) error) (Applied, error) {
	return p.As("").ApplyWith(token, gate)
}

// ApplyWith is Apply with a gate: after the token is matched to the candidate,
// and before anything is written, gate is told whether this exact candidate
// needs a step-up (Review.Stepup) and may refuse. It runs under the pipeline
// lock, so the answer can't go stale between the check and the write.
func (e Edits) ApplyWith(token string, gate func(needsStepup bool) error) (Applied, error) {
	p := e.p
	p.mu.Lock()
	defer p.mu.Unlock()

	before, after, err := p.candidateLocked(e.owner)
	if err != nil {
		return Applied{}, err
	}
	sum := sha256.Sum256(after)
	sha := hex.EncodeToString(sum[:])

	if !p.tokenFor(token, e.owner, sha) {
		return Applied{}, ErrStale
	}
	if gate != nil {
		widening, guarded, _ := p.classifyLocked(before, after)
		if err := gate(len(widening) > 0 || len(guarded) > 0); err != nil {
			return Applied{}, err
		}
	}

	if ComputeDiff(before, after).Changed == false {
		// Nothing to do; still a clean result.
		p.tokens.Delete(token)
		delete(p.staged, e.owner)
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
	delete(p.staged, e.owner)

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
	return p.withCandidateFile(b, p.validate)
}

// withCandidateFile writes b to a scratch file beside the config, runs fn on
// it, and removes it.
func (p *Pipeline) withCandidateFile(b []byte, fn func(string) error) error {
	tmp, err := os.CreateTemp(filepath.Dir(p.path), ".conductor-candidate-*.yaml")
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
	return fn(name)
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
