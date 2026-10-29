// Package api holds the dashboard's HTTP request and response types — the
// contract shared by the Go handlers and the TypeScript client. The TS types in
// web/src/api/types.ts are generated from this file with tygo (see
// web/tygo.yaml and `make web-types`); this file is the single source of truth
// for the shapes the dashboard serves.
//
// Phase 1 covers the Overview/Activity slice: status, runs and the live event
// stream. The remaining endpoints in design §5 are added here as they are built.
package api

// StatusResponse is GET /api/v1/status: a snapshot of the daemon's identity and
// health, read entirely from live in-process state.
type StatusResponse struct {
	Version   string `json:"version"`    // conductor build version ("dev" for an unstamped build)
	PID       int    `json:"pid"`        // the daemon process id
	StartedAt string `json:"started_at"` // RFC3339 time the dashboard server came up
	UptimeSec int64  `json:"uptime_sec"` // seconds since StartedAt
	Paused    bool   `json:"paused"`     // dispatch is paused (the `conductor pause` control file is present)
	ConfigSHA string `json:"config_sha"` // short hex sha256 of the loaded config file
}

// RunSummary is one row of run history for the Activity list and the Overview.
// It is the display projection of store.RunHistory — timestamps as RFC3339
// strings, step count instead of the full step records.
type RunSummary struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	Workflow   string  `json:"workflow"` // the trigger variant name, falling back to Kind
	Repo       string  `json:"repo,omitempty"`
	Number     int     `json:"number,omitempty"`
	Status     string  `json:"status"`             // running | ok | failed | retried
	Started    string  `json:"started"`            // RFC3339
	Finished   string  `json:"finished,omitempty"` // RFC3339; empty while running
	DurationMS int64   `json:"duration_ms"`        // finished runs: Finished-Started; running: elapsed so far
	Error      string  `json:"error,omitempty"`
	FailedStep string  `json:"failed_step,omitempty"`
	Steps      int     `json:"steps"`
	RetryOf    string  `json:"retry_of,omitempty"`
	Tokens     int     `json:"tokens,omitempty"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
}

// RunsResponse is GET /api/v1/runs: a page of run history, newest first, with a
// cursor for the next page ("" when there is no more).
type RunsResponse struct {
	Runs []RunSummary `json:"runs"`
	Next string       `json:"next,omitempty"`
}

// RunDetailResponse is GET /api/v1/runs/{id}: one execution in full — the run
// summary, its steps with per-step I/O and timing, whether it is live in this
// process, and the retry back/forward links. It is the display projection of
// store.RunHistory; secret values never appear (the records are scrubbed before
// they are written).
type RunDetailResponse struct {
	Run     RunSummary `json:"run"`
	Live    bool       `json:"live"`              // the run is still executing in this daemon
	Trigger string     `json:"trigger,omitempty"` // a human line for what started it
	Steps   []RunStep  `json:"steps"`
}

// RunStep is one step's execution record for the run page's step tree and step
// card. Inputs/Outputs are rendered as YAML text (scrubbed, checkpoint form);
// Diff is a unified diff when the step recorded one in its outputs.
type RunStep struct {
	ID               string  `json:"id"`
	Index            int     `json:"index"`
	Kind             string  `json:"kind"`   // agent | command | code | verb | decide | handoff | call | step
	Status           string  `json:"status"` // ok | failed | skipped | running | idle
	Started          string  `json:"started,omitempty"`
	DurationMS       int64   `json:"duration_ms,omitempty"`
	InputsYAML       string  `json:"inputs_yaml,omitempty"`
	OutputsYAML      string  `json:"outputs_yaml,omitempty"`
	Diff             string  `json:"diff,omitempty"`
	Error            string  `json:"error,omitempty"`
	ContinuedOnError bool    `json:"continued_on_error,omitempty"`
	Tokens           int     `json:"tokens,omitempty"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
	Runtime          string  `json:"runtime,omitempty"`
	Model            string  `json:"model,omitempty"`
}

// RetryRequest is POST /api/v1/runs/{id}/retry: re-run a recorded execution
// from a step. From is the step id to restart from ("" = the recorded failed
// step, else the beginning). Force replays steps the record says already
// succeeded (replaying their committed side effects) — never the default.
type RetryRequest struct {
	From  string `json:"from,omitempty"`
	Force bool   `json:"force,omitempty"`
}

// ActionResponse is a plain acknowledgment for retry/stop: a human message and,
// for a retry, the new run's id when the engine reports one.
type ActionResponse struct {
	Message string `json:"message"`
	RunID   string `json:"run_id,omitempty"`
}

