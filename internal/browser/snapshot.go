package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// An accessibility snapshot: the page as a screen reader sees it, one line
// per meaningful node, each actionable element with a ref the other actions
// take ("e" + its backend DOM node id — stable while the element lives, so
// a ref from an earlier snapshot still names the same element).

type axValue struct {
	Value json.RawMessage `json:"value"`
}

func (v *axValue) str() string {
	if v == nil || len(v.Value) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(v.Value, &s) == nil {
		return s
	}
	return strings.Trim(string(v.Value), `"`)
}

type axNode struct {
	NodeID     string   `json:"nodeId"`
	Ignored    bool     `json:"ignored"`
	Role       *axValue `json:"role"`
	Name       *axValue `json:"name"`
	Value      *axValue `json:"value"`
	ParentID   string   `json:"parentId"`
	ChildIDs   []string `json:"childIds"`
	BackendID  int64    `json:"backendDOMNodeId"`
	Properties []axProp `json:"properties"`
}

type axProp struct {
	Name  string  `json:"name"`
	Value axValue `json:"value"`
}

// actionable roles get a ref.
var actionable = map[string]bool{
	"button": true, "link": true, "textbox": true, "searchbox": true, "combobox": true,
	"listbox": true, "option": true, "checkbox": true, "radio": true, "switch": true,
	"slider": true, "spinbutton": true, "menuitem": true, "menuitemcheckbox": true,
	"menuitemradio": true, "tab": true, "treeitem": true, "MenuListPopup": true,
	"ComboBoxSelect": true, "PopUpButton": true, "textField": true,
}

// transparent roles are not printed; their children are, one level up.
var transparent = map[string]bool{
	"none": true, "generic": true, "presentation": true, "InlineTextBox": true,
	"LineBreak": true, "RootWebArea": true, "WebArea": true, "group": false,
	"paragraph": true, "Section": true, "div": true, "LabelText": true,
}

// labelled roles carry their text in their name: their text children are
// not repeated.
var labelled = map[string]bool{
	"button": true, "link": true, "heading": true, "option": true, "menuitem": true,
	"tab": true, "checkbox": true, "radio": true, "switch": true, "treeitem": true,
	"cell": true, "columnheader": true, "rowheader": true, "listitem": false,
	"menuitemcheckbox": true, "menuitemradio": true, "image": true, "img": true,
}

// snapshot renders the active tab and records its refs.
func (in *Instance) snapshot(ctx context.Context, t *tab, max int) (string, int, error) {
	var r struct {
		Nodes []axNode `json:"nodes"`
	}
	if err := in.conn().Call(ctx, t.session, "Accessibility.getFullAXTree", map[string]any{}, &r); err != nil {
		return "", 0, err
	}
	// A field conductor filled never shows its value (a CVC or a region
	// code is too short to scrub from text: its node is what is known).
	in.mu.Lock()
	for i := range r.Nodes {
		if in.secretNodes[r.Nodes[i].BackendID] && r.Nodes[i].Value != nil {
			r.Nodes[i].Value = &axValue{Value: json.RawMessage(`"` + FilledMark + `"`)}
		}
	}
	in.mu.Unlock()
	text, refs := renderAX(r.Nodes, max)
	in.mu.Lock()
	if t.refs == nil {
		t.refs = map[string]int64{}
	}
	for ref, id := range refs {
		t.refs[ref] = id
	}
	in.mu.Unlock()
	return text, len(refs), nil
}

// FilledMark stands in a snapshot for the value of a field conductor filled.
const FilledMark = "«filled by conductor»"

// renderAX turns an AX tree into the snapshot text, and the refs it shows.
func renderAX(nodes []axNode, max int) (string, map[string]int64) {
	byID := make(map[string]*axNode, len(nodes))
	for i := range nodes {
		byID[nodes[i].NodeID] = &nodes[i]
	}
	var root *axNode
	for i := range nodes {
		if nodes[i].ParentID == "" {
			root = &nodes[i]
			break
		}
	}
	refs := map[string]int64{}
	var b strings.Builder
	lines, cut := 0, 0
	emit := func(depth int, s string) {
		if max > 0 && b.Len()+len(s) > max {
			cut++
			return
		}
		b.WriteString(strings.Repeat("  ", depth))
		b.WriteString(s)
		b.WriteByte('\n')
		lines++
	}
	var lastText string
	var walk func(n *axNode, depth int, quiet bool)
	walk = func(n *axNode, depth int, quiet bool) {
		if n == nil {
			return
		}
		role := n.Role.str()
		name := strings.TrimSpace(collapse(n.Name.str()))
		next := depth
		childQuiet := quiet
		switch {
		case n.Ignored || transparent[role]:
		case role == "StaticText":
			if !quiet && name != "" && name != lastText {
				emit(depth, "- text: "+clip(name, 300))
				lastText = name
			}
		default:
			line := "- " + role
			if name != "" {
				line += " " + strconv.Quote(clip(name, 200))
			}
			ref := ""
			if n.BackendID > 0 && (actionable[role] || boolProp(n, "focusable") && name != "" && role != "image") {
				ref = "e" + strconv.FormatInt(n.BackendID, 10)
				refs[ref] = n.BackendID
				line += " [ref=" + ref + "]"
			}
			line += props(n, role)
			if (name == "" && ref == "" && role != "image") && !hasProps(n) {
				// An unnamed container: its children stand for it.
				break
			}
			emit(depth, line)
			lastText = ""
			next = depth + 1
			childQuiet = quiet || labelled[role]
		}
		for _, c := range n.ChildIDs {
			walk(byID[c], next, childQuiet)
		}
	}
	walk(root, 0, false)
	if cut > 0 {
		fmt.Fprintf(&b, "… (snapshot truncated at %d bytes; pass --max to see more)\n", max)
	}
	_ = lines
	return b.String(), refs
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func boolProp(n *axNode, name string) bool {
	for _, p := range n.Properties {
		if p.Name == name {
			return string(p.Value.Value) == "true"
		}
	}
	return false
}

func hasProps(n *axNode) bool {
	for _, p := range n.Properties {
		switch p.Name {
		case "checked", "expanded", "selected", "level":
			return true
		}
	}
	return false
}

// props renders the states an agent acts on.
func props(n *axNode, role string) string {
	var out []string
	for _, p := range n.Properties {
		v := strings.Trim(string(p.Value.Value), `"`)
		switch p.Name {
		case "checked", "pressed":
			if v == "true" || v == "mixed" {
				out = append(out, p.Name)
				if v == "mixed" {
					out[len(out)-1] += "=mixed"
				}
			}
		case "disabled", "expanded", "selected", "required", "focused", "readonly":
			if v == "true" {
				out = append(out, p.Name)
			}
		case "level":
			out = append(out, "level="+v)
		}
	}
	if v := strings.TrimSpace(n.Value.str()); v != "" && (role == "textbox" || role == "searchbox" || role == "combobox" || role == "slider" || role == "spinbutton" || role == "textField") {
		out = append(out, "value="+strconv.Quote(clip(v, 200)))
	}
	if len(out) == 0 {
		return ""
	}
	return " [" + strings.Join(out, ", ") + "]"
}
