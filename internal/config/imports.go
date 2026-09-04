// Section-scoped config splitting (issue #36). The connectors-model sections
// split across files without a monolith:
//
// One vocabulary: `imports:` (plural) is a list of file globs, used by every
// section including the triggers: list; `import:` (singular) loads exactly
// one file, only as a named-entry body or a workflow step ref.
//
//   - Map sections (connectors:/runtimes:/hosts:/agents:/workflows:) take an
//     `imports:` key listing files or globs whose entries join that section,
//     alongside inline entries. A duplicate name across files is a load
//     error naming the key and both files — merge, never last-wins.
//   - A named entry keeps its name in the main file with its body in its own
//     file: `workflows: { assess: { import: ./workflows/assess.yaml } }` —
//     valid in any map section.
//   - The triggers: LIST takes the same plural key as an item —
//     `- imports: [triggers/*.yaml]` — spliced in place, mixable with inline
//     triggers.
//   - A `workflow:` step invokes a workflow by name from any imported file,
//     or points at a file directly: `{workflow: name, import: ./file.yaml}`
//     or a bare `{workflow: ./file.yaml}` when the file defines a single
//     workflow (see
//     resolveWorkflowFiles).
//
// An imported section file holds either bare entries (`gh: {type: github}`)
// or the section-wrapped form (`connectors: {gh: …}`); both are accepted.
// The legacy TOP-level `imports:` (whole-document deep merge) is unchanged.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// sectionMaps are the map-shaped sections that accept an `imports:` key.
var sectionMaps = []string{"connectors", "runtimes", "hosts", "agents", "workflows"}

// hasAnyImports reports whether the raw config document uses any import form
// — the probe that routes Load through the merge machinery.
func hasAnyImports(m map[string]any) bool {
	if _, has := m["imports"]; has {
		return true
	}
	for _, sec := range sectionMaps {
		sm, ok := m[sec].(map[string]any)
		if !ok {
			continue
		}
		if _, has := sm["imports"]; has {
			return true
		}
		for _, v := range sm {
			if entryImportPath(v) != "" {
				return true
			}
		}
	}
	if l, ok := m["triggers"].([]any); ok {
		for _, it := range l {
			if im, ok := it.(map[string]any); ok {
				if _, has := im["imports"]; has {
					return true
				}
				// The singular form is invalid on trigger items — route it to
				// the merge path so the loader can say so clearly.
				if _, has := im["import"]; has {
					return true
				}
			}
		}
	}
	return false
}

// entryImportPath reports a named entry written as `{ import: <one file> }`
// — the body-from-a-file form — returning the path ("" otherwise).
func entryImportPath(v any) string {
	em, ok := v.(map[string]any)
	if !ok || len(em) != 1 {
		return ""
	}
	p, ok := em["import"].(string)
	if !ok {
		return ""
	}
	return p
}

// expandSectionImports resolves the file at path's per-section imports in
// place: each map section's `imports:` files contribute named entries (a name
// collision errors, naming both files), a named entry's `import:` loads its
// body from one file, and `- import:` trigger items splice their files'
// trigger lists at that position.
func expandSectionImports(path string, m map[string]any) error {
	dir := filepath.Dir(path)
	for _, sec := range sectionMaps {
		sm, ok := m[sec].(map[string]any)
		if !ok {
			continue
		}
		src := map[string]string{}
		for name := range sm {
			src[name] = path
		}
		if impv, has := sm["imports"]; has {
			imports := toStrings(impv)
			if len(imports) == 0 {
				return fmt.Errorf("%s: %s.imports must be a file/glob list", path, sec)
			}
			delete(sm, "imports")
			delete(src, "imports")
			for _, pat := range imports {
				files, err := globImport(dir, path, pat)
				if err != nil {
					return err
				}
				for _, f := range files {
					entries, err := sectionFileEntries(f, sec)
					if err != nil {
						return err
					}
					names := make([]string, 0, len(entries))
					for n := range entries {
						names = append(names, n)
					}
					sort.Strings(names)
					for _, name := range names {
						if prev, dup := src[name]; dup {
							return fmt.Errorf("%s.%s is defined in both %s and %s — rename one (imports merge, they never override)", sec, name, prev, f)
						}
						src[name] = f
						sm[name] = entries[name]
					}
				}
			}
		}
		// Named entries whose body lives in its own file:
		//   workflows: { assess: { import: ./workflows/assess.yaml } }
		for name, v := range sm {
			ep := entryImportPath(v)
			if ep == "" {
				continue
			}
			body, err := entryFileBody(resolveImportPath(dir, ep), sec, name)
			if err != nil {
				return fmt.Errorf("%s: %s.%s: %w", path, sec, name, err)
			}
			sm[name] = body
		}
	}

	l, ok := m["triggers"].([]any)
	if !ok {
		return nil
	}
	out := make([]any, 0, len(l))
	for i, it := range l {
		im, isMap := it.(map[string]any)
		if !isMap {
			out = append(out, it)
			continue
		}
		if _, has := im["import"]; has && len(im) == 1 {
			return fmt.Errorf("%s: triggers[%d]: use `imports:` (plural, a glob list) to split the trigger list — `import:` (singular) is only a named-entry body or a workflow step ref", path, i)
		}
		impv, has := im["imports"]
		if !has {
			out = append(out, it)
			continue
		}
		if len(im) != 1 {
			return fmt.Errorf("%s: triggers[%d]: an imports item carries only `imports:`", path, i)
		}
		imports := toStrings(impv)
		if len(imports) == 0 {
			return fmt.Errorf("%s: triggers[%d]: imports must be a file/glob list", path, i)
		}
		for _, pat := range imports {
			files, err := globImport(dir, path, pat)
			if err != nil {
				return err
			}
			for _, f := range files {
				items, err := triggersFileItems(f)
				if err != nil {
					return err
				}
				out = append(out, items...)
			}
		}
	}
	m["triggers"] = out
	return nil
}

