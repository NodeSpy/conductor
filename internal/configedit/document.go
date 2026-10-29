// Package configedit edits conductor's live config file the way an operator
// would: one value at a time, spliced into the file's text so every comment,
// blank line, anchor and section the editor did not touch stays byte-for-byte
// as it was written. It is the engine behind the dashboard's Config screen and
// the backup→validate-on-copy→diff→apply→reload pipeline in pipeline.go.
//
// The splice approach is deliberate. Re-encoding the whole document through
// yaml.v3's emitter — the obvious implementation — reflows indentation, drops
// comments on sections the edit never touched, and reorders nothing but
// reformats everything, so a one-value change produces a noisy diff and a
// no-op change produces a diff at all. (That was the web prototype's known
// failure.) Locating the one scalar node by its path and rewriting only its
// value token leaves the rest of the file alone: a no-op edit yields an empty
// diff, and a one-value edit yields a one-line diff.
package configedit

import (
	"bytes"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Document is a parsed config file whose text is edited in place. The original
// bytes are the source of truth; the yaml.Node tree is only a map from a
// logical path to a line/column in that text.
type Document struct {
	src  []byte
	root *yaml.Node // the DocumentNode
}

// Parse reads config bytes into a Document. An empty input is a valid empty
// document (edits create the top-level mapping).
func Parse(src []byte) (*Document, error) {
	d := &Document{src: src}
	if len(bytes.TrimSpace(src)) == 0 {
		return d, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("configedit: parse: %w", err)
	}
	if doc.Kind != yaml.DocumentNode {
		return nil, fmt.Errorf("configedit: parse: not a YAML document")
	}
	d.root = &doc
	return d, nil
}

// Bytes returns the current document text.
func (d *Document) Bytes() []byte { return d.src }

// mapping returns the top-level mapping node, or nil if the document is empty
// or not a mapping.
func (d *Document) mapping() *yaml.Node {
	if d.root == nil || len(d.root.Content) == 0 {
		return nil
	}
	top := d.root.Content[0]
	if top.Kind != yaml.MappingNode {
		return nil
	}
	return top
}

// node walks path and returns the value node at it, plus its parent mapping (or
// sequence) and the key node when the parent is a mapping. found is false when
// the path does not resolve.
func (d *Document) node(path []string) (val, parent, key *yaml.Node, found bool) {
	cur := d.mapping()
	if cur == nil {
		return nil, nil, nil, false
	}
	// cur is the mapping/sequence we are descending into.
	var container *yaml.Node = cur
	for i, seg := range path {
		switch container.Kind {
		case yaml.MappingNode:
			k, v := mapChild(container, seg)
			if v == nil {
				return nil, container, k, false
			}
			if i == len(path)-1 {
				return v, container, k, true
			}
			container = resolveAlias(v)
		case yaml.SequenceNode:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(container.Content) {
				return nil, container, nil, false
			}
			v := container.Content[idx]
			if i == len(path)-1 {
				return v, container, nil, true
			}
			container = resolveAlias(v)
		default:
			return nil, container, nil, false
		}
	}
	return nil, nil, nil, false
}

// Get returns the decoded value at path (as yaml would decode it) and whether
// the path resolves.
func (d *Document) Get(path []string) (any, bool) {
	v, _, _, ok := d.node(path)
	if !ok {
		return nil, false
	}
	var out any
	if err := v.Decode(&out); err != nil {
		return v.Value, true
	}
	return out, true
}

// SetScalar sets the scalar value at path. The change is spliced into the text:
// only the one value token is rewritten and any inline comment on the line is
// preserved. A change whose new value is semantically equal to the current one
// is a no-op (the text is left exactly as it was), so staging a value back to
// itself produces an empty diff.
//
// value may be a scalar, a map or a list (the shapes a JSON request decodes
// to). A scalar written over an existing scalar is spliced in place, keeping
// the line's inline comment. Anything structural rewrites only the one entry it
// names: a new key is inserted at the end of its mapping, an existing entry's
// block is replaced, and a bare "section:" with nothing under it is filled.
// Missing parent sections are created. Every edit is all-or-nothing: if the
// result does not parse, or does not read back as value at path, the text is
// left exactly as it was and an error is returned. Refused outright: a parent
// written in flow style ({...}), a path through a YAML alias, an index past the
// end of a list, replacing a list item with a structure, and a multi-line
// string (it has no single-line form).
func (d *Document) SetScalar(path []string, value any) error {
	if len(path) == 0 {
		return fmt.Errorf("configedit: empty path")
	}
	structured := isStructured(value)
	var rendered string
	if !structured {
		r, err := renderScalar(value)
		if err != nil {
			return err
		}
		rendered = r
	}
	loc, err := d.locate(path)
	if err != nil {
		return err
	}
	if loc.found() && scalarEqual(loc.val, value) {
		return nil // no-op: leave the text byte-for-byte
	}

	oldSrc, oldRoot := d.src, d.root
	switch {
	case loc.found() && !structured && loc.val.Kind == yaml.ScalarNode && !isBlockScalar(loc.val):
		err = d.spliceScalar(loc.val, rendered)
	case loc.found():
		err = d.replaceEntry(loc, path, value)
	default:
		err = d.createEntry(loc, path, value)
	}
	if err != nil {
		return err
	}
	// The edit must land where it was aimed: a mis-indented insert can parse
	// cleanly yet attach the key to a different section.
	if got, _, _, ok := d.node(path); !ok || !scalarEqual(got, value) {
		d.src, d.root = oldSrc, oldRoot
		return fmt.Errorf("configedit: setting %s did not produce the requested value; the file is unchanged", strings.Join(path, "."))
	}
	return nil
}

// spliceScalar rewrites the value token of an existing scalar node in place,
// preserving the line's prefix (indent + "key: ") and any trailing inline
// comment.
func (d *Document) spliceScalar(val *yaml.Node, rendered string) error {
	lines := splitLinesKeepEnds(d.src)
	ln := val.Line - 1
	if ln < 0 || ln >= len(lines) {
		return fmt.Errorf("configedit: node line %d out of range", val.Line)
	}
	line, eol := splitEOL(lines[ln])
	col := val.Column - 1
	if col < 0 || col > len(line) {
		return fmt.Errorf("configedit: node column %d out of range on line %d", val.Column, val.Line)
	}
	prefix := line[:col]
	remainder := line[col:]

	// Preserve a trailing inline comment (yaml stores it with its leading #).
	suffix := ""
	if c := val.LineComment; c != "" {
		if idx := strings.LastIndex(remainder, c); idx >= 0 {
			suffix = remainder[idx:] // the comment itself
			gap := remainder[:idx]   // value text + spaces before #
			trimmed := strings.TrimRight(gap, " \t")
			suffix = gap[len(trimmed):] + suffix // keep the exact whitespace gap
		}
	}
	newLine := prefix + rendered + suffix + eol
	lines[ln] = newLine
	return d.commit([]byte(strings.Join(lines, "")))
}

// location is how far a path resolves in the document.
type location struct {
	n, depth int        // path length; how many segments resolved
	val      *yaml.Node // the node at path[:depth] (nil at depth 0)
	parent   *yaml.Node // the mapping or sequence holding val
	key      *yaml.Node // val's key, when parent is a mapping
	viaAlias bool       // a step before val went through a YAML alias
}

func (l location) found() bool { return l.depth == l.n }

// locate walks path as far as it resolves. A missing mapping key, or reaching
// a scalar, stops the walk without error; an index past the end of a list is
// an error, since there is nothing sensible to create.
func (d *Document) locate(path []string) (location, error) {
	l := location{n: len(path)}
	container := d.mapping()
	if container == nil {
		if d.root != nil && len(d.root.Content) > 0 {
			return l, fmt.Errorf("configedit: the config's top level is not a mapping")
		}
		return l, nil // empty document
	}
	for l.depth < len(path) {
		seg := path[l.depth]
		var k, v *yaml.Node
		switch container.Kind {
		case yaml.MappingNode:
			k, v = mapChild(container, seg)
		case yaml.SequenceNode:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(container.Content) {
				return l, fmt.Errorf("configedit: %s has no item %q", pathOrRoot(path[:l.depth]), seg)
			}
			v = container.Content[idx]
		default:
			return l, nil // a scalar: createEntry decides whether it can hold a section
		}
		if v == nil {
			return l, nil
		}
		l.parent, l.key, l.val = container, k, v
		l.depth++
		if l.depth < len(path) {
			r := resolveAlias(v)
			if r != v {
				l.viaAlias = true
			}
			container = r
		}
	}
	return l, nil
}

// createEntry adds path, which resolves only to loc.depth, creating whatever
// sections are missing below the deepest one that exists.
func (d *Document) createEntry(loc location, path []string, value any) error {
	if loc.viaAlias {
		return fmt.Errorf("configedit: %s goes through a YAML alias; edit the anchored section instead", strings.Join(path, "."))
	}
	rest := path[loc.depth:]
	if d.mapping() == nil { // empty document: the first top-level key
		text, err := renderEntry("", rest[0], nest(rest[1:], value), 0)
		if err != nil {
			return err
		}
		src := d.src
		if len(src) > 0 && src[len(src)-1] != '\n' {
			src = append(append([]byte{}, src...), '\n')
		}
		return d.commit(append(append([]byte{}, src...), text...))
	}
	container := d.mapping()
	if loc.depth > 0 {
		container = resolveAlias(loc.val)
	}
	switch {
	case container.Kind == yaml.MappingNode:
		if container.Style&yaml.FlowStyle != 0 {
			return fmt.Errorf("configedit: %s is written in flow style ({...}); edit it by hand", pathOrRoot(path[:loc.depth]))
		}
		return d.insertEntry(container, rest[0], nest(rest[1:], value))
	case container == loc.val && container.Kind == yaml.ScalarNode && container.Tag == "!!null" && loc.key != nil:
		// "section:" with nothing under it.
		return d.replaceEntry(loc, path[:loc.depth], nest(rest, value))
	default:
		return fmt.Errorf("configedit: cannot set %s: %s is a value, not a section", strings.Join(path, "."), pathOrRoot(path[:loc.depth]))
	}
}

// insertEntry appends key: value to a block mapping, indented like its
// existing children.
func (d *Document) insertEntry(mapping *yaml.Node, key string, value any) error {
	indent := childIndentOf(mapping)
	text, err := renderEntry(strings.Repeat(" ", indent), key, value, indent)
	if err != nil {
		return err
	}
	lines := splitLinesKeepEnds(d.src)
	at := mappingBlockEnd(mapping, lines)
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:at]...)
	if at > 0 {
		if _, eol := splitEOL(out[at-1]); eol == "" {
			out[at-1] += "\n"
		}
	}
	out = append(out, text)
	out = append(out, lines[at:]...)
	return d.commit([]byte(strings.Join(out, "")))
}

