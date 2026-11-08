// Package configmigrate rewrites a deployed config in place when a release
// retires a config shape, so an auto-updating box never needs a hand edit.
//
// It follows the mechanism the legacy-schema migration used (the package
// internal/migrate, removed with that schema):
//
//   - It runs at boot, before the config is loaded, and on demand as
//     `conductor config migrate [--dry-run]`. `conductor validate` reports
//     what boot would rewrite without rewriting it.
//   - It reads the RAW files of the whole import tree (no environment
//     expansion), so a ${VAR} or ${settings.X} reference is carried over
//     verbatim, never its value.
//   - Every edit is spliced into the file's text (internal/configedit), so the
//     comments, blank lines, key order and formatting the edit did not touch
//     stay byte-for-byte as written.
//   - It is idempotent: a file with nothing to rewrite is untouched, and a
//     second run over its own output is a no-op.
//   - It is fail-safe. Every file is transformed in memory first. Plugins the
//     rewrite starts referencing are installed before anything is written
//     (Options.Ensure) — if they can't be, nothing is written and the box
//     keeps running on the shape it has. A rewrite that leaves only the main
//     file changed is validated on a copy beside it before the live file is
//     touched. Then each changed file is backed up (<file>.pre-<rule>, the
//     first backup wins, with the file's mode), swapped in atomically, and
//     the whole tree is validated once; a failure restores every file.
//     Validating means more than loading: every connector the rewrite touched
//     must build enabled, and a plugin it wires to a listener must be granted
//     that listener, unless the connector was already failing before.
//   - A rewrite that needs a plugin granted something its install record does
//     not (Grant: smee wired to a jailed listener needs expose_local) gets it
//     first (Options.Grant). One that can't be granted is held back, left as
//     it is with a warning while its old shape still works, and the rest of
//     the migration goes ahead.
//   - Each rewritten file is logged with its backup, and every decision
//     worth a human's eye is returned as a note.
package configmigrate

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/configedit"
)

// Options configure one migration run.
type Options struct {
	// Check loads and validates the whole on-disk config tree rooted at the
	// given main file, returning the connectors that would not work (name →
	// reason): built disabled, or wired to a listener their plugin is not
	// granted. Apply runs it on the live config first (the baseline), then on
	// the rewrite: on a copy of the main file before writing when only the
	// main file changes, else on the live tree after the swap. nil skips
	// validation (tests of the rewrite alone).
	Check func(mainPath string) (failing map[string]string, err error)
	// Ensure installs plugins the rewrite starts referencing (their `use:`
	// values), before anything is written. from maps each to the `use:`
	// values of the connectors whose work it takes over (Plan.From). An
	// error holds the whole migration back: nothing is written. nil skips it.
	Ensure func(uses []string, from map[string][]string) error
	// Grant makes the installed plugin g.Use one granted g: installing or
	// updating it to a release that declares it, and recording the approval
	// of a held update that asks for exactly what g names (the migration is
	// the change that wires it). An error holds back the rewrites that need
	// g — left as they are, with a warning — and the rest goes ahead. Its
	// notes (what it updated or approved) join the run's. nil skips it.
	Grant func(g Grant) (notes []string, err error)
	// Logf receives one line per rewritten file. nil discards.
	Logf func(string, ...any)
	// PortFree reports whether a loopback port can be bound now (a seam for
	// tests; nil uses a real bind probe).
	PortFree func(port int) bool
}

// Result is a run's outcome.
type Result struct {
	// Files are the rewritten files (empty when nothing needed it).
	Files []string
	// Backups are their backups, index-aligned with Files.
	Backups []string
	// Notes are the decisions worth reading: each rewrite, and each site left
	// alone with the reason.
	Notes []string
	// Uses are the `use:` values the rewrite added (plugins it needs).
	Uses []string
	// Warnings are the rewrites held back (a plugin they need could not be
	// granted what they wire): worth a notification, not a failure.
	Warnings []string
}