// resolveImportPath anchors a relative import at the importing file's dir.
func resolveImportPath(dir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, p)
}

// entryFileBody reads one entry's body from its own file: the body directly,
// or unwrapped from `{<name>: body}` / `{<section>: {<name>: body}}` so a
// file written in either split style still reads.
func entryFileBody(path, sec, name string) (map[string]any, error) {
	m, err := importFileMap(path)
	if err != nil {
		return nil, err
	}
	if len(m) == 1 {
		if wrapped, ok := m[sec].(map[string]any); ok {
			m = wrapped
		}
	}
	if len(m) == 1 {
		if body, ok := m[name].(map[string]any); ok {
			return body, nil
		}
	}
	if _, has := m["import"]; has {
		return nil, fmt.Errorf("import %s: nested imports are not supported", path)
	}
	return m, nil
}

// globImport resolves one import pattern relative to dir. An unmatched GLOB
// is a no-op (the default split layout ships empty conf.d/ folders that fill
// up over time); a missing LITERAL path is an error (a typo'd filename must
// not vanish silently).
func globImport(dir, from, pat string) ([]string, error) {
	p := resolveImportPath(dir, pat)
	matches, err := filepath.Glob(p)
	if err != nil {
		return nil, fmt.Errorf("%s: bad import glob %q: %w", from, pat, err)
	}
	if len(matches) == 0 {
		if strings.ContainsAny(pat, "*?[") {
			return nil, nil // an empty glob is a ready-to-fill folder
		}
		return nil, fmt.Errorf("%s: import %q matched no files", from, pat)
	}
	sort.Strings(matches)
	return matches, nil
}

// importFileMap reads one imported file as a raw map (env-expanded like every
// config file).
func importFileMap(path string) (map[string]any, error) {
	expanded, err := readExpanded(path)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := yaml.Unmarshal(expanded, &m); err != nil {
		return nil, fmt.Errorf("parse import %s: %w", path, err)
	}
	return m, nil
}

// sectionFileEntries reads an imported section file's named entries: the
// bare form (entries at the top level) or the section-wrapped form. Imported
// files contribute entries only — one level, no nested imports.
func sectionFileEntries(path, sec string) (map[string]any, error) {
	m, err := importFileMap(path)
	if err != nil {
		return nil, err
	}
	if len(m) == 1 {
		if wrapped, ok := m[sec].(map[string]any); ok {
			m = wrapped
		}
	}
	if _, has := m["imports"]; has {
		return nil, fmt.Errorf("import %s: nested imports are not supported — list every file from the config's own %s.imports", path, sec)
	}
	return m, nil
}

// triggersFileItems reads an imported trigger file's list: a bare list or
// the `triggers:`-wrapped form.
func triggersFileItems(path string) ([]any, error) {
	expanded, err := readExpanded(path)
	if err != nil {
		return nil, err
	}
	var bare []any
	if err := yaml.Unmarshal(expanded, &bare); err == nil && bare != nil {
		return bare, nil
	}
	var wrapped struct {
		Triggers []any `yaml:"triggers"`
	}
	if err := yaml.Unmarshal(expanded, &wrapped); err != nil {
		return nil, fmt.Errorf("parse import %s: %w", path, err)
	}
	if wrapped.Triggers == nil {
		return nil, fmt.Errorf("import %s: expected a trigger list (bare, or under `triggers:`)", path)
	}
	return wrapped.Triggers, nil
}

// ---------------------------------------------------------------------------
// File-based workflow references
// ---------------------------------------------------------------------------

// workflowPathRef reports whether a `workflow:` value is a file path rather
// than a workflow name.
func workflowPathRef(use string) bool {
	return strings.ContainsRune(use, '/') ||
		strings.HasSuffix(use, ".yaml") || strings.HasSuffix(use, ".yml")
}

