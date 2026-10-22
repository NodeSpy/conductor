package config

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// TEAMS (docs/wiki/Bots.md#teams, docs/design/bots.md): a team is one
// more thing the operator defines and cuts however they like — by system,
// function, environment. Conductor has no notion of an org; a team is a set
// of bots that share defaults, peer with each other, may share a knowledge
// repo, and can be routed work as a unit.
//
// The rule is the packs' rule: a team's defaults are CEILINGS. A bot
// inherits each one it does not set and may only narrow it, never widen it —
// a bot setting past its team's is a load error naming both values. The
// inheritance is resolved once, at load (resolveTeams), into the bot's own
// config, so everything downstream (the gate, the service, the home launch)
// reads one effective BotConfig.
//
// Optional throughout: no `teams:` and no bot `team:` behaves exactly as
// before.

// TeamConfig is one team.
type TeamConfig struct {
	// Description is the team's one-line identity.
	Description string `yaml:"description,omitempty"`
	// Folder is removed (removed.go): accepted, ignored, warned about.
	Folder RemovedField `yaml:"folder,omitempty"`
	// Collections are custom content kinds in the team's knowledge repo
	// (collections.go): written by an operator, or by approved promotion.
	Collections map[string]Collection `yaml:"collections,omitempty"`
	// Operators may approve the team's bots' proposals and its knowledge
	// promotions. A bot's operators: must be a subset; a bot that sets
	// none inherits these. Empty = every configured operator.
	Operators []string `yaml:"operators,omitempty"`
	// Peers is how the team's bots message each other: "team" (the
	// default — every bot may `ask` every other, with the ordinary peer
	// rules: data only, fenced, audited, rate-limited) or "none" (only
	// explicit operator links, as between teams).
	Peers string `yaml:"peers,omitempty"`
	// Delegation bounds `conductor bot delegate` chains through the team's
	// bots (who may delegate to whom is the same rule as ask: Peers, or an
	// operator's link).
	Delegation *TeamDelegation `yaml:"delegation,omitempty"`
	// Autonomy bounds the bots' gates.
	Autonomy TeamAutonomy `yaml:"autonomy,omitempty"`
	// Budget bounds spend and proactive sweeps.
	Budget TeamBudget `yaml:"budget,omitempty"`
	// Isolation is the sandbox / session policy every bot inherits.
	Isolation BotIsolation `yaml:"isolation,omitempty"`
	// Credentials bounds the secrets the broker may issue to bots.
	Credentials *TeamCredentials `yaml:"credentials,omitempty"`
	// Browser bounds the saved browser logins and sign-in credentials
	// bots may use.
	Browser *BotBrowser `yaml:"browser,omitempty"`
	// Knowledge is the team's shared knowledge repo (nil: none).
	Knowledge *TeamKnowledge `yaml:"knowledge,omitempty"`
	// Routing picks who handles work routed to the team (`bot: { team:
	// … }`). Nil: the team cannot be routed to.
	Routing *TeamRouting `yaml:"routing,omitempty"`
	// Chat bounds the team conversation (`conductor team say`).
	Chat TeamChat `yaml:"chat,omitempty"`
	// Avatar is the team's face: { icon, color } or { image: <file> }
	// beside the config (botidentity.go) — never a preset, which are bots'
	// mascots. None: clients show the team's initials.
	Avatar *BotAvatar `yaml:"avatar,omitempty"`
}

// TeamChat bounds a team conversation. On by default.
type TeamChat struct {
	// EveryonePerHour caps `--everyone` messages per hour (each reaches
	// every member: one session turn and one reply each).
	EveryonePerHour int `yaml:"everyone_per_hour,omitempty"`
	// MaxReply caps one bot's reply (bytes).
	MaxReply ByteSize `yaml:"max_reply,omitempty"`
}

// Team chat defaults and ceilings.
const (
	DefaultEveryonePerHour = 6
	MaxEveryonePerHour     = 60
	DefaultChatReply       = 4000
	MaxChatReply           = 16 << 10
)

