package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// The bot CASE LEDGER (docs/design/bots.md): every bot's durable open
// work — events routed in, proposals awaiting approval, workers running, the
// home session that owns the conversation, the inbox of messages not yet
// delivered to it, and the spend it has committed. It lives in bots.json
// beside affinity.json/plans.json: conductor's own state, always present, never
// a user stores: entry. Sessions are replaceable; this file is what a fresh
// session resumes from, so nothing here may depend on a live agent.
//
// Mutation is transactional per bot (UpdateBot): the callback edits a
// copy under the store lock and the result is persisted before the call
// returns, so a crash never leaves a half-applied change, and two goroutines
// working the same bot serialize.

// BotRecord is one bot's whole ledger.
type BotRecord struct {
	Name      string               `json:"name"`
	Cases     map[string]*Case     `json:"cases,omitempty"`
	Proposals map[string]*Proposal `json:"proposals,omitempty"`
	Workers   map[string]*Worker   `json:"workers,omitempty"`
	// Home is the live home session (nil before the first launch); Past keeps
	// the rotated-out ones, newest last, bounded.
	Home  *HomeSession   `json:"home,omitempty"`
	Past  []*HomeSession `json:"past,omitempty"`
	Inbox []*InboxMsg    `json:"inbox,omitempty"`
	Spend []SpendEntry   `json:"spend,omitempty"`
	// Sweeps are proactive sweeps (bounded, newest kept); Groups are worker
	// fan-outs a join waits on; Peer is the bot-to-bot message log
	// (metadata only — the text lives in the audit and the receiving case).
	Sweeps map[string]*Sweep `json:"sweeps,omitempty"`
	Groups map[string]*Group `json:"groups,omitempty"`
	Peer   []PeerMsg         `json:"peer,omitempty"`
	// Delegations are the work this bot handed to other bots — the
	// delegation edges (who handed what to whom), keyed by id. The
	// receiving side's case carries a DelegationRef back to its edge.
	Delegations map[string]*Delegation `json:"delegations,omitempty"`
	// Consolidated is when episodes were last distilled into knowledge.
	Consolidated time.Time `json:"consolidated,omitempty"`
	// ConsolidatedDocs are log entries a FINISHED consolidation covered, by
	// path, with the git blob each was consolidated at: an entry is
	// archivable only while it still holds exactly that content. Archived
	// entries leave the map.
	ConsolidatedDocs map[string]string `json:"consolidated_docs,omitempty"`
	// Template is a template bot's applied template state (nil for a bot
	// not instantiated from a template).
	Template *TemplateState `json:"template,omitempty"`
	// Seq numbers ids within this bot so they stay short and readable.
	Seq int `json:"seq"`
}

// NextID mints a bot-local id with the given prefix ("c", "p", "w", "s").
func (m *BotRecord) NextID(prefix string) string {
	m.Seq++
	return fmt.Sprintf("%s%d", prefix, m.Seq)
}

// Case is one unit of open work.
type Case struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"` // open | closed
	Urgent bool   `json:"urgent,omitempty"`
	Source string `json:"source"` // where it came from: "operator", "<conn>.<event>", "bot"
	// Requester is WHO asked, as the routing layer attributed it, and
	// RequesterClass what the gate makes of that: operator | event | user |
	// bot. Daemon-recorded, never agent-asserted.
	Requester      string    `json:"requester,omitempty"`
	RequesterClass string    `json:"requester_class"`
	Summary        string    `json:"summary,omitempty"`
	Outcome        string    `json:"outcome,omitempty"`
	Workers        []string  `json:"workers,omitempty"`
	Proposals      []string  `json:"proposals,omitempty"`
	Created        time.Time `json:"created"`
	Updated        time.Time `json:"updated"`
	// Key and Severity are set on a case a proactive finding opened: the
	// finding's dedupe key (a later sweep's finding with the same key notes
	// this case instead of opening another) and its highest severity.
	Key      string `json:"key,omitempty"`
	Severity string `json:"severity,omitempty"`
	// Peer is the bot whose message opened this case, and PeerCase the
	// sender's case — where a reply is routed.
	Peer     string `json:"peer,omitempty"`
	PeerCase string `json:"peer_case,omitempty"`
	// Team is the team a team-routed case came in through, and Routing how
	// it reached this bot (or, on a lead's triage case, where it went).
	// Payload keeps a triage case's event text for its assignee.
	Team    string   `json:"team,omitempty"`
	Routing *Routing `json:"routing,omitempty"`
	Payload string   `json:"payload,omitempty"`
	// Delegation is set on a case another bot delegated to this one: the
	// edge it came in on, and the ancestry the loop guard checks.
	Delegation *DelegationRef `json:"delegation,omitempty"`
}