// WorkflowsResponse is GET /api/v1/workflows: every configured trigger as a
// workflow, with how many steps it runs and its recent run tally.
type WorkflowsResponse struct {
	Workflows []WorkflowSummary `json:"workflows"`
}

// WorkflowSummary is one row of the Workflows list.
type WorkflowSummary struct {
	Name     string `json:"name"`           // the trigger name, or its on: when unnamed
	On       string `json:"on,omitempty"`   // the inbound event (<connector>.<event> | manual)
	Steps    int    `json:"steps"`          // top-level step count
	Disabled bool   `json:"disabled"`       // enabled: false
	Manual   bool   `json:"manual"`         // reachable by `conductor run` / Test
	Runs     int    `json:"runs,omitempty"` // runs recorded for it (history)
	Failed   int    `json:"failed,omitempty"`
	LastRun  string `json:"last_run,omitempty"` // RFC3339 of its most recent run
	LastKey  string `json:"last_key,omitempty"` // id of its most recent run (for a link)
}

// WorkflowDetailResponse is GET /api/v1/workflows/{name}: the workflow's graph
// (one node per step, edges from the step order / parallel branches), its YAML
// (the trigger spec re-marshalled), and its recent runs.
type WorkflowDetailResponse struct {
	Name     string       `json:"name"`
	On       string       `json:"on,omitempty"`
	Manual   bool         `json:"manual"`
	Disabled bool         `json:"disabled"`
	YAML     string       `json:"yaml"`
	Nodes    []GraphNode  `json:"nodes"`
	Runs     []RunSummary `json:"runs"`
}

// GraphNode is one step in a workflow's graph. Needs lists the ids it waits on
// (the previous step, or the branch head for a parallel branch). Line is the
// 1-based line in YAML where the node is defined, so clicking a node can reveal
// it in the YAML pane (node→YAML).
type GraphNode struct {
	ID    string   `json:"id"`
	Kind  string   `json:"kind"`
	Label string   `json:"label,omitempty"`
	Needs []string `json:"needs,omitempty"`
	Line  int      `json:"line,omitempty"`
}

// TestResponse is POST /api/v1/workflows/{name}/test: a dry-run that validates
// the workflow resolves (templates, verbs, runtimes) without dispatching any
// agent or outbound verb. Ok is true when it would run; Steps echoes the plan.
type TestResponse struct {
	Ok      bool        `json:"ok"`
	Message string      `json:"message"`
	Nodes   []GraphNode `json:"nodes,omitempty"`
}

// AttrsResponse is GET /api/v1/connectors/attrs: the search qualifiers the
// connectors publish for the filter bar (e.g. github.repo). These are
// connector concepts — they are offered as search-qualifier pills, never as
// their own filter menu (menus are conductor's own: status, workflow, bot).
type AttrsResponse struct {
	Attrs []AttrQualifier `json:"attrs"`
}

// AttrQualifier is one connector-published search qualifier.
type AttrQualifier struct {
	Key       string `json:"key"`       // the qualifier, "<connector>.<field>" (github.repo)
	Connector string `json:"connector"` // the connector namespace (github)
	Field     string `json:"field"`     // repo | number
	Label     string `json:"label"`     // human label (Repo)
}

// AttrValuesResponse is GET /api/v1/connectors/attrs/values?attr=github.repo:
// the distinct values present in the run history for that qualifier, for
// autocomplete.
type AttrValuesResponse struct {
	Values []string `json:"values"`
}

// EventTopic names a stream a client subscribes to via GET /api/v1/events?topics=.
type EventTopic string

const (
	// TopicRun carries run lifecycle events (run_started, step_started,
	// step_done, run_done), sourced from flow.EventHub.
	TopicRun EventTopic = "run"
	// TopicAttention carries items that need a person. In this slice it is fed
	// by failed runs; the full attention aggregator (design §4.1) is Phase 2.
	TopicAttention EventTopic = "attention"
)

// Event is one server-sent event. On the wire the SSE `id:` field carries ID
// (so a reconnect with Last-Event-ID resumes), `event:` carries Topic, and
// `data:` is the JSON encoding of this whole envelope.
type Event struct {
	ID    int64      `json:"id"`
	Topic EventTopic `json:"topic"`
	Type  string     `json:"type"` // run_started | step_started | step_done | run_done | attention | heartbeat | reset
	TS    string     `json:"ts"`   // RFC3339
	Data  any        `json:"data,omitempty"`
}

