package bot

import (
	"bytes"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
)

// CONTENT KINDS (docs/wiki/Bots.md#content-kinds). Everything in a memory
// or knowledge repo is a typed document: a kind gives it a fixed home, a
// path derived from a slug, a frontmatter schema, and the writers allowed
// to touch it. Repo.write checks all of it — the one path every write takes:
// a bot's own memory writes, conductor's logs and handoffs, an operator's
// edits, and a promotion landing in a team's repo. Nothing is written
// anywhere else.

// Writer is who is writing.
type Writer string

const (
	// WriterOperator is an operator's direct edit (the control socket).
	WriterOperator Writer = "operator"
	// WriterBot is a bot session writing its own memory.
	WriterBot Writer = "bots"
	// WriterProposal is an approved (or direct) promotion landing.
	WriterProposal Writer = "proposal"
	// WriterSystem is conductor itself: log entries it composes for a case,
	// handoff notes, seeding.
	WriterSystem Writer = "system"
)

// Built-in kind names.
const (
	// KindKnowledge ("knowledge") is shared with the promotion proposal kind.
	KindRunbook = "runbook"
	KindWatch   = "watch"
	KindLog     = "log"
	KindHandoff = "handoff"
)

// Field is one frontmatter field of a kind's schema.
type Field struct {
	Name     string
	Type     string // string | text | list | steps | enum | number | bool
	Required bool
	Values   []string // enum
}

// ContentKind is one kind of document.
type ContentKind struct {
	Name string
	// Dir is the kind's home; Legacy are older homes still read (never
	// written): log's episodes/.
	Dir    string
	Legacy []string
	Fields []Field
	// FreePaths lets a file sit anywhere under Dir; otherwise its path is
	// exactly Dir/<slug>.md. (Log and handoff paths are composed by
	// conductor, so theirs are free.)
	FreePaths bool
	Writers   []Writer
	// Annotate lets an operator annotate or flag a document wrong without
	// being one of its writers (log).
	Annotate bool
	Template string
	Builtin  bool
}

// Writable reports whether w may write this kind.
func (k *ContentKind) Writable(w Writer) bool {
	for _, x := range k.Writers {
		if x == w {
			return true
		}
	}
	return false
}

func (k *ContentKind) field(name string) *Field {
	for i := range k.Fields {
		if k.Fields[i].Name == name {
			return &k.Fields[i]
		}
	}
	return nil
}

// Kinds is one repo's set of kinds.
type Kinds struct {
	list []*ContentKind
}

// builtinKinds is the built-in set: a bot's memory has all five; a team's
// knowledge repo the shared three (written by operators and promotions).
func builtinKinds(team bool) []*ContentKind {
	shared := []Writer{WriterOperator, WriterBot}
	if team {
		shared = []Writer{WriterOperator, WriterProposal}
	}
	ks := []*ContentKind{
		{Name: KindKnowledge, Dir: "knowledge", Builtin: true, Writers: shared, Fields: []Field{
			{Name: "title", Type: "string", Required: true},
			{Name: "applies_to", Type: "list"},
			{Name: "evidence", Type: "text", Required: true},
			{Name: "confidence", Type: "enum", Required: true, Values: []string{"low", "medium", "high"}},
		}, Template: "What is true, and why you believe it.\n"},
		{Name: KindRunbook, Dir: "runbooks", Builtin: true, Writers: shared, Fields: []Field{
			{Name: "title", Type: "string", Required: true},
			{Name: "when_to_use", Type: "text", Required: true},
			{Name: "prerequisites", Type: "list"},
			{Name: "steps", Type: "steps", Required: true},
			{Name: "verification", Type: "text", Required: true},
			{Name: "rollback", Type: "text", Required: true},
			{Name: "owner", Type: "string", Required: true},
		}, Template: "Notes on the procedure (the steps are in the frontmatter).\n"},
		{Name: KindWatch, Dir: "watch", Builtin: true, Writers: shared, Fields: []Field{
			{Name: "what", Type: "string", Required: true},
			{Name: "signal", Type: "text", Required: true},
			{Name: "threshold", Type: "text", Required: true},
			{Name: "why", Type: "text", Required: true},
		}},
	}
	if !team {
		ks = append(ks,
			&ContentKind{Name: KindLog, Dir: "log", Legacy: []string{"episodes"}, Builtin: true, FreePaths: true,
				Writers: []Writer{WriterSystem}, Annotate: true, Fields: []Field{
					{Name: "title", Type: "string", Required: true},
					{Name: "case", Type: "string"},
					{Name: "sweep", Type: "string"},
					{Name: "flagged_wrong", Type: "text"},
				}},
			&ContentKind{Name: KindHandoff, Dir: "handoff", Builtin: true, FreePaths: true,
				Writers: []Writer{WriterSystem}, Fields: []Field{
					{Name: "session", Type: "string", Required: true},
				}},
		)
	}
	return ks
}