// Delegation is one delegation edge, kept by the delegating bot: what it
// handed to whom, on which of its cases, at what provenance, and how it
// ended.
type Delegation struct {
	ID       string `json:"id"`
	From     string `json:"from"`
	FromCase string `json:"from_case"`
	To       string `json:"to"`
	ToCase   string `json:"to_case,omitempty"`
	Title    string `json:"title"`
	// Requester / RequesterClass are the provenance the receiving case was
	// opened with (before the recipient re-checks an operator).
	Requester      string `json:"requester,omitempty"`
	RequesterClass string `json:"requester_class"`
	// Depth is the number of delegation hops this edge ends, counting
	// itself; Chain the bots it passed through, the delegator last.
	Depth int      `json:"depth"`
	Chain []string `json:"chain"`
	// Status: open | done | declined.
	Status  string    `json:"status"`
	Result  string    `json:"result,omitempty"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// DelegationRef ties a delegated case to its edge in the delegator's
// ledger.
type DelegationRef struct {
	ID       string   `json:"id"`
	From     string   `json:"from"`
	FromCase string   `json:"from_case"`
	Depth    int      `json:"depth"`
	Chain    []string `json:"chain"`
}

// Routing is a team routing decision, as the case's bot records it.
type Routing struct {
	Team string `json:"team"`
	// Via is how the assignee was picked: rule | lead | fallback.
	Via  string `json:"via"`
	Rule string `json:"rule,omitempty"` // the matching rule's label
	// Lead and LeadCase are the triaging lead and its triage case.
	Lead     string `json:"lead,omitempty"`
	LeadCase string `json:"lead_case,omitempty"`
	// Assignee / AssigneeCase: who handles it, and the case it opened there.
	Assignee     string `json:"assignee,omitempty"`
	AssigneeCase string `json:"assignee_case,omitempty"`
	// State is a lead's triage case's: triage (unassigned) | assigned.
	State string `json:"state,omitempty"`
	// Reason says why it fell back; Note is the lead's triage note (data).
	Reason string    `json:"reason,omitempty"`
	Note   string    `json:"note,omitempty"`
	At     time.Time `json:"at"`
}

// Proposal is an action the gate held for operator approval.
type Proposal struct {
	ID     string `json:"id"`
	CaseID string `json:"case_id,omitempty"`
	// Kind is "verb" (conductor executes the verb on approval) or
	// "terraform" (conductor applies the saved plan it made, on approval).
	Kind string `json:"kind"`
	Tier string `json:"tier"`
	Uses string `json:"uses,omitempty"`
	// Terraform is a terraform proposal's pins and the plan conductor made.
	Terraform *TerraformPlan `json:"terraform,omitempty"`
	// Knowledge is a "knowledge" proposal: promoting a file of the bot's
	// memory into its team's knowledge repo.
	Knowledge *KnowledgeChange `json:"knowledge,omitempty"`
	// Template is a "template" proposal: a newer template's charter and
	// seed documents, as changes to the bot's memory repo.
	Template *TemplateUpdate `json:"template,omitempty"`
	// Team is the proposing bot's team, and Approvers the operators who
	// may decide it (the bot's; for a knowledge promotion, the team's) —
	// recorded for display; the daemon re-checks the config at decision.
	Team      string   `json:"team,omitempty"`
	Approvers []string `json:"approvers,omitempty"`
	// Options are the verb's options exactly as proposed (secrets redacted
	// before they land here — see bot.Gate).
	Options map[string]any `json:"options,omitempty"`
	Amount  float64        `json:"amount,omitempty"`
	Summary string         `json:"summary,omitempty"`
	// Digest is the SHA-256 of the canonical action; approvals sign it, so an
	// approval can never be replayed onto a different action.
	Digest string `json:"digest"`
	// Proposer is the session that made the call: its role, agent profile
	// and dispatch id.
	Role     string `json:"role"`
	Agent    string `json:"agent,omitempty"`
	Dispatch string `json:"dispatch,omitempty"`
	// Worker is the proposing worker (a child), when one proposed it — with
	// CaseID and Requester, the trace back to the originating request.
	Worker string `json:"worker,omitempty"`
	// Identity is the token-bound caller identity, kept daemon-side so the
	// approved verb executes under exactly the skill policy it was proposed
	// under.
	Identity json.RawMessage `json:"identity,omitempty"`
	// Requester/RequesterClass are copied from the case at proposal time.
	Requester      string `json:"requester,omitempty"`
	RequesterClass string `json:"requester_class"`
	// Status: planning | pending | shadow | refused | approved | denied |
	// executed | failed | expired.
	Status   string    `json:"status"`
	Reason   string    `json:"reason,omitempty"`
	Created  time.Time `json:"created"`
	Expires  time.Time `json:"expires"`
	Decided  time.Time `json:"decided,omitempty"`
	Approver string    `json:"approver,omitempty"`
	// Result is the executed verb's output, or the apply's.
	Result map[string]any `json:"result,omitempty"`
}

// TerraformPlan is a terraform proposal: what the bot pinned, and what
// conductor's own plan of it showed. The signed digest covers PlanSHA256.
type TerraformPlan struct {
	// Pinned by the proposer.
	Repo      string   `json:"repo"`                // a bot terraform.repos name
	Commit    string   `json:"commit"`              // the full commit id
	Dir       string   `json:"dir"`                 // root module, repo-relative ("." = root)
	Workspace string   `json:"workspace"`           // terraform workspace
	VarFiles  []string `json:"var_files,omitempty"` // relative to Dir
	Targets   []string `json:"targets,omitempty"`
	// Filled by conductor's plan.
	PlanSHA256 string    `json:"plan_sha256,omitempty"`
	Add        int       `json:"add"`
	Change     int       `json:"change"`
	Destroy    int       `json:"destroy"`
	Replaced   []string  `json:"replaced,omitempty"`  // addresses replaced (delete+create)
	Destroyed  []string  `json:"destroyed,omitempty"` // addresses deleted outright
	PlanText   string    `json:"plan_text,omitempty"` // `terraform show` of the plan, capped
	PlannedAt  time.Time `json:"planned_at,omitempty"`
	// ReplanOf names the proposal this one re-plans (a stale plan's).
	ReplanOf string `json:"replan_of,omitempty"`
}

// KnowledgeChange is a promotion of a bot's memory file into its team's
// knowledge repo: the text exactly as proposed (the digest covers its hash).
type KnowledgeChange struct {
	Team   string `json:"team"`
	From   string `json:"from"`             // the bot's memory path
	To     string `json:"to"`               // the team repo path
	SHA256 string `json:"sha256"`           // of Text
	Text   string `json:"text"`             // the content that lands
	Source string `json:"source,omitempty"` // the bot repo's commit it was read at
	Note   string `json:"note,omitempty"`
	Landed string `json:"landed,omitempty"` // the team repo commit, once it lands
	// EditedBy is the operator who approved it WITH CHANGES; Text/SHA256 are
	// then the operator's final content and ProposedSHA256 the bot's.
	EditedBy       string `json:"edited_by,omitempty"`
	ProposedSHA256 string `json:"proposed_sha256,omitempty"`
}

// TemplateState is what a template bot's memory took from its template:
// the template, the version last applied, and — per memory path — the
// SHA-256 of the template text applied there. A newer template's text that
// differs reaches the bot only as a proposal; a path whose memory content
// already equals the template's counts as applied.
type TemplateState struct {
	Ref     string            `json:"ref"` // <pack instance>/<template>
	Pack    string            `json:"pack"`
	Version string            `json:"version"`
	Applied map[string]string `json:"applied,omitempty"`
	// Since is when the bot was first seen from this template.
	Since time.Time `json:"since"`
	// Declined is the change-set digest an operator denied, so the same
	// changes are not proposed again (a newer template's are).
	Declined string `json:"declined,omitempty"`
}

// TemplateUpdate is a template proposal: each change is one memory path,
// with the template's new text and the memory content it was proposed
// against (landing refuses if the memory moved since).
type TemplateUpdate struct {
	Ref         string           `json:"ref"`
	Pack        string           `json:"pack"`
	FromVersion string           `json:"from_version,omitempty"`
	ToVersion   string           `json:"to_version"`
	Changes     []TemplateChange `json:"changes"`
	// Digest identifies the change set (what a denial remembers).
	Digest string `json:"digest"`
	// Requests are requested-verb changes in this version — shown, never
	// applied: a grant is the operator's config.
	Requests []string `json:"requests,omitempty"`
	Landed   string   `json:"landed,omitempty"` // the memory commit, once it lands
}

// TemplateChange is one memory path a template update would write.
type TemplateChange struct {
	Path   string `json:"path"`
	Action string `json:"action"` // new | update
	Text   string `json:"text"`   // the template's text
	SHA256 string `json:"sha256"` // of Text
	// Current is the memory content when proposed ("" for new) and its
	// SHA-256; Evolved is set when that content is not what the template
	// last gave (the bot or an operator changed it since).
	Current       string `json:"current,omitempty"`
	CurrentSHA256 string `json:"current_sha256,omitempty"`
	Evolved       bool   `json:"evolved,omitempty"`
}

// Worker is one worker launched for a case.
type Worker struct {
	ID        string    `json:"id"`
	CaseID    string    `json:"case_id"`
	Brief     string    `json:"brief"`
	Runtime   string    `json:"runtime,omitempty"`
	Handoff   bool      `json:"handoff,omitempty"` // a paseo hand-off worker (explicit opt-in)
	HistoryID string    `json:"history_id"`
	Dispatch  string    `json:"dispatch"`
	Cwd       string    `json:"cwd"`
	Status    string    `json:"status"` // starting | running | done | failed | exited
	Result    string    `json:"result,omitempty"`
	Started   time.Time `json:"started"`
	Ended     time.Time `json:"ended,omitempty"`
	// Output is the worker's step.done output as reported (bounded), what a
	// join hands back. Group is the fan-out it belongs to; Sweep the
	// proactive sweep it runs (then CaseID is empty).
	Output json.RawMessage `json:"output,omitempty"`
	Group  string          `json:"group,omitempty"`
	Sweep  string          `json:"sweep,omitempty"`
	// The parent → child tree and the child's authority ceiling.
	// Parent is who started it ("home:<session>", or "conductor" for an
	// urgent event's, a consolidation's or a sweep's worker).
	// Requester/RequesterClass are its INHERITED provenance: the least
	// trusted of its case's requester and its parent's own trust when it was
	// spawned — what the gate holds every call it makes to. Verbs (when set)
	// and ReadOnly only ever narrow what the bot's policy allows.
	Parent         string        `json:"parent,omitempty"`
	Requester      string        `json:"requester,omitempty"`
	RequesterClass string        `json:"requester_class,omitempty"`
	Verbs          []string      `json:"verbs,omitempty"`
	ReadOnly       bool          `json:"read_only,omitempty"`
	Instructions   []Instruction `json:"instructions,omitempty"`
	// Consolidates are the log entries a consolidation worker was given
	// (path → git blob): they count as consolidated only once it finishes
	// done having written a digest.
	Consolidates map[string]string `json:"consolidates,omitempty"`
}

// Instruction is a follow-up from a worker's parent: an instruction, not
// data. Read is when the worker received it.
type Instruction struct {
	ID   string    `json:"id"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
	Read time.Time `json:"read,omitempty"`
}

