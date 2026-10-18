package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Instantiating a template into a bot. A `bots:` entry with `use:` takes
// from its template what is behavior — description, scope, escalation,
// charter, seed documents, collections, the suggested model and proactive
// checklist — wherever the operator left it unset, and takes VERBS only
// from its own `grant:`. The template's requests bound the grant (a looser
// tier is cut to the template's maximum); they never add to it.

// BotOrigin is a template bot's provenance and what its grant came to.
type BotOrigin struct {
	Instance string `json:"pack_instance"`
	Template string `json:"template"`
	Pack     string `json:"pack"`
	Version  string `json:"version"`
	Source   string `json:"source,omitempty"`
	Digest   string `json:"digest"`
	// Charter is the template's charter text (what seeds the memory repo's
	// charter document); Dir the vendored pack.
	Charter string `json:"-"`
	Dir     string `json:"-"`
	// SeedDir is the template's seed in the vendored pack.
	SeedDir string `json:"-"`
	// Bindings maps each template input to the operator's connector.
	Bindings map[string]string `json:"bindings,omitempty"`
	// ReadOnly are the granted verbs at observe; Widenings what the bot may
	// do beyond the template's read-only baseline; Ungranted the requests
	// the operator has not granted (verb → maximum tier).
	ReadOnly  []string          `json:"read_only,omitempty"`
	Widenings []string          `json:"widenings,omitempty"`
	Ungranted map[string]string `json:"ungranted,omitempty"`
	// Practice is whether the bot is in practice (shadow) mode.
	Practice bool `json:"practice"`
}

// Ref is <pack instance>/<template>.
func (o BotOrigin) Ref() string { return o.Instance + "/" + o.Template }

// BotTemplateNotes are what applying templates found: each template bot's
// provenance, its read-only grant, its widenings, requests left ungranted,
// and any grant cut to the template's maximum. `conductor validate` prints
// them; a widening is never silent.
func (c *Config) BotTemplateNotes() []string { return c.templateNotes }

// Templates lists every template the configured packs ship, sorted by ref.
func (c *Config) Templates() []LoadedTemplate {
	var out []LoadedTemplate
	for _, inst := range sortedTemplateInstances(c.templates) {
		for _, n := range sortedLoadedKeys(c.templates[inst]) {
			out = append(out, c.templates[inst][n])
		}
	}
	return out
}

// Template is one template by ref.
func (c *Config) Template(ref string) (LoadedTemplate, error) {
	inst, name, ok := strings.Cut(ref, "/")
	if !ok || inst == "" || name == "" || strings.Contains(name, "/") {
		return LoadedTemplate{}, fmt.Errorf("use: %q: a template is named <pack instance>/<template>", ref)
	}
	ts, ok := c.templates[inst]
	if !ok {
		if _, configured := c.Packs[inst]; configured {
			return LoadedTemplate{}, fmt.Errorf("use: %q: pack instance %q ships no bot templates", ref, inst)
		}
		return LoadedTemplate{}, fmt.Errorf("use: %q: no pack instance %q in packs: — add one (packs: { %s: { use: … } }) and run `conductor init`", ref, inst, inst)
	}
	lt, ok := ts[name]
	if !ok {
		return LoadedTemplate{}, fmt.Errorf("use: %q: pack instance %s has no template %q (it ships: %s)", ref, inst, name, strings.Join(sortedLoadedKeys(ts), ", "))
	}
	return lt, nil
}

func sortedTemplateInstances(m map[string]map[string]LoadedTemplate) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedLoadedKeys(m map[string]LoadedTemplate) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// applyBotTemplates instantiates every `use:` bot from its template.
func (c *Config) applyBotTemplates(configDir string) error {
	c.templateNotes = nil
	for _, name := range sortedBotKeys(c.Bots) {
		m := c.Bots[name]
		if m.Use == "" {
			if len(m.Connectors) > 0 || len(m.Grant) > 0 {
				return fmt.Errorf("config: bots.%s: connectors: and grant: bind and grant a template's requests — set use: <pack instance>/<template>", name)
			}
			continue
		}
		lt, err := c.Template(m.Use)
		if err != nil {
			return fmt.Errorf("config: bots.%s: %w", name, err)
		}
		m, notes, err := c.instantiateTemplate(name, m, lt)
		if err != nil {
			return fmt.Errorf("config: bots.%s: %w", name, err)
		}
		c.Bots[name] = m
		c.templateNotes = append(c.templateNotes, notes...)
	}
	return nil
}

