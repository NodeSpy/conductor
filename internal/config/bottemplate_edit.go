package config

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Editing the operator's config for `conductor bot new` and `conductor bot
// grant`: the change is spliced into the file's text — one bot entry, its
// suggested triggers, a grant — so everything else (comments, layout,
// anchors) stays byte-for-byte as the operator wrote it, and the diff shows
// exactly the change. The caller validates the result on a copy.

// TemplateBotDraft is what `conductor bot new` instantiates.
type TemplateBotDraft struct {
	Name       string
	Template   LoadedTemplate
	Connectors map[string]string // explicit bindings
	Grants     map[string]string // widenings asked for, on top of the read-only ones
	Proactive  bool
	// Unbound are optional inputs left unbound: their requests are not
	// granted.
	Unbound []string
}

// ReadOnlyGrants are a template's requests at observe: what a new bot
// starts with.
func ReadOnlyGrants(t BotTemplate) map[string]string {
	out := map[string]string{}
	for v, r := range t.Verbs {
		if r.Max == TierObserve {
			out[v] = TierObserve
		}
	}
	return out
}

// CheckGrants refuses a grant the template does not allow: an unrequested
// verb, an unknown tier, or a tier looser than the template's maximum.
func CheckGrants(lt LoadedTemplate, grants map[string]string) error {
	for _, v := range sortedStringKeys(grants) {
		tier := grants[v]
		r, ok := lt.T.Verbs[v]
		if !ok {
			return fmt.Errorf("grant %s: template %s does not request it (it requests: %s)", v, lt.Ref(), strings.Join(sortedRequestKeys(lt.T.Verbs), ", "))
		}
		if TierRank(tier) < 0 {
			return fmt.Errorf("grant %s: tier %q is not one of %s", v, tier, strings.Join(BotTiers, ", "))
		}
		if !GrantAllowed(tier, r.Max) {
			return fmt.Errorf("grant %s: %s is looser than template %s allows — at most %s", v, tier, lt.Ref(), r.Max)
		}
	}
	return nil
}

// BotEntryYAML renders a draft's bots: entry.
func BotEntryYAML(d TemplateBotDraft) (string, error) {
	if !validBotName(d.Name) {
		return "", fmt.Errorf("bot name %q: lowercase letters, digits, - or _", d.Name)
	}
	grants := ReadOnlyGrants(d.Template.T)
	for v := range grants {
		in, _, _ := strings.Cut(v, ".")
		for _, u := range d.Unbound {
			if in == u {
				delete(grants, v)
			}
		}
	}
	if err := CheckGrants(d.Template, d.Grants); err != nil {
		return "", err
	}
	for v, t := range d.Grants {
		grants[v] = t
	}
	for in := range d.Connectors {
		if _, ok := d.Template.T.Connectors[in]; !ok {
			return "", fmt.Errorf("--connector %s: not an input of template %s (its inputs: %s)", in, d.Template.Ref(), strings.Join(sortedInputKeys(d.Template.T.Connectors), ", "))
		}
	}
	entry := &yaml.Node{Kind: yaml.MappingNode}
	add := func(k string, v *yaml.Node, comment string) {
		kn := &yaml.Node{Kind: yaml.ScalarNode, Value: k}
		kn.HeadComment = comment
		entry.Content = append(entry.Content, kn, v)
	}
	scalar := func(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: s} }
	mapping := func(m map[string]string, flow bool) *yaml.Node {
		n := &yaml.Node{Kind: yaml.MappingNode}
		if flow {
			n.Style = yaml.FlowStyle
		}
		for _, k := range sortedStringKeys(m) {
			n.Content = append(n.Content, scalar(k), scalar(m[k]))
		}
		return n
	}
	add("use", scalar(d.Template.Ref()), fmt.Sprintf("from template %s (%s %s): starts in practice mode with only its read-only verbs", d.Template.Ref(), d.Template.Pack.Name, d.Template.Pack.Version))
	if len(d.Connectors) > 0 {
		add("connectors", mapping(d.Connectors, true), "")
	}
	if len(grants) > 0 {
		add("grant", mapping(grants, false), "what you allow of what the template requests — anything above observe is a widening")
	}
	if d.Proactive {
		add("proactive", &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}, "the template's suggested proactive checks; set a budget: to bound them")
	}
	wrap := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar(d.Name), entry}}
	return encodeYAMLNode(wrap)
}