// replaceEntry rewrites the whole entry (key line through the end of its
// value's block) for the mapping key at loc. The key's head comment and
// everything outside the entry stay as written.
func (d *Document) replaceEntry(loc location, path []string, value any) error {
	if loc.viaAlias {
		return fmt.Errorf("configedit: %s goes through a YAML alias; edit the anchored section instead", strings.Join(path, "."))
	}
	if loc.key == nil || loc.parent == nil {
		return fmt.Errorf("configedit: replacing list item %s with a section isn't supported; edit the list", strings.Join(path, "."))
	}
	if loc.parent.Style&yaml.FlowStyle != 0 {
		return fmt.Errorf("configedit: %s sits in a flow-style section ({...}); edit it by hand", strings.Join(path, "."))
	}
	lines := splitLinesKeepEnds(d.src)
	start := loc.key.Line - 1
	end := blockEndLine(loc.val, loc.key, lines)
	if start < 0 || start >= len(lines) || end > len(lines) || end <= start {
		return fmt.Errorf("configedit: cannot locate %s to replace", strings.Join(path, "."))
	}
	line, _ := splitEOL(lines[start])
	col := loc.key.Column - 1
	if col < 0 || col > len(line) {
		return fmt.Errorf("configedit: key column out of range for %s", strings.Join(path, "."))
	}
	// line[:col] is the indent, or "  - " for the first key of a list item.
	text, err := renderEntry(line[:col], loc.key.Value, value, col)
	if err != nil {
		return err
	}
	out := append([]string{}, lines[:start]...)
	out = append(out, text)
	out = append(out, lines[end:]...)
	return d.commit([]byte(strings.Join(out, "")))
}

