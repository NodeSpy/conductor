package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BOTS (docs/design/bots.md, docs/wiki/Bots.md): persistent,
// conversational agents with a charter, a git memory repo, a case ledger, a
// paseo home session the operator talks to, and cli workers they delegate to.
// Every mutating verb a bot's sessions call passes the bot's AUTONOMY
// GATE.
//
// Both sections are optional and absent from every config written before
// them: no `bots:` means no bot, no expansion, nothing to validate.

// Autonomy tiers, least to most guarded. The gate classifies every verb a
// bot session calls into exactly one.
const (
	TierObserve     = "observe"      // runs autonomously
	TierReversible  = "reversible"   // proposal until promoted (and sandboxed)
	TierPropose     = "propose"      // always a proposal; runs on operator approval
	TierCommitMoney = "commit_money" // proposal + typed amount under the spend cap
	TierNever       = "never"        // refused structurally
)

// BotTiers lists the tiers in strictness order (strictest last), which is
// also the order a verb matching several tiers resolves to: the strictest.
var BotTiers = []string{TierObserve, TierReversible, TierPropose, TierCommitMoney, TierNever}

// TierRank orders tiers by strictness (-1 for an unknown tier).
func TierRank(t string) int {
	for i, x := range BotTiers {
		if x == t {
			return i
		}
	}
	return -1
}

// OperatorConfig is one human who may approve bot proposals.
type OperatorConfig struct {
	// SSHKey is the operator's PUBLIC key in authorized_keys form. An
	// approval is an SSH signature over the proposal's digest made with the
	// matching private key (`conductor bot approve` signs through
	// ssh-keygen, so an ssh-agent / hardware key / 1Password agent works),
	// verified by the daemon. Required: an operator with no key cannot
	// approve anything.
	SSHKey string `yaml:"ssh_key"`
	// Aliases are requester ids that ARE this operator when an event names
	// them (e.g. "slack:U012ABC"), so a chat request from the operator is
	// attributed to the operator rather than to an anonymous user.
	Aliases []string `yaml:"aliases,omitempty"`
}

// BotConfig is one bot. The bot can never write any of this: it is
// operator config, and nothing on the agent-facing surface edits config.
type BotConfig struct {
	// Description is the bot's one-line identity ("SRE for acme").
	Description string `yaml:"description,omitempty"`
	// Charter is the bot's standing instructions — role, scope,
	// escalation policy, house rules. CharterFile reads it from a file
	// (relative to the config's directory). Exactly one, or neither (a
	// generic charter is used).
	Charter     string `yaml:"charter,omitempty"`
	CharterFile string `yaml:"charter_file,omitempty"`
	// Scope and Escalation are short structured charter sections rendered
	// with the charter; free text.
	Scope      string `yaml:"scope,omitempty"`
	Escalation string `yaml:"escalation,omitempty"`
	// Operators names the operators: entries who may approve this bot's
	// proposals. Empty = every configured operator.
	Operators []string `yaml:"operators,omitempty"`
	// MemoryRepo is the bot's memory git repo. Default:
	// <state dir>/bots/<name>/memory. Created on first start.
	MemoryRepo string `yaml:"memory_repo,omitempty"`
	// SeedDir seeds a NEW memory repo: its knowledge/ and runbooks/ Markdown
	// (a pack's, say) are copied into the first commit. Read once, at
	// creation; an existing repo is never touched. Relative to the config's
	// directory.
	SeedDir string `yaml:"seed_dir,omitempty"`
	// Home is the conversational session (paseo).
	Home BotHome `yaml:"home,omitempty"`
	// Worker is the background worker profile (cli runtime by default).
	Worker BotWorker `yaml:"worker,omitempty"`
	// Skill is the bot's verb grant. The home session and every worker
	// carry exactly this grant — a worker never gets more — and every verb in
	// it is additionally classified by Autonomy.
	Skill *SkillPolicy `yaml:"skill,omitempty"`
	// Autonomy is the gate: tiers, promotion, shadow mode, spend cap.
	Autonomy BotAutonomy `yaml:"autonomy,omitempty"`
	// Disabled keeps the bot configured but not started.
	Disabled bool `yaml:"disabled,omitempty"`
	// Terraform lets the bot propose infrastructure changes that
	// CONDUCTOR plans and, on a signed approval, applies. Nil: no terraform
	// proposals.
	Terraform *BotTerraform `yaml:"terraform,omitempty"`
	// Proactive makes the bot look around on its own, read-only, on a
	// schedule (a SWEEP: one cli worker per run). Nil: never — a bot opts
	// in.
	Proactive *BotProactive `yaml:"proactive,omitempty"`
	// Peers are the bots this one may exchange messages with (`conductor
	// bot ask`). A link needs BOTH sides to list each other, or an
	// operator-side bot_links: entry. Empty: no peers (teammates in a
	// team that peers are linked without it).
	Peers []string `yaml:"peers,omitempty"`
	// Team is the one team (teams:) this bot belongs to, if any. Its
	// defaults are this bot's ceilings (teams.go).
	Team TeamName `yaml:"team,omitempty"`
	// Folder is removed (removed.go): accepted, ignored, warned about.
	Folder RemovedField `yaml:"folder,omitempty"`
	// Collections are custom content kinds in the bot's memory repo, beside
	// the built-ins (collections.go).
	Collections map[string]Collection `yaml:"collections,omitempty"`
	// Use instantiates a bot TEMPLATE a pack ships (bottemplate.go):
	// "<pack instance>/<template>", the way a pack's other members are
	// named. The template supplies the charter, seed memory, collections and
	// a list of REQUESTED verbs; nothing it requests is active until Grant
	// says so.
	Use string `yaml:"use,omitempty"`
	// Connectors binds the template's connector inputs to your connectors
	// (input → connector instance). An input you have exactly one connector
	// of the type for binds itself.
	Connectors map[string]string `yaml:"connectors,omitempty"`
	// Grant is what you allow of the template's requested verbs: verb (in
	// the template's vocabulary, <input>.<verb>) → tier. A tier looser than
	// the template's maximum is cut to it. `conductor bot new` writes the
	// read-only ones; anything above observe is a widening you add.
	Grant map[string]string `yaml:"grant,omitempty"`
	// Origin is where a template bot came from — computed at load, never
	// written.
	Origin *BotOrigin `yaml:"-"`
	// Identity is how the bot presents: avatar, color, display_name, tagline,
	// personality (botidentity.go). Presentation only.
	Identity `yaml:",inline"`
	// IdentityFrom names the template a display name, tagline or
	// personality came from ("" when the operator wrote them) — computed.
	IdentityFrom string `yaml:"-"`
	// Memory bounds what the bot's sessions read from and write to its
	// memory repo: the prompt's index budget, the per-read cap, the
	// per-kind document caps (memlimits.go). On by default.
	Memory MemoryLimits `yaml:"memory,omitempty"`
	// Attachments bounds the files a message can carry to the bot: sizes,
	// count, media types and how long they are kept (attachments.go). On by
	// default.
	Attachments AttachmentLimits `yaml:"attachments,omitempty"`
	// Tickets: whether the bot may mark its own tickets ready (tickets.go).
	Tickets TicketSettings `yaml:"tickets,omitempty"`
	// Isolation is the bot's session policy (inherited from its team,
	// and only ever stricter).
	Isolation BotIsolation `yaml:"isolation,omitempty"`
	// Browser scopes the saved browser logins and sign-in credentials the
	// bot's sessions may use (nil: whatever the browser config allows its
	// identity).
	Browser *BotBrowser `yaml:"browser,omitempty"`
	// Purchases are the bot's own purchase caps (per purchase, per day, per
	// 30 days, optionally per card): another layer under the card's limits,
	// its team's and a ticket's spend_limit — the lowest wins. Inside its
	// team's budget.purchases.
	Purchases *PurchaseLimits `yaml:"purchases,omitempty"`
}