// Grant is what a plugin a rewrite wires must be granted for the rewrite to
// work, beyond being installed.
type Grant struct {
	// Use is the plugin's `use:` value.
	Use string
	// ExposeLocal: it delivers to a listener from its jail.
	ExposeLocal bool
	// Egress are destination entries (as a manifest declares them) the
	// rewrite points it at.
	Egress []string
}

// Plan is a computed migration: every file's rewrite, held in memory.
type Plan struct {
	Main  string
	Files []*File // the whole import tree, main first
	Notes []string
	Uses  []string
	// Grants are what the plugins the rewrite wires must be granted.
	Grants []Grant
	// From maps each plugin the rewrite wires to the `use:` values of the
	// connectors whose work it takes over (smee → the github or webhook
	// connectors whose smee_url it now relays): on a host that cannot jail
	// plugins it inherits their treatment, so an upgrade breaks nothing.
	From map[string][]string
	// Touched are the connectors the rewrite changed or added.
	Touched []string
	// backupSuffix names this run's backups (the rules that fired).
	backupSuffix string
}

// File is one config file of the tree.
type File struct {
	Path string
	Orig []byte
	Mode os.FileMode
	doc  *configedit.Document
}

// Changed reports whether this file was rewritten.
func (f *File) Changed() bool { return f.doc != nil && string(f.doc.Bytes()) != string(f.Orig) }

// Output is the rewritten text (the original when unchanged).
func (f *File) Output() []byte {
	if f.doc == nil {
		return f.Orig
	}
	return f.doc.Bytes()
}

// Changed reports whether the plan rewrites anything.
func (p *Plan) Changed() bool {
	for _, f := range p.Files {
		if f.Changed() {
			return true
		}
	}
	return false
}

// ChangedFiles lists the files the plan rewrites.
func (p *Plan) ChangedFiles() []*File {
	var out []*File
	for _, f := range p.Files {
		if f.Changed() {
			out = append(out, f)
		}
	}
	return out
}

// rule is one retired shape and its rewrite.
type rule struct {
	// name is the rule's backup suffix part (<file>.pre-<name>).
	name string
	// apply rewrites the tree in place, returning notes and the `use:` values
	// it added. It changes nothing it does not own.
	apply func(t *tree) error
}

// rules are applied in order. A new retirement adds one here.
var rules = []rule{smeeRule}

// Compute reads the tree rooted at mainPath and computes every rule's
// rewrite in memory. Nothing is written.
func Compute(mainPath string, opt Options) (*Plan, error) {
	return compute(mainPath, opt, nil)
}

// compute is Compute with held: plugin use → why it can't be granted what
// a rewrite needs; rewrites needing it are left as they are.
func compute(mainPath string, opt Options, held map[string]string) (*Plan, error) {
	paths, err := discoverFiles(mainPath)
	if err != nil {
		return nil, err
	}
	t := &tree{portFree: opt.PortFree, main: mainPath, held: held}
	if t.portFree == nil {
		t.portFree = portFree
	}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		doc, err := configedit.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		t.files = append(t.files, &File{Path: p, Orig: raw, Mode: fileMode(p), doc: doc})
	}
	var fired []string
	for _, r := range rules {
		before := t.changedCount()
		if err := r.apply(t); err != nil {
			return nil, err
		}
		if t.changedCount() != before || t.ruleChanged {
			fired = append(fired, r.name)
		}
		t.ruleChanged = false
	}
	plan := &Plan{Main: mainPath, Files: t.files, Notes: t.notes, Uses: dedupe(t.uses), Grants: mergeGrants(t.grants), From: t.from, Touched: dedupe(t.touched)}
	plan.backupSuffix = ".pre-" + strings.Join(fired, "+")
	return plan, nil
}