// NewKinds is the built-in kinds plus a repo's collections.
func NewKinds(team bool, cols map[string]config.Collection) *Kinds {
	ks := &Kinds{list: builtinKinds(team)}
	names := make([]string, 0, len(cols))
	for n := range cols {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		c := cols[n]
		k := &ContentKind{Name: n, Dir: strings.Trim(c.Dir, "/"), FreePaths: c.FreePaths, Template: c.Template}
		fns := make([]string, 0, len(c.Fields))
		for fn := range c.Fields {
			fns = append(fns, fn)
		}
		sort.Strings(fns)
		for _, fn := range fns {
			f := c.Fields[fn]
			k.Fields = append(k.Fields, Field{Name: fn, Type: f.Type, Required: f.Required, Values: f.Values})
		}
		for _, w := range c.Writers {
			k.Writers = append(k.Writers, Writer(w))
		}
		ks.list = append(ks.list, k)
	}
	return ks
}

// List is every kind, built-ins first.
func (ks *Kinds) List() []*ContentKind { return ks.list }

// Get is a kind by name.
func (ks *Kinds) Get(name string) (*ContentKind, error) {
	for _, k := range ks.list {
		if k.Name == name {
			return k, nil
		}
	}
	return nil, fmt.Errorf("unknown kind %q (kinds here: %s)", name, ks.names())
}

func (ks *Kinds) names() string {
	var n []string
	for _, k := range ks.list {
		n = append(n, k.Name)
	}
	return strings.Join(n, ", ")
}

// Of is the kind whose home (or legacy home) holds p, the longest match.
func (ks *Kinds) Of(p string) (*ContentKind, bool) {
	var best *ContentKind
	bestLen := -1
	for _, k := range ks.list {
		for _, d := range append([]string{k.Dir}, k.Legacy...) {
			if strings.HasPrefix(p, d+"/") && len(d) > bestLen {
				best, bestLen = k, len(d)
			}
		}
	}
	return best, best != nil
}

// homeOf reports whether p is in k's current home (not a legacy one).
func (k *ContentKind) homeOf(p string) bool { return strings.HasPrefix(p, k.Dir+"/") }

var slugPathRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)

// Derive is a document's path: Dir/<slug>.md.
func (k *ContentKind) Derive(slug string) (string, error) {
	if !slugPathRe.MatchString(slug) {
		return "", fmt.Errorf("slug %q: lowercase letters, digits and -, starting with a letter or digit, at most 80", slug)
	}
	return k.Dir + "/" + slug + ".md", nil
}

// CheckPath refuses a path that is not where this kind's documents live:
// outside its home, in a legacy home (read-only), or — unless it has free
// paths — anything but Dir/<slug>.md.
func (k *ContentKind) CheckPath(p string) error {
	if !k.homeOf(p) {
		for _, d := range k.Legacy {
			if strings.HasPrefix(p, d+"/") {
				return fmt.Errorf("%s: %s/ is a legacy home of %s documents, read-only — new ones go under %s/", p, d, k.Name, k.Dir)
			}
		}
		return fmt.Errorf("%s: a %s document lives under %s/, not here", p, k.Name, k.Dir)
	}
	if path.Ext(p) != ".md" {
		return fmt.Errorf("%s: a %s document is a .md file", p, k.Name)
	}
	if k.FreePaths {
		return nil
	}
	rest := strings.TrimSuffix(strings.TrimPrefix(p, k.Dir+"/"), ".md")
	if strings.Contains(rest, "/") || !slugPathRe.MatchString(rest) {
		return fmt.Errorf("%s: %s paths are %s/<slug>.md (slug: lowercase letters, digits, -) — free paths are only for a collection with free_paths: true", p, k.Name, k.Dir)
	}
	return nil
}

