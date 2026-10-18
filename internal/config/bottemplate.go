package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// BOT TEMPLATES (docs/wiki/Bot-Templates.md). A pack can ship bots the way it
// ships workflows: as TEMPLATES an operator instantiates into real bots. A
// template is behavior — a charter, typed seed documents, collections, the
// connectors it needs as inputs, the verbs it REQUESTS (each with a maximum
// tier), and suggested triggers, proactive checks, runtime and model. It is
// distributed by the pack it sits in (version, hold, trust, lockfile), never
// by a second channel.
//
// A template only requests. Everything that decides what a bot may do, who
// approves it, where it sits and what it reaches is the operator's:
// operators, folder, team, connector bindings, secrets, autonomy, sandbox
// and egress. A template that names any of them is refused by name
// (forbiddenTemplateKeys), and the verbs it requests are active only where
// the operator's own `bots.<b>.grant:` says so, at a tier no looser than
// the template's maximum (applyBotTemplates).

// BotTemplate is one `bots.<name>` entry of a pack manifest.
type BotTemplate struct {
	Description string `yaml:"description"`
	// Charter is the bot's standing instructions; CharterFile reads it from
	// a file in the pack. At most one.
	Charter     string `yaml:"charter,omitempty"`
	CharterFile string `yaml:"charter_file,omitempty"`
	Scope       string `yaml:"scope,omitempty"`
	Escalation  string `yaml:"escalation,omitempty"`
	// Seed is a directory in the pack whose typed documents (knowledge/,
	// runbooks/, watch/, a collection's home) seed a new bot's memory —
	// checked against the kinds and their caps like any write.
	Seed string `yaml:"seed,omitempty"`
	// Collections are custom document kinds the template's bots use, inline
	// or `from:` a file in the pack.
	Collections map[string]Collection `yaml:"collections,omitempty"`
	// Connectors are the template's INPUTS: the connectors it needs, which
	// the operator binds (bots.<b>.connectors) or conductor binds when the
	// operator has exactly one of that type.
	Connectors map[string]TemplateInput `yaml:"connectors,omitempty"`
	// Verbs are what the template REQUESTS, in its own vocabulary
	// (<input>.<verb>), each with the loosest tier the operator may grant.
	Verbs map[string]VerbRequest `yaml:"verbs,omitempty"`
	// Triggers are suggested event routes into the bot. `conductor bot new`
	// writes them into the operator's config, disarmed.
	Triggers []TemplateTrigger `yaml:"triggers,omitempty"`
	// Proactive is the suggested proactive sweep; it applies only to a bot
	// whose operator opted in (`proactive: {}`), and never sets a budget.
	Proactive *TemplateProactive `yaml:"proactive,omitempty"`
	// Model is the suggested home model (a fleet, id or list); Runtime the
	// suggested worker runtime, as text — runtimes are environment, so it is
	// shown, never applied.
	Model   ModelSpec `yaml:"model,omitempty"`
	Runtime string    `yaml:"runtime,omitempty"`
}

// forbiddenTemplateKeys are what a template can never set, with why. Each is
// the operator's decision; naming one refuses the pack at load.
var forbiddenTemplateKeys = map[string]string{
	"operators":      "who approves a bot is the operator's choice",
	"approvers":      "who approves a bot is the operator's choice",
	"folder":         "where a bot is filed is the operator's choice (by default it follows the pack instance's placement)",
	"team":           "which team a bot joins is the operator's choice",
	"secrets":        "secrets are the operator's environment",
	"skill":          "a template requests verbs (verbs:) and never grants them — the operator grants them (bots.<b>.grant)",
	"grant":          "the operator grants a template's requested verbs, not the template",
	"autonomy":       "autonomy (tiers, shadow, promotion, spend caps) is the operator's — a template requests verbs with a maximum tier in verbs:",
	"isolation":      "a template cannot change a bot's sandbox",
	"browser":        "a template cannot grant browser logins or credentials",
	"network":        "a template cannot widen egress",
	"egress":         "a template cannot widen egress",
	"memory_repo":    "where a bot's memory lives is the operator's choice — ship seed documents with seed:",
	"seed_dir":       "a template seeds memory from its own pack (seed:), not from a path on the operator's machine",
	"memory":         "memory limits are the operator's knob",
	"peers":          "linking bots is a channel for data to cross — the operator's choice",
	"terraform":      "conductor-executed Terraform binds connectors and repos — environment",
	"home":           "the home runtime is environment — suggest a model with model:",
	"worker":         "the worker runtime is environment — suggest one with runtime:",
	"disabled":       "whether a bot runs is the operator's choice",
	"use":            "a template cannot instantiate another template",
	"charter_digest": "provenance is recorded by conductor, not declared",
}

