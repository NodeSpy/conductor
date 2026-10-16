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

// The member CASE LEDGER (docs/design/members.md): every member's durable open
// work — events routed in, proposals awaiting approval, workers running, the
// home session that owns the conversation, the inbox of messages not yet
// delivered to it, and the spend it has committed. It lives in members.json
// beside affinity.json/plans.json: conductor's own state, always present, never
// a user stores: entry. Sessions are replaceable; this file is what a fresh
// session resumes from, so nothing here may depend on a live agent.
//
// Mutation is transactional per member (UpdateMember): the callback edits a
// copy under the store lock and the result is persisted before the call
// returns, so a crash never leaves a half-applied change, and two goroutines
// working the same member serialize.

// MemberRecord is one member's whole ledger.
type MemberRecord struct {
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
	// fan-outs a join waits on; Peer is the member-to-member message log
	// (metadata only — the text lives in the audit and the receiving case).
	Sweeps map[string]*Sweep `json:"sweeps,omitempty"`
	Groups map[string]*Group `json:"groups,omitempty"`
	Peer   []PeerMsg         `json:"peer,omitempty"`
	// Consolidated is when episodes were last distilled into knowledge.
	Consolidated time.Time `json:"consolidated,omitempty"`
	// Seq numbers ids within this member so they stay short and readable.
	Seq int `json:"seq"`
}

// NextID mints a member-local id with the given prefix ("c", "p", "w", "s").
func (m *MemberRecord) NextID(prefix string) string {
	m.Seq++
	return fmt.Sprintf("%s%d", prefix, m.Seq)
}

// Case is one unit of open work.
type Case struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"` // open | closed
	Urgent bool   `json:"urgent,omitempty"`
	Source string `json:"source"` // where it came from: "operator", "<conn>.<event>", "member"
	// Requester is WHO asked, as the routing layer attributed it, and
	// RequesterClass what the gate makes of that: operator | event | user |
	// member. Daemon-recorded, never agent-asserted.
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
	// Peer is the member whose message opened this case, and PeerCase the
	// sender's case — where a reply is routed.
	Peer     string `json:"peer,omitempty"`
	PeerCase string `json:"peer_case,omitempty"`
	// Team is the team a team-routed case came in through, and Routing how
	// it reached this member (or, on a lead's triage case, where it went).
	// Payload keeps a triage case's event text for its assignee.
	Team    string   `json:"team,omitempty"`
	Routing *Routing `json:"routing,omitempty"`
	Payload string   `json:"payload,omitempty"`
}

// Routing is a team routing decision, as the case's member records it.
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
	// Knowledge is a "knowledge" proposal: promoting a file of the member's
	// memory into its team's knowledge repo.
	Knowledge *KnowledgeChange `json:"knowledge,omitempty"`
	// Team is the proposing member's team, and Approvers the operators who
	// may decide it (the member's; for a knowledge promotion, the team's) —
	// recorded for display; the daemon re-checks the config at decision.
	Team      string   `json:"team,omitempty"`
	Approvers []string `json:"approvers,omitempty"`
	// Options are the verb's options exactly as proposed (secrets redacted
	// before they land here — see member.Gate).
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