// commit adopts new text only if it parses; otherwise the document is left
// exactly as it was.
func (d *Document) commit(src []byte) error {
	old := d.src
	d.src = src
	if err := d.reparse(); err != nil {
		d.src = old
		return err
	}
	return nil
}

// Delete removes the key/value pair (or sequence element) at path.
func (d *Document) Delete(path []string) error {
	if len(path) == 0 {
		return fmt.Errorf("configedit: empty path")
	}
	val, parent, key, found := d.node(path)
	if !found {
		return nil // already absent
	}
	lines := splitLinesKeepEnds(d.src)
	var start, end int // [start,end) line range (0-based) to remove
	if parent.Kind == yaml.MappingNode && key != nil {
		start = key.Line - 1
		if key.HeadComment != "" {
			start -= strings.Count(key.HeadComment, "\n") + 1
			if start < 0 {
				start = 0
			}
		}
	} else {
		start = val.Line - 1
	}
	end = blockEndLine(val, key, lines)
	if start < 0 || start >= len(lines) || end > len(lines) || end <= start {
		return fmt.Errorf("configedit: cannot locate %s for delete", strings.Join(path, "."))
	}
	out := append([]string{}, lines[:start]...)
	out = append(out, lines[end:]...)
	return d.commit([]byte(strings.Join(out, "")))
}