// UnmarshalYAML refuses a forbidden key by name, then decodes strictly.
func (t *BotTemplate) UnmarshalYAML(n *yaml.Node) error {
	if err := refuseTemplateKeys(n, forbiddenTemplateKeys); err != nil {
		return err
	}
	type plain BotTemplate
	return strictNodeDecode(n, (*plain)(t))
}

func refuseTemplateKeys(n *yaml.Node, forbidden map[string]string) error {
	if n.Kind != yaml.MappingNode {
		return nil
	}
	var bad []string
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i].Value
		if why, ok := forbidden[k]; ok {
			bad = append(bad, fmt.Sprintf("%s (%s)", k, why))
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("a bot template cannot set %s", strings.Join(bad, "; "))
	}
	return nil
}

// TemplateInput is one connector a template needs. The scalar form is a
// version constraint: `pagerduty: ">=1.0"`.
type TemplateInput struct {
	// Use is the connector type ("" = the input's own name).
	Use     string `yaml:"use,omitempty"`
	Version string `yaml:"version,omitempty"`
	// Required (default true): unbound, the bot is a load error. An
	// optional input left unbound drops the verbs on it.
	Required *bool  `yaml:"required,omitempty"`
	Desc     string `yaml:"desc,omitempty"`
}

// UnmarshalYAML accepts a version constraint or the map form.
func (t *TemplateInput) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		t.Version = n.Value
		return nil
	}
	type plain TemplateInput
	return strictNodeDecode(n, (*plain)(t))
}

// Type is the connector type the input binds to.
func (t TemplateInput) Type(name string) string {
	if t.Use != "" {
		return t.Use
	}
	return name
}

// IsRequired reports a required input (the default).
func (t TemplateInput) IsRequired() bool { return t.Required == nil || *t.Required }

// VerbRequest is one requested verb. The scalar form is its maximum tier.
type VerbRequest struct {
	// Max is the LOOSEST tier the operator may grant (observe … commit_money).
	Max string `yaml:"max"`
	// Amount names the verb's typed amount option (commit_money).
	Amount string `yaml:"amount,omitempty"`
	Why    string `yaml:"why,omitempty"`
}

// UnmarshalYAML accepts a tier or the map form.
func (v *VerbRequest) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		v.Max = n.Value
		return nil
	}
	type plain VerbRequest
	return strictNodeDecode(n, (*plain)(v))
}

// TemplateTrigger is a suggested route of an event into the bot: one `bot:`
// step, on one of the template's inputs.
type TemplateTrigger struct {
	Name      string  `yaml:"name"`
	On        string  `yaml:"on"`
	Filter    *Filter `yaml:"filter,omitempty"`
	Title     string  `yaml:"title,omitempty"`
	Text      string  `yaml:"text,omitempty"`
	Requester string  `yaml:"requester,omitempty"`
	Urgent    bool    `yaml:"urgent,omitempty"`
	// Action is "" (deliver), consolidate or rotate.
	Action string `yaml:"action,omitempty"`
	Desc   string `yaml:"desc,omitempty"`
}

// TemplateProactive is a suggested proactive sweep. No budget: that spends
// the operator's money.
type TemplateProactive struct {
	Every      Duration           `yaml:"every,omitempty"`
	QuietHours string             `yaml:"quiet_hours,omitempty"`
	Checklist  ProactiveChecklist `yaml:"checklist,omitempty"`
	NotifyAt   string             `yaml:"notify_at,omitempty"`
}

// UnmarshalYAML refuses a budget by name.
func (p *TemplateProactive) UnmarshalYAML(n *yaml.Node) error {
	if err := refuseTemplateKeys(n, map[string]string{"budget": "a proactive budget spends the operator's money — the operator sets it"}); err != nil {
		return err
	}
	type plain TemplateProactive
	return strictNodeDecode(n, (*plain)(p))
}

var (
	templateInputName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
	templateVerbRe    = regexp.MustCompile(`^([a-z][a-z0-9_-]{0,39})\.([a-z0-9_*.-]{1,80})$`)
)

// RequestTiers are the tiers a template may name as a maximum, loosest
// first (never is not a request).
var RequestTiers = []string{TierObserve, TierReversible, TierPropose, TierCommitMoney}