// BotProactive configures proactive sweeps. A sweep is a cli worker that
// works through the checklist (and what the bot's memory says is worth
// watching) with the gate holding it to observe-tier verbs: any other call
// becomes a proposal, as for a third party's request. Findings at or above
// NotifyAt open a case (deduplicated against open cases by key) and notify
// the operator; anything below is recorded and stays quiet.
type BotProactive struct {
	// Every is the interval between sweep starts (required).
	Every Duration `yaml:"every"`
	// QuietHours is a local-time window, "22:00-07:00", in which no sweep
	// starts (wraps midnight). Timezone is an IANA zone (default: the
	// daemon's local zone).
	QuietHours string `yaml:"quiet_hours,omitempty"`
	Timezone   string `yaml:"timezone,omitempty"`
	// Budget caps sweeps over a rolling 24 hours. A sweep that would exceed
	// it does not start (logged and audited).
	Budget ProactiveBudget `yaml:"budget,omitempty"`
	// Checklist is what every sweep works through: a section of the charter
	// (its heading text) or a file (config-relative). Exactly one.
	Checklist ProactiveChecklist `yaml:"checklist"`
	// NotifyAt is the lowest finding severity that opens a case and notifies
	// the operator: low | medium | high | critical (default high).
	NotifyAt string `yaml:"notify_at,omitempty"`
	// Timeout bounds one sweep (default 20m).
	Timeout Duration `yaml:"timeout,omitempty"`
}

// ProactiveBudget bounds sweeps per rolling 24 hours, in units — runs,
// model turns, tokens — and, optionally, dollars of model spend (only
// meaningful on API billing). Any combination, none required: the first cap
// a window reaches stops the next sweep. (commit_money's spend_cap is cloud
// spend, a different thing.)
type ProactiveBudget struct {
	MaxRunsPerDay   int        `yaml:"max_runs_per_day,omitempty"`
	MaxTurnsPerDay  int        `yaml:"max_turns_per_day,omitempty"`
	MaxTokensPerDay TokenCount `yaml:"max_tokens_per_day,omitempty"` // "500k" / "2m" accepted
	MaxCostPerDay   float64    `yaml:"max_cost_per_day,omitempty"`   // USD of model spend
}

// IsZero reports a budget with no cap set.
func (b ProactiveBudget) IsZero() bool {
	return b.MaxRunsPerDay == 0 && b.MaxTurnsPerDay == 0 && b.MaxTokensPerDay == 0 && b.MaxCostPerDay == 0
}

// validate refuses a negative cap.
func (b ProactiveBudget) validate() error {
	if b.MaxRunsPerDay < 0 || b.MaxTurnsPerDay < 0 || b.MaxTokensPerDay < 0 || b.MaxCostPerDay < 0 {
		return fmt.Errorf("max_runs_per_day, max_turns_per_day, max_tokens_per_day and max_cost_per_day must not be negative")
	}
	return nil
}

// BudgetUsage is what a window of sweeps used, in every unit a budget caps.
type BudgetUsage struct {
	Runs, Turns, Tokens int
	CostUSD             float64
}

// Exceeded names the first cap the usage has reached ("" = it fits): runs,
// then turns, then tokens, then dollars.
func (b ProactiveBudget) Exceeded(u BudgetUsage) string {
	switch {
	case b.MaxRunsPerDay > 0 && u.Runs >= b.MaxRunsPerDay:
		return fmt.Sprintf("%d sweeps in the last 24h (max_runs_per_day %d)", u.Runs, b.MaxRunsPerDay)
	case b.MaxTurnsPerDay > 0 && u.Turns >= b.MaxTurnsPerDay:
		return fmt.Sprintf("%d model turns in the last 24h (max_turns_per_day %d)", u.Turns, b.MaxTurnsPerDay)
	case b.MaxTokensPerDay > 0 && u.Tokens >= int(b.MaxTokensPerDay):
		return fmt.Sprintf("%d tokens in the last 24h (max_tokens_per_day %d)", u.Tokens, b.MaxTokensPerDay)
	case b.MaxCostPerDay > 0 && u.CostUSD >= b.MaxCostPerDay:
		return fmt.Sprintf("$%.2f of model spend in the last 24h (max_cost_per_day $%.2f)", u.CostUSD, b.MaxCostPerDay)
	}
	return ""
}

// withinCeiling refuses a bot budget past its team pool's, per unit: a unit
// the pool caps must be capped no higher (an unset unit inherits it).
func (b ProactiveBudget) withinCeiling(pool ProactiveBudget, team string) error {
	type unit struct {
		name      string
		own, ceil float64
	}
	for _, u := range []unit{
		{"max_runs_per_day", float64(b.MaxRunsPerDay), float64(pool.MaxRunsPerDay)},
		{"max_turns_per_day", float64(b.MaxTurnsPerDay), float64(pool.MaxTurnsPerDay)},
		{"max_tokens_per_day", float64(b.MaxTokensPerDay), float64(pool.MaxTokensPerDay)},
		{"max_cost_per_day", b.MaxCostPerDay, pool.MaxCostPerDay},
	} {
		if u.ceil > 0 && u.own > u.ceil {
			return fmt.Errorf("proactive.budget.%s %g exceeds team %s's budget.proactive.%s %g", u.name, u.own, team, u.name, u.ceil)
		}
	}
	return nil
}

// inherit fills each unit the bot left unset from its team's pool.
func (b ProactiveBudget) inherit(pool ProactiveBudget) ProactiveBudget {
	if b.MaxRunsPerDay == 0 {
		b.MaxRunsPerDay = pool.MaxRunsPerDay
	}
	if b.MaxTurnsPerDay == 0 {
		b.MaxTurnsPerDay = pool.MaxTurnsPerDay
	}
	if b.MaxTokensPerDay == 0 {
		b.MaxTokensPerDay = pool.MaxTokensPerDay
	}
	if b.MaxCostPerDay == 0 {
		b.MaxCostPerDay = pool.MaxCostPerDay
	}
	return b
}

// ProactiveChecklist names the sweep checklist's source.
type ProactiveChecklist struct {
	Section string `yaml:"section,omitempty"`
	File    string `yaml:"file,omitempty"`
}

// Finding severities, least first.
var FindingSeverities = []string{"low", "medium", "high", "critical"}

// SeverityRank orders finding severities (-1 for an unknown one).
func SeverityRank(s string) int {
	for i, x := range FindingSeverities {
		if x == s {
			return i
		}
	}
	return -1
}

// DefaultProactiveTimeout bounds one sweep.
const DefaultProactiveTimeout = 20 * time.Minute

// NotifyLevel is the effective notify threshold.
func (p BotProactive) NotifyLevel() string {
	if p.NotifyAt != "" {
		return p.NotifyAt
	}
	return "high"
}