// EveryonePerHourOr is the effective fan-out cap.
func (t TeamChat) EveryonePerHourOr() int {
	if t.EveryonePerHour > 0 {
		return t.EveryonePerHour
	}
	return DefaultEveryonePerHour
}

// MaxReplyOr is the effective reply cap.
func (t TeamChat) MaxReplyOr() int {
	if t.MaxReply > 0 {
		return int(t.MaxReply)
	}
	return DefaultChatReply
}

// TeamDelegation bounds delegation chains.
type TeamDelegation struct {
	// MaxDepth is the most delegation hops a chain may take through this
	// team's bots (default DefaultDelegationMaxDepth).
	MaxDepth int `yaml:"max_depth,omitempty"`
}

// DefaultDelegationMaxDepth is how many hops a delegation chain may take
// when no team sets delegation.max_depth: a → b → c → d, and no further.
const DefaultDelegationMaxDepth = 3

// MaxDelegationDepth is the most hops a delegation may end at bot: its
// team's delegation.max_depth, or the default for a team that sets none
// and for a bot with no team.
func (c *Config) MaxDelegationDepth(bot string) int {
	if t, ok := c.Teams[string(c.Bots[bot].Team)]; ok && t.Delegation != nil && t.Delegation.MaxDepth > 0 {
		return t.Delegation.MaxDepth
	}
	return DefaultDelegationMaxDepth
}

// TeamAutonomy is a team's ceiling on its bots' gates.
type TeamAutonomy struct {
	// Ceiling is the highest tier any bot may be PROMOTED to, i.e. run
	// without an operator's signature: "reversible" (the default — the most
	// promotion can reach) or "observe" (no bot promotes anything).
	Ceiling string `yaml:"ceiling,omitempty"`
	// Shadow, when true, holds every bot in shadow mode; a bot cannot
	// set shadow: false. Bots that don't set shadow inherit it.
	Shadow *bool `yaml:"shadow,omitempty"`
	// Tiers are classified into every bot's tiers: a verb matching several
	// tiers takes the strictest, so a team tier can only make a bot
	// stricter. A bot pattern the team classifies stricter is an error.
	Tiers map[string][]string `yaml:"tiers,omitempty"`
}

// TeamBudget is a team's spend and proactive limits.
type TeamBudget struct {
	// SpendCap is the ceiling on each bot's commit_money cap (a bot
	// without one inherits it).
	SpendCap *SpendCap `yaml:"spend_cap,omitempty"`
	// Proactive is a POOL: the team's bots' sweeps together, over a
	// rolling 24h. A bot's own proactive.budget must fit inside it (one
	// that sets none inherits it), and no sweep starts once the team's
	// sweeps have used it up.
	Proactive *ProactiveBudget `yaml:"proactive,omitempty"`
}

// BotIsolation is a bot's (or a team's) session policy. Unset fields
// are inherited from the team; each can only be made stricter.
type BotIsolation struct {
	// Sandbox: "optional" (the default) or "required" — the bot's
	// sessions (home, workers, sweeps) start only while conductor's agent
	// sandbox is enforcing agent launches.
	Sandbox string `yaml:"sandbox,omitempty"`
	// HandoffWorkers (default true) allows `spawn --handoff` (a paseo
	// session a human may take over). false: cli workers only.
	HandoffWorkers *bool `yaml:"handoff_workers,omitempty"`
	// SkipPermissionPrompts (default true) allows a home.mode that skips
	// tool-permission prompts. false: such a mode is a load error.
	SkipPermissionPrompts *bool `yaml:"skip_permission_prompts,omitempty"`
}

// SandboxRequired reports the effective sandbox requirement.
func (i BotIsolation) SandboxRequired() bool { return i.Sandbox == "required" }

// HandoffAllowed reports whether hand-off workers are allowed.
func (i BotIsolation) HandoffAllowed() bool { return i.HandoffWorkers == nil || *i.HandoffWorkers }