// GrantAllowed reports whether granting `tier` stays within a request's
// maximum: the same tier or a stricter one. A money verb is granted as
// commit_money (under the spend cap) or never — propose would drop the cap.
func GrantAllowed(tier, max string) bool {
	if TierRank(tier) < 0 || TierRank(max) < 0 {
		return false
	}
	if max == TierCommitMoney {
		return tier == TierCommitMoney || tier == TierNever
	}
	return TierRank(tier) >= TierRank(max) && (tier != TierCommitMoney)
}

// LoadedTemplate is a validated template with its file references resolved.
type LoadedTemplate struct {
	Name     string
	T        BotTemplate
	Charter  string // the charter text (inline or from its file)
	SeedDir  string // absolute, "" without a seed
	Digest   string
	Instance string
	Pack     PackMeta
	Dir      string // the vendored pack dir
	Source   string
}

// Ref is how a bot names it: <pack instance>/<template>.
func (l LoadedTemplate) Ref() string { return l.Instance + "/" + l.Name }

// loadTemplates validates every template a pack ships and resolves its
// files inside the pack. A template that breaks a rule fails the pack.
func loadTemplates(man *PackManifest, dir string) (map[string]LoadedTemplate, error) {
	out := map[string]LoadedTemplate{}
	for _, name := range sortedTemplateKeys(man.Bots) {
		lt, err := loadTemplate(name, man.Bots[name], dir)
		if err != nil {
			return nil, fmt.Errorf("bots.%s: %w", name, err)
		}
		lt.Pack = man.Pack
		out[name] = lt
	}
	return out, nil
}

func sortedTemplateKeys(m map[string]BotTemplate) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// packFile resolves a path inside the pack, refusing one that leaves it.
func packFile(dir, rel, what string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(rel, "~") {
		return "", fmt.Errorf("%s: %q must be a path inside the pack", what, rel)
	}
	p := filepath.Join(dir, rel)
	if !withinDir(dir, p) {
		return "", fmt.Errorf("%s: %q leaves the pack", what, rel)
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		rdir, _ := filepath.EvalSymlinks(dir)
		if rdir != "" && !withinDir(rdir, real) {
			return "", fmt.Errorf("%s: %q leaves the pack (a symlink)", what, rel)
		}
	}
	return p, nil
}