// SplitFrontmatter splits a document into its YAML frontmatter and body.
// ok is false when it has none.
func SplitFrontmatter(text string) (fm map[string]any, body string, ok bool, err error) {
	t := strings.TrimLeft(text, "\ufeff")
	if !strings.HasPrefix(t, "---\n") {
		return nil, text, false, nil
	}
	end := strings.Index(t[4:], "\n---")
	if end < 0 {
		return nil, text, false, fmt.Errorf("frontmatter: no closing ---")
	}
	raw := t[4 : 4+end]
	rest := t[4+end+4:]
	if i := strings.IndexByte(rest, '\n'); i >= 0 {
		if strings.TrimSpace(rest[:i]) != "" {
			return nil, text, false, fmt.Errorf("frontmatter: the closing --- must be on a line of its own")
		}
		rest = rest[i+1:]
	} else {
		if strings.TrimSpace(rest) != "" {
			return nil, text, false, fmt.Errorf("frontmatter: the closing --- must be on a line of its own")
		}
		rest = ""
	}
	fm = map[string]any{}
	if err := yaml.Unmarshal([]byte(raw), &fm); err != nil {
		return nil, text, false, fmt.Errorf("frontmatter: not YAML: %v", err)
	}
	if fm == nil {
		fm = map[string]any{}
	}
	return fm, rest, true, nil
}

// JoinFrontmatter renders a document from its frontmatter and body, fields
// in the kind's order.
func (k *ContentKind) JoinFrontmatter(fm map[string]any, body string) string {
	var b bytes.Buffer
	b.WriteString("---\n")
	for _, f := range k.Fields {
		v, ok := fm[f.Name]
		if !ok {
			continue
		}
		out, _ := yaml.Marshal(map[string]any{f.Name: v})
		b.Write(out)
	}
	b.WriteString("---\n")
	b.WriteString(strings.TrimLeft(body, "\n"))
	return b.String()
}