// TimeoutD is the effective sweep bound.
func (p BotProactive) TimeoutD() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout.D()
	}
	return DefaultProactiveTimeout
}

// Location is the zone quiet hours are read in.
func (p BotProactive) Location() *time.Location {
	if p.Timezone != "" {
		if l, err := time.LoadLocation(p.Timezone); err == nil {
			return l
		}
	}
	return time.Local
}

// parseQuietHours reads "HH:MM-HH:MM" into minutes after midnight.
func parseQuietHours(s string) (from, to int, err error) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), "-")
	if !ok {
		return 0, 0, fmt.Errorf("quiet_hours %q: want \"HH:MM-HH:MM\"", s)
	}
	parse := func(x string) (int, error) {
		t, err := time.Parse("15:04", strings.TrimSpace(x))
		if err != nil {
			return 0, fmt.Errorf("quiet_hours %q: want \"HH:MM-HH:MM\"", s)
		}
		return t.Hour()*60 + t.Minute(), nil
	}
	if from, err = parse(a); err != nil {
		return 0, 0, err
	}
	if to, err = parse(b); err != nil {
		return 0, 0, err
	}
	if from == to {
		return 0, 0, fmt.Errorf("quiet_hours %q: an empty window", s)
	}
	return from, to, nil
}

// Quiet reports whether t falls inside the quiet hours (false when unset).
func (p BotProactive) Quiet(t time.Time) bool {
	if p.QuietHours == "" {
		return false
	}
	from, to, err := parseQuietHours(p.QuietHours)
	if err != nil {
		return false
	}
	lt := t.In(p.Location())
	m := lt.Hour()*60 + lt.Minute()
	if from < to {
		return m >= from && m < to
	}
	return m >= from || m < to // wraps midnight
}

// BotTerraform configures conductor-executed Terraform for a bot. The
// bot pins a change (repo, pushed commit, root module, workspace); conductor
// checks that commit out in its own private directory, plans it with the READ
// role, shows the plan, and after a signed approval applies exactly that saved
// plan with the WRITE role — which only conductor's own process ever holds.
type BotTerraform struct {
	// PlanConnector / ApplyConnector name `use: terraform` connectors whose
	// engine, binary and env are the read and write roles. The apply
	// connector is conductor's alone: no bot grant may reach its verbs.
	PlanConnector  string `yaml:"plan_connector"`
	ApplyConnector string `yaml:"apply_connector"`
	// Repos are the repositories the bot may propose changes in: name →
	// the git URL (or path) conductor fetches the pinned commit from.
	Repos map[string]string `yaml:"repos"`
	// Stateful lists resource TYPES (globs) whose destruction or replacement
	// loses data. A plan that destroys or replaces one is REFUSED — not
	// offered for approval. Unset: DefaultStatefulTypes.
	Stateful []string `yaml:"stateful,omitempty"`
	// AllowDestroy lists resource ADDRESSES (globs) explicitly allowed to be
	// destroyed or replaced even when stateful — the knob that loosens the
	// default, one address pattern at a time.
	AllowDestroy []string `yaml:"allow_destroy,omitempty"`
	// Timeout bounds one plan or apply (default 30m).
	Timeout Duration `yaml:"timeout,omitempty"`
}

// DefaultStatefulTypes are the resource types a plan may not destroy or
// replace unless allow_destroy names the address: databases, storage,
// volumes, keys, secrets, queues and streams that hold state of their own.
var DefaultStatefulTypes = []string{
	"aws_db_instance", "aws_rds_cluster*", "aws_dynamodb_table", "aws_s3_bucket",
	"aws_efs_file_system", "aws_ebs_volume", "aws_elasticache_*", "aws_elasticsearch_domain",
	"aws_opensearch_domain", "aws_kms_key", "aws_secretsmanager_secret", "aws_redshift_cluster",
	"aws_docdb_cluster*", "aws_neptune_cluster*", "aws_msk_cluster", "aws_sqs_queue", "aws_kinesis_stream",
	"aws_backup_vault", "aws_route53_zone",
	"google_sql_database_instance", "google_storage_bucket", "google_bigquery_dataset", "google_spanner_*",
	"google_kms_*", "google_compute_disk",
	"azurerm_*database*", "azurerm_storage_account", "azurerm_key_vault", "azurerm_cosmosdb_*", "azurerm_managed_disk",
	"kubernetes_persistent_volume*",
}

// StatefulTypes is the effective stateful list.
func (t BotTerraform) StatefulTypes() []string {
	if len(t.Stateful) > 0 {
		return t.Stateful
	}
	return DefaultStatefulTypes
}

// TimeoutD is the effective plan/apply bound.
func (t BotTerraform) TimeoutD() time.Duration {
	if t.Timeout > 0 {
		return t.Timeout.D()
	}
	return 30 * time.Minute
}

// TerraformRole is a terraform connector's engine, binary and env — what
// conductor runs a plan (read) or an apply (write) with.
type TerraformRole struct {
	Engine string            `yaml:"engine"`
	Binary string            `yaml:"binary"`
	Env    map[string]string `yaml:"env"`
}

// Bin is the CLI to run (binary, else the engine name, else terraform).
func (r TerraformRole) Bin() string {
	switch {
	case r.Binary != "":
		return r.Binary
	case r.Engine != "":
		return r.Engine
	}
	return "terraform"
}

// BotTerraformRole decodes a terraform connector's role from its config.
func (c *Config) BotTerraformRole(connector string) (TerraformRole, error) {
	ref, ok := c.ConnectorsMap[connector]
	if !ok {
		return TerraformRole{}, fmt.Errorf("no connector %q", connector)
	}
	if t := ref.TypeName(); t != "terraform" {
		return TerraformRole{}, fmt.Errorf("connector %q is %q, not a terraform connector", connector, t)
	}
	var raw struct {
		TerraformRole `yaml:",inline"`
		Rest          map[string]any `yaml:",inline"`
	}
	if err := ref.Decode(&raw); err != nil {
		return TerraformRole{}, fmt.Errorf("connector %q: %w", connector, err)
	}
	if raw.Engine == "terragrunt" {
		return TerraformRole{}, fmt.Errorf("connector %q: bot terraform needs terraform or tofu, not terragrunt", connector)
	}
	return raw.TerraformRole, nil
}

// BotHome configures the home session.
type BotHome struct {
	// Runtime is a paseo runtime ("" = the built-in/default paseo). The home
	// is what the operator talks to, so it is always on paseo.
	Runtime string `yaml:"runtime,omitempty"`
	// Model is the home agent's model — a fleet, id, wildcard or list, as on
	// a step — resolved through the same fleet ladder and discovered roster
	// (which also supplies paseo's --provider). Unset: the runtime's
	// models.default, else a bare launch.
	Model ModelSpec `yaml:"model,omitempty"`
	// Mode is the paseo session mode the home agent launches in (paseo
	// --mode, as a step's mode:). Unset: the provider's default. A mode that
	// skips tool-permission prompts lets the home agent run shell commands
	// unprompted AS YOU unless conductor's agent sandbox is on — the gate
	// covers conductor verbs, not raw shell.
	Mode string `yaml:"mode,omitempty"`
	// Rotate bounds one home session; a fresh session resumes from the
	// handoff note and the ledger.
	Rotate BotRotate `yaml:"rotate,omitempty"`
}