// PromptSkipAllowed reports whether a prompt-skipping home mode is allowed.
func (i BotIsolation) PromptSkipAllowed() bool {
	return i.SkipPermissionPrompts == nil || *i.SkipPermissionPrompts
}

// TeamCredentials bounds what the secret broker may issue to a team's
// bots: their skill.secrets_via and skill.allow_secrets.
type TeamCredentials struct {
	SecretsVia   string   `yaml:"secrets_via,omitempty"` // none | broker | env
	AllowSecrets []string `yaml:"allow_secrets,omitempty"`
}

// BotBrowser names the saved browser logins (sites) and sign-in
// credentials (names) a bot's sessions may use. It narrows what the
// browser's own logins:/credentials: identities allow; it never grants.
type BotBrowser struct {
	Logins      []string `yaml:"logins,omitempty"`
	Credentials []string `yaml:"credentials,omitempty"`
}

// TeamKnowledge is a team's shared knowledge repo.
type TeamKnowledge struct {
	// Repo is the git repo (default <state dir>/teams/<name>/knowledge).
	Repo string `yaml:"repo,omitempty"`
	// SeedDir seeds a NEW repo's first commit (knowledge/ and runbooks/).
	SeedDir string `yaml:"seed_dir,omitempty"`
	// Promotion is what a bot's `conductor bot team promote` does:
	// "proposal" (the default — an operator of the team approves it) or
	// "direct" (it lands as a commit at once, when the bot's request
	// provenance would let a promoted verb run; otherwise it is a proposal).
	Promotion string `yaml:"promotion,omitempty"`
	// MemoryLimits bound what the team's bots read from the repo (its index
	// in their prompts, a read, a recall) and what a promotion or an
	// operator may write to it (memlimits.go).
	MemoryLimits `yaml:",inline"`
}

// PromotionMode is the effective promotion setting.
func (k TeamKnowledge) PromotionMode() string {
	if k.Promotion == "" {
		return "proposal"
	}
	return k.Promotion
}

// TeamRouting decides who handles work routed to the team: the first rule
// whose match: holds, else the lead's triage, else the fallback.
type TeamRouting struct {
	// Rules are deterministic: event fields matched against globs, no model.
	Rules []RoutingRule `yaml:"rules,omitempty"`
	// Lead triages anything no rule matched: a read-only worker of the lead
	// reads the event and picks a teammate (`conductor bot assign`).
	Lead string `yaml:"lead,omitempty"`
	// Fallback handles what no rule matched with no lead — and what the lead
	// did not assign within Timeout, or could not triage. Required.
	Fallback string `yaml:"fallback"`
	// Timeout bounds the lead's triage (default 10m).
	Timeout Duration `yaml:"timeout,omitempty"`
}

// DefaultTriageTimeout bounds a lead's triage.
const DefaultTriageTimeout = 10 * time.Minute

// TimeoutD is the effective triage bound.
func (r TeamRouting) TimeoutD() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout.D()
	}
	return DefaultTriageTimeout
}

// RoutingRule is one deterministic routing rule. Every key of Match must
// match (a path.Match glob, case-sensitive): source, title, text, requester,
// or any field of the event (dotted for nested ones: labels.team).
type RoutingRule struct {
	Name  string            `yaml:"name,omitempty"`
	Match map[string]string `yaml:"match"`
	To    string            `yaml:"to"`
}

// Label names a rule in the ledger: its name, else its position.
func (r RoutingRule) Label(i int) string {
	if r.Name != "" {
		return r.Name
	}
	return fmt.Sprintf("rules[%d]", i)
}

// TeamName is a bot's team: one name. A list is refused at decode — a
// bot belongs to at most one team.
type TeamName string

// UnmarshalYAML implements yaml.Unmarshaler.
func (t *TeamName) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: team: a bot belongs to at most one team — name one team, not a list", n.Line)
	}
	*t = TeamName(n.Value)
	return nil
}