// Apply computes the migration and, when it rewrites anything, commits it
// fail-safe (see the package doc). A non-nil error means nothing was left
// changed: the caller keeps running on the config as it is.
func Apply(mainPath string, opt Options) (*Result, error) {
	logf := opt.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	plan, err := Compute(mainPath, opt)
	if err != nil {
		return nil, err
	}
	res := &Result{Notes: plan.Notes, Uses: plan.Uses}
	todo := plan.ChangedFiles()
	if len(todo) == 0 {
		return res, nil
	}
	if opt.Ensure != nil && len(plan.Uses) > 0 {
		if err := opt.Ensure(plan.Uses, plan.From); err != nil {
			return res, fmt.Errorf("the rewritten config needs %s, which could not be installed (%v) — nothing was rewritten; the next boot tries again", strings.Join(plan.Uses, ", "), err)
		}
	}
	if opt.Grant != nil && len(plan.Grants) > 0 {
		held := map[string]string{}
		var granted []string
		for _, g := range plan.Grants {
			notes, err := opt.Grant(g)
			granted = append(granted, notes...)
			if err != nil {
				held[g.Use] = err.Error()
			}
		}
		res.Notes = append(res.Notes, granted...)
		if len(held) > 0 {
			if plan, err = compute(mainPath, opt, held); err != nil {
				return res, err
			}
			res.Notes, res.Uses = append(plan.Notes, granted...), plan.Uses
			for _, use := range slices.Sorted(maps.Keys(held)) {
				res.Warnings = append(res.Warnings, fmt.Sprintf("the rewrites that wire %s are held back: %s — those connectors stay as they are and keep working; the next boot tries again", use, held[use]))
			}
			if todo = plan.ChangedFiles(); len(todo) == 0 {
				return res, nil
			}
		}
	}
	var verify func(path string) error
	if opt.Check != nil {
		// A connector already disabled before the rewrite (no credential,
		// say) isn't the rewrite's doing; one it disables is.
		before, berr := opt.Check(plan.Main)
		verify = func(path string) error {
			after, err := opt.Check(path)
			if err != nil {
				return err
			}
			for _, name := range plan.Touched {
				if why, off := after[name]; off {
					if _, was := before[name]; was && berr == nil {
						continue
					}
					return fmt.Errorf("connector %s would not work: %s", name, why)
				}
			}
			return nil
		}
	}
	// Validate before writing when the main file is the only one that
	// changes: a copy beside it resolves imports and the sibling env file
	// exactly as the real one does.
	if verify != nil && len(todo) == 1 && todo[0].Path == plan.Main {
		if err := validateCopy(plan.Main, todo[0].Output(), verify); err != nil {
			return res, fmt.Errorf("the rewritten config does not validate: %w (nothing was rewritten)", err)
		}
		verify = nil // validated as written; the swap below writes exactly that
	}

	restoreAll := func() error {
		var first error
		for _, f := range todo {
			if err := writeAtomic(f.Path, f.Orig, f.Mode); err != nil && first == nil {
				first = fmt.Errorf("restoring %s: %w", f.Path, err)
			}
		}
		return first
	}
	for _, f := range todo {
		backup := f.Path + plan.backupSuffix
		if _, err := os.Stat(backup); os.IsNotExist(err) {
			if err := os.WriteFile(backup, f.Orig, f.Mode); err != nil {
				_ = restoreAll()
				return res, fmt.Errorf("write backup %s: %w", backup, err)
			}
			// WriteFile's mode is clamped by the umask; a config holding
			// secrets keeps its exact permissions.
			_ = os.Chmod(backup, f.Mode)
		}
		if err := writeAtomic(f.Path, f.Output(), f.Mode); err != nil {
			_ = restoreAll()
			return res, err
		}
		res.Files = append(res.Files, f.Path)
		res.Backups = append(res.Backups, backup)
	}
	if verify != nil {
		if err := verify(plan.Main); err != nil {
			res.Files, res.Backups = nil, nil
			if werr := restoreAll(); werr != nil {
				return res, fmt.Errorf("the rewritten config does not validate (%v) AND restoring failed (%v) — restore from the %s files by hand", err, werr, plan.backupSuffix)
			}
			return res, fmt.Errorf("the rewritten config does not validate: %w (originals restored)", err)
		}
	}
	for i, f := range res.Files {
		logf("config migrate: rewrote %s (backup %s)", f, res.Backups[i])
	}
	return res, nil
}