// reparse re-reads the tree after a text edit so subsequent edits see correct
// line/column positions.
func (d *Document) reparse() error {
	if len(bytes.TrimSpace(d.src)) == 0 {
		d.root = nil
		return nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(d.src, &doc); err != nil {
		return fmt.Errorf("configedit: re-parse after edit: %w", err)
	}
	d.root = &doc
	return nil
}

// --- helpers ---

// mapChild returns the key and value nodes for key in a mapping, or nil,nil.
func mapChild(m *yaml.Node, key string) (knode, vnode *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

func resolveAlias(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}

// renderScalar renders a Go value as a single-line YAML scalar token. It
// returns an error for values that would need a block scalar (multi-line
// strings) — those are not inline-spliceable.
func renderScalar(v any) (string, error) {
	b, err := yaml.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("configedit: render value: %w", err)
	}
	s := strings.TrimRight(string(b), "\n")
	if strings.Contains(s, "\n") {
		return "", fmt.Errorf("configedit: value is not a single-line scalar")
	}
	return s, nil
}

// scalarEqual reports whether the node's decoded value is semantically equal to
// v, comparing canonical YAML encodings so `yes`==`true` and `8`==`8.0`.
func scalarEqual(node *yaml.Node, v any) bool {
	var cur any
	if err := node.Decode(&cur); err != nil {
		cur = node.Value
	}
	cb, err1 := yaml.Marshal(cur)
	vb, err2 := yaml.Marshal(v)
	if err1 != nil || err2 != nil {
		return reflect.DeepEqual(cur, v)
	}
	return bytes.Equal(cb, vb)
}

// childIndentOf returns the column (0-based) at which a mapping's children are
// indented, inferred from its first child key (column 0 for the top level).
func childIndentOf(m *yaml.Node) int {
	if len(m.Content) > 0 {
		return m.Content[0].Column - 1
	}
	return m.Column + 1
}

// mappingBlockEnd returns the 0-based line index just past the last line of a
// mapping's block (where a new child line is inserted).
func mappingBlockEnd(m *yaml.Node, lines []string) int {
	last := 0
	for _, c := range m.Content {
		if e := nodeMaxLine(c, lines); e > last {
			last = e
		}
	}
	// Walk back over trailing blank lines so the new key sits snug under the
	// last real child, not after a blank separator.
	idx := last // 0-based line just past `last` (which is 1-based)
	for idx < len(lines) && strings.TrimSpace(stripEOL(lines[idx])) == "" {
		// a blank line that belongs to this block's trailing whitespace
		break
	}
	return idx
}

// blockEndLine returns the 0-based line index just past the block owned by a
// value node (and its key's head comment), for deletion.
func blockEndLine(val, key *yaml.Node, lines []string) int {
	floor := 0
	if key != nil {
		floor = key.Line
	}
	last := nodeMaxLine(val, lines)
	if floor > last {
		last = floor
	}
	return last
}

// nodeMaxLine returns the greatest 1-based line touched by a node (so, as a
// 0-based "one past the end" it is this value).
func nodeMaxLine(n *yaml.Node, lines []string) int {
	if n == nil {
		return 0
	}
	m := n.Line
	for _, c := range n.Content {
		if e := nodeMaxLine(c, lines); e > m {
			m = e
		}
	}
	if isBlockScalar(n) {
		if e := blockScalarEnd(n, lines); e > m {
			m = e
		}
	}
	return m
}

func isBlockScalar(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0
}

// blockScalarEnd returns the 1-based last source line of a literal (|) or
// folded (>) scalar. It reads the text, not the decoded value: folding and
// chomping mean the value's line count doesn't match the source. The content is
// every following line indented past the indicator's line; trailing blank lines
// belong to it only with keep chomping (|+, >+).
func blockScalarEnd(n *yaml.Node, lines []string) int {
	i := n.Line - 1
	if i < 0 || i >= len(lines) {
		return n.Line
	}
	head := stripEOL(lines[i])
	base := indentOf(head)
	keep := false
	if c := n.Column - 1; c >= 0 && c < len(head) {
		indicator := strings.Fields(head[c:])
		keep = len(indicator) > 0 && strings.Contains(indicator[0], "+")
	}
	last := n.Line
	for j := i + 1; j < len(lines); j++ {
		line := stripEOL(lines[j])
		if strings.TrimSpace(line) == "" {
			if keep {
				last = j + 1
			}
			continue
		}
		if indentOf(line) <= base {
			break
		}
		last = j + 1
	}
	return last
}

func indentOf(s string) int {
	return len(s) - len(strings.TrimLeft(s, " "))
}

func pathOrRoot(path []string) string {
	if len(path) == 0 {
		return "the top level"
	}
	return strings.Join(path, ".")
}

// splitLinesKeepEnds splits text into lines, each retaining its trailing "\n"
// (the last line may have none). Joining the result reproduces the input.
func splitLinesKeepEnds(b []byte) []string {
	s := string(b)
	if s == "" {
		return []string{}
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// splitEOL splits a line (which may retain its newline) into its content and
// the trailing newline ("" if none).
func splitEOL(line string) (content, eol string) {
	if strings.HasSuffix(line, "\n") {
		return line[:len(line)-1], "\n"
	}
	return line, ""
}

func stripEOL(line string) string { c, _ := splitEOL(line); return c }

// isStructured reports whether v is a map or a list (as JSON decodes them).
func isStructured(v any) bool {
	if v == nil {
		return false
	}
	switch reflect.ValueOf(v).Kind() {
	case reflect.Map, reflect.Slice, reflect.Array:
		return true
	}
	return false
}

// nest wraps value in one single-key map per segment: nest([a b], v) = {a: {b: v}}.
func nest(segs []string, value any) any {
	for i := len(segs) - 1; i >= 0; i-- {
		value = map[string]any{segs[i]: value}
	}
	return value
}

// renderEntry renders "key: value" as complete lines. prefix is written before
// the key on the first line (indent, or "  - " for a list item's first key);
// keyCol is the key's column, which nested lines are indented two past.
func renderEntry(prefix, key string, value any, keyCol int) (string, error) {
	k, err := renderScalar(key)
	if err != nil {
		return "", err
	}
	if !isStructured(value) {
		v, err := renderScalar(value)
		if err != nil {
			return "", err
		}
		return prefix + k + ": " + v + "\n", nil
	}
	rv := reflect.ValueOf(value)
	if rv.Len() == 0 {
		if rv.Kind() == reflect.Map {
			return prefix + k + ": {}\n", nil
		}
		return prefix + k + ": []\n", nil
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(value); err != nil {
		return "", fmt.Errorf("configedit: render %s: %w", key, err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("configedit: render %s: %w", key, err)
	}
	pad := strings.Repeat(" ", keyCol+2)
	var b strings.Builder
	b.WriteString(prefix + k + ":\n")
	body := strings.Split(buf.String(), "\n")
	for _, ln := range body[:len(body)-1] { // the encoder ends with a newline
		if ln == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(pad + ln + "\n")
	}
	return b.String(), nil
}
