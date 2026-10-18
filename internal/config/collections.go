package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// COLLECTIONS (docs/wiki/Bots.md#content-kinds): custom content kinds for a
// bot's memory repo (bots.<b>.collections) or a team's knowledge repo
// (teams.<t>.collections), beside the built-in kinds. A collection has a
// home dir, a frontmatter schema, an optional body template, whether files
// may sit at free sub-paths, and who may write it. conductor enforces all of
// it at the one memory write path (internal/bot/kinds.go).
//
//	collections:
//	  postmortems:
//	    dir: postmortems
//	    fields:
//	      title:    { type: string, required: true }
//	      severity: { type: enum, values: [sev1, sev2, sev3], required: true }
//	    template: "## Timeline\n\n## Cause\n\n## Follow-ups\n"
//	    free_paths: false
//	    writers: [operator, bots]        # a team's: operator, proposal
//	    max_bytes: 16k                   # one document (default 8k)
//	  # or a definition a pack ships:
//	  postmortems: { from: .conductor/packs/platform-sre/collections/postmortems.yaml }
type Collection struct {
	Dir       string                     `yaml:"dir,omitempty"`
	Fields    map[string]CollectionField `yaml:"fields,omitempty"`
	Template  string                     `yaml:"template,omitempty"`
	FreePaths bool                       `yaml:"free_paths,omitempty"`
	Writers   []string                   `yaml:"writers,omitempty"`
	// MaxBytes caps one document of the collection (default 8KiB).
	MaxBytes ByteSize `yaml:"max_bytes,omitempty"`
	// From loads the whole definition from a YAML file (config-relative),
	// e.g. one a pack ships. Exclusive with the inline keys.
	From string `yaml:"from,omitempty"`
}

// CollectionField is one frontmatter field.
type CollectionField struct {
	// Type: string | text | list | steps | enum | number | bool.
	Type     string   `yaml:"type"`
	Required bool     `yaml:"required,omitempty"`
	Values   []string `yaml:"values,omitempty"` // enum
}

// FieldTypes are the frontmatter field types a schema may use.
var FieldTypes = map[string]bool{"string": true, "text": true, "list": true, "steps": true, "enum": true, "number": true, "bool": true}

// Collection writers.
const (
	WriterOperator = "operator"
	WriterBots     = "bots"
	WriterProposal = "proposal"
)

// BuiltinKindDirs are the dirs the built-in kinds own (and legacy homes):
// no collection may claim one.
var BuiltinKindDirs = []string{"knowledge", "runbooks", "watch", "digests", "log", "handoff", "episodes", "charter"}

var collectionName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)

// resolveCollections loads every from: definition (once, at load).
func (c *Config) resolveCollections() error {
	load := func(where string, cs map[string]Collection) error {
		for name, col := range cs {
			if col.From == "" {
				continue
			}
			if col.Dir != "" || len(col.Fields) > 0 || col.Template != "" || col.FreePaths || len(col.Writers) > 0 || col.MaxBytes != 0 {
				return fmt.Errorf("config: %s.collections.%s: from: is the whole definition — drop the inline keys", where, name)
			}
			p := expandHome(col.From)
			if !filepath.IsAbs(p) && c.baseDir != "" {
				p = filepath.Join(c.baseDir, p)
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return fmt.Errorf("config: %s.collections.%s: from: %w", where, name, err)
			}
			var def Collection
			if err := yaml.Unmarshal(b, &def); err != nil {
				return fmt.Errorf("config: %s.collections.%s: from %s: %w", where, name, col.From, err)
			}
			if def.From != "" {
				return fmt.Errorf("config: %s.collections.%s: from %s: a definition file cannot itself use from:", where, name, col.From)
			}
			cs[name] = def
		}
		return nil
	}
	for _, n := range sortedBotKeys(c.Bots) {
		if err := load("bots."+n, c.Bots[n].Collections); err != nil {
			return err
		}
	}
	for _, n := range c.TeamNames() {
		if err := load("teams."+n, c.Teams[n].Collections); err != nil {
			return err
		}
	}
	return nil
}

// validateCollections checks one repo's collections. team: a team repo's
// (writers operator/proposal), else a bot memory's (operator/bots).
func validateCollections(cs map[string]Collection, team bool) error {
	dirs := map[string]string{}
	names := make([]string, 0, len(cs))
	for n := range cs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		col := cs[name]
		where := "collections." + name
		if !collectionName.MatchString(name) {
			return fmt.Errorf("%s: name must be lowercase letters, digits, - or _", where)
		}
		d := strings.Trim(col.Dir, "/")
		if d == "" {
			return fmt.Errorf("%s: dir is required", where)
		}
		for _, seg := range strings.Split(d, "/") {
			if !collectionName.MatchString(seg) {
				return fmt.Errorf("%s: dir %q: each segment lowercase letters, digits, - or _", where, col.Dir)
			}
		}
		top := strings.SplitN(d, "/", 2)[0]
		for _, b := range BuiltinKindDirs {
			if top == b {
				return fmt.Errorf("%s: dir %q is a built-in kind's home (%s/)", where, col.Dir, b)
			}
		}
		for other, od := range dirs {
			if d == od || strings.HasPrefix(d, od+"/") || strings.HasPrefix(od, d+"/") {
				return fmt.Errorf("%s: dir %q overlaps collection %s's %q", where, col.Dir, other, od)
			}
		}
		dirs[name] = d
		if len(col.Fields) == 0 {
			return fmt.Errorf("%s: fields: a collection needs a frontmatter schema (at least one field)", where)
		}
		for fn, f := range col.Fields {
			if !collectionName.MatchString(fn) {
				return fmt.Errorf("%s.fields.%s: field names are lowercase letters, digits, - or _", where, fn)
			}
			if !FieldTypes[f.Type] {
				return fmt.Errorf("%s.fields.%s: type %q (want string, text, list, steps, enum, number or bool)", where, fn, f.Type)
			}
			if (f.Type == "enum") != (len(f.Values) > 0) {
				return fmt.Errorf("%s.fields.%s: values: go with type enum, and an enum needs them", where, fn)
			}
		}
		if col.MaxBytes < 0 || col.MaxBytes > MaxDocBytes {
			return fmt.Errorf("%s: max_bytes: want 1 to %d bytes, got %d", where, MaxDocBytes, col.MaxBytes)
		}
		allowed := map[string]bool{WriterOperator: true, WriterBots: !team, WriterProposal: team}
		if len(col.Writers) == 0 {
			return fmt.Errorf("%s: writers: name who may write it (%s)", where, map[bool]string{true: "operator, proposal", false: "operator, bots"}[team])
		}
		for _, w := range col.Writers {
			if !allowed[w] {
				return fmt.Errorf("%s: writer %q is not valid here (%s)", where, w, map[bool]string{true: "a team's: operator, proposal", false: "a bot's: operator, bots"}[team])
			}
		}
	}
	return nil
}