// validateCopy writes the candidate beside the live main file and validates
// that, then removes it.
func validateCopy(mainPath string, out []byte, validate func(string) error) error {
	tmp := mainPath + ".migrate-check"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	defer os.Remove(tmp)
	return validate(tmp)
}

func writeAtomic(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".migrate-tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	_ = os.Chmod(tmp, mode)
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// fileMode returns the file's mode (0600 when it can't be read), so a
// rewritten config keeps its permissions.
func fileMode(path string) os.FileMode {
	if fi, err := os.Stat(path); err == nil {
		return fi.Mode().Perm()
	}
	return 0o600
}

// mergeGrants folds the grants per plugin, sorted by use.
func mergeGrants(in []Grant) []Grant {
	by := map[string]*Grant{}
	var uses []string
	for _, g := range in {
		m, ok := by[g.Use]
		if !ok {
			m = &Grant{Use: g.Use}
			by[g.Use] = m
			uses = append(uses, g.Use)
		}
		m.ExposeLocal = m.ExposeLocal || g.ExposeLocal
		m.Egress = dedupe(append(m.Egress, g.Egress...))
	}
	sort.Strings(uses)
	out := make([]Grant, 0, len(uses))
	for _, u := range uses {
		out = append(out, *by[u])
	}
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// --- the tree ---------------------------------------------------------------

// tree is the import tree under rewrite.
type tree struct {
	main        string
	files       []*File
	notes       []string
	uses        []string
	grants      []Grant
	from        map[string][]string
	held        map[string]string // plugin use → why it can't be granted (compute)
	touched     []string
	portFree    func(int) bool
	takenPorts  map[int]bool
	ruleChanged bool
}

func (t *tree) changedCount() int {
	n := 0
	for _, f := range t.files {
		if f.Changed() {
			n++
		}
	}
	return n
}

func (t *tree) note(format string, a ...any) { t.notes = append(t.notes, fmt.Sprintf(format, a...)) }

func (t *tree) file(path string) *File {
	for _, f := range t.files {
		if f.Path == path {
			return f
		}
	}
	return nil
}

// connectorSite is one connector entry and where it lives: its body (the
// map its fields are in) and its siblings (the map a new connector joins).
type connectorSite struct {
	name        string
	body        *File
	bodyPath    []string // path to the connector's body map in body
	siblings    *File
	siblingPath []string // path to the connectors map in siblings (nil = the file's root)
}

// connectorSites finds every connector entry in the tree: inline under a
// file's `connectors:`, in a file a `connectors.imports:` glob names (bare
// entries or `connectors:`-wrapped), and a named entry whose body is its own
// file (`gh: {import: ./gh.yaml}`).
func (t *tree) connectorSites() []connectorSite {
	var out []connectorSite
	sectionFiles := map[string]bool{}
	for _, f := range t.files {
		v, ok := f.doc.Get([]string{"connectors"})
		if !ok {
			continue
		}
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		for _, pat := range sectionImports(m) {
			matches, _ := config.GlobImport(filepath.Dir(f.Path), f.Path, pat)
			for _, mp := range matches {
				sectionFiles[mp] = true
			}
		}
	}
	for _, f := range t.files {
		if sectionFiles[f.Path] {
			// A section file: its entries are bare, or wrapped in connectors:.
			prefix := []string(nil)
			m := docMap(f.doc, nil)
			if w, ok := m["connectors"].(map[string]any); ok && len(m) == 1 {
				prefix, m = []string{"connectors"}, w
			}
			for _, name := range sortedKeys(m) {
				if _, ok := m[name].(map[string]any); ok {
					out = append(out, connectorSite{name: name, body: f, bodyPath: append(clone(prefix), name), siblings: f, siblingPath: prefix})
				}
			}
			continue
		}
		m := docMap(f.doc, []string{"connectors"})
		for _, name := range sortedKeys(m) {
			if name == "imports" {
				continue
			}
			entry, ok := m[name].(map[string]any)
			if !ok {
				continue
			}
			if ep, ok := entry["import"].(string); ok && len(entry) == 1 {
				bf := t.file(resolveImport(f.Path, ep))
				if bf == nil {
					continue
				}
				body, prefix := docMap(bf.doc, nil), []string(nil)
				if w, ok := body["connectors"].(map[string]any); ok && len(body) == 1 {
					body, prefix = w, []string{"connectors"}
				}
				if w, ok := body[name].(map[string]any); ok && len(body) == 1 {
					prefix = append(prefix, name)
					_ = w
				}
				out = append(out, connectorSite{name: name, body: bf, bodyPath: prefix, siblings: f, siblingPath: []string{"connectors"}})
				continue
			}
			out = append(out, connectorSite{name: name, body: f, bodyPath: []string{"connectors", name}, siblings: f, siblingPath: []string{"connectors"}})
		}
	}
	return out
}

// connectorNames is every connector name the tree declares.
func (t *tree) connectorNames() map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, s := range t.connectorSites() {
		out[s.name] = docMap(s.body.doc, s.bodyPath)
	}
	return out
}

// sectionImports resolves a connectors map's `imports:` globs.
func sectionImports(m map[string]any) []string {
	var out []string
	switch v := m["imports"].(type) {
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	case string:
		out = append(out, v)
	}
	return out
}

func docMap(d *configedit.Document, path []string) map[string]any {
	if len(path) == 0 {
		v, _ := d.Value()
		if m, ok := v.(map[string]any); ok {
			return m
		}
		return map[string]any{}
	}
	v, ok := d.Get(path)
	if !ok {
		return map[string]any{}
	}
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func clone(s []string) []string { return append([]string(nil), s...) }

func resolveImport(from, p string) string {
	p = config.ExpandHome(p)
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(filepath.Dir(from), p)
}

// discoverFiles returns mainPath plus every file its imports reach — the
// top-level `imports:` (recursively), a `connectors.imports:` glob, and a
// connector's own `import:` body file — deduped, main first.
func discoverFiles(mainPath string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	var walk func(p string, topLevel bool) error
	walk = func(p string, topLevel bool) error {
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		if seen[abs] {
			return nil
		}
		seen[abs] = true
		out = append(out, p)
		if !topLevel {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		doc, err := configedit.Parse(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		var top, section []string
		if v, ok := doc.Get([]string{"imports"}); ok {
			top = stringsOf(v)
		}
		conns := docMap(doc, []string{"connectors"})
		section = sectionImports(conns)
		dir := filepath.Dir(p)
		glob := func(pat string, topLevel bool) error {
			if containsRef(pat) {
				return fmt.Errorf("%s: import %q names a variable — resolve it by hand before migrating", p, pat)
			}
			matches, err := config.GlobImport(dir, p, pat)
			if err != nil {
				return err
			}
			for _, m := range matches {
				if err := walk(m, topLevel); err != nil {
					return err
				}
			}
			return nil
		}
		for _, pat := range top {
			if err := glob(pat, true); err != nil {
				return err
			}
		}
		for _, pat := range section {
			if err := glob(pat, false); err != nil {
				return err
			}
		}
		for _, name := range sortedKeys(conns) {
			if e, ok := conns[name].(map[string]any); ok && len(e) == 1 {
				if ep, ok := e["import"].(string); ok {
					if err := walk(resolveImport(p, ep), false); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	if err := walk(mainPath, true); err != nil {
		return nil, err
	}
	return out, nil
}

func stringsOf(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func containsRef(s string) bool { return strings.Contains(s, "${") }