// instantiateTemplate is applyBotTemplates for one bot: the effective
// BotConfig and what to tell the operator.
func (c *Config) instantiateTemplate(name string, m BotConfig, lt LoadedTemplate) (BotConfig, []string, error) {
	t := lt.T
	o := &BotOrigin{Instance: lt.Instance, Template: lt.Name, Pack: lt.Pack.Name, Version: lt.Pack.Version,
		Source: lt.Source, Digest: lt.Digest, Charter: lt.Charter, Dir: lt.Dir, SeedDir: lt.SeedDir, Bindings: map[string]string{}}
	var notes []string
	// Bind the inputs: explicit, else the one connector of that type.
	for in := range m.Connectors {
		if _, ok := t.Connectors[in]; !ok {
			return m, nil, fmt.Errorf("connectors.%s: not an input of template %s (its inputs: %s)", in, lt.Ref(), strings.Join(sortedInputKeys(t.Connectors), ", "))
		}
	}
	for _, in := range sortedInputKeys(t.Connectors) {
		req := t.Connectors[in]
		typ := req.Type(in)
		if b := m.Connectors[in]; b != "" {
			ref, ok := c.ConnectorsMap[b]
			if !ok {
				return m, nil, fmt.Errorf("connectors.%s: %q is not one of your connectors", in, b)
			}
			if got := connectorType(ref); got != "" && got != typ {
				return m, nil, fmt.Errorf("connectors.%s: %q is a connector of type %s; template %s needs one of type %s here", in, b, got, lt.Ref(), typ)
			}
			o.Bindings[in] = b
			continue
		}
		var cands []string
		for inst, ref := range c.ConnectorsMap {
			if connectorType(ref) == typ {
				cands = append(cands, inst)
			}
		}
		sort.Strings(cands)
		switch {
		case len(cands) == 1:
			o.Bindings[in] = cands[0]
		case len(cands) > 1:
			return m, nil, fmt.Errorf("connectors.%s: ambiguous — you have %d %s connectors (%s); name one: connectors: { %s: <connector> }", in, len(cands), typ, strings.Join(cands, ", "), in)
		case req.IsRequired():
			return m, nil, fmt.Errorf("connectors.%s: template %s needs a connector of type %s and you have none — add one, or bind it: connectors: { %s: <connector> }", in, lt.Ref(), typ, in)
		default:
			notes = append(notes, fmt.Sprintf("bot %s: template input %s (a connector of type %s) is unbound — the verbs on it are off", name, in, typ))
		}
	}
	// The operator's own verbs and autonomy, before the grant adds to them.
	var own []string
	if m.Skill != nil {
		own = append(own, m.Skill.Verbs...)
	}
	tiers := map[string][]string{}
	for k, v := range m.Autonomy.Tiers {
		tiers[k] = append([]string(nil), v...)
	}
	amounts := map[string]string{}
	for k, v := range m.Autonomy.Amounts {
		amounts[k] = v
	}
	var verbs []string
	o.Ungranted = map[string]string{}
	for _, v := range sortedRequestKeys(t.Verbs) {
		if _, ok := m.Grant[v]; !ok {
			o.Ungranted[v] = t.Verbs[v].Max
		}
	}
	grants := make([]string, 0, len(m.Grant))
	for v := range m.Grant {
		grants = append(grants, v)
	}
	sort.Strings(grants)
	for _, v := range grants {
		tier := m.Grant[v]
		req, ok := t.Verbs[v]
		if !ok {
			return m, nil, fmt.Errorf("grant: %s: not requested by template %s — a verb the template does not request is yours to add in skill.verbs and autonomy.tiers (it requests: %s)", v, lt.Ref(), strings.Join(sortedRequestKeys(t.Verbs), ", "))
		}
		if TierRank(tier) < 0 {
			return m, nil, fmt.Errorf("grant: %s: tier %q is not one of %s", v, tier, strings.Join(BotTiers, ", "))
		}
		if !GrantAllowed(tier, req.Max) {
			notes = append(notes, fmt.Sprintf("bot %s: grant %s: %s is not within what template %s allows (at most %s) — granted at %s", name, v, tier, lt.Ref(), req.Max, req.Max))
			tier = req.Max
		}
		in, rest, _ := strings.Cut(v, ".")
		conn, bound := o.Bindings[in]
		if !bound {
			continue // an optional input left unbound: its verbs stay off
		}
		eff := conn + "." + rest
		verbs = append(verbs, eff)
		tiers[tier] = append(tiers[tier], eff)
		if tier == TierCommitMoney && req.Amount != "" && amounts[eff] == "" {
			amounts[eff] = req.Amount
		}
		switch tier {
		case TierObserve:
			o.ReadOnly = append(o.ReadOnly, eff)
		case TierNever:
		default:
			o.Widenings = append(o.Widenings, fmt.Sprintf("%s at %s", eff, tier))
		}
	}
	for _, v := range own {
		o.Widenings = append(o.Widenings, "own verb "+v)
	}
	if len(verbs) > 0 || len(own) > 0 {
		sk := &SkillPolicy{}
		if m.Skill != nil {
			cp := *m.Skill
			sk = &cp
		}
		sk.Verbs = uniqStrings(append(append([]string(nil), own...), verbs...))
		m.Skill = sk
	}
	if len(tiers) > 0 {
		for k := range tiers {
			tiers[k] = uniqStrings(tiers[k])
		}
		m.Autonomy.Tiers = tiers
	}
	if len(amounts) > 0 {
		m.Autonomy.Amounts = amounts
	}
	o.Practice = m.Autonomy.ShadowMode()
	if !o.Practice {
		o.Widenings = append(o.Widenings, "practice mode off (autonomy.shadow: false)")
	}
	if len(m.Autonomy.Promote) > 0 {
		o.Widenings = append(o.Widenings, "promoted to run unapproved: "+strings.Join(m.Autonomy.Promote, ", "))
	}
	for pat := range m.Autonomy.Unattended {
		o.Widenings = append(o.Widenings, "buys unattended: "+pat)
	}
	// Behavior the operator left unset.
	if m.Description == "" {
		m.Description = t.Description
	}
	if m.Scope == "" {
		m.Scope = t.Scope
	}
	if m.Escalation == "" {
		m.Escalation = t.Escalation
	}
	if m.SeedDir == "" && lt.SeedDir != "" {
		m.SeedDir = lt.SeedDir
	}
	if len(t.Collections) > 0 {
		cols := map[string]Collection{}
		for k, v := range t.Collections {
			cols[k] = v
		}
		for k, v := range m.Collections {
			cols[k] = v // the operator's own definition wins
		}
		m.Collections = cols
	}
	if !m.Home.Model.Set() && t.Model.Set() {
		m.Home.Model = t.Model
	}
	if m.Folder == "" && m.Team == "" {
		f := c.Packs[lt.Instance].Folder
		if f == "" {
			f = "packs/" + lt.Instance
		}
		m.Folder = f
	}
	if p, tp := m.Proactive, t.Proactive; p != nil && tp != nil {
		cp := *p
		if cp.Every == 0 {
			cp.Every = tp.Every
		}
		if cp.QuietHours == "" {
			cp.QuietHours = tp.QuietHours
		}
		if cp.NotifyAt == "" {
			cp.NotifyAt = tp.NotifyAt
		}
		if cp.Checklist.Section == "" && cp.Checklist.File == "" {
			cp.Checklist = tp.Checklist
		}
		m.Proactive = &cp
	}
	m.Origin = o
	mode := "practice mode"
	if !o.Practice {
		mode = "PRACTICE MODE OFF"
	}
	line := fmt.Sprintf("bot %s: from template %s (%s %s): %s; read-only: %s", name, lt.Ref(), lt.Pack.Name, orNoneStr(lt.Pack.Version), mode, orNoneStr(strings.Join(o.ReadOnly, ", ")))
	if len(o.Widenings) > 0 {
		line += "; WIDENING: " + strings.Join(o.Widenings, "; ")
	}
	if len(o.Ungranted) > 0 {
		var ug []string
		for _, v := range sortedStringKeys(o.Ungranted) {
			ug = append(ug, v+" (at most "+o.Ungranted[v]+")")
		}
		line += "; requested, not granted: " + strings.Join(ug, ", ")
	}
	return m, append([]string{line}, notes...), nil
}

func orNoneStr(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func uniqStrings(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func sortedStringKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CharterDocPath is where a template bot's charter lives in its memory repo:
// a conductor-owned document (no bot writes it), so a newer template's
// charter reaches it only as a reviewed proposal.
const CharterDocPath = "charter/charter.md"

// templateCharter is a template bot's charter: the memory repo's charter
// document once it exists, else the template's own text.
func (c *Config) templateCharter(name string, m BotConfig) string {
	if b, err := os.ReadFile(filepath.Join(c.BotRepoDir(name), CharterDocPath)); err == nil {
		return stripFrontmatter(string(b))
	}
	return m.Origin.Charter
}

// stripFrontmatter drops a leading --- YAML block.
func stripFrontmatter(s string) string {
	t := strings.TrimPrefix(s, "\ufeff")
	if !strings.HasPrefix(t, "---\n") {
		return s
	}
	end := strings.Index(t[4:], "\n---")
	if end < 0 {
		return s
	}
	rest := t[4+end+4:]
	if i := strings.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[i+1:]
	} else {
		rest = ""
	}
	return strings.TrimSpace(rest)
}