// BotRotate is when a home session is rotated out.
type BotRotate struct {
	MaxAge   Duration `yaml:"max_age,omitempty"`   // default 24h
	MaxTurns int      `yaml:"max_turns,omitempty"` // conductor-delivered turns; default 100
}

// BotWorker configures workers.
type BotWorker struct {
	// Runtime is a non-paseo runtime ("" = the one `use: cli` runtime).
	// Background work never creates paseo workspaces; only an explicit
	// hand-off worker (`conductor bot spawn --handoff`) runs on paseo.
	Runtime string `yaml:"runtime,omitempty"`
	// HandoffRuntime is the paseo runtime a hand-off worker runs on ("" =
	// the home runtime).
	HandoffRuntime string `yaml:"handoff_runtime,omitempty"`
	// Model is the worker's model (a fleet, id, or list), as on a step.
	Model ModelSpec `yaml:"model,omitempty"`
	// Timeout bounds one worker run (default 2h).
	Timeout Duration `yaml:"timeout,omitempty"`
	// MaxConcurrent caps live workers per bot (default 4).
	MaxConcurrent int `yaml:"max_concurrent,omitempty"`
	// MaxPerCase caps live workers on one case — a fan-out's width
	// (default 3).
	MaxPerCase int `yaml:"max_per_case,omitempty"`
}

// BotAutonomy is the bot's gate configuration.
type BotAutonomy struct {
	// Shadow (default TRUE) is calibration mode: nothing above observe ever
	// executes — the bot records what it WOULD do as shadow proposals,
	// which cannot be approved. Set false to allow approved execution.
	Shadow *bool `yaml:"shadow,omitempty"`
	// Tiers classifies verbs: tier → verb patterns (same matcher as
	// skill.verbs). A verb matching several tiers takes the strictest. An
	// unclassified verb is `propose`.
	Tiers map[string][]string `yaml:"tiers,omitempty"`
	// Promote lists reversible-tier verb patterns the bot may run
	// autonomously. Promotion is per action class and only takes effect
	// while the bot's sessions are sandboxed — without the sandbox a
	// promoted verb is still a proposal.
	Promote []string `yaml:"promote,omitempty"`
	// Amounts names, per commit_money verb pattern, the verb option that
	// carries the amount (a number in SpendCap.Currency).
	Amounts map[string]string `yaml:"amounts,omitempty"`
	// SpendCap is the hard ceiling on commit_money actions. Required when
	// any verb is classified commit_money.
	SpendCap *SpendCap `yaml:"spend_cap,omitempty"`
	// ApprovalTTL is how long a proposal stays approvable (default 24h).
	ApprovalTTL Duration `yaml:"approval_ttl,omitempty"`
	// Unattended lets a commit_money verb run WITHOUT an operator's
	// approval, under a smaller cap and only at named merchants: per verb
	// pattern. Never the default and a widening; it must fit inside
	// spend_cap (and so the team's), a team's bot needs the team's
	// autonomy.ceiling: commit_money, and it runs only while the bot is not
	// in shadow mode, its sessions are sandboxed, and the request is the
	// operator's or the bot's own. A recurring charge always needs approval.
	Unattended map[string]Unattended `yaml:"unattended,omitempty"`
}

// Unattended is one commit_money verb's unattended allowance: a spend cap
// (counted in the same ledger as every approved spend) and the merchants.
type Unattended struct {
	SpendCap `yaml:",inline"`
	// Merchants are the exact origins it may buy from unattended.
	Merchants []string `yaml:"merchants"`
	// MerchantOption is the verb option naming the merchant (default
	// "merchant").
	MerchantOption string `yaml:"merchant_option,omitempty"`
}

// MerchantOptionOr is the option carrying the merchant.
func (u Unattended) MerchantOptionOr() string {
	if u.MerchantOption != "" {
		return u.MerchantOption
	}
	return "merchant"
}

// SpendCap bounds commit-money actions.
type SpendCap struct {
	PerAction float64  `yaml:"per_action"`
	PerPeriod float64  `yaml:"per_period"`
	Period    Duration `yaml:"period,omitempty"`   // default 720h (30 days)
	Currency  string   `yaml:"currency,omitempty"` // default USD
}

// HandoffSessionTTL is the life of a durable conductor session token — an
// interactive hand-off's, and every bot session's — sliding on each use
// (internal/skill aliases this constant). A home session idle longer than
// this loses its connection to conductor, so rotation must come first.
const HandoffSessionTTL = 7 * 24 * time.Hour

// BotRotateMargin is how far inside HandoffSessionTTL home.rotate.max_age
// must stay: a session rotates at least this long before an idle token could
// expire under it.
const BotRotateMargin = 24 * time.Hour

// Bot defaults.
const (
	DefaultBotRotateAge     = 24 * time.Hour
	DefaultBotRotateTurns   = 100
	DefaultBotWorkerTimeout = 2 * time.Hour
	DefaultBotMaxWorkers    = 4
	DefaultBotMaxPerCase    = 3
	DefaultBotApprovalTTL   = 24 * time.Hour
	DefaultSpendPeriod      = 720 * time.Hour
)

// ShadowMode reports whether the bot is in shadow (calibration) mode — the
// default for a bot that does not say otherwise.
func (a BotAutonomy) ShadowMode() bool { return a.Shadow == nil || *a.Shadow }

// TTL is the proposal approval window.
func (a BotAutonomy) TTL() time.Duration {
	if a.ApprovalTTL > 0 {
		return a.ApprovalTTL.D()
	}
	return DefaultBotApprovalTTL
}

// PeriodD is the spend window.
func (s SpendCap) PeriodD() time.Duration {
	if s.Period > 0 {
		return s.Period.D()
	}
	return DefaultSpendPeriod
}

// Check is the one spend-cap rule (a bot's commit_money, a payment card's
// and an identity's limits all use it): amt may not exceed PerAction, and
// with what was spent in the trailing Period it may not exceed PerPeriod.
// spentSince sums what was spent since a time. Zero caps allow nothing.
func (s SpendCap) Check(amt float64, now time.Time, spentSince func(since time.Time) float64) error {
	cur := s.CurrencyOr()
	if amt > s.PerAction {
		return fmt.Errorf("amount %.2f %s exceeds the per-action cap %.2f %s", amt, cur, s.PerAction, cur)
	}
	spent := spentSince(now.Add(-s.PeriodD()))
	if spent+amt > s.PerPeriod {
		return fmt.Errorf("amount %.2f %s would bring spend in the last %s to %.2f, over the cap %.2f %s", amt, cur, s.PeriodD(), spent+amt, s.PerPeriod, cur)
	}
	return nil
}

// CurrencyOr is the cap's currency.
func (s SpendCap) CurrencyOr() string {
	if s.Currency != "" {
		return s.Currency
	}
	return "USD"
}

// RotateAge / RotateTurns are the effective rotation bounds.
func (h BotHome) RotateAge() time.Duration {
	if h.Rotate.MaxAge > 0 {
		return h.Rotate.MaxAge.D()
	}
	return DefaultBotRotateAge
}

func (h BotHome) RotateTurns() int {
	if h.Rotate.MaxTurns > 0 {
		return h.Rotate.MaxTurns
	}
	return DefaultBotRotateTurns
}

// TimeoutD / MaxWorkers are the effective worker bounds.
func (w BotWorker) TimeoutD() time.Duration {
	if w.Timeout > 0 {
		return w.Timeout.D()
	}
	return DefaultBotWorkerTimeout
}

