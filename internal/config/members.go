package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MEMBERS (docs/design/members.md, docs/wiki/Members.md): persistent,
// conversational agents with a charter, a git memory repo, a case ledger, a
// paseo home session the operator talks to, and cli workers they delegate to.
// Every mutating verb a member's sessions call passes the member's AUTONOMY
// GATE.
//
// Both sections are optional and absent from every config written before
// them: no `members:` means no member, no expansion, nothing to validate.

// Autonomy tiers, least to most guarded. The gate classifies every verb a
// member session calls into exactly one.
const (
	TierObserve     = "observe"      // runs autonomously
	TierReversible  = "reversible"   // proposal until promoted (and jailed)
	TierPropose     = "propose"      // always a proposal; runs on operator approval
	TierCommitMoney = "commit_money" // proposal + typed amount under the spend cap
	TierNever       = "never"        // refused structurally
)

// MemberTiers lists the tiers in strictness order (strictest last), which is
// also the order a verb matching several tiers resolves to: the strictest.
var MemberTiers = []string{TierObserve, TierReversible, TierPropose, TierCommitMoney, TierNever}

// TierRank orders tiers by strictness (-1 for an unknown tier).
func TierRank(t string) int {
	for i, x := range MemberTiers {
		if x == t {
			return i
		}
	}
	return -1
}

// OperatorConfig is one human who may approve member proposals.
type OperatorConfig struct {
	// SSHKey is the operator's PUBLIC key in authorized_keys form. An
	// approval is an SSH signature over the proposal's digest made with the
	// matching private key (`conductor member approve` signs through
	// ssh-keygen, so an ssh-agent / hardware key / 1Password agent works),
	// verified by the daemon. Required: an operator with no key cannot
	// approve anything.
	SSHKey string `yaml:"ssh_key"`
	// Aliases are requester ids that ARE this operator when an event names
	// them (e.g. "slack:U012ABC"), so a chat request from the operator is
	// attributed to the operator rather than to an anonymous user.
	Aliases []string `yaml:"aliases,omitempty"`
}

// MemberConfig is one member. The member can never write any of this: it is
// operator config, and nothing on the agent-facing surface edits config.
type MemberConfig struct {
	// Description is the member's one-line identity ("SRE for acme").
	Description string `yaml:"description,omitempty"`
	// Charter is the member's standing instructions — role, scope,
	// escalation policy, house rules. CharterFile reads it from a file
	// (relative to the config's directory). Exactly one, or neither (a
	// generic charter is used).
	Charter     string `yaml:"charter,omitempty"`
	CharterFile string `yaml:"charter_file,omitempty"`
	// Scope and Escalation are short structured charter sections rendered
	// with the charter; free text.
	Scope      string `yaml:"scope,omitempty"`
	Escalation string `yaml:"escalation,omitempty"`
	// Operators names the operators: entries who may approve this member's
	// proposals. Empty = every configured operator.
	Operators []string `yaml:"operators,omitempty"`
	// MemoryRepo is the member's memory git repo. Default:
	// <state dir>/members/<name>/memory. Created on first start.
	MemoryRepo string `yaml:"memory_repo,omitempty"`
	// SeedDir seeds a NEW memory repo: its knowledge/ and runbooks/ Markdown
	// (a pack's, say) are copied into the first commit. Read once, at
	// creation; an existing repo is never touched. Relative to the config's
	// directory.
	SeedDir string `yaml:"seed_dir,omitempty"`
	// Home is the conversational session (paseo).
	Home MemberHome `yaml:"home,omitempty"`
	// Worker is the background worker profile (cli runtime by default).
	Worker MemberWorker `yaml:"worker,omitempty"`
	// Skill is the member's verb grant. The home session and every worker
	// carry exactly this grant — a worker never gets more — and every verb in
	// it is additionally classified by Autonomy.
	Skill *SkillPolicy `yaml:"skill,omitempty"`
	// Autonomy is the gate: tiers, promotion, shadow mode, spend cap.
	Autonomy MemberAutonomy `yaml:"autonomy,omitempty"`
	// Disabled keeps the member configured but not started.
	Disabled bool `yaml:"disabled,omitempty"`
	// Terraform lets the member propose infrastructure changes that
	// CONDUCTOR plans and, on a signed approval, applies. Nil: no terraform
	// proposals.
	Terraform *MemberTerraform `yaml:"terraform,omitempty"`
}

