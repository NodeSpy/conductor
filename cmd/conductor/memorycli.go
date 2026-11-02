package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// The OPERATOR's memory commands. `conductor memory recall|remember` is the
// agent's face (over the session socket, through its token); these two run
// on the box against the configured backend directly, the way `report` reads
// the audit log, and never touch a pack's read-only entries.
//
//	conductor memory rescope --tag <t> --to <template> [--apply]
//	conductor memory export --scope <s> [--scope …] --out <dir> [--name <pack>]

// operatorMemory builds a manager over the configured backend — the
// instance's own entries, no pack layers. A `store:` backend needs the
// stores: registry, so it goes through the same stack build the other
// config-reading commands use; a directory needs nothing but the path.
func operatorMemory(cfg *config.Config) (*memory.Manager, func(), error) {
	if cfg.Memory == nil {
		return nil, nil, fmt.Errorf("no memory: section in this config — nothing to read")
	}
	if cfg.Memory.Type == "memory" {
		return nil, nil, fmt.Errorf("memory: type: memory is ephemeral (in the daemon's process) — there is nothing on disk to read")
	}
	if cfg.Memory.Store == "" {
		mgr, err := memory.Build(memory.Options{Dir: cfg.Memory.Dir, Type: cfg.Memory.Type})
		if err != nil {
			return nil, nil, err
		}
		return mgr, func() { _ = mgr.Close() }, nil
	}
	stack, err := buildFlowStack(cfg, nil, nil, true)
	if err != nil {
		return nil, nil, err
	}
	mgr, err := memory.Build(memory.Options{Store: cfg.Memory.Store})
	if err != nil {
		stack.Close()
		return nil, nil, fmt.Errorf("%w (a boltdb store is locked while the daemon runs — stop it first)", err)
	}
	return mgr, func() { _ = mgr.Close(); stack.Close() }, nil
}

