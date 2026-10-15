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