// ---- accessors ----

// TeamOf is a bot's team, when it has one.
func (c *Config) TeamOf(bot string) (string, TeamConfig, bool) {
	m, ok := c.Bots[bot]
	if !ok || m.Team == "" {
		return "", TeamConfig{}, false
	}
	t, ok := c.Teams[string(m.Team)]
	return string(m.Team), t, ok
}

// TeamBots lists a team's enabled bots, sorted.
func (c *Config) TeamBots(team string) []string {
	var out []string
	for _, n := range c.BotNames() {
		if string(c.Bots[n].Team) == team {
			out = append(out, n)
		}
	}
	return out
}

// TeamNames lists the configured teams, sorted.
func (c *Config) TeamNames() []string {
	out := make([]string, 0, len(c.Teams))
	for n := range c.Teams {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// TeamOperators is who may approve for a team: its operators, or every
// configured operator.
func (c *Config) TeamOperators(team string) []string {
	t := c.Teams[team]
	if len(t.Operators) > 0 {
		return append([]string(nil), t.Operators...)
	}
	out := make([]string, 0, len(c.Operators))
	for n := range c.Operators {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// TeamKnowledgeDir is a team's knowledge repo ("" without knowledge:).
func (c *Config) TeamKnowledgeDir(team string) string {
	k := c.Teams[team].Knowledge
	if k == nil {
		return ""
	}
	if k.Repo != "" {
		return expandHome(k.Repo)
	}
	return filepath.Join(StateDir(), "teams", team, "knowledge")
}

// TeamSeedDir is a team's knowledge seed directory, resolved ("" unset).
func (c *Config) TeamSeedDir(team string) string {
	k := c.Teams[team].Knowledge
	if k == nil || k.SeedDir == "" {
		return ""
	}
	p := expandHome(k.SeedDir)
	if !filepath.IsAbs(p) && c.baseDir != "" {
		p = filepath.Join(c.baseDir, p)
	}
	return p
}

// sameTeamPeers reports whether two bots are teammates whose team peers.
func (c *Config) sameTeamPeers(a, b string) bool {
	ta, tb := c.Bots[a].Team, c.Bots[b].Team
	if ta != tb {
		return false
	}
	t, ok := c.Teams[string(ta)] // a teamless pair: there is no team ""
	return ok && t.Peers != "none"
}

// ---- narrow, never widen ----

var secretsViaRank = map[string]int{"": 0, "none": 0, "broker": 1, "env": 2}

// ResolveTeams applies every bot's team: refuses a bot setting past
// its team's ceiling (naming both values), then fills in each default the
// bot left unset. Idempotent — an inherited value sits exactly at the
// ceiling. Load runs it; a Config built in code calls it itself.
func (c *Config) ResolveTeams() error {
	for _, name := range sortedBotKeys(c.Bots) {
		m := c.Bots[name]
		if m.Team == "" {
			continue
		}
		t, ok := c.Teams[string(m.Team)]
		if !ok {
			return fmt.Errorf("config: bots.%s: team: %q is not a configured team", name, m.Team)
		}
		if err := checkTeamCeilings(string(m.Team), t, m); err != nil {
			return fmt.Errorf("config: bots.%s: %w", name, err)
		}
		c.Bots[name] = inheritTeam(t, m)
	}
	return nil
}

func sortedBotKeys(ms map[string]BotConfig) []string {
	out := make([]string, 0, len(ms))
	for n := range ms {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// checkTeamCeilings is the pure half of ResolveTeams: every bot setting
// that would widen its team's is an error naming both values.
func checkTeamCeilings(team string, t TeamConfig, m BotConfig) error {
	if len(t.Operators) > 0 {
		for _, op := range m.Operators {
			if !hasString(t.Operators, op) {
				return fmt.Errorf("operators: %q is not one of team %s's operators [%s]", op, team, strings.Join(t.Operators, ", "))
			}
		}
	}
	a := m.Autonomy
	if t.Autonomy.Ceiling == TierObserve && len(a.Promote) > 0 {
		return fmt.Errorf("autonomy.promote [%s] promotes to %s, above team %s's autonomy.ceiling %s", strings.Join(a.Promote, ", "), TierReversible, team, TierObserve)
	}
	if t.Autonomy.Shadow != nil && *t.Autonomy.Shadow && a.Shadow != nil && !*a.Shadow {
		return fmt.Errorf("autonomy.shadow false widens team %s's autonomy.shadow true", team)
	}
	for tier, pats := range a.Tiers {
		for _, p := range pats {
			for ttier, tpats := range t.Autonomy.Tiers {
				for _, tp := range tpats {
					if ok, _ := path.Match(tp, p); ok && TierRank(ttier) > TierRank(tier) {
						return fmt.Errorf("autonomy.tiers.%s %q is less strict than team %s's autonomy.tiers.%s %q", tier, p, team, ttier, tp)
					}
				}
			}
		}
	}
	if tc, mc := t.Budget.SpendCap, a.SpendCap; tc != nil && mc != nil {
		switch {
		case mc.CurrencyOr() != tc.CurrencyOr():
			return fmt.Errorf("autonomy.spend_cap.currency %s differs from team %s's budget.spend_cap.currency %s", mc.CurrencyOr(), team, tc.CurrencyOr())
		case mc.PerAction > tc.PerAction:
			return fmt.Errorf("autonomy.spend_cap.per_action %g exceeds team %s's budget.spend_cap.per_action %g", mc.PerAction, team, tc.PerAction)
		case mc.PerPeriod > tc.PerPeriod:
			return fmt.Errorf("autonomy.spend_cap.per_period %g exceeds team %s's budget.spend_cap.per_period %g", mc.PerPeriod, team, tc.PerPeriod)
		case mc.PeriodD() < tc.PeriodD():
			// The same amount over a shorter window is a faster spend rate.
			return fmt.Errorf("autonomy.spend_cap.period %s is shorter than team %s's budget.spend_cap.period %s (the same cap over less time widens it)", mc.PeriodD(), team, tc.PeriodD())
		}
	}
	if pool, p := t.Budget.Proactive, m.Proactive; pool != nil && p != nil {
		if err := p.Budget.withinCeiling(*pool, team); err != nil {
			return err
		}
	}
	ti, mi := t.Isolation, m.Isolation
	if ti.SandboxRequired() && mi.Sandbox != "" && !mi.SandboxRequired() {
		return fmt.Errorf("isolation.sandbox %s widens team %s's isolation.sandbox required", mi.Sandbox, team)
	}
	if !ti.HandoffAllowed() && mi.HandoffWorkers != nil && *mi.HandoffWorkers {
		return fmt.Errorf("isolation.handoff_workers true widens team %s's isolation.handoff_workers false", team)
	}
	if !ti.PromptSkipAllowed() && mi.SkipPermissionPrompts != nil && *mi.SkipPermissionPrompts {
		return fmt.Errorf("isolation.skip_permission_prompts true widens team %s's isolation.skip_permission_prompts false", team)
	}
	if tc := t.Credentials; tc != nil && m.Skill != nil {
		if m.Skill.SecretsVia != "" && secretsViaRank[m.Skill.SecretsVia] > secretsViaRank[tc.SecretsVia] {
			return fmt.Errorf("skill.secrets_via %s widens team %s's credentials.secrets_via %s", m.Skill.SecretsVia, team, noneIfEmpty(tc.SecretsVia))
		}
		for _, s := range m.Skill.AllowSecrets {
			if !hasString(tc.AllowSecrets, s) {
				return fmt.Errorf("skill.allow_secrets %q is not in team %s's credentials.allow_secrets [%s]", s, team, strings.Join(tc.AllowSecrets, ", "))
			}
		}
	}
	if tb, mb := t.Browser, m.Browser; tb != nil && mb != nil {
		for _, site := range mb.Logins {
			if !siteWithin(tb.Logins, site) {
				return fmt.Errorf("browser.logins %q is not within team %s's browser.logins [%s]", site, team, strings.Join(tb.Logins, ", "))
			}
		}
		for _, cr := range mb.Credentials {
			if !hasString(tb.Credentials, cr) {
				return fmt.Errorf("browser.credentials %q is not in team %s's browser.credentials [%s]", cr, team, strings.Join(tb.Credentials, ", "))
			}
		}
	}
	return nil
}

func noneIfEmpty(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// siteWithin reports whether a login site is covered by a list of sites: an
// exact entry, or a "*.domain" entry covering it (a host under the domain, or
// a narrower "*.sub.domain" pattern).
func siteWithin(list []string, site string) bool {
	for _, s := range list {
		if s == site {
			return true
		}
		if dom, ok := strings.CutPrefix(s, "*."); ok {
			h := strings.TrimPrefix(site, "*.")
			if strings.HasSuffix(h, "."+dom) {
				return true
			}
		}
	}
	return false
}

// inheritTeam fills in each team default the bot left unset. Values are
// copied, never shared, so a bot's config can't alias its team's.
func inheritTeam(t TeamConfig, m BotConfig) BotConfig {
	if len(m.Operators) == 0 && len(t.Operators) > 0 {
		m.Operators = append([]string(nil), t.Operators...)
	}
	if m.Autonomy.Shadow == nil && t.Autonomy.Shadow != nil {
		v := *t.Autonomy.Shadow
		m.Autonomy.Shadow = &v
	}
	if len(t.Autonomy.Tiers) > 0 {
		tiers := map[string][]string{}
		for k, v := range m.Autonomy.Tiers {
			tiers[k] = append([]string(nil), v...)
		}
		for k, v := range t.Autonomy.Tiers {
			for _, p := range v {
				if !hasString(tiers[k], p) {
					tiers[k] = append(tiers[k], p)
				}
			}
		}
		m.Autonomy.Tiers = tiers
	}
	if m.Autonomy.SpendCap == nil && t.Budget.SpendCap != nil {
		sc := *t.Budget.SpendCap
		m.Autonomy.SpendCap = &sc
	}
	if pool := t.Budget.Proactive; pool != nil && m.Proactive != nil {
		p := *m.Proactive
		p.Budget = p.Budget.inherit(*pool)
		m.Proactive = &p
	}
	if m.Isolation.Sandbox == "" {
		m.Isolation.Sandbox = t.Isolation.Sandbox
	}
	if m.Isolation.HandoffWorkers == nil && t.Isolation.HandoffWorkers != nil {
		v := *t.Isolation.HandoffWorkers
		m.Isolation.HandoffWorkers = &v
	}
	if m.Isolation.SkipPermissionPrompts == nil && t.Isolation.SkipPermissionPrompts != nil {
		v := *t.Isolation.SkipPermissionPrompts
		m.Isolation.SkipPermissionPrompts = &v
	}
	if tc := t.Credentials; tc != nil {
		var sk SkillPolicy
		if m.Skill != nil {
			sk = *m.Skill
		}
		if sk.SecretsVia == "" {
			sk.SecretsVia = tc.SecretsVia
		}
		if len(sk.AllowSecrets) == 0 {
			sk.AllowSecrets = append([]string(nil), tc.AllowSecrets...)
		}
		if m.Skill != nil || sk.SecretsVia != "" || len(sk.AllowSecrets) > 0 {
			m.Skill = &sk
		}
	}
	if m.Browser == nil && t.Browser != nil {
		m.Browser = &BotBrowser{Logins: append([]string(nil), t.Browser.Logins...), Credentials: append([]string(nil), t.Browser.Credentials...)}
	}
	return m
}

// ---- validation ----

// validateTeams checks teams: and re-checks each bot's ceilings (a no-op
// after ResolveTeams, which also catches a Config assembled in code).
func (c *Config) validateTeams() error {
	for _, name := range c.TeamNames() {
		t := c.Teams[name]
		if err := c.validateBotTeam(name, t); err != nil {
			return fmt.Errorf("config: teams.%s: %w", name, err)
		}
		// The team's picture, beside the config: checked and resolved as a
		// bot's is.
		if a, err := resolveAvatarImage(t.Avatar, c.baseDir, "the config's directory"); err != nil {
			return fmt.Errorf("config: teams.%s: %w", name, err)
		} else if a != t.Avatar {
			t.Avatar = a
			c.Teams[name] = t
		}
	}
	for _, name := range sortedBotKeys(c.Bots) {
		m := c.Bots[name]
		if m.Team == "" {
			continue
		}
		t, ok := c.Teams[string(m.Team)]
		if !ok {
			return fmt.Errorf("config: bots.%s: team: %q is not a configured team", name, m.Team)
		}
		if err := checkTeamCeilings(string(m.Team), t, m); err != nil {
			return fmt.Errorf("config: bots.%s: %w", name, err)
		}
	}
	return nil
}

func (c *Config) validateBotTeam(name string, t TeamConfig) error {
	if !validBotName(name) {
		return fmt.Errorf("name must be lowercase letters, digits, - or _")
	}
	if err := validateAvatar(t.Avatar, "", false); err != nil {
		return err
	}
	if t.Chat.EveryonePerHour < 0 || t.Chat.EveryonePerHour > MaxEveryonePerHour {
		return fmt.Errorf("chat.everyone_per_hour: want 1 to %d, got %d", MaxEveryonePerHour, t.Chat.EveryonePerHour)
	}
	if t.Chat.MaxReply < 0 || t.Chat.MaxReply > MaxChatReply {
		return fmt.Errorf("chat.max_reply: want 1 to %d bytes, got %d", MaxChatReply, t.Chat.MaxReply)
	}
	if err := validateCollections(t.Collections, true); err != nil {
		return err
	}
	if t.Knowledge != nil {
		if err := t.Knowledge.MemoryLimits.validate(true); err != nil {
			return fmt.Errorf("knowledge.%w", err)
		}
	}
	for _, op := range t.Operators {
		if _, ok := c.Operators[op]; !ok {
			return fmt.Errorf("operators: %q is not in the top-level operators: section", op)
		}
	}
	switch t.Peers {
	case "", "team", "none":
	default:
		return fmt.Errorf("peers: want team or none, got %q", t.Peers)
	}
	if d := t.Delegation; d != nil && (d.MaxDepth < 1 || d.MaxDepth > 10) {
		return fmt.Errorf("delegation.max_depth: want 1 to 10 hops, got %d", d.MaxDepth)
	}
	switch t.Autonomy.Ceiling {
	case "", TierObserve, TierReversible:
	default:
		return fmt.Errorf("autonomy.ceiling: want %s or %s (the highest tier promotion can reach), got %q", TierObserve, TierReversible, t.Autonomy.Ceiling)
	}
	for tier, pats := range t.Autonomy.Tiers {
		if TierRank(tier) < 0 {
			return fmt.Errorf("autonomy.tiers: unknown tier %q (want one of %s)", tier, strings.Join(BotTiers, ", "))
		}
		for _, p := range pats {
			if _, err := filepath.Match(p, "x"); err != nil || strings.TrimSpace(p) == "" {
				return fmt.Errorf("autonomy.tiers.%s: bad pattern %q", tier, p)
			}
		}
	}
	if sc := t.Budget.SpendCap; sc != nil {
		if sc.PerAction <= 0 || sc.PerPeriod <= 0 {
			return fmt.Errorf("budget.spend_cap: per_action and per_period must both be > 0")
		}
		if sc.PerAction > sc.PerPeriod {
			return fmt.Errorf("budget.spend_cap: per_action exceeds per_period")
		}
	}
	if p := t.Budget.Proactive; p != nil {
		if err := p.validate(); err != nil {
			return fmt.Errorf("budget.proactive: %w", err)
		}
	}
	if err := validateBotIsolation(t.Isolation); err != nil {
		return err
	}
	if cr := t.Credentials; cr != nil {
		if _, ok := secretsViaRank[cr.SecretsVia]; !ok {
			return fmt.Errorf("credentials.secrets_via: want none, broker or env, got %q", cr.SecretsVia)
		}
		if len(cr.AllowSecrets) > 0 && secretsViaRank[cr.SecretsVia] == 0 {
			return fmt.Errorf("credentials.allow_secrets is set but credentials.secrets_via is none — bots could never be issued them; set secrets_via: broker")
		}
	}
	if k := t.Knowledge; k != nil {
		switch k.Promotion {
		case "", "proposal", "direct":
		default:
			return fmt.Errorf("knowledge.promotion: want proposal or direct, got %q", k.Promotion)
		}
		if d := c.TeamSeedDir(name); d != "" {
			if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
				return fmt.Errorf("knowledge.seed_dir: %s is not a directory", d)
			}
		}
	}
	if r := t.Routing; r != nil {
		inTeam := func(field, m string) error {
			mc, ok := c.Bots[m]
			if !ok || mc.Disabled {
				return fmt.Errorf("routing.%s: %q is not an enabled bot", field, m)
			}
			if string(mc.Team) != name {
				return fmt.Errorf("routing.%s: bot %s is not in team %s", field, m, name)
			}
			return nil
		}
		if r.Fallback == "" {
			return fmt.Errorf("routing.fallback: name the bot that handles what no rule or lead assigned")
		}
		if err := inTeam("fallback", r.Fallback); err != nil {
			return err
		}
		if r.Lead != "" {
			if err := inTeam("lead", r.Lead); err != nil {
				return err
			}
		}
		for i, rule := range r.Rules {
			if len(rule.Match) == 0 {
				return fmt.Errorf("routing.rules[%d]: match: needs at least one field (a rule matching everything is the fallback)", i)
			}
			for k, v := range rule.Match {
				if strings.TrimSpace(k) == "" {
					return fmt.Errorf("routing.rules[%d].match: empty field name", i)
				}
				if _, err := path.Match(v, ""); err != nil {
					return fmt.Errorf("routing.rules[%d].match.%s: bad pattern %q", i, k, v)
				}
			}
			if err := inTeam(fmt.Sprintf("rules[%d].to", i), rule.To); err != nil {
				return err
			}
		}
		if r.Timeout < 0 {
			return fmt.Errorf("routing.timeout must not be negative")
		}
	}
	return nil
}

func validateBotIsolation(i BotIsolation) error {
	switch i.Sandbox {
	case "", "optional", "required":
		return nil
	}
	return fmt.Errorf("isolation.sandbox: want optional or required, got %q", i.Sandbox)
}

// TeamWarnings are non-fatal findings `conductor validate` prints.
func (c *Config) TeamWarnings() []string {
	var out []string
	for _, name := range c.TeamNames() {
		t := c.Teams[name]
		if len(c.TeamBots(name)) == 0 {
			out = append(out, fmt.Sprintf("teams.%s: no enabled bot names this team", name))
		}
		if p := t.Budget.Proactive; p != nil && p.IsZero() {
			out = append(out, fmt.Sprintf("teams.%s: budget.proactive sets no cap, so it limits nothing (set max_runs_per_day, max_turns_per_day, max_tokens_per_day or max_cost_per_day)", name))
		}
		if t.Isolation.SandboxRequired() {
			out = append(out, fmt.Sprintf("teams.%s: isolation.sandbox required — its bots' sessions start only while conductor's agent sandbox is on", name))
		}
		if t.Browser != nil {
			out = append(out, fmt.Sprintf("teams.%s: browser: scopes logins for conductor's agent browser; until it is on this daemon bot sessions use no saved login at all", name))
		}
	}
	return out
}