// refuseInSession keeps the operator commands out of an agent's shell: an
// agent writes memory through its token (`conductor memory remember`), under
// its step's scope rules, never by rewriting the backend. Not the boundary —
// a same-uid agent can reach the files anyway — a clear refusal for the
// honest case, as `conductor bot memory` does.
func refuseInSession(sub string) error {
	if os.Getenv("CONDUCTOR_SKILL_TOKEN") != "" {
		return fmt.Errorf("refusing: this shell is a conductor agent session — `conductor memory %s` is an operator command; an agent writes memory with `conductor memory remember`", sub)
	}
	return nil
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// cmdMemoryRescope moves the entries a filter selects — a tag, a scope
// pattern, or both — to a templated scope: `--to 'outcome:{scope}'` files
// each under `outcome:<its current scope>`. A dry run (the default) prints
// what would move and writes nothing.
func cmdMemoryRescope(args []string) error {
	if err := refuseInSession("rescope"); err != nil {
		return err
	}
	cfg, rest, err := loadConfig(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("memory rescope", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	tag := fs.String("tag", "", "move entries carrying this tag")
	scope := fs.String("scope", "", "move entries whose scope matches (a key, * or a glob like 'standard:acme/*')")
	to := fs.String("to", "", "target scope; {scope} is the entry's current scope")
	apply := fs.Bool("apply", false, "write the moves (default: dry run)")
	if err := fs.Parse(rest); err != nil || fs.NArg() > 0 || (*tag == "" && *scope == "") || *to == "" {
		return fmt.Errorf("usage: conductor memory rescope (--tag <tag> | --scope <pattern> | both) --to <scope template, e.g. 'outcome:{scope}'> [--apply] [--config <path>]")
	}
	mgr, done, err := operatorMemory(cfg)
	if err != nil {
		return err
	}
	defer done()
	return runRescope(os.Stdout, mgr, memory.RescopeFilter{Tag: *tag, Scope: *scope}, *to, *apply)
}

func runRescope(w io.Writer, mgr *memory.Manager, sel memory.RescopeFilter, to string, apply bool) error {
	moves, err := mgr.Rescope(sel, to, apply)
	if err != nil {
		return err
	}
	verb := "would move"
	if apply {
		verb = "moved"
	}
	for _, m := range moves {
		fmt.Fprintf(w, "%s %s: %s -> %s  %s\n", verb, m.ID, m.From, m.To, firstLine(m.Text))
	}
	byScope := map[string]int{}
	for _, m := range moves {
		byScope[m.From+" -> "+m.To]++
	}
	keys := make([]string, 0, len(byScope))
	for k := range byScope {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "  %5d  %s\n", byScope[k], k)
	}
	var what []string
	if sel.Tag != "" {
		what = append(what, fmt.Sprintf("tagged %q", sel.Tag))
	}
	if sel.Scope != "" {
		what = append(what, fmt.Sprintf("in scope %q", sel.Scope))
	}
	fmt.Fprintf(w, "%s %d entr%s %s\n", verb, len(moves), plural(len(moves), "y", "ies"), strings.Join(what, " and "))
	if !apply && len(moves) > 0 {
		fmt.Fprintln(w, "dry run — re-run with --apply to write")
	}
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// packNameRE is what an exported pack's name may be: the identity other
// configs and the lockfile refer to.
var packNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// cmdMemoryExport writes a pack carrying the named scopes' entries as its
// read-only memory: conductor-pack.yaml + memory/*.md. The source backend is
// only read.
func cmdMemoryExport(args []string) error {
	if err := refuseInSession("export"); err != nil {
		return err
	}
	cfg, rest, err := loadConfig(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("memory export", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var scopes multiFlag
	fs.Var(&scopes, "scope", "scope to export (repeatable)")
	out := fs.String("out", "", "directory to write the pack into")
	name := fs.String("name", "memory-export", "the pack's name (pack.name)")
	force := fs.Bool("force", false, "replace the manifest and memory/ of an existing pack at --out")
	if err := fs.Parse(rest); err != nil || fs.NArg() > 0 || len(scopes) == 0 || *out == "" {
		return fmt.Errorf("usage: conductor memory export --scope <scope> [--scope …] --out <dir> [--name <pack>] [--force] [--config <path>]")
	}
	mgr, done, err := operatorMemory(cfg)
	if err != nil {
		return err
	}
	defer done()
	n, err := exportMemoryPack(mgr, exportOpts{
		Scopes: scopes, Out: *out, Name: *name, Force: *force,
		Conductor: exportConductorConstraint(version), Secret: trackedSecretCheck(cfg),
	})
	if err != nil {
		return err
	}
	fmt.Printf("exported %d entr%s from %s into %s (pack %s)\n", n, plural(n, "y", "ies"), strings.Join(scopes, ", "), *out, *name)
	fmt.Printf("check it with: conductor pack lint %s\n", *out)
	return nil
}

type exportOpts struct {
	Scopes    []string
	Out       string
	Name      string
	Force     bool
	Conductor string
	// Secret reports whether a text carries tracked secret material; nil
	// when the config tracks none.
	Secret func(string) bool
}

// exportedManifest is the pack an export writes.
type exportedManifest struct {
	Pack struct {
		Name        string `yaml:"name"`
		Version     string `yaml:"version"`
		Description string `yaml:"description"`
		Requires    struct {
			Conductor string `yaml:"conductor"`
		} `yaml:"requires"`
	} `yaml:"pack"`
	Memory struct {
		Entries string `yaml:"entries"`
	} `yaml:"memory"`
}

const exportEntriesDir = "memory"

func exportMemoryPack(mgr *memory.Manager, o exportOpts) (int, error) {
	if !packNameRE.MatchString(o.Name) {
		return 0, fmt.Errorf("--name %q: a pack name is lowercase letters, digits, '.', '_' and '-'", o.Name)
	}
	for _, s := range o.Scopes {
		if strings.TrimSpace(s) == "" {
			return 0, fmt.Errorf("--scope: empty scope (name the shared set as %q)", memory.GlobalScope)
		}
	}
	entries, err := mgr.Own(o.Scopes)
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, fmt.Errorf("no entries in scope(s) %s — nothing to export", strings.Join(o.Scopes, ", "))
	}
	if o.Secret != nil {
		for _, e := range entries {
			if o.Secret(e.Text) {
				return 0, fmt.Errorf("entry %s carries tracked secret material — remove it (conductor memory recall / forget) before exporting", e.ID)
			}
		}
	}
	manifest := filepath.Join(o.Out, config.PackManifestFile)
	memDir := filepath.Join(o.Out, exportEntriesDir)
	if _, err := os.Stat(manifest); err == nil && !o.Force {
		return 0, fmt.Errorf("%s already exists — pass --force to replace its manifest and %s/", manifest, exportEntriesDir)
	}
	if o.Force {
		// Replace, never merge: an entry forgotten since the last export
		// must not survive in the pack.
		old, _ := filepath.Glob(filepath.Join(memDir, "*.md"))
		for _, f := range old {
			if err := os.Remove(f); err != nil {
				return 0, err
			}
		}
	}
	if err := os.MkdirAll(o.Out, 0o755); err != nil {
		return 0, err
	}
	dst, err := memory.NewDirBackend(memDir)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		if err := dst.Put(exportedEntry(e)); err != nil {
			return 0, fmt.Errorf("write %s: %w", e.ID, err)
		}
	}
	var m exportedManifest
	m.Pack.Name = o.Name
	m.Pack.Version = "0.1.0"
	m.Pack.Description = "Memory entries exported from conductor (scopes: " + strings.Join(o.Scopes, ", ") + ")"
	m.Pack.Requires.Conductor = o.Conductor
	m.Memory.Entries = exportEntriesDir
	b, err := yaml.Marshal(m)
	if err != nil {
		return 0, err
	}
	head := "# Written by `conductor memory export`. The entries under " + exportEntriesDir + "/ are recalled\n" +
		"# read-only by every config that installs this pack (packs: { <name>: { use: … } }).\n"
	if err := os.WriteFile(manifest, append([]byte(head), b...), 0o644); err != nil {
		return 0, err
	}
	return len(entries), nil
}

// exportedEntry is what of an entry may leave the box in a pack: its id,
// tags, scope, created time and text. Source — the repo, step, run, trigger
// and dispatch that wrote it — is this daemon's provenance, not the note's,
// and a pack is meant to be shared, so it is left behind. Built field by
// field so a field added to Entry later stays home until someone decides
// it may travel.
func exportedEntry(e memory.Entry) memory.Entry {
	return memory.Entry{ID: e.ID, Tags: e.Tags, Scope: e.Scope, Created: e.Created, Text: e.Text}
}

// exportConductorConstraint is the requires.conductor an exported pack
// carries: at least the version that wrote it, which is the first that can
// read pack memory, in the canonical unspaced form (">=0.69.0"). A dev build
// cannot name its own version, so it names none in particular.
func exportConductorConstraint(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(v) {
		return ">=" + v
	}
	return ">=0.0.0"
}

// trackedSecretCheck resolves the config's named secrets so an export can
// refuse an entry carrying one of their values — a pack is meant to be
// shared, and an entry written before the memory write guard existed may
// hold one. Unresolvable secrets are skipped (they cannot be in an entry
// this box wrote from them either).
func trackedSecretCheck(cfg *config.Config) func(string) bool {
	if len(cfg.SecretRefs) == 0 {
		return nil
	}
	sec := secrets.New()
	for _, ref := range cfg.SecretRefs {
		_, _ = sec.Resolve(context.Background(), ref)
	}
	return func(s string) bool { return sec.Redact(s) != s }
}

// cmdMemoryKey renders one scope key (memory.RenderScopeKey) over facts
// given as a JSON object (--facts, a file or - for stdin) plus --fact k=v
// pairs (strings; they win). It is core's own renderer, so a script that
// needs the key a step's template names for some event — a calibration
// mapping a past pull request to its key — computes it exactly as the
// engine does instead of re-implementing the grammar. Pure: it reads no
// config and no memory, so it runs anywhere, an agent session included.
// A key that does not render exits non-zero with the reason.
func cmdMemoryKey(stdin io.Reader, w io.Writer, args []string) error {
	fs := flag.NewFlagSet("memory key", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	tmpl := fs.String("template", "", "the scope key, e.g. 'team:{{.inputs.team}}'")
	factsFrom := fs.String("facts", "", "a JSON object of facts: a file path, or - for stdin")
	var pairs multiFlag
	fs.Var(&pairs, "fact", "one fact as name=value (repeatable)")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || *tmpl == "" {
		return fmt.Errorf("usage: conductor memory key --template <key> [--facts <json file|->] [--fact name=value …]")
	}
	facts := map[string]any{}
	if *factsFrom != "" {
		var raw []byte
		var err error
		if *factsFrom == "-" {
			raw, err = io.ReadAll(stdin)
		} else {
			raw, err = os.ReadFile(*factsFrom)
		}
		if err != nil {
			return err
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&facts); err != nil {
			return fmt.Errorf("--facts: want a JSON object: %w", err)
		}
		facts = jsonFacts(facts).(map[string]any)
	}
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return fmt.Errorf("--fact %q: want name=value", p)
		}
		facts[strings.TrimSpace(k)] = v
	}
	key, err := memory.RenderScopeKey(*tmpl, facts)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, key)
	return nil
}

// jsonFacts turns decoded JSON numbers into ints where they are whole, the
// shape the engine's facts carry (a PR number is an int), so `{{.number}}`
// renders the same here as in a run.
func jsonFacts(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = jsonFacts(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = jsonFacts(e)
		}
		return x
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return int(n)
		}
		f, _ := x.Float64()
		return f
	}
	return v
}