// Group is a fan-out: workers started on one case under one label, joined
// together.
type Group struct {
	ID      string    `json:"id"`
	CaseID  string    `json:"case_id"`
	Label   string    `json:"label"`
	Workers []string  `json:"workers"`
	Created time.Time `json:"created"`
	// Reported is set once the combined report reached the home inbox.
	Reported bool `json:"reported,omitempty"`
}

// Sweep is one proactive sweep.
type Sweep struct {
	ID        string    `json:"id"`
	Worker    string    `json:"worker"`
	HistoryID string    `json:"history_id"`
	Status    string    `json:"status"` // running | quiet | findings | failed | exited | interrupted
	Summary   string    `json:"summary,omitempty"`
	Findings  []Finding `json:"findings,omitempty"`
	CostUSD   float64   `json:"cost_usd,omitempty"`
	Tokens    int       `json:"tokens,omitempty"`
	Turns     int       `json:"turns,omitempty"`
	// Usage is the same per runtime and model (for display; budgets count
	// the totals above).
	Usage   []ModelUsage `json:"usage,omitempty"`
	Started time.Time    `json:"started"`
	Ended   time.Time    `json:"ended,omitempty"`
}

// Finding is one thing a sweep reported.
type Finding struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Severity string `json:"severity"`
	Detail   string `json:"detail,omitempty"`
	// CaseID is the case it opened or matched (""= below the notify
	// threshold and new: recorded only). Notified: the operator was told.
	CaseID   string `json:"case_id,omitempty"`
	Notified bool   `json:"notified,omitempty"`
}