// TemplateTriggersYAML renders a template's suggested triggers for one bot,
// disarmed, on the connectors the bot's inputs are bound to. Inputs left
// unbound skip their triggers, as does an event in taken (one another
// trigger already listens on — a schedule drives exactly one); skipped says
// which and why.
func TemplateTriggersYAML(bot string, lt LoadedTemplate, bindings map[string]string, taken map[string]string) (string, []string, error) {
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	var skipped []string
	for _, tr := range lt.T.Triggers {
		in, ev, _ := strings.Cut(tr.On, ".")
		conn, ok := bindings[in]
		if !ok {
			skipped = append(skipped, fmt.Sprintf("suggested trigger %s: input %s is unbound", tr.Name, in))
			continue
		}
		if other, used := taken[conn+"."+ev]; used {
			skipped = append(skipped, fmt.Sprintf("suggested trigger %s: %s already drives trigger %s — not written; give %s its own (a schedule of its own, say) and route it with bot: { name: %s }", tr.Name, conn+"."+ev, other, bot, bot))
			continue
		}
		step := map[string]any{"name": bot}
		if tr.Action != "" && tr.Action != "deliver" {
			step["action"] = tr.Action
		} else {
			if tr.Title != "" {
				step["title"] = tr.Title
			}
			if tr.Text != "" {
				step["text"] = tr.Text
			}
			if tr.Urgent {
				step["urgent"] = true
			}
			if tr.Requester != "" {
				step["requester"] = tr.Requester
			}
		}
		t := map[string]any{"name": bot + "-" + tr.Name, "enabled": false, "on": conn + "." + ev,
			"steps": []any{map[string]any{"bot": step}}}
		if tr.Filter != nil {
			t["filter"] = tr.Filter
		}
		var n yaml.Node
		if err := n.Encode(t); err != nil {
			return "", nil, err
		}
		orderKeys(&n, []string{"name", "enabled", "on", "filter", "steps"})
		n.HeadComment = fmt.Sprintf("suggested by template %s — disarmed; set enabled: true to route %s into %s", lt.Ref(), conn+"."+ev, bot)
		seq.Content = append(seq.Content, &n)
	}
	if len(seq.Content) == 0 {
		return "", skipped, nil
	}
	out, err := encodeYAMLNode(seq)
	return out, skipped, err
}

func orderKeys(n *yaml.Node, order []string) {
	if n.Kind != yaml.MappingNode {
		return
	}
	rank := map[string]int{}
	for i, k := range order {
		rank[k] = i + 1
	}
	type kv struct{ k, v *yaml.Node }
	var kvs []kv
	for i := 0; i+1 < len(n.Content); i += 2 {
		kvs = append(kvs, kv{n.Content[i], n.Content[i+1]})
	}
	sort.SliceStable(kvs, func(i, j int) bool { return rank[kvs[i].k.Value] < rank[kvs[j].k.Value] })
	n.Content = n.Content[:0]
	for _, p := range kvs {
		n.Content = append(n.Content, p.k, p.v)
	}
}

func encodeYAMLNode(n *yaml.Node) (string, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return "", err
	}
	_ = enc.Close()
	return b.String(), nil
}

// topSection locates a top-level key: its key node, its value, and the line
// range its block occupies (1-based, inclusive), or found=false.
type topSection struct {
	key, val   *yaml.Node
	start, end int
	found      bool
}

func findTop(src []byte, key string) (topSection, *yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return topSection{}, nil, err
	}
	if len(doc.Content) == 0 {
		return topSection{}, nil, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return topSection{}, nil, fmt.Errorf("the config is not a YAML mapping")
	}
	lines := strings.Split(string(src), "\n")
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != key {
			continue
		}
		next := len(lines) + 1
		if i+2 < len(root.Content) {
			next = root.Content[i+2].Line
		}
		s := topSection{key: root.Content[i], val: root.Content[i+1], start: root.Content[i].Line, found: true}
		s.end = blockEnd(lines, maxLine(root.Content[i+1], root.Content[i].Line), next)
		return s, root, nil
	}
	return topSection{}, root, nil
}

// maxLine is the last line any node under n starts on.
func maxLine(n *yaml.Node, floor int) int {
	m := floor
	if n.Line > m {
		m = n.Line
	}
	for _, c := range n.Content {
		if l := maxLine(c, m); l > m {
			m = l
		}
	}
	return m
}

// blockEnd extends a block from its last node's line over the indented
// lines that continue it (a block scalar's body), stopping before next.
func blockEnd(lines []string, last, next int) int {
	end := last
	for l := last + 1; l < next && l <= len(lines); l++ {
		t := lines[l-1]
		if strings.TrimSpace(t) == "" {
			continue
		}
		if t[0] == ' ' || t[0] == '\t' {
			if !strings.HasPrefix(strings.TrimSpace(t), "#") {
				end = l
			}
			continue
		}
		break
	}
	return end
}