// RunEventData is the payload of a TopicRun event — a projection of
// flow.RunEvent. (It is also the shape of a TopicAttention event's data, which
// is a failed run_done in this slice.)
type RunEventData struct {
	Type       string `json:"type"`
	Run        string `json:"run"`
	RunID      string `json:"run_id,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Repo       string `json:"repo,omitempty"`
	Number     int    `json:"number,omitempty"`
	Step       string `json:"step,omitempty"`
	Status     string `json:"status,omitempty"`
	Detail     string `json:"detail,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// --- Config edit (design §5.3): the backup→validate-on-copy→diff→apply→reload
// pipeline the Config, Secrets, Packs, Plugins and System screens drive. The
// shapes mirror internal/configedit (Change, Review, Diff, DiffLine, Effect,
// HistoryEntry, Applied) so the TS client and the Go pipeline share one
// contract.

// ConfigResponse is GET /api/v1/config: the live config file, its short sha, the
// raw text, a generic decoded tree for section rendering, and a flattened list
// of scalar leaves (path+value) for the settings-style editors.
type ConfigResponse struct {
	Path   string         `json:"path"`
	SHA    string         `json:"sha"`
	Text   string         `json:"text"`
	Tree   any            `json:"tree"`   // generic decode of the YAML (maps/lists/scalars)
	Leaves []ConfigLeaf   `json:"leaves"` // every scalar, by dotted path
	Staged []ConfigChange `json:"staged"` // the currently staged edits
}

// ConfigLeaf is one scalar value in the config, addressed by its path segments.
type ConfigLeaf struct {
	Path    []string `json:"path"`
	Section string   `json:"section"`
	Kind    string   `json:"kind"` // string | int | float | bool | null
	Value   any      `json:"value"`
}

// ConfigChange is one staged edit (mirrors configedit.Change).
type ConfigChange struct {
	ID      string   `json:"id"`
	Path    []string `json:"path"`
	Value   any      `json:"value,omitempty"`
	Delete  bool     `json:"delete,omitempty"`
	Summary string   `json:"summary"`
	Section string   `json:"section"`
}

// DiffLineDTO is one rendered diff line (mirrors configedit.DiffLine).
type DiffLineDTO struct {
	Op      string `json:"op"` // context | add | del
	Text    string `json:"text"`
	OldLine int    `json:"old_line,omitempty"`
	NewLine int    `json:"new_line,omitempty"`
}

// ReviewResponse is GET /api/v1/config/review and the restore review: the diff,
// the effects (every change in words), the widening subset to surface loudly,
// the candidate sha and the step-up confirm token apply must echo.
type ReviewResponse struct {
	Changed  bool          `json:"changed"`
	Added    int           `json:"added"`
	Removed  int           `json:"removed"`
	Lines    []DiffLineDTO `json:"lines"`
	Unified  string        `json:"unified"`
	Effects  []EffectDTO   `json:"effects"`
	Widening []EffectDTO   `json:"widening"`
	SHA      string        `json:"sha"`
	Token    string        `json:"token"`
}

// EffectDTO describes one change in words (mirrors configedit.Effect).
type EffectDTO struct {
	Path   string `json:"path"`
	Text   string `json:"text"`
	Widens bool   `json:"widens"`
}

// AppliedResponse is POST /api/v1/config/apply and /restore.
type AppliedResponse struct {
	SHA         string `json:"sha"`
	BackupID    string `json:"backup_id,omitempty"`
	ReloadError string `json:"reload_error,omitempty"`
	Unchanged   bool   `json:"unchanged,omitempty"`
}

// HistoryItem is one restorable backup (mirrors configedit.HistoryEntry).
type HistoryItem struct {
	ID    string `json:"id"`
	TS    string `json:"ts"`
	SHA   string `json:"sha"`
	Bytes int64  `json:"bytes"`
}

// StageRequest is POST /api/v1/config/stage.
type StageRequest struct {
	Path    []string `json:"path"`
	Value   any      `json:"value,omitempty"`
	Delete  bool     `json:"delete,omitempty"`
	Summary string   `json:"summary,omitempty"`
}

// ApplyRequest is POST /api/v1/config/apply. Token is the pipeline confirm token
// from the matching review (step-up, candidate-bound). StepupToken is the
// step-up token required ONLY when auth is configured AND the staged set
// widens a guardrail (auth.PurposeConfigApply); the step-up ceremony's digest is
// the pipeline confirm Token.
type ApplyRequest struct {
	Token       string `json:"token"`
	StepupToken string `json:"stepup_token,omitempty"`
}

// RestoreRequest is POST /api/v1/config/restore and /restore/review. StepupToken
// is the step-up token required when auth is configured (a restore
// re-writes the whole file, so it always needs step-up under auth); its ceremony
// digest is the restore confirm Token.
type RestoreRequest struct {
	ID          string `json:"id"`
	Token       string `json:"token,omitempty"`
	StepupToken string `json:"stepup_token,omitempty"`
}

// --- Section read models (design §5.4): one shape per screen, derived from the
// live config. Secret VALUES are never included — only names and where they are
// referenced.

// SecretsResponse is GET /api/v1/secrets: the vaults, the top-level secret refs,
// and, for each, where in the config it is used.
type SecretsResponse struct {
	Vaults  []VaultInfo  `json:"vaults"`
	Secrets []SecretInfo `json:"secrets"`
}

// VaultInfo is one named vault (values never rendered).
type VaultInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// SecretInfo is one top-level secret ref and its vault/key, with the config
// paths that reference it.
type SecretInfo struct {
	Name   string   `json:"name"`
	Ref    string   `json:"ref"` // <vault>/<key>
	UsedBy []string `json:"used_by,omitempty"`
}

// PacksResponse is GET /api/v1/packs.
type PacksResponse struct {
	Packs []PackInfo `json:"packs"`
}

// PackInfo is one installed pack instance.
type PackInfo struct {
	Name    string `json:"name"`
	Use     string `json:"use"`
	Version string `json:"version,omitempty"`
	Hold    bool   `json:"hold"`
}

// PluginsResponse is GET /api/v1/plugins: the flat plugin list and the version
// policy, derived from the `use:` references across connectors/runtimes.
type PluginsResponse struct {
	Plugins []PluginInfo `json:"plugins"`
}

// PluginInfo is one plugin referenced by a `use:` entry.
type PluginInfo struct {
	Name    string   `json:"name"`    // the plugin name (use ref, minus @version)
	Version string   `json:"version"` // the pinned version, or "" for floating
	Pinned  bool     `json:"pinned"`  // true when a @version is set (the version policy)
	Builtin bool     `json:"builtin"` // a conductor built-in, not fetched
	UsedBy  []string `json:"used_by"` // the connector/runtime names using it
}

// ConnectionsResponse is GET /api/v1/connections: connectors, runtimes and hosts
// as a flat list for the Connections screen.
type ConnectionsResponse struct {
	Connectors []NamedUse `json:"connectors"`
	Runtimes   []NamedUse `json:"runtimes"`
	Hosts      []HostInfo `json:"hosts"`
}

// NamedUse is a named connector/runtime and its `use:` reference.
type NamedUse struct {
	Name string `json:"name"`
	Use  string `json:"use"`
}

// HostInfo is one SSH host target.
type HostInfo struct {
	Name string `json:"name"`
	Host string `json:"host"`
	User string `json:"user,omitempty"`
}

// SystemResponse is GET /api/v1/system: the System screen's tabs — version and
// settings, the Safety posture (the live least-privilege guardrails), and the
// Updates block.
type SystemResponse struct {
	Version   string            `json:"version"`
	ConfigSHA string            `json:"config_sha"`
	Settings  map[string]string `json:"settings"`
	Safety    SafetyInfo        `json:"safety"`
	Update    UpdateInfo        `json:"update"`
}

// SafetyInfo summarizes the live security posture for the System → Safety tab.
type SafetyInfo struct {
	SandboxAgents   string   `json:"sandbox_agents"` // on | off | "" (default)
	Egress          []string `json:"egress"`         // named egress hosts (empty = default-deny)
	DashboardListen []string `json:"dashboard_listen"`
	Warnings        []string `json:"warnings"` // posture notes (e.g. "agents run unsandboxed")
}

// UpdateInfo is the System → Updates tab.
type UpdateInfo struct {
	Auto     bool   `json:"auto"`
	Interval string `json:"interval,omitempty"`
	Apply    string `json:"apply,omitempty"`
}

// AuditResponse is GET /api/v1/audit: recent audit-log entries, newest first.
type AuditResponse struct {
	Entries []map[string]any `json:"entries"`
}

// Error is one API error. Reads and writes that fail return {error: Error}.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// ErrorResponse is the body of any non-2xx JSON response.
type ErrorResponse struct {
	Error Error `json:"error"`
}