// Validate checks a document against the kind's schema: frontmatter
// present, every required field set, every field of its type, no field the
// schema does not name. The error lists every problem, precisely.
func (k *ContentKind) Validate(text string) error {
	fm, _, ok, err := SplitFrontmatter(text)
	if err != nil {
		return fmt.Errorf("%s: %v", k.Name, err)
	}
	if !ok {
		return fmt.Errorf("%s: no frontmatter — a %s document starts with a --- YAML block (fields: %s)", k.Name, k.Name, k.fieldList())
	}
	var probs []string
	for _, f := range k.Fields {
		v, present := fm[f.Name]
		if !present || v == nil || v == "" {
			if f.Required {
				probs = append(probs, fmt.Sprintf("missing required field %s (%s)", f.Name, f.Type))
			}
			continue
		}
		if p := checkField(f, v); p != "" {
			probs = append(probs, fmt.Sprintf("field %s: %s", f.Name, p))
		}
	}
	var unknown []string
	for name := range fm {
		if k.field(name) == nil {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, u := range unknown {
		probs = append(probs, fmt.Sprintf("unknown field %s", u))
	}
	if len(probs) > 0 {
		return fmt.Errorf("%s schema: %s (fields: %s)", k.Name, strings.Join(probs, "; "), k.fieldList())
	}
	return nil
}

func (k *ContentKind) fieldList() string {
	var out []string
	for _, f := range k.Fields {
		s := f.Name + " " + f.Type
		if f.Type == "enum" {
			s += " (" + strings.Join(f.Values, "|") + ")"
		}
		if f.Required {
			s += ", required"
		}
		out = append(out, s)
	}
	return strings.Join(out, "; ")
}

func checkField(f Field, v any) string {
	switch f.Type {
	case "string":
		s, ok := v.(string)
		if !ok {
			return fmt.Sprintf("want a string, got %T", v)
		}
		if strings.Contains(s, "\n") {
			return "want one line"
		}
	case "text":
		if _, ok := v.(string); !ok {
			return fmt.Sprintf("want text, got %T", v)
		}
	case "list", "steps":
		xs, ok := v.([]any)
		if !ok {
			return fmt.Sprintf("want a list, got %T", v)
		}
		if f.Type == "steps" && len(xs) == 0 {
			return "want at least one step"
		}
		for i, x := range xs {
			if s, ok := x.(string); !ok || strings.TrimSpace(s) == "" {
				return fmt.Sprintf("item %d: want a non-empty string", i+1)
			}
		}
	case "enum":
		s, _ := v.(string)
		for _, ok := range f.Values {
			if s == ok {
				return ""
			}
		}
		return fmt.Sprintf("%v is not one of %s", v, strings.Join(f.Values, ", "))
	case "number":
		switch v.(type) {
		case int, int64, float64:
		default:
			return fmt.Sprintf("want a number, got %T", v)
		}
	case "bool":
		if _, ok := v.(bool); !ok {
			return fmt.Sprintf("want true or false, got %T", v)
		}
	}
	return ""
}

// CheckWrite is the chokepoint's rule for one write: p is in a kind's home
// at an allowed path, w may write that kind (wantKind, when named, is that
// kind), and the text — what the file will hold — is valid for it.
func (ks *Kinds) CheckWrite(p, text string, w Writer, wantKind string) (*ContentKind, error) {
	k, ok := ks.Of(p)
	if !ok {
		return nil, fmt.Errorf("%s is not in any kind's home (kinds here: %s)", p, ks.homes())
	}
	if wantKind != "" && wantKind != k.Name {
		want, err := ks.Get(wantKind)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%s is a %s path (%s/); a %s document lives under %s/", p, k.Name, k.Dir, want.Name, want.Dir)
	}
	if err := k.CheckPath(p); err != nil {
		return nil, err
	}
	if !k.Writable(w) {
		return nil, fmt.Errorf("%s: %s documents are written by %s, not by %s", p, k.Name, writerList(k.Writers), w)
	}
	if err := k.Validate(text); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return k, nil
}

// CheckRemove is the rule for deleting or moving a document away.
func (ks *Kinds) CheckRemove(p string, w Writer) error {
	k, ok := ks.Of(p)
	if !ok {
		return fmt.Errorf("%s is not in any kind's home", p)
	}
	if !k.Writable(w) {
		return fmt.Errorf("%s: %s documents are written by %s — %s cannot remove or move one", p, k.Name, writerList(k.Writers), w)
	}
	return nil
}

func writerList(ws []Writer) string {
	var out []string
	for _, w := range ws {
		if w == WriterSystem {
			out = append(out, "conductor only")
			continue
		}
		out = append(out, string(w))
	}
	return strings.Join(out, ", ")
}

func (ks *Kinds) homes() string {
	var out []string
	for _, k := range ks.list {
		out = append(out, k.Name+" "+k.Dir+"/")
	}
	return strings.Join(out, ", ")
}

// Describe is a kind for an agent or an operator: its home, its path rule,
// its writers, its fields, and a skeleton to start from.
func (k *ContentKind) Describe() map[string]any {
	var fields []any
	for _, f := range k.Fields {
		e := map[string]any{"name": f.Name, "type": f.Type, "required": f.Required}
		if len(f.Values) > 0 {
			e["values"] = f.Values
		}
		fields = append(fields, e)
	}
	pathRule := k.Dir + "/<slug>.md"
	if k.FreePaths {
		pathRule = k.Dir + "/<any path>.md"
	}
	return map[string]any{"kind": k.Name, "dir": k.Dir + "/", "path": pathRule, "writers": writerList(k.Writers),
		"fields": fields, "skeleton": k.Skeleton(nil)}
}

// Skeleton is a document of this kind with the given fields set and every
// other required one shown as a placeholder (which Validate still refuses
// for a non-string type).
func (k *ContentKind) Skeleton(set map[string]any) string {
	fm := map[string]any{}
	for _, f := range k.Fields {
		if v, ok := set[f.Name]; ok {
			fm[f.Name] = v
			continue
		}
		if !f.Required {
			continue
		}
		switch f.Type {
		case "list", "steps":
			fm[f.Name] = []any{"<" + f.Name + ">"}
		case "enum":
			fm[f.Name] = f.Values[0]
		case "number":
			fm[f.Name] = 0
		case "bool":
			fm[f.Name] = false
		default:
			fm[f.Name] = "<" + f.Name + ">"
		}
	}
	return k.JoinFrontmatter(fm, k.Template)
}