// PeerMsg is one bot-to-bot message, as one side recorded it.
type PeerMsg struct {
	ID     string    `json:"id"`
	Dir    string    `json:"dir"` // out | in
	Peer   string    `json:"peer"`
	CaseID string    `json:"case_id,omitempty"` // this side's case
	Bytes  int       `json:"bytes"`
	At     time.Time `json:"at"`
}

// HomeSession is one home agent session.
type HomeSession struct {
	ID       string    `json:"id"`
	AgentID  string    `json:"agent_id"`
	Dispatch string    `json:"dispatch"`
	Cwd      string    `json:"cwd"`
	Started  time.Time `json:"started"`
	Turns    int       `json:"turns"`
	Ended    time.Time `json:"ended,omitempty"`
	// Handoff is the memory-repo path of the handoff note this session
	// started from ("" for the first session).
	Handoff string `json:"handoff,omitempty"`
}

// InboxMsg is one message waiting to reach (or delivered to) the home agent.
type InboxMsg struct {
	ID        string    `json:"id"`
	CaseID    string    `json:"case_id,omitempty"`
	Text      string    `json:"text"`
	Created   time.Time `json:"created"`
	Delivered time.Time `json:"delivered,omitempty"`
	Attempts  int       `json:"attempts,omitempty"`
}

