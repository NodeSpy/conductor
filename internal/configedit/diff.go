package configedit

import (
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// DiffOp is the kind of a diff line.
type DiffOp string

const (
	OpContext DiffOp = "context" // unchanged line shown for context
	OpAdd     DiffOp = "add"     // line present only in the new text
	OpDel     DiffOp = "del"     // line present only in the old text
)

// DiffLine is one line of a rendered diff.
type DiffLine struct {
	Op      DiffOp `json:"op"`
	Text    string `json:"text"`
	OldLine int    `json:"old_line,omitempty"` // 1-based line in the old text (0 for an add)
	NewLine int    `json:"new_line,omitempty"` // 1-based line in the new text (0 for a del)
}

// Diff is the result of comparing two config texts.
type Diff struct {
	Changed bool       `json:"changed"`
	Added   int        `json:"added"`   // lines only in the new text
	Removed int        `json:"removed"` // lines only in the old text
	Lines   []DiffLine `json:"lines"`   // the hunks, with context
	Unified string     `json:"unified"` // the classic unified-diff text
}

// ComputeDiff compares before and after line by line. An identical input yields
// {Changed:false} with no lines — the no-op guarantee the pipeline relies on.
// A single changed value yields one del + one add on the same line number.
func ComputeDiff(before, after []byte) Diff {
	a := difflib.SplitLines(string(before))
	b := difflib.SplitLines(string(after))

	m := difflib.NewMatcher(a, b)
	var d Diff
	for _, op := range m.GetOpCodes() {
		switch op.Tag {
		case 'e': // equal
		case 'r': // replace
			d.Removed += op.I2 - op.I1
			d.Added += op.J2 - op.J1
		case 'd': // delete
			d.Removed += op.I2 - op.I1
		case 'i': // insert
			d.Added += op.J2 - op.J1
		}
	}
	d.Changed = d.Added > 0 || d.Removed > 0
	if !d.Changed {
		return d
	}

	// Rendered hunk lines with 3 lines of context, for the review UI.
	d.Lines = groupedLines(a, b, m, 3)

	ud := difflib.UnifiedDiff{
		A: a, B: b, FromFile: "config.yaml", ToFile: "config.yaml (candidate)", Context: 3,
	}
	d.Unified, _ = difflib.GetUnifiedDiffString(ud)
	return d
}

// groupedLines turns the matcher's opcodes into a flat list of context/add/del
// lines, limiting runs of equal lines to `context` on each side of a change.
func groupedLines(a, b []string, m *difflib.SequenceMatcher, context int) []DiffLine {
	var out []DiffLine
	groups := m.GetGroupedOpCodes(context)
	for gi, group := range groups {
		if gi > 0 {
			out = append(out, DiffLine{Op: OpContext, Text: "…"})
		}
		for _, c := range group {
			switch c.Tag {
			case 'e':
				for i, j := c.I1, c.J1; i < c.I2; i, j = i+1, j+1 {
					out = append(out, DiffLine{Op: OpContext, Text: strings.TrimRight(a[i], "\n"), OldLine: i + 1, NewLine: j + 1})
				}
			case 'r':
				for i := c.I1; i < c.I2; i++ {
					out = append(out, DiffLine{Op: OpDel, Text: strings.TrimRight(a[i], "\n"), OldLine: i + 1})
				}
				for j := c.J1; j < c.J2; j++ {
					out = append(out, DiffLine{Op: OpAdd, Text: strings.TrimRight(b[j], "\n"), NewLine: j + 1})
				}
			case 'd':
				for i := c.I1; i < c.I2; i++ {
					out = append(out, DiffLine{Op: OpDel, Text: strings.TrimRight(a[i], "\n"), OldLine: i + 1})
				}
			case 'i':
				for j := c.J1; j < c.J2; j++ {
					out = append(out, DiffLine{Op: OpAdd, Text: strings.TrimRight(b[j], "\n"), NewLine: j + 1})
				}
			}
		}
	}
	return out
}