func indentText(s string, n int) string {
	pad := strings.Repeat(" ", n)
	var out []string
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if l == "" {
			out = append(out, l)
			continue
		}
		out = append(out, pad+l)
	}
	return strings.Join(out, "\n") + "\n"
}

// childIndent is the column a section's entries are written at.
func childIndent(val *yaml.Node) int {
	if len(val.Content) > 0 && val.Content[0].Column > 1 {
		return val.Content[0].Column - 1
	}
	return 2
}

// InsertTopLevel adds block (entries, unindented) to the top-level section
// key — appending the section when there is none. A section written in flow
// style is converted only when empty.
func InsertTopLevel(src []byte, key, block string) ([]byte, error) {
	if block == "" {
		return src, nil
	}
	sec, _, err := findTop(src, key)
	if err != nil {
		return nil, err
	}
	text := string(src)
	if !sec.found {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		return []byte(text + "\n" + key + ":\n" + indentText(block, 2)), nil
	}
	lines := strings.Split(text, "\n")
	ind := 2
	switch {
	case sec.val.Tag == "!!null" || (sec.val.Kind == yaml.ScalarNode && sec.val.Value == ""):
	case sec.val.Style == yaml.FlowStyle && len(sec.val.Content) == 0:
		lines[sec.start-1] = key + ":"
		sec.end = sec.start
	case sec.val.Style == yaml.FlowStyle:
		return nil, fmt.Errorf("%s: is written in flow style ({…}/[…]) — write it as a block to have conductor add to it", key)
	default:
		ind = childIndent(sec.val)
		if sec.val.Kind == yaml.SequenceNode && len(sec.val.Content) > 0 {
			ind = sec.val.Content[0].Column - 3 // the "- " sits before the item
			if ind < 0 {
				ind = 0
			}
		}
	}
	ins := strings.Split(strings.TrimRight(indentText(block, ind), "\n"), "\n")
	out := append([]string{}, lines[:sec.end]...)
	out = append(out, ins...)
	out = append(out, lines[sec.end:]...)
	return []byte(strings.Join(out, "\n")), nil
}

// SetBotGrants sets verbs → tiers in bots.<name>.grant, re-rendering that one
// entry.
func SetBotGrants(src []byte, name string, grants map[string]string) ([]byte, error) {
	sec, _, err := findTop(src, "bots")
	if err != nil {
		return nil, err
	}
	if !sec.found || sec.val.Kind != yaml.MappingNode || sec.val.Style == yaml.FlowStyle {
		return nil, fmt.Errorf("no bots: block (in block style) to change")
	}
	lines := strings.Split(string(src), "\n")
	for i := 0; i+1 < len(sec.val.Content); i += 2 {
		k, v := sec.val.Content[i], sec.val.Content[i+1]
		if k.Value != name {
			continue
		}
		next := sec.end + 1
		if i+2 < len(sec.val.Content) {
			next = sec.val.Content[i+2].Line
		}
		end := blockEnd(lines, maxLine(v, k.Line), next)
		var g *yaml.Node
		for j := 0; j+1 < len(v.Content); j += 2 {
			if v.Content[j].Value == "grant" {
				g = v.Content[j+1]
			}
		}
		if g == nil {
			g = &yaml.Node{Kind: yaml.MappingNode}
			v.Content = append(v.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "grant"}, g)
		}
		g.Style = 0
		for _, verb := range sortedStringKeys(grants) {
			set := false
			for j := 0; j+1 < len(g.Content); j += 2 {
				if g.Content[j].Value == verb {
					g.Content[j+1] = &yaml.Node{Kind: yaml.ScalarNode, Value: grants[verb]}
					set = true
				}
			}
			if !set {
				g.Content = append(g.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: verb}, &yaml.Node{Kind: yaml.ScalarNode, Value: grants[verb]})
			}
		}
		k.HeadComment, k.LineComment = "", ""
		rendered, err := encodeYAMLNode(&yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{k, v}})
		if err != nil {
			return nil, err
		}
		ins := strings.Split(strings.TrimRight(indentText(rendered, k.Column-1), "\n"), "\n")
		out := append([]string{}, lines[:k.Line-1]...)
		out = append(out, ins...)
		out = append(out, lines[end:]...)
		return []byte(strings.Join(out, "\n")), nil
	}
	return nil, fmt.Errorf("no bot %q in bots:", name)
}