// MemberTerraform configures conductor-executed Terraform for a member. The
// member pins a change (repo, pushed commit, root module, workspace); conductor
// checks that commit out in its own private directory, plans it with the READ
// role, shows the plan, and after a signed approval applies exactly that saved
// plan with the WRITE role — which only conductor's own process ever holds.
type MemberTerraform struct {
	// PlanConnector / ApplyConnector name `use: terraform` connectors whose
	// engine, binary and env are the read and write roles. The apply
	// connector is conductor's alone: no member grant may reach its verbs.
	PlanConnector  string `yaml:"plan_connector"`
	ApplyConnector string `yaml:"apply_connector"`
	// Repos are the repositories the member may propose changes in: name →
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
func (t MemberTerraform) StatefulTypes() []string {
	if len(t.Stateful) > 0 {
		return t.Stateful
	}
	return DefaultStatefulTypes
}

// TimeoutD is the effective plan/apply bound.
func (t MemberTerraform) TimeoutD() time.Duration {
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

// MemberTerraformRole decodes a terraform connector's role from its config.
func (c *Config) MemberTerraformRole(connector string) (TerraformRole, error) {
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
		return TerraformRole{}, fmt.Errorf("connector %q: member terraform needs terraform or tofu, not terragrunt", connector)
	}
	return raw.TerraformRole, nil
}

// MemberHome configures the home session.
type MemberHome struct {
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
	// unprompted AS YOU until the jail lands — the gate covers conductor
	// verbs, not raw shell.
	Mode string `yaml:"mode,omitempty"`
	// Rotate bounds one home session; a fresh session resumes from the
	// handoff note and the ledger.
	Rotate MemberRotate `yaml:"rotate,omitempty"`
}

// MemberRotate is when a home session is rotated out.
type MemberRotate struct {
	MaxAge   Duration `yaml:"max_age,omitempty"`   // default 24h
	MaxTurns int      `yaml:"max_turns,omitempty"` // conductor-delivered turns; default 100
}

// MemberWorker configures workers.
type MemberWorker struct {
	// Runtime is a non-paseo runtime ("" = the one `use: cli` runtime).
	// Background work never creates paseo workspaces; only an explicit
	// hand-off worker (`conductor member spawn --handoff`) runs on paseo.
	Runtime string `yaml:"runtime,omitempty"`
	// HandoffRuntime is the paseo runtime a hand-off worker runs on ("" =
	// the home runtime).
	HandoffRuntime string `yaml:"handoff_runtime,omitempty"`
	// Model is the worker's model (a fleet, id, or list), as on a step.
	Model ModelSpec `yaml:"model,omitempty"`
	// Timeout bounds one worker run (default 2h).
	Timeout Duration `yaml:"timeout,omitempty"`
	// MaxConcurrent caps live workers per member (default 4).
	MaxConcurrent int `yaml:"max_concurrent,omitempty"`
}

// MemberAutonomy is the member's gate configuration.
type MemberAutonomy struct {
	// Shadow (default TRUE) is calibration mode: nothing above observe ever
	// executes — the member records what it WOULD do as shadow proposals,
	// which cannot be approved. Set false to allow approved execution.
	Shadow *bool `yaml:"shadow,omitempty"`
	// Tiers classifies verbs: tier → verb patterns (same matcher as
	// skill.verbs). A verb matching several tiers takes the strictest. An
	// unclassified verb is `propose`.
	Tiers map[string][]string `yaml:"tiers,omitempty"`
	// Promote lists reversible-tier verb patterns the member may run
	// autonomously. Promotion is per action class and only takes effect
	// while the member's sessions are jailed (#154) — without the jail a
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
}

// SpendCap bounds commit-money actions.
type SpendCap struct {
	PerAction float64  `yaml:"per_action"`
	PerPeriod float64  `yaml:"per_period"`
	Period    Duration `yaml:"period,omitempty"`   // default 720h (30 days)
	Currency  string   `yaml:"currency,omitempty"` // default USD
}

// HandoffSessionTTL is the life of a durable conductor session token — an
// interactive hand-off's, and every member session's — sliding on each use
// (internal/skill aliases this constant). A home session idle longer than
// this loses its connection to conductor, so rotation must come first.
const HandoffSessionTTL = 7 * 24 * time.Hour

// MemberRotateMargin is how far inside HandoffSessionTTL home.rotate.max_age
// must stay: a session rotates at least this long before an idle token could
// expire under it.
const MemberRotateMargin = 24 * time.Hour

// Member defaults.
const (
	DefaultMemberRotateAge     = 24 * time.Hour
	DefaultMemberRotateTurns   = 100
	DefaultMemberWorkerTimeout = 2 * time.Hour
	DefaultMemberMaxWorkers    = 4
	DefaultMemberApprovalTTL   = 24 * time.Hour
	DefaultSpendPeriod         = 720 * time.Hour
)

// ShadowMode reports whether the member is in shadow (calibration) mode — the
// default for a member that does not say otherwise.
func (a MemberAutonomy) ShadowMode() bool { return a.Shadow == nil || *a.Shadow }

// TTL is the proposal approval window.
func (a MemberAutonomy) TTL() time.Duration {
	if a.ApprovalTTL > 0 {
		return a.ApprovalTTL.D()
	}
	return DefaultMemberApprovalTTL
}

// PeriodD is the spend window.
func (s SpendCap) PeriodD() time.Duration {
	if s.Period > 0 {
		return s.Period.D()
	}
	return DefaultSpendPeriod
}

// CurrencyOr is the cap's currency.
func (s SpendCap) CurrencyOr() string {
	if s.Currency != "" {
		return s.Currency
	}
	return "USD"
}

// RotateAge / RotateTurns are the effective rotation bounds.
func (h MemberHome) RotateAge() time.Duration {
	if h.Rotate.MaxAge > 0 {
		return h.Rotate.MaxAge.D()
	}
	return DefaultMemberRotateAge
}

func (h MemberHome) RotateTurns() int {
	if h.Rotate.MaxTurns > 0 {
		return h.Rotate.MaxTurns
	}
	return DefaultMemberRotateTurns
}

// TimeoutD / MaxWorkers are the effective worker bounds.
func (w MemberWorker) TimeoutD() time.Duration {
	if w.Timeout > 0 {
		return w.Timeout.D()
	}
	return DefaultMemberWorkerTimeout
}

func (w MemberWorker) MaxWorkers() int {
	if w.MaxConcurrent > 0 {
		return w.MaxConcurrent
	}
	return DefaultMemberMaxWorkers
}

// Synthesized trigger names: each member expands into two `on: manual`
// triggers the member service fires to start a worker — through the same
// engine path `conductor run` takes, so a worker is an ordinary run (history,
// watch, budget, model resolution, guidance) and not a parallel launcher.
func MemberWorkerTrigger(member string) string  { return "member:" + member + ":worker" }
func MemberHandoffTrigger(member string) string { return "member:" + member + ":handoff" }

// MemberWorkerStepID is the id of the agent step in both synthesized
// triggers; a worker's dispatch id is "<history id>:<this>".
const MemberWorkerStepID = "work"

// Member roles a session token carries.
const (
	MemberRoleHome   = "home"
	MemberRoleWorker = "worker"
)

// MemberRepoDir is the member's memory repo (configured or default).
func (c *Config) MemberRepoDir(name string) string {
	m := c.Members[name]
	if m.MemoryRepo != "" {
		return expandHome(m.MemoryRepo)
	}
	return filepath.Join(StateDir(), "members", name, "memory")
}

// MemberSeedDir is the member's seed directory, resolved ("" when unset).
func (c *Config) MemberSeedDir(name string) string {
	m := c.Members[name]
	if m.SeedDir == "" {
		return ""
	}
	p := expandHome(m.SeedDir)
	if !filepath.IsAbs(p) && c.baseDir != "" {
		p = filepath.Join(c.baseDir, p)
	}
	return p
}

// MemberCharter is the member's charter text: inline, from its file, or "".
func (c *Config) MemberCharter(name string) (string, error) {
	m := c.Members[name]
	if m.CharterFile == "" {
		return m.Charter, nil
	}
	p := expandHome(m.CharterFile)
	if !filepath.IsAbs(p) && c.baseDir != "" {
		p = filepath.Join(c.baseDir, p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("member %q: charter_file: %w", name, err)
	}
	return string(b), nil
}

// MemberOperators is the operators who may approve this member's proposals.
func (c *Config) MemberOperators(name string) []string {
	m := c.Members[name]
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

// MemberNames lists the enabled members, sorted.
func (c *Config) MemberNames() []string {
	out := make([]string, 0, len(c.Members))
	for n, m := range c.Members {
		if !m.Disabled {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// memberWorkerRuntime resolves the worker runtime: explicit, else the single
// `use: cli` runtime.
func (c *Config) memberWorkerRuntime(m MemberConfig) (string, error) {
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

// memberHomeRuntime resolves the home runtime name ("" = built-in paseo).
func (c *Config) memberHomeRuntime(m MemberConfig) string {
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

// MemberHomeRuntime is the home session's runtime name ("" = built-in paseo).
func (c *Config) MemberHomeRuntime(name string) string { return c.memberHomeRuntime(c.Members[name]) }

// expandMembers synthesizes each enabled member's two worker triggers. It
// runs at load, after packs and before trigger normalization, so the
// synthesized triggers are validated like any other. A no-op without
// `members:`.
func (c *Config) expandMembers() error {
	if len(c.Members) == 0 {
		return nil
	}
	c.applyRuntimeUseDefaults()
	for _, name := range c.MemberNames() {
		m := c.Members[name]
		wrt, err := c.memberWorkerRuntime(m)
		if err != nil {
			return fmt.Errorf("config: members.%s: %w", name, err)
		}
		hrt := m.Worker.HandoffRuntime
		if hrt == "" {
			hrt = c.memberHomeRuntime(m)
		}
		var sk *SkillPolicy
		if m.Skill != nil {
			cp := *m.Skill
			cp.Verbs = append([]string(nil), m.Skill.Verbs...)
			sk = &cp
		}
		worker := Step{
			ID:              MemberWorkerStepID,
			Name:            MemberWorkerTrigger(name),
			Type:            "agent",
			Runtime:         wrt,
			Model:           m.Worker.Model,
			Prompt:          "{{.inputs.prompt}}",
			WorkDir:         "{{.inputs.workdir}}",
			Checkout:        "none",
			Skill:           sk,
			WaitTimeout:     Duration(m.Worker.TimeoutD()),
			BoundMember:     name,
			BoundMemberRole: MemberRoleWorker,
		}
		handoff := worker
		handoff.Name = MemberHandoffTrigger(name)
		handoff.Runtime = hrt
		handoff.Background = true
		handoff.Model = ModelSpec{}
		enabled := true
		c.Triggers = append(c.Triggers,
			TriggerSpec{On: ManualSource, Name: MemberWorkerTrigger(name), Enabled: &enabled, Steps: []Step{worker}},
			TriggerSpec{On: ManualSource, Name: MemberHandoffTrigger(name), Enabled: &enabled, Steps: []Step{handoff}},
		)
	}
	return nil
}

// validateMembers checks operators: and members:. Nil-safe and a no-op for
// a config with neither.
func (c *Config) validateMembers() error {
	if err := c.validateMemberSteps(); err != nil {
		return err
	}
	for name, op := range c.Operators {
		if !validMemberName(name) {
			return fmt.Errorf("config: operators.%s: name must be lowercase letters, digits, - or _", name)
		}
		k := strings.Fields(op.SSHKey)
		if len(k) < 2 || !strings.HasPrefix(k[0], "ssh-") && !strings.HasPrefix(k[0], "ecdsa-") && !strings.HasPrefix(k[0], "sk-") {
			return fmt.Errorf("config: operators.%s: ssh_key must be a public key line (\"ssh-ed25519 AAAA…\")", name)
		}
	}
	for name, m := range c.Members {
		if err := c.validateMember(name, m); err != nil {
			return fmt.Errorf("config: members.%s: %w", name, err)
		}
	}
	return nil
}

func (c *Config) validateMember(name string, m MemberConfig) error {
	if !validMemberName(name) {
		return fmt.Errorf("name must be lowercase letters, digits, - or _")
	}
	if m.Charter != "" && m.CharterFile != "" {
		return fmt.Errorf("set charter or charter_file, not both")
	}
	if m.CharterFile != "" {
		if _, err := c.MemberCharter(name); err != nil {
			return err
		}
	}
	if d := c.MemberSeedDir(name); d != "" {
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
		wr, err := c.memberWorkerRuntime(m)
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
	if age := m.Home.Rotate.MaxAge.D(); age > HandoffSessionTTL-MemberRotateMargin {
		return fmt.Errorf("home.rotate.max_age %s is too long: a member session's conductor token lives %s from its last use, so a home agent idle that long would lose its connection mid-session — rotation must come at least %s sooner (max_age <= %s)",
			age, HandoffSessionTTL, MemberRotateMargin, HandoffSessionTTL-MemberRotateMargin)
	}
	if m.Home.Rotate.MaxAge < 0 {
		return fmt.Errorf("home.rotate.max_age must not be negative")
	}
	if t := m.Terraform; t != nil {
		if err := c.validateMemberTerraform(m, t); err != nil {
			return fmt.Errorf("terraform: %w", err)
		}
	}
	if m.Worker.MaxConcurrent < 0 || m.Home.Rotate.MaxTurns < 0 {
		return fmt.Errorf("max_concurrent / rotate.max_turns must not be negative")
	}
	a := m.Autonomy
	hasMoney := false
	for tier, pats := range a.Tiers {
		if TierRank(tier) < 0 {
			return fmt.Errorf("autonomy.tiers: unknown tier %q (want one of %s)", tier, strings.Join(MemberTiers, ", "))
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
	return nil
}

// MemberWarnings are non-fatal findings `conductor validate` prints.
func (c *Config) MemberWarnings() []string {
	var out []string
	for _, name := range c.MemberNames() {
		m := c.Members[name]
		if len(c.MemberOperators(name)) == 0 {
			out = append(out, fmt.Sprintf("members.%s: no operators are configured — its proposals can never be approved", name))
		}
		if strings.Contains(strings.ToLower(m.Home.Mode), "bypass") {
			out = append(out, fmt.Sprintf("members.%s: home.mode %q skips tool-permission prompts; until member sessions are jailed (#154) the home agent's shell runs unprompted as you — the gate covers conductor verbs, not raw shell", name, m.Home.Mode))
		}
		if len(m.Autonomy.Promote) > 0 {
			out = append(out, fmt.Sprintf("members.%s: autonomy.promote takes effect only while member sessions are jailed (#154); until then promoted verbs stay proposals", name))
		}
		if m.Skill == nil || len(m.Skill.Verbs) == 0 {
			out = append(out, fmt.Sprintf("members.%s: no skill.verbs — the member can only observe through memory and its ledger", name))
		}
	}
	return out
}

func validMemberName(s string) bool {
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

// MemberStep is the `member:` helper step: how a trigger routes an event
// into a member. Every string is templated against the run's scope.
//
//   - member:
//     name: sre-acme
//     title: "{{.title}}"
//     text: "{{.summary}}"       # the payload — data, never instructions
//     requester: "{{.user}}"     # who asked; "" = no human (an alert)
//     urgent: true               # start a worker now, don't wait on the conversation
//
// action: rotate | consolidate runs those instead of a delivery (a cron
// trigger's weekly consolidation, say).
type MemberStep struct {
	Name      string `yaml:"name"`
	Action    string `yaml:"action,omitempty"` // deliver (default) | rotate | consolidate
	Title     string `yaml:"title,omitempty"`
	Text      string `yaml:"text,omitempty"`
	Requester string `yaml:"requester,omitempty"`
	Urgent    bool   `yaml:"urgent,omitempty"`
}

func (m *MemberStep) validate() error {
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("name: is required (the member to route to)")
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

// validateMemberSteps checks that every `member:` step a trigger or workflow
// names a configured member (statically named ones; a templated name is
// checked when it renders).
func (c *Config) validateMemberSteps() error {
	check := func(where string, steps []Step) error {
		for i, st := range steps {
			if st.Member == nil || strings.Contains(st.Member.Name, "{{") {
				continue
			}
			if _, ok := c.Members[st.Member.Name]; !ok {
				return fmt.Errorf("config: %s.steps[%d]: member: %q is not a configured member", where, i, st.Member.Name)
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

func (c *Config) validateMemberTerraform(m MemberConfig, t *MemberTerraform) error {
	if t.PlanConnector == "" || t.ApplyConnector == "" {
		return fmt.Errorf("plan_connector and apply_connector are required (the read and write roles)")
	}
	for _, n := range []string{t.PlanConnector, t.ApplyConnector} {
		if _, err := c.MemberTerraformRole(n); err != nil {
			return err
		}
	}
	if len(t.Repos) == 0 {
		return fmt.Errorf("repos: name at least one repository the member may propose changes in")
	}
	for name, url := range t.Repos {
		if !validMemberName(name) || strings.TrimSpace(url) == "" || strings.HasPrefix(url, "-") {
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
	// The write role is conductor's alone: a member grant that could call
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