// resolveWorkflowFiles materializes the file-referencing `workflow:` forms into
// cfg.Workflows so everything downstream (validation, cycle checks, the flow
// runner) sees one merged workflow set:
//
//	{ workflow: review-flow, import: ./workflows/review.yaml }  name + its file
//	{ workflow: ./workflows/review.yaml }                        the file's single workflow
//
// Relative paths resolve against the config file's directory. A loaded
// file's workflows may themselves carry `workflow:`/`import:` steps —
// resolved recursively.
func (c *Config) resolveWorkflowFiles(dir string) error {
	if c.Workflows == nil {
		c.Workflows = map[string]WorkflowDef{}
	}
	src := map[string]string{} // workflow name -> defining file ("" = inline)
	for name := range c.Workflows {
		src[name] = ""
	}

	// register adds a file's workflow under name, erroring on a cross-file
	// name collision (re-loading the same file+name is idempotent).
	register := func(name, file string, def WorkflowDef) error {
		if prev, exists := src[name]; exists {
			if prev == file {
				return nil
			}
			where := prev
			if where == "" {
				where = "the config itself"
			}
			return fmt.Errorf("config: workflow %q from %s is already defined in %s — rename one", name, file, where)
		}
		src[name] = file
		c.Workflows[name] = def
		return nil
	}

	var resolveSteps func(where string, steps []Step) error
	resolveStep := func(where string, s *Step) error {
		if s.Import != "" && s.Workflow == "" {
			return fmt.Errorf("config: %s: a step-level `import:` needs `workflow: <name>` naming the workflow in that file", where)
		}
		if s.Workflow == "" {
			return nil
		}
		isPath := workflowPathRef(s.Workflow)
		if s.Import == "" && !isPath {
			return nil // a plain name — resolved from the merged set
		}
		if s.Import != "" && isPath {
			return fmt.Errorf("config: %s: `workflow: %s` with `import:` — workflow: names it, import: names the file", where, s.Workflow)
		}
		file := s.Import
		if isPath {
			file = s.Workflow
		}
		file = resolveImportPath(dir, file)
		defs, err := workflowFileDefs(file)
		if err != nil {
			return fmt.Errorf("config: %s: %w", where, err)
		}
		name := s.Workflow
		if isPath {
			if len(defs) != 1 {
				return fmt.Errorf("config: %s: `workflow: %s` needs a file with exactly one workflow (found %d) — name one with `workflow:` + `import:`", where, s.Workflow, len(defs))
			}
			for n := range defs {
				name = n
			}
			s.Workflow = name // downstream resolution is by name
		}
		def, ok := defs[name]
		if !ok {
			names := make([]string, 0, len(defs))
			for n := range defs {
				names = append(names, n)
			}
			sort.Strings(names)
			return fmt.Errorf("config: %s: %s defines no workflow %q (has: %s)", where, file, name, strings.Join(names, ", "))
		}
		if err := register(name, file, def); err != nil {
			return err
		}
		// The loaded workflow's own steps may reference further files.
		return resolveSteps(fmt.Sprintf("workflow %q (%s)", name, file), def.Steps)
	}
	resolveSteps = func(where string, steps []Step) error {
		for i := range steps {
			s := &steps[i]
			w := fmt.Sprintf("%s step %d", where, i+1)
			if err := resolveStep(w, s); err != nil {
				return err
			}
			if s.Parallel != nil {
				for bi := range s.Parallel.Branches {
					if err := resolveSteps(fmt.Sprintf("%s branch %d", w, bi+1), s.Parallel.Branches[bi]); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}

	for ti := range c.Triggers {
		if err := resolveSteps(fmt.Sprintf("triggers[%d]", ti), c.Triggers[ti].Steps); err != nil {
			return err
		}
	}
	// Inline workflows may also call out to files.
	names := make([]string, 0, len(c.Workflows))
	for n := range c.Workflows {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := resolveSteps(fmt.Sprintf("workflows.%s", n), c.Workflows[n].Steps); err != nil {
			return err
		}
	}
	return nil
}

// workflowFileDefs loads a workflow file: `workflows:`-wrapped or a bare
// name→definition map.
func workflowFileDefs(path string) (map[string]WorkflowDef, error) {
	expanded, err := readExpanded(path)
	if err != nil {
		return nil, err
	}
	var wrapped struct {
		Workflows map[string]WorkflowDef `yaml:"workflows"`
	}
	if err := yaml.Unmarshal(expanded, &wrapped); err == nil && len(wrapped.Workflows) > 0 {
		return wrapped.Workflows, nil
	}
	var bare map[string]WorkflowDef
	if err := yaml.Unmarshal(expanded, &bare); err != nil {
		return nil, fmt.Errorf("parse workflow file %s: %w", path, err)
	}
	if len(bare) == 0 {
		return nil, fmt.Errorf("workflow file %s defines no workflows", path)
	}
	return bare, nil
}

// readExpanded reads one config-owned file with the standard env expansion.
func readExpanded(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return expandEnv(path, raw)
}