func (w BotWorker) MaxWorkers() int {
	if w.MaxConcurrent > 0 {
		return w.MaxConcurrent
	}
	return DefaultBotMaxWorkers
}

// PerCase is the effective per-case worker cap.
func (w BotWorker) PerCase() int {
	if w.MaxPerCase > 0 {
		return w.MaxPerCase
	}
	return DefaultBotMaxPerCase
}

// Synthesized trigger names: each bot expands into two `on: manual`
// triggers the bot service fires to start a worker — through the same
// engine path `conductor run` takes, so a worker is an ordinary run (history,
// watch, budget, model resolution, guidance) and not a parallel launcher.
func BotWorkerTrigger(bot string) string  { return "bot:" + bot + ":worker" }
func BotHandoffTrigger(bot string) string { return "bot:" + bot + ":handoff" }

// BotSweepTrigger is the third synthesized trigger, for a bot with
// proactive: — the worker step with the sweep's timeout and, when the budget
// sets a cost, a 24h workflow budget the engine enforces on top of the
// bot service's own check.
func BotSweepTrigger(bot string) string { return "bot:" + bot + ":sweep" }

// BotWorkerStepID is the id of the agent step in both synthesized
// triggers; a worker's dispatch id is "<history id>:<this>".
const BotWorkerStepID = "work"

// Bot roles a session token carries.
const (
	BotRoleHome   = "home"
	BotRoleWorker = "worker"
)

// BotRepoDir is the bot's memory repo (configured or default).
func (c *Config) BotRepoDir(name string) string {
	m := c.Bots[name]
	if m.MemoryRepo != "" {
		return expandHome(m.MemoryRepo)
	}
	return filepath.Join(StateDir(), "bots", name, "memory")
}

// BotAttachmentsDir is where the bot's attachments are kept for its
// sessions: a read-only directory per message, in conductor's state and
// never in the memory repo (even one the operator placed elsewhere).
func (c *Config) BotAttachmentsDir(name string) string {
	return filepath.Join(StateDir(), "bots", name, "attachments")
}

// BotSeedDir is the bot's seed directory, resolved ("" when unset).
func (c *Config) BotSeedDir(name string) string {
	m := c.Bots[name]
	if m.SeedDir == "" {
		return ""
	}
	p := expandHome(m.SeedDir)
	if !filepath.IsAbs(p) && c.baseDir != "" {
		p = filepath.Join(c.baseDir, p)
	}
	return p
}

