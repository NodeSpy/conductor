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
// When the final path segment names a key that does not yet exist in an
// existing mapping, the key/value pair is inserted; creating whole missing
// parent sections is not supported (SetScalar returns an error naming the
// missing section) — the dashboard stages those as section adds, not scalar
// edits.
func (d *Document) SetScalar(path []string, value any) error {
	if len(path) == 0 {
		return fmt.Errorf("configedit: empty path")
	}
	rendered, err := renderScalar(value)
	if err != nil {
		return err
	}

	val, parent, _, found := d.node(path)
	if found {
		if val.Kind != yaml.ScalarNode {
			return fmt.Errorf("configedit: %s is not a scalar", strings.Join(path, "."))
		}
		if scalarEqual(val, value) {
			return nil // no-op: leave the text byte-for-byte
		}
		return d.spliceScalar(val, rendered)
	}
	// Not found: the last segment is a missing key in an existing mapping.
	if parent != nil && parent.Kind == yaml.MappingNode {
		return d.insertKey(path, rendered)
	}
	return fmt.Errorf("configedit: cannot set %s: section %q does not exist", strings.Join(path, "."), parentPath(path))
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
			suffix = remainder[idx:]           // the comment itself
			gap := remainder[:idx]             // value text + spaces before #
			trimmed := strings.TrimRight(gap, " \t")
			suffix = gap[len(trimmed):] + suffix // keep the exact whitespace gap
		}
	}
	newLine := prefix + rendered + suffix + eol
	lines[ln] = newLine
	d.src = []byte(strings.Join(lines, ""))
	return d.reparse()
}

// insertKey adds a new key/value pair to the mapping named by path[:len-1].
// The new line is indented to match the mapping's existing children and placed
// at the end of the mapping's block.
func (d *Document) insertKey(path []string, rendered string) error {
	parentSegs := path[:len(path)-1]
	key := path[len(path)-1]

	var mapping *yaml.Node
	if len(parentSegs) == 0 {
		mapping = d.mapping()
	} else {
		v, _, _, ok := d.node(parentSegs)
		if !ok {
			return fmt.Errorf("configedit: cannot add %s: section %q does not exist", strings.Join(path, "."), strings.Join(parentSegs, "."))
		}
		mapping = resolveAlias(v)
	}
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return fmt.Errorf("configedit: %q is not a mapping", strings.Join(parentSegs, "."))
	}

	indent := childIndentOf(mapping)
	lines := splitLinesKeepEnds(d.src)
	insertAt := mappingBlockEnd(mapping, lines)
	newLine := strings.Repeat(" ", indent) + key + ": " + rendered + "\n"

	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:insertAt]...)
	out = append(out, newLine)
	out = append(out, lines[insertAt:]...)
	// Ensure the line before the insert ends with a newline.
	if insertAt > 0 {
		if _, eol := splitEOL(out[insertAt-1]); eol == "" {
			out[insertAt-1] += "\n"
		}
	}
	d.src = []byte(strings.Join(out, ""))
	return d.reparse()
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
	d.src = []byte(strings.Join(out, ""))
	return d.reparse()
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
// indented, inferred from its first child key. Defaults to the mapping's own
// indent + 2 when it has no children.
func childIndentOf(m *yaml.Node) int {
	if len(m.Content) > 0 && m.Content[0].Column > 1 {
		return m.Content[0].Column - 1
	}
	if m.Column > 1 {
		return m.Column - 1
	}
	return 2
}

// mappingBlockEnd returns the 0-based line index just past the last line of a
// mapping's block (where a new child line is inserted).
func mappingBlockEnd(m *yaml.Node, lines []string) int {
	last := 0
	for _, c := range m.Content {
		if e := nodeMaxLine(c); e > last {
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
	last := nodeMaxLine(val)
	if floor > last {
		last = floor
	}
	return last
}

// nodeMaxLine returns the greatest 1-based line touched by a node (so, as a
// 0-based "one past the end" it is this value).
func nodeMaxLine(n *yaml.Node) int {
	if n == nil {
		return 0
	}
	m := n.Line
	for _, c := range n.Content {
		if e := nodeMaxLine(c); e > m {
			m = e
		}
	}
	// account for a block scalar's own extra lines
	if n.Kind == yaml.ScalarNode && strings.Contains(n.Value, "\n") {
		m += strings.Count(strings.TrimRight(n.Value, "\n"), "\n")
	}
	return m
}

func parentPath(path []string) string {
	if len(path) <= 1 {
		return "(root)"
	}
	return strings.Join(path[:len(path)-1], ".")
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