// TerraformPlan is a terraform proposal: what the member pinned, and what
// conductor's own plan of it showed. The signed digest covers PlanSHA256.
type TerraformPlan struct {
	// Pinned by the proposer.
	Repo      string   `json:"repo"`                // a member terraform.repos name
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

// KnowledgeChange is a promotion of a member's memory file into its team's
// knowledge repo: the text exactly as proposed (the digest covers its hash).
type KnowledgeChange struct {
	Team   string `json:"team"`
	From   string `json:"from"`             // the member's memory path
	To     string `json:"to"`               // the team repo path
	SHA256 string `json:"sha256"`           // of Text
	Text   string `json:"text"`             // the content that lands
	Source string `json:"source,omitempty"` // the member repo's commit it was read at
	Note   string `json:"note,omitempty"`
	Landed string `json:"landed,omitempty"` // the team repo commit, once it lands
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
	// and ReadOnly only ever narrow what the member's policy allows.
	Parent         string        `json:"parent,omitempty"`
	Requester      string        `json:"requester,omitempty"`
	RequesterClass string        `json:"requester_class,omitempty"`
	Verbs          []string      `json:"verbs,omitempty"`
	ReadOnly       bool          `json:"read_only,omitempty"`
	Instructions   []Instruction `json:"instructions,omitempty"`
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
	Started   time.Time `json:"started"`
	Ended     time.Time `json:"ended,omitempty"`
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

// PeerMsg is one member-to-member message, as one side recorded it.
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

func (s *Store) membersPath() string {
	if s.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.path), "members.json")
}

// loadMembers reads members.json at Open. A file that does not parse is NOT
// silently replaced: it is moved aside (members.json.corrupt-<unix>) and the
// ledger is flagged degraded, which the gate reads to refuse commit-money
// approvals — a spend history that vanished must not reset a spend cap.
func (s *Store) loadMembers() {
	s.members = map[string]*MemberRecord{}
	p := s.membersPath()
	if p == "" {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	if err := json.Unmarshal(b, &s.members); err != nil || s.members == nil {
		_ = os.Rename(p, fmt.Sprintf("%s.corrupt-%d", p, time.Now().Unix()))
		s.members = map[string]*MemberRecord{}
		s.membersDegraded = true
	}
}

// MembersDegraded reports whether members.json was unreadable at boot.
func (s *Store) MembersDegraded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.membersDegraded
}

// Member returns a deep copy of one member's ledger (an empty record for a
// member never seen).
func (s *Store) Member(name string) MemberRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.members[name]
	if r == nil {
		return MemberRecord{Name: name}
	}
	return *cloneMember(r)
}

// MemberNames lists every member with a ledger, sorted.
func (s *Store) MemberNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.members))
	for n := range s.members {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// UpdateMember applies fn to a copy of the member's ledger and, when fn
// succeeds, installs and persists it. fn's error aborts with nothing changed.
func (s *Store) UpdateMember(name string, fn func(*MemberRecord) error) error {
	// The member's own lock serializes read-modify-write per member (fn may
	// read what a concurrent update just wrote); persist orders the saves.
	ml := s.memberLock(name)
	ml.Lock()
	defer ml.Unlock()
	s.mu.Lock()
	cur := s.members[name]
	var work *MemberRecord
	if cur == nil {
		work = &MemberRecord{Name: name}
	} else {
		work = cloneMember(cur)
	}
	if err := fn(work); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.members == nil {
		s.members = map[string]*MemberRecord{}
	}
	s.members[name] = work
	s.mu.Unlock()
	return s.saveMembers()
}

// saveMembers persists the ledger through the store's one write chokepoint.
func (s *Store) saveMembers() error {
	return s.persist(func() ([]byte, string, error) {
		b, err := json.MarshalIndent(s.members, "", " ")
		return b, s.membersPath(), err
	})
}

func (s *Store) memberLock(name string) *sync.Mutex {
	s.memberLocksMu.Lock()
	defer s.memberLocksMu.Unlock()
	if s.memberLocks == nil {
		s.memberLocks = map[string]*sync.Mutex{}
	}
	l := s.memberLocks[name]
	if l == nil {
		l = &sync.Mutex{}
		s.memberLocks[name] = l
	}
	return l
}

// cloneMember deep-copies via JSON: the ledger is plain data, and a copy that
// shares a map with the stored record would let a reader mutate state outside
// a transaction.
func cloneMember(r *MemberRecord) *MemberRecord {
	b, err := json.Marshal(r)
	if err != nil {
		return &MemberRecord{Name: r.Name}
	}
	var out MemberRecord
	if json.Unmarshal(b, &out) != nil {
		return &MemberRecord{Name: r.Name}
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