// BotCharter is the bot's charter text: inline, from its file, or "".
func (c *Config) BotCharter(name string) (string, error) {
	m := c.Bots[name]
	if m.CharterFile == "" && m.Charter == "" && m.Origin != nil {
		return c.templateCharter(name, m), nil
	}
	if m.CharterFile == "" {
		return m.Charter, nil
	}
	p := expandHome(m.CharterFile)
	if !filepath.IsAbs(p) && c.baseDir != "" {
		p = filepath.Join(c.baseDir, p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("bot %q: charter_file: %w", name, err)
	}
	return string(b), nil
}

// BotOperators is the operators who may approve this bot's proposals.
func (c *Config) BotOperators(name string) []string {
	m := c.Bots[name]
	if len(m.Operators) > 0 {
		return append([]string(nil), m.Operators...)
	}
	out := make([]string, 0, len(c.Operators))
	for n := range c.Operators {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// BotNames lists the enabled bots, sorted.
func (c *Config) BotNames() []string {
	out := make([]string, 0, len(c.Bots))
	for n, m := range c.Bots {
		if !m.Disabled {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// botWorkerRuntime resolves the worker runtime: explicit, else the single
// `use: cli` runtime.
func (c *Config) botWorkerRuntime(m BotConfig) (string, error) {
	if m.Worker.Runtime != "" {
		return m.Worker.Runtime, nil
	}
	var cli []string
	for n, rt := range c.Runtimes {
		if rt.BuiltinType() == "cli" {
			cli = append(cli, n)
		}
	}
	sort.Strings(cli)
	switch len(cli) {
	case 1:
		return cli[0], nil
	case 0:
		return "", fmt.Errorf("no `use: cli` runtime is configured for its workers — add one (runtimes: { claude: { use: cli, tool: claude-code } }) or set worker.runtime")
	default:
		return "", fmt.Errorf("several cli runtimes (%s) — set worker.runtime", strings.Join(cli, ", "))
	}
}

// botHomeRuntime resolves the home runtime name ("" = built-in paseo).
func (c *Config) botHomeRuntime(m BotConfig) string {
	if m.Home.Runtime != "" {
		return m.Home.Runtime
	}
	for n, rt := range c.Runtimes {
		if rt.Default && rt.BuiltinType() == "paseo" {
			return n
		}
	}
	for n, rt := range c.Runtimes {
		if rt.BuiltinType() == "paseo" {
			return n
		}
	}
	return ""
}

// BotHomeRuntime is the home session's runtime name ("" = built-in paseo).
func (c *Config) BotHomeRuntime(name string) string { return c.botHomeRuntime(c.Bots[name]) }

// expandBots synthesizes each enabled bot's two worker triggers. It
// runs at load, after packs and before trigger normalization, so the
// synthesized triggers are validated like any other. A no-op without
// `bots:`.
func (c *Config) expandBots() error {
	if len(c.Bots) == 0 {
		return nil
	}
	c.applyRuntimeUseDefaults()
	for _, name := range c.BotNames() {
		m := c.Bots[name]
		wrt, err := c.botWorkerRuntime(m)
		if err != nil {
			return fmt.Errorf("config: bots.%s: %w", name, err)
		}
		hrt := m.Worker.HandoffRuntime
		if hrt == "" {
			hrt = c.botHomeRuntime(m)
		}
		var sk *SkillPolicy
		if m.Skill != nil {
			cp := *m.Skill
			cp.Verbs = append([]string(nil), m.Skill.Verbs...)
			sk = &cp
		}
		worker := Step{
			ID:           BotWorkerStepID,
			Name:         BotWorkerTrigger(name),
			Type:         "agent",
			Runtime:      wrt,
			Model:        m.Worker.Model,
			Prompt:       "{{.inputs.prompt}}",
			WorkDir:      "{{.inputs.workdir}}",
			Checkout:     "none",
			Skill:        sk,
			WaitTimeout:  Duration(m.Worker.TimeoutD()),
			BoundBot:     name,
			BoundBotRole: BotRoleWorker,
		}
		handoff := worker
		handoff.Name = BotHandoffTrigger(name)
		handoff.Runtime = hrt
		handoff.Background = true
		handoff.Model = ModelSpec{}
		enabled := true
		c.Triggers = append(c.Triggers,
			TriggerSpec{On: ManualSource, Name: BotWorkerTrigger(name), Enabled: &enabled, Steps: []Step{worker}, Bot: name},
			TriggerSpec{On: ManualSource, Name: BotHandoffTrigger(name), Enabled: &enabled, Steps: []Step{handoff}, Bot: name},
		)
		if p := m.Proactive; p != nil {
			sweep := worker
			sweep.Name = BotSweepTrigger(name)
			sweep.WaitTimeout = Duration(p.TimeoutD())
			ts := TriggerSpec{On: ManualSource, Name: BotSweepTrigger(name), Enabled: &enabled, Steps: []Step{sweep}, Bot: name}
			// Tokens and dollars also bound a sweep while it runs (the
			// workflow budget sheds its next agent dispatch); runs and turns
			// are checked before each sweep starts.
			if p.Budget.MaxCostPerDay > 0 || p.Budget.MaxTokensPerDay > 0 {
				ts.Policy = &Policy{Budget: &BudgetPolicy{Window: Duration(24 * time.Hour), MaxCostUSD: p.Budget.MaxCostPerDay, MaxTokens: p.Budget.MaxTokensPerDay}}
			}
			c.Triggers = append(c.Triggers, ts)
		}
	}
	return nil
}

// BotLinked reports whether two bots may message each other: both
// list the other under peers:, an operator-side bot_links: entry pairs
// them, or they are teammates in a team whose peers: is not none. A bot is
// never its own peer.
func (c *Config) BotLinked(a, b string) bool {
	if a == b || a == "" || b == "" {
		return false
	}
	ma, oka := c.Bots[a]
	mb, okb := c.Bots[b]
	if !oka || !okb || ma.Disabled || mb.Disabled {
		return false
	}
	for _, l := range c.BotLinks {
		if len(l) == 2 && (l[0] == a && l[1] == b || l[0] == b && l[1] == a) {
			return true
		}
	}
	if c.sameTeamPeers(a, b) {
		return true
	}
	return hasString(ma.Peers, b) && hasString(mb.Peers, a)
}

func hasString(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// BotChecklist is the text a bot's sweeps work through: the named
// charter section, or the checklist file.
func (c *Config) BotChecklist(name string) (string, error) {
	m := c.Bots[name]
	if m.Proactive == nil {
		return "", fmt.Errorf("bot %q has no proactive: block", name)
	}
	cl := m.Proactive.Checklist
	if cl.File != "" {
		p := expandHome(cl.File)
		if !filepath.IsAbs(p) && c.baseDir != "" {
			p = filepath.Join(c.baseDir, p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("bot %q: proactive.checklist.file: %w", name, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	ch, err := c.BotCharter(name)
	if err != nil {
		return "", err
	}
	text, ok := MarkdownSection(ch, cl.Section)
	if !ok {
		return "", fmt.Errorf("bot %q: proactive.checklist.section: the charter has no heading %q", name, cl.Section)
	}
	return text, nil
}

// MarkdownSection returns the body under the heading whose text is title
// (case-insensitive), up to the next heading of the same or a higher level.
func MarkdownSection(doc, title string) (string, bool) {
	want := strings.ToLower(strings.TrimSpace(title))
	lines := strings.Split(doc, "\n")
	level := 0
	var out []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		lv := 0
		for lv < len(t) && t[lv] == '#' {
			lv++
		}
		isHeading := lv > 0 && lv <= 6 && len(t) > lv && t[lv] == ' '
		if level == 0 {
			if isHeading && strings.ToLower(strings.TrimSpace(t[lv:])) == want {
				level = lv
			}
			continue
		}
		if isHeading && lv <= level {
			break
		}
		out = append(out, l)
	}
	if level == 0 {
		return "", false
	}
	return strings.TrimSpace(strings.Join(out, "\n")), true
}

// validateBots checks operators: and bots:. Nil-safe and a no-op for
// a config with neither.
func (c *Config) validateBots() error {
	if err := c.validateBotSteps(); err != nil {
		return err
	}
	if err := c.validateTeams(); err != nil {
		return err
	}
	for name, op := range c.Operators {
		if !validBotName(name) {
			return fmt.Errorf("config: operators.%s: name must be lowercase letters, digits, - or _", name)
		}
		k := strings.Fields(op.SSHKey)
		if len(k) < 2 || !strings.HasPrefix(k[0], "ssh-") && !strings.HasPrefix(k[0], "ecdsa-") && !strings.HasPrefix(k[0], "sk-") {
			return fmt.Errorf("config: operators.%s: ssh_key must be a public key line (\"ssh-ed25519 AAAA…\")", name)
		}
	}
	for _, name := range sortedBotKeys(c.Bots) {
		m := c.Bots[name]
		if err := c.validateBot(name, m); err != nil {
			return fmt.Errorf("config: bots.%s: %w", name, err)
		}
		// A deprecated preset reads as its shape and eyes. A template's
		// was read in its pack, so one left here is the operator's own (and
		// a second Validate finds none, keeping the warning).
		if m.Avatar != nil && m.Avatar.Preset != "" {
			m.Avatar = resolveAvatarPreset(m.Avatar)
			c.Bots[name] = m
			c.avatarWarnings = append(c.avatarWarnings, presetWarning("bots."+name, m.Avatar))
		}
		// The operator's own avatar image, beside the config (a template's
		// was resolved in its pack).
		if a, err := resolveAvatarImage(m.Avatar, c.baseDir, "the config's directory"); err != nil {
			return fmt.Errorf("config: bots.%s: %w", name, err)
		} else if a != m.Avatar {
			m.Avatar = a
			c.Bots[name] = m
		}
	}
	for i, l := range c.BotLinks {
		if len(l) != 2 || l[0] == l[1] {
			return fmt.Errorf("config: bot_links[%d]: want a pair of two different bots", i)
		}
		for _, n := range l {
			if _, ok := c.Bots[n]; !ok {
				return fmt.Errorf("config: bot_links[%d]: %q is not a configured bot", i, n)
			}
		}
	}
	return nil
}

func (c *Config) validateBotProactive(name string, p *BotProactive) error {
	if p.Every.D() < 5*time.Minute {
		return fmt.Errorf("every: set an interval of at least 5m between sweeps")
	}
	if p.QuietHours != "" {
		if _, _, err := parseQuietHours(p.QuietHours); err != nil {
			return err
		}
	}
	if p.Timezone != "" {
		if _, err := time.LoadLocation(p.Timezone); err != nil {
			return fmt.Errorf("timezone: %w", err)
		}
	}
	if err := p.Budget.validate(); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	if (p.Checklist.Section == "") == (p.Checklist.File == "") {
		return fmt.Errorf("checklist: name exactly one of section (a charter heading) or file")
	}
	if _, err := c.BotChecklist(name); err != nil {
		return err
	}
	if p.NotifyAt != "" && SeverityRank(p.NotifyAt) < 0 {
		return fmt.Errorf("notify_at: %q is not one of %s", p.NotifyAt, strings.Join(FindingSeverities, ", "))
	}
	if p.Timeout < 0 {
		return fmt.Errorf("timeout must not be negative")
	}
	return nil
}

func (c *Config) validateBot(name string, m BotConfig) error {
	if !validBotName(name) {
		return fmt.Errorf("name must be lowercase letters, digits, - or _")
	}
	if err := m.Identity.Validate(); err != nil {
		return err
	}
	if err := validateCollections(m.Collections, false); err != nil {
		return err
	}
	if err := m.Memory.validate(false); err != nil {
		return fmt.Errorf("memory.%w", err)
	}
	if err := m.Attachments.validate(); err != nil {
		return fmt.Errorf("attachments.%w", err)
	}
	if err := m.Tickets.validate(false); err != nil {
		return err
	}
	if m.Charter != "" && m.CharterFile != "" {
		return fmt.Errorf("set charter or charter_file, not both")
	}
	if m.CharterFile != "" {
		if _, err := c.BotCharter(name); err != nil {
			return err
		}
	}
	if d := c.BotSeedDir(name); d != "" {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			return fmt.Errorf("seed_dir: %s is not a directory", d)
		}
	}
	for _, op := range m.Operators {
		if _, ok := c.Operators[op]; !ok {
			return fmt.Errorf("operators: %q is not in the top-level operators: section", op)
		}
	}
	if hr := m.Home.Runtime; hr != "" {
		rt, ok := c.Runtimes[hr]
		if !ok {
			return fmt.Errorf("home.runtime: unknown runtime %q", hr)
		}
		if rt.BuiltinType() != "paseo" {
			return fmt.Errorf("home.runtime: %q is not a paseo runtime — the home session is the one you talk to, and it lives in paseo", hr)
		}
	}
	if hr := m.Worker.HandoffRuntime; hr != "" {
		rt, ok := c.Runtimes[hr]
		if !ok || rt.BuiltinType() != "paseo" {
			return fmt.Errorf("worker.handoff_runtime: %q is not a paseo runtime", hr)
		}
	}
	if !m.Disabled {
		wr, err := c.botWorkerRuntime(m)
		if err != nil {
			return err
		}
		rt, ok := c.Runtimes[wr]
		if !ok {
			return fmt.Errorf("worker.runtime: unknown runtime %q", wr)
		}
		if rt.BuiltinType() == "paseo" {
			return fmt.Errorf("worker.runtime: %q is a paseo runtime — background workers must not create paseo workspaces; use a cli runtime (a hand-off worker is the explicit paseo opt-in)", wr)
		}
	}
	if age := m.Home.Rotate.MaxAge.D(); age > HandoffSessionTTL-BotRotateMargin {
		return fmt.Errorf("home.rotate.max_age %s is too long: a bot session's conductor token lives %s from its last use, so a home agent idle that long would lose its connection mid-session — rotation must come at least %s sooner (max_age <= %s)",
			age, HandoffSessionTTL, BotRotateMargin, HandoffSessionTTL-BotRotateMargin)
	}
	if m.Home.Rotate.MaxAge < 0 {
		return fmt.Errorf("home.rotate.max_age must not be negative")
	}
	if t := m.Terraform; t != nil {
		if err := c.validateBotTerraform(m, t); err != nil {
			return fmt.Errorf("terraform: %w", err)
		}
	}
	if p := m.Proactive; p != nil {
		if err := c.validateBotProactive(name, p); err != nil {
			return fmt.Errorf("proactive: %w", err)
		}
	}
	for _, peer := range m.Peers {
		if peer == name {
			return fmt.Errorf("peers: a bot is not its own peer")
		}
		if _, ok := c.Bots[peer]; !ok {
			return fmt.Errorf("peers: %q is not a configured bot", peer)
		}
	}
	if err := validateBotIsolation(m.Isolation); err != nil {
		return err
	}
	if !m.Isolation.PromptSkipAllowed() && SkipsPrompts(m.Home.Mode) {
		return fmt.Errorf("home.mode %q skips tool-permission prompts, but isolation.skip_permission_prompts is false%s", m.Home.Mode, teamNote(m))
	}
	if m.Worker.MaxConcurrent < 0 || m.Home.Rotate.MaxTurns < 0 || m.Worker.MaxPerCase < 0 {
		return fmt.Errorf("max_concurrent / max_per_case / rotate.max_turns must not be negative")
	}
	a := m.Autonomy
	hasMoney := false
	for tier, pats := range a.Tiers {
		if TierRank(tier) < 0 {
			return fmt.Errorf("autonomy.tiers: unknown tier %q (want one of %s)", tier, strings.Join(BotTiers, ", "))
		}
		for _, p := range pats {
			if strings.TrimSpace(p) == "" {
				return fmt.Errorf("autonomy.tiers.%s: empty pattern", tier)
			}
			if _, err := filepath.Match(p, "x"); err != nil {
				return fmt.Errorf("autonomy.tiers.%s: bad pattern %q: %w", tier, p, err)
			}
		}
		if tier == TierCommitMoney && len(pats) > 0 {
			hasMoney = true
		}
	}
	for _, p := range a.Promote {
		if _, err := filepath.Match(p, "x"); err != nil {
			return fmt.Errorf("autonomy.promote: bad pattern %q: %w", p, err)
		}
	}
	for pat, opt := range a.Amounts {
		if strings.TrimSpace(opt) == "" {
			return fmt.Errorf("autonomy.amounts.%s: name the option that carries the amount", pat)
		}
	}
	if hasMoney {
		if a.SpendCap == nil {
			return fmt.Errorf("autonomy: a commit_money tier needs autonomy.spend_cap (per_action and per_period)")
		}
		if len(a.Amounts) == 0 {
			return fmt.Errorf("autonomy: a commit_money tier needs autonomy.amounts naming each verb's amount option")
		}
	}
	if sc := a.SpendCap; sc != nil {
		if sc.PerAction <= 0 || sc.PerPeriod <= 0 {
			return fmt.Errorf("autonomy.spend_cap: per_action and per_period must both be > 0")
		}
		if sc.PerAction > sc.PerPeriod {
			return fmt.Errorf("autonomy.spend_cap: per_action exceeds per_period")
		}
	}
	if err := m.Purchases.check("purchases"); err != nil {
		return err
	}
	for pat, u := range a.Unattended {
		where := "autonomy.unattended." + pat
		money := false
		for _, p := range a.Tiers[TierCommitMoney] {
			if p == pat {
				money = true
			}
		}
		switch {
		case !money:
			return fmt.Errorf("%s: only a commit_money verb can be unattended — list %s under autonomy.tiers.commit_money", where, pat)
		case a.SpendCap == nil:
			return fmt.Errorf("%s needs autonomy.spend_cap", where)
		case u.PerAction <= 0 || u.PerPeriod <= 0 || u.PerAction > u.PerPeriod:
			return fmt.Errorf("%s: per_action and per_period are required, per_action ≤ per_period", where)
		case u.CurrencyOr() != a.SpendCap.CurrencyOr():
			return fmt.Errorf("%s: currency %s differs from spend_cap's %s", where, u.CurrencyOr(), a.SpendCap.CurrencyOr())
		case u.PerAction > a.SpendCap.PerAction || u.PerPeriod > a.SpendCap.PerPeriod:
			// Both caps apply at every unattended purchase; this keeps the
			// smaller one honest.
			return fmt.Errorf("%s: its cap must fit inside autonomy.spend_cap (per_action %g, per_period %g)", where, a.SpendCap.PerAction, a.SpendCap.PerPeriod)
		case len(u.Merchants) == 0:
			return fmt.Errorf("%s: name the merchants (exact origins) it may buy from unattended", where)
		}
		for _, mo := range u.Merchants {
			if err := exactOrigin(mo); err != nil {
				return fmt.Errorf("%s.merchants: %v", where, err)
			}
		}
	}
	return nil
}

// BotWarnings are non-fatal findings `conductor validate` prints.
func (c *Config) BotWarnings() []string {
	var out []string
	for _, name := range c.BotNames() {
		for _, pat := range walletKeys(c.Bots[name].Autonomy.Unattended) {
			u := c.Bots[name].Autonomy.Unattended[pat]
			out = append(out, fmt.Sprintf("bots.%s: autonomy.unattended lets %s spend without approval — up to %g %s per action, %g per %s, at %s (a widening)",
				name, pat, u.PerAction, u.CurrencyOr(), u.PerPeriod, u.PeriodD(), strings.Join(u.Merchants, ", ")))
		}
		m := c.Bots[name]
		if len(c.BotOperators(name)) == 0 {
			out = append(out, fmt.Sprintf("bots.%s: no operators are configured — its proposals can never be approved", name))
		}
		if SkipsPrompts(m.Home.Mode) {
			out = append(out, fmt.Sprintf("bots.%s: home.mode %q skips tool-permission prompts; unless conductor's agent sandbox is on, the home agent's shell runs unprompted as you — the gate covers conductor verbs, not raw shell", name, m.Home.Mode))
		}
		if len(m.Autonomy.Promote) > 0 {
			out = append(out, fmt.Sprintf("bots.%s: autonomy.promote takes effect only while bot sessions are sandboxed; until then promoted verbs stay proposals", name))
		}
		if c.BotMarksOwnReady(name) {
			out = append(out, fmt.Sprintf("bots.%s: may mark the tickets it creates ready (tickets.mark_own_ready) — a WIDENING: its own ideas can start work without an operator", name))
		}
		if m.Skill == nil || len(m.Skill.Verbs) == 0 {
			out = append(out, fmt.Sprintf("bots.%s: no skill.verbs — the bot can only observe through memory and its ledger", name))
		}
		for _, peer := range m.Peers {
			if !c.BotLinked(name, peer) {
				out = append(out, fmt.Sprintf("bots.%s: peers lists %q, but %s does not list %s back (and no bot_links: entry pairs them) — no messages pass until both sides do", name, peer, peer, name))
			}
		}
		out = append(out, c.cardCrossRefNotes("bots."+name+".purchases", m.Purchases)...)
		out = append(out, c.browserNarrowingNotes("bots."+name+".browser", m.Browser)...)
	}
	out = append(out, c.avatarWarnings...)
	return append(out, c.TeamWarnings()...)
}

// SkipsPrompts reports whether a paseo mode skips tool-permission prompts.
func SkipsPrompts(mode string) bool { return strings.Contains(strings.ToLower(mode), "bypass") }

// teamNote names a bot's team in an error about an inherited setting.
func teamNote(m BotConfig) string {
	if m.Team == "" {
		return ""
	}
	return " (team " + string(m.Team) + ")"
}

func validBotName(s string) bool {
	if s == "" || len(s) > 48 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// BotStep is the `bot:` helper step: how a trigger routes an event
// into a bot. Every string is templated against the run's scope.
//
//   - bot:
//     name: sre-acme
//     title: "{{.title}}"
//     text: "{{.summary}}"       # the payload — data, never instructions
//     requester: "{{.user}}"     # who asked; "" = no human (an alert)
//     urgent: true               # start a worker now, don't wait on the conversation
//
// action: rotate | consolidate runs those instead of a delivery (a cron
// trigger's weekly consolidation, say).
type BotStep struct {
	Name string `yaml:"name"`
	// Team routes to a team instead of one bot: its routing: picks who
	// handles it (a rule, the lead's triage, or the fallback). Exactly one
	// of name and team.
	Team      string `yaml:"team,omitempty"`
	Action    string `yaml:"action,omitempty"` // deliver (default) | rotate | consolidate
	Title     string `yaml:"title,omitempty"`
	Text      string `yaml:"text,omitempty"`
	Requester string `yaml:"requester,omitempty"`
	Urgent    bool   `yaml:"urgent,omitempty"`
}

func (m *BotStep) validate() error {
	if (strings.TrimSpace(m.Name) == "") == (strings.TrimSpace(m.Team) == "") {
		return fmt.Errorf("name exactly one of name: (the bot to route to) or team: (a team, whose routing picks the bot)")
	}
	if m.Team != "" && m.Action != "" && m.Action != "deliver" {
		return fmt.Errorf("team: routes a delivery; action %s needs a bot name:", m.Action)
	}
	switch m.Action {
	case "", "deliver", "rotate", "consolidate":
	default:
		return fmt.Errorf("action must be deliver, rotate or consolidate, got %q", m.Action)
	}
	if (m.Action == "rotate" || m.Action == "consolidate") && (m.Text != "" || m.Urgent || m.Requester != "") {
		return fmt.Errorf("action %s takes no text/requester/urgent", m.Action)
	}
	return nil
}

// validateBotSteps checks that every `bot:` step a trigger or workflow
// names a configured bot (statically named ones; a templated name is
// checked when it renders).
func (c *Config) validateBotSteps() error {
	check := func(where string, steps []Step) error {
		for i, st := range steps {
			if st.Bot == nil {
				continue
			}
			if tn := st.Bot.Team; tn != "" {
				if strings.Contains(tn, "{{") {
					continue
				}
				t, ok := c.Teams[tn]
				if !ok {
					return fmt.Errorf("config: %s.steps[%d]: bot.team: %q is not a configured team", where, i, tn)
				}
				if t.Routing == nil {
					return fmt.Errorf("config: %s.steps[%d]: bot.team: team %s has no routing: (rules, a lead, a fallback) — it cannot be routed to", where, i, tn)
				}
				continue
			}
			if strings.Contains(st.Bot.Name, "{{") {
				continue
			}
			if _, ok := c.Bots[st.Bot.Name]; !ok {
				return fmt.Errorf("config: %s.steps[%d]: bot: %q is not a configured bot", where, i, st.Bot.Name)
			}
		}
		return nil
	}
	for i, t := range c.Triggers {
		if err := check(fmt.Sprintf("triggers[%d]", i), t.Steps); err != nil {
			return err
		}
	}
	for n, w := range c.Workflows {
		if err := check("workflows."+n, w.Steps); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) validateBotTerraform(m BotConfig, t *BotTerraform) error {
	if t.PlanConnector == "" || t.ApplyConnector == "" {
		return fmt.Errorf("plan_connector and apply_connector are required (the read and write roles)")
	}
	for _, n := range []string{t.PlanConnector, t.ApplyConnector} {
		if _, err := c.BotTerraformRole(n); err != nil {
			return err
		}
	}
	if len(t.Repos) == 0 {
		return fmt.Errorf("repos: name at least one repository the bot may propose changes in")
	}
	for name, url := range t.Repos {
		if !validBotName(name) || strings.TrimSpace(url) == "" || strings.HasPrefix(url, "-") {
			return fmt.Errorf("repos.%s: want a name of lowercase letters/digits and a git URL or path", name)
		}
	}
	for _, list := range [][]string{t.Stateful, t.AllowDestroy} {
		for _, p := range list {
			if _, err := filepath.Match(p, "x"); err != nil || strings.TrimSpace(p) == "" {
				return fmt.Errorf("bad pattern %q", p)
			}
		}
	}
	// The write role is conductor's alone: a bot grant that could call
	// the apply connector's verbs would hand the agent the write role.
	if m.Skill != nil {
		for _, v := range m.Skill.Verbs {
			// The connector half as a pattern: "tf-write", "tf-*" and "*" all
			// reach tf-write.
			conn := strings.SplitN(v, ".", 2)[0]
			if ok, _ := filepath.Match(conn, t.ApplyConnector); ok {
				return fmt.Errorf("skill.verbs %q reaches the apply connector %q — the write role is conductor's alone", v, t.ApplyConnector)
			}
		}
	}
	return nil
}