// SpendEntry is one executed commit-money action.
type SpendEntry struct {
	At       time.Time `json:"at"`
	Amount   float64   `json:"amount"`
	Proposal string    `json:"proposal"`
}

func (s *Store) botsPath() string {
	if s.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.path), "bots.json")
}

// loadBots reads bots.json at Open. A file that does not parse is NOT
// silently replaced: it is moved aside (bots.json.corrupt-<unix>) and the
// ledger is flagged degraded, which the gate reads to refuse commit-money
// approvals — a spend history that vanished must not reset a spend cap.
func (s *Store) loadBots() {
	s.bots = map[string]*BotRecord{}
	p := s.botsPath()
	if p == "" {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	if err := json.Unmarshal(b, &s.bots); err != nil || s.bots == nil {
		_ = os.Rename(p, fmt.Sprintf("%s.corrupt-%d", p, time.Now().Unix()))
		s.bots = map[string]*BotRecord{}
		s.botsDegraded = true
	}
}

// BotsDegraded reports whether bots.json was unreadable at boot.
func (s *Store) BotsDegraded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.botsDegraded
}

// Bot returns a deep copy of one bot's ledger (an empty record for a
// bot never seen).
func (s *Store) Bot(name string) BotRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.bots[name]
	if r == nil {
		return BotRecord{Name: name}
	}
	return *cloneBot(r)
}

// BotNames lists every bot with a ledger, sorted.
func (s *Store) BotNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.bots))
	for n := range s.bots {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// UpdateBot applies fn to a copy of the bot's ledger and, when fn
// succeeds, installs and persists it. fn's error aborts with nothing changed.
func (s *Store) UpdateBot(name string, fn func(*BotRecord) error) error {
	// The bot's own lock serializes read-modify-write per bot (fn may
	// read what a concurrent update just wrote); persist orders the saves.
	ml := s.botLock(name)
	ml.Lock()
	defer ml.Unlock()
	s.mu.Lock()
	cur := s.bots[name]
	var work *BotRecord
	if cur == nil {
		work = &BotRecord{Name: name}
	} else {
		work = cloneBot(cur)
	}
	if err := fn(work); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.bots == nil {
		s.bots = map[string]*BotRecord{}
	}
	s.bots[name] = work
	s.mu.Unlock()
	return s.saveBots()
}

// saveBots persists the ledger through the store's one write chokepoint.
func (s *Store) saveBots() error {
	return s.persist(func() ([]byte, string, error) {
		b, err := json.MarshalIndent(s.bots, "", " ")
		return b, s.botsPath(), err
	})
}

func (s *Store) botLock(name string) *sync.Mutex {
	s.botLocksMu.Lock()
	defer s.botLocksMu.Unlock()
	if s.botLocks == nil {
		s.botLocks = map[string]*sync.Mutex{}
	}
	l := s.botLocks[name]
	if l == nil {
		l = &sync.Mutex{}
		s.botLocks[name] = l
	}
	return l
}

// cloneBot deep-copies via JSON: the ledger is plain data, and a copy that
// shares a map with the stored record would let a reader mutate state outside
// a transaction.
func cloneBot(r *BotRecord) *BotRecord {
	b, err := json.Marshal(r)
	if err != nil {
		return &BotRecord{Name: r.Name}
	}
	var out BotRecord
	if json.Unmarshal(b, &out) != nil {
		return &BotRecord{Name: r.Name}
	}
	return &out
}

// Clone deep-copies a proposal.
func (p *Proposal) Clone() Proposal {
	b, _ := json.Marshal(p)
	var out Proposal
	_ = json.Unmarshal(b, &out)
	return out
}