func loadTemplate(name string, t BotTemplate, dir string) (LoadedTemplate, error) {
	lt := LoadedTemplate{Name: name, T: t, Dir: dir}
	if !validBotName(name) {
		return lt, fmt.Errorf("template name must be lowercase letters, digits, - or _")
	}
	if strings.TrimSpace(t.Description) == "" {
		return lt, fmt.Errorf("description: say what the bot is for, in a line")
	}
	if t.Charter != "" && t.CharterFile != "" {
		return lt, fmt.Errorf("set charter or charter_file, not both")
	}
	lt.Charter = strings.TrimSpace(t.Charter)
	if t.CharterFile != "" {
		p, err := packFile(dir, t.CharterFile, "charter_file")
		if err != nil {
			return lt, err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return lt, fmt.Errorf("charter_file: %w", err)
		}
		lt.Charter = strings.TrimSpace(string(b))
	}
	if t.Seed != "" {
		p, err := packFile(dir, t.Seed, "seed")
		if err != nil {
			return lt, err
		}
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			return lt, fmt.Errorf("seed: %q is not a directory in the pack", t.Seed)
		}
		lt.SeedDir = p
	}
	// Collections: a `from:` is a file in the pack, read now.
	cols := map[string]Collection{}
	for cname, col := range t.Collections {
		if col.From != "" {
			if col.Dir != "" || len(col.Fields) > 0 || col.Template != "" || col.FreePaths || len(col.Writers) > 0 || col.MaxBytes != 0 {
				return lt, fmt.Errorf("collections.%s: from: is the whole definition — drop the inline keys", cname)
			}
			p, err := packFile(dir, col.From, "collections."+cname+".from")
			if err != nil {
				return lt, err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return lt, fmt.Errorf("collections.%s: %w", cname, err)
			}
			var def Collection
			if err := yaml.Unmarshal(b, &def); err != nil {
				return lt, fmt.Errorf("collections.%s: %w", cname, err)
			}
			if def.From != "" {
				return lt, fmt.Errorf("collections.%s: a definition file cannot itself use from:", cname)
			}
			col = def
		}
		cols[cname] = col
	}
	if err := validateCollections(cols, false); err != nil {
		return lt, err
	}
	lt.T.Collections = cols
	for in, req := range t.Connectors {
		if !templateInputName.MatchString(in) {
			return lt, fmt.Errorf("connectors.%s: input names are lowercase letters, digits, - or _", in)
		}
		if v := strings.TrimSpace(req.Version); v != "" && v != AnyVersion && !validConstraint(v) {
			return lt, fmt.Errorf("connectors.%s: %q is not a version constraint — a template declares the connector it needs; the operator binds it (bots.<b>.connectors)", in, req.Version)
		}
	}
	for _, v := range sortedRequestKeys(t.Verbs) {
		req := t.Verbs[v]
		m := templateVerbRe.FindStringSubmatch(v)
		if m == nil {
			return lt, fmt.Errorf("verbs.%s: a requested verb is <input>.<verb> (a pattern may end in *)", v)
		}
		if _, ok := t.Connectors[m[1]]; !ok {
			return lt, fmt.Errorf("verbs.%s: %q is not one of the template's connector inputs (%s)", v, m[1], strings.Join(sortedInputKeys(t.Connectors), ", "))
		}
		if strings.HasPrefix(m[2], "*") {
			return lt, fmt.Errorf("verbs.%s: request verbs by name or prefix (%s.get_*), never every verb of a connector", v, m[1])
		}
		ok := false
		for _, tier := range RequestTiers {
			ok = ok || req.Max == tier
		}
		if !ok {
			return lt, fmt.Errorf("verbs.%s: maximum tier %q is not one of %s", v, req.Max, strings.Join(RequestTiers, ", "))
		}
		if (req.Max == TierCommitMoney) != (req.Amount != "") {
			return lt, fmt.Errorf("verbs.%s: a commit_money verb names its amount option (amount:), and only a commit_money verb does", v)
		}
	}
	seen := map[string]bool{}
	for i, tr := range t.Triggers {
		where := fmt.Sprintf("triggers[%d]", i)
		if !validBotName(tr.Name) || seen[tr.Name] {
			return lt, fmt.Errorf("%s: a unique name of lowercase letters, digits, - or _", where)
		}
		seen[tr.Name] = true
		in, _, ok := strings.Cut(tr.On, ".")
		if _, declared := t.Connectors[in]; !ok || !declared {
			return lt, fmt.Errorf("%s (%s): on: is <input>.<event> on one of the template's connector inputs", where, tr.Name)
		}
		switch tr.Action {
		case "", "deliver", "consolidate", "rotate":
		default:
			return lt, fmt.Errorf("%s (%s): action %q (want deliver, consolidate or rotate)", where, tr.Name, tr.Action)
		}
	}
	if p := t.Proactive; p != nil {
		if p.Every != 0 && p.Every.D() < 5*60e9 {
			return lt, fmt.Errorf("proactive.every: at least 5m")
		}
		if p.Checklist.Section != "" && p.Checklist.File != "" {
			return lt, fmt.Errorf("proactive.checklist: a section or a file, not both")
		}
		if p.Checklist.File != "" {
			f, err := packFile(dir, p.Checklist.File, "proactive.checklist.file")
			if err != nil {
				return lt, err
			}
			lt.T.Proactive = &TemplateProactive{Every: p.Every, QuietHours: p.QuietHours, NotifyAt: p.NotifyAt, Checklist: ProactiveChecklist{File: f}}
		}
		if p.NotifyAt != "" && SeverityRank(p.NotifyAt) < 0 {
			return lt, fmt.Errorf("proactive.notify_at: %q is not one of %s", p.NotifyAt, strings.Join(FindingSeverities, ", "))
		}
	}
	if len(t.Runtime) > 200 {
		return lt, fmt.Errorf("runtime: a short suggestion (at most 200 characters)")
	}
	h := sha256.New()
	b, _ := yaml.Marshal(t)
	h.Write(b)
	h.Write([]byte(lt.Charter))
	lt.Digest = hex.EncodeToString(h.Sum(nil))[:12]
	return lt, nil
}

func sortedRequestKeys(m map[string]VerbRequest) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedInputKeys(m map[string]TemplateInput) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// validConstraint reports a version constraint the semver matcher parses.
func validConstraint(c string) bool {
	for _, part := range constraintParts(c) {
		_, rest := splitOp(part)
		if _, err := parseSemver(rest); err != nil {
			return false
		}
	}
	return len(constraintParts(c)) > 0
}
