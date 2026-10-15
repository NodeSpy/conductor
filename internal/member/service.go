package member

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/store"
)

// THE MEMBER SERVICE: the daemon-side half of every member. It owns the
// lifecycle the agents can't — launching and rotating the home session,
// delivering the inbox into it, routing events into cases, starting workers
// and recording what they report — and serves the agent-facing ops
// (`conductor member …` from a member session) and the operator ones.
//
// Launch seams: the HOME session goes through HomeLauncher (the paseo
// dispatcher today; `--provider conductor` with the jail's cwd→identity
// lookup once #154 lands). WORKERS go through WorkerLauncher, which fires the
// member's synthesized `on: manual` trigger — the ordinary engine path to the
// cli runtime. Neither is a parallel launcher.

// HomeLauncher starts and drives home sessions.
type HomeLauncher interface {
	// Launch starts a home agent in spec.Cwd and returns its agent id.
	Launch(ctx context.Context, spec HomeSpec) (string, error)
	// Send delivers one message and returns when the turn is done.
	Send(ctx context.Context, member, agentID, text string) error
	// State is "running", "idle", … or "gone".
	State(ctx context.Context, member, agentID string) (string, error)
	// Retire archives an agent conductor launched, revokes its session, and
	// (jailed) ends every ACP session opened in its cwd.
	Retire(ctx context.Context, member, agentID, dispatchID, cwd string) error
}

// HomeSpec is one home launch.
type HomeSpec struct {
	Member, Session, Dispatch, Cwd, Prompt string
}

// WorkerLauncher fires a member worker run.
type WorkerLauncher interface {
	Fire(ctx context.Context, trigger, historyID string, inputs map[string]any) error
}

// Actor is the token-bound identity of an agent calling a member op.
type Actor struct {
	Member, Role, CaseID, Dispatch, Agent string
}

// Event is something routed into a member.
type Event struct {
	Member    string
	Source    string // "<connector>.<event>", "operator", "schedule"
	Requester string // who asked, as the event names them ("" = no human)
	Title     string
	Text      string // the payload (data)
	Urgent    bool
}

// Service is the member service.
type Service struct {
	Cfg     func() *config.Config
	Ledger  Ledger
	Gate    *Gate
	Exec    *Executor
	Home    HomeLauncher
	Workers WorkerLauncher
	// SessionRoot is where per-session worktrees live (each live agent its
	// own cwd). Default ~/.conductor/runs, which conductor's paseo project
	// prune already covers.
	SessionRoot string
	Log         func(string, ...any)
	Audit       func(map[string]any)
	Now         func() time.Time
	// PollEvery paces the delivery loop (default 5s); SendTimeout bounds one
	// delivered turn (default 30m); HandoffTimeout bounds the old session's
	// handoff turn at rotation (default 10m).
	PollEvery, SendTimeout, HandoffTimeout time.Duration

	mu       sync.Mutex
	failedAt map[string]time.Time // last failed home launch, per member (backoff)
	repos    map[string]*Repo
	locks    map[string]*sync.Mutex
	wake     map[string]chan struct{}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) logf(f string, a ...any) {
	if s.Log != nil {
		s.Log("member: "+f, a...)
	}
}

func (s *Service) audit(e map[string]any) {
	if s.Audit != nil {
		s.Audit(e)
	}
}

// Repo returns a member's memory repo handle.
func (s *Service) Repo(name string) *Repo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.repos == nil {
		s.repos = map[string]*Repo{}
	}
	r := s.repos[name]
	if r == nil {
		r = &Repo{Dir: s.Cfg().MemberRepoDir(name), Member: name, Now: s.Now, Seed: s.Cfg().MemberSeedDir(name)}
		s.repos[name] = r
	}
	return r
}

// lock serializes lifecycle work (launch, rotate, deliver) per member.
func (s *Service) lock(name string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locks == nil {
		s.locks = map[string]*sync.Mutex{}
	}
	l := s.locks[name]
	if l == nil {
		l = &sync.Mutex{}
		s.locks[name] = l
	}
	return l
}

func (s *Service) wakeCh(name string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wake == nil {
		s.wake = map[string]chan struct{}{}
	}
	c := s.wake[name]
	if c == nil {
		c = make(chan struct{}, 1)
		s.wake[name] = c
	}
	return c
}

// Kick wakes a member's delivery loop.
func (s *Service) Kick(name string) {
	select {
	case s.wakeCh(name) <- struct{}{}:
	default:
	}
}

func (s *Service) sessionRoot() string {
	if s.SessionRoot != "" {
		return s.SessionRoot
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".conductor", "runs")
}

func (s *Service) sessionDir(member, id string) string {
	return filepath.Join(s.sessionRoot(), "member-"+member+"-"+id)
}

func (s *Service) member(name string) (config.MemberConfig, error) {
	m, ok := s.Cfg().Members[name]
	if !ok || m.Disabled {
		return m, fmt.Errorf("unknown member %q", name)
	}
	return m, nil
}

// Start initializes every member and runs its loop until ctx ends.
func (s *Service) Start(ctx context.Context) {
	for _, name := range s.Cfg().MemberNames() {
		if err := s.Repo(name).Init(ctx); err != nil {
			s.logf("%s: memory repo: %v", name, err)
			continue
		}
		s.recoverWorkers(name)
		go s.loop(ctx, name)
	}
}

// recoverWorkers: a cli worker is a child of the daemon, so a restart ended
// it. Say so in the ledger and the inbox rather than leave it "running".
func (s *Service) recoverWorkers(name string) {
	var lost []store.Worker
	_ = s.Ledger.UpdateMember(name, func(r *store.MemberRecord) error {
		for _, w := range r.Workers {
			if (w.Status == "starting" || w.Status == "running") && !w.Handoff {
				w.Status, w.Ended, w.Result = "interrupted", s.now(), "the daemon restarted while this worker ran"
				lost = append(lost, *w)
			}
		}
		return nil
	})
	for _, w := range lost {
		_ = s.Repo(name).RemoveWorktree(context.Background(), w.Cwd)
		s.enqueue(name, w.CaseID, fmt.Sprintf("[conductor] worker %s (case %s) was interrupted by a daemon restart before it reported. Respawn it if the work is still needed.", w.ID, w.CaseID))
	}
}

func (s *Service) loop(ctx context.Context, name string) {
	every := s.PollEvery
	if every <= 0 {
		every = 5 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		s.Tick(ctx, name)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wakeCh(name):
		}
	}
}

// Tick is one pass of a member's loop: keep the home session alive and
// fresh, expire stale proposals, deliver the inbox.
func (s *Service) Tick(ctx context.Context, name string) {
	m, err := s.member(name)
	if err != nil || s.Home == nil {
		return
	}
	if s.Exec != nil {
		s.Exec.ExpireProposals(name)
	}
	rec := s.Ledger.Member(name)
	switch {
	case rec.Home == nil:
		if !s.launchDue(name) {
			return
		}
		if err := s.Rotate(ctx, name, "first session"); err != nil {
			s.launchFailed(name)
			s.logf("%s: start home session: %v (retrying in %s)", name, err, LaunchBackoff)
			return
		}
	default:
		st, err := s.Home.State(ctx, name, rec.Home.AgentID)
		if err != nil {
			s.logf("%s: home state: %v", name, err)
			return
		}
		due := ""
		switch {
		case st == "gone":
			due = "the home session is gone"
		case s.now().Sub(rec.Home.Started) >= m.Home.RotateAge():
			due = "session age limit"
		case rec.Home.Turns >= m.Home.RotateTurns():
			due = "session turn limit"
		}
		if due != "" && (st == "gone" || st != "running") && s.launchDue(name) {
			if err := s.Rotate(ctx, name, due); err != nil {
				s.launchFailed(name)
				s.logf("%s: rotate (%s): %v (retrying in %s)", name, due, err, LaunchBackoff)
				return
			}
		}
	}
	s.deliver(ctx, name)
}

// LaunchBackoff spaces out home-session launch attempts after a failure, so
// a misconfiguration does not become a launch every tick.
const LaunchBackoff = time.Minute

func (s *Service) launchDue(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.failedAt[name]
	return !ok || s.now().Sub(t) >= LaunchBackoff
}

func (s *Service) launchFailed(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failedAt == nil {
		s.failedAt = map[string]time.Time{}
	}
	s.failedAt[name] = s.now()
}

// enqueue adds a message to the home inbox (durable) and wakes delivery.
func (s *Service) enqueue(name, caseID, text string) {
	_ = s.Ledger.UpdateMember(name, func(r *store.MemberRecord) error {
		r.Inbox = append(r.Inbox, &store.InboxMsg{ID: r.NextID("m"), CaseID: caseID, Text: text, Created: s.now()})
		// Keep delivered history bounded.
		if n := len(r.Inbox); n > 200 {
			keep := r.Inbox[:0]
			drop := n - 200
			for _, m := range r.Inbox {
				if drop > 0 && !m.Delivered.IsZero() {
					drop--
					continue
				}
				keep = append(keep, m)
			}
			r.Inbox = keep
		}
		return nil
	})
	s.Kick(name)
}

// Say queues a message from the operator side into the home conversation
// (the operator can equally just type into paseo).
func (s *Service) Say(name, text string) error {
	if _, err := s.member(name); err != nil {
		return err
	}
	s.enqueue(name, "", "[conductor: message from your operator]\n"+text)
	return nil
}

// deliver sends every pending inbox message as one turn, when the home
// agent is idle. A busy agent (the operator mid-conversation) is never
// interrupted: delivery waits for the next idle moment.
func (s *Service) deliver(ctx context.Context, name string) {
	l := s.lock(name)
	l.Lock()
	defer l.Unlock()
	rec := s.Ledger.Member(name)
	if rec.Home == nil {
		return
	}
	var ids []string
	var parts []string
	for _, m := range rec.Inbox {
		if m.Delivered.IsZero() {
			ids = append(ids, m.ID)
			parts = append(parts, m.Text)
		}
	}
	if len(ids) == 0 {
		return
	}
	st, err := s.Home.State(ctx, name, rec.Home.AgentID)
	if err != nil || st == "running" || st == "gone" {
		return
	}
	text := strings.Join(parts, "\n\n")
	if len(parts) > 1 {
		text = fmt.Sprintf("[conductor: %d messages arrived]\n\n", len(parts)) + text
	}
	to := s.SendTimeout
	if to <= 0 {
		to = 30 * time.Minute
	}
	sctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	err = s.Home.Send(sctx, name, rec.Home.AgentID, text)
	_ = s.Ledger.UpdateMember(name, func(r *store.MemberRecord) error {
		want := map[string]bool{}
		for _, id := range ids {
			want[id] = true
		}
		for _, m := range r.Inbox {
			if want[m.ID] {
				if err == nil {
					m.Delivered = s.now()
				} else {
					m.Attempts++
				}
			}
		}
		if err == nil && r.Home != nil {
			r.Home.Turns++
		}
		return nil
	})
	if err != nil {
		s.logf("%s: deliver to home: %v", name, err)
	}
}

// Rotate replaces the home session: ask the old one for a handoff note (or
// write one from the ledger), start a fresh session in its OWN new worktree
// from that note and the ledger, then retire the old one. A launch failure
// leaves the old session in place.
func (s *Service) Rotate(ctx context.Context, name, reason string) error {
	if _, err := s.member(name); err != nil {
		return err
	}
	if s.Home == nil {
		return errors.New("no home launcher (paseo) on this daemon")
	}
	l := s.lock(name)
	l.Lock()
	defer l.Unlock()
	repo := s.Repo(name)
	if err := repo.Init(ctx); err != nil {
		return err
	}
	rec := s.Ledger.Member(name)
	old := rec.Home
	if old != nil {
		s.collectHandoff(ctx, name, old, reason)
		rec = s.Ledger.Member(name)
	}
	hPath, hText := repo.LatestHandoff()
	var sid string
	_ = s.Ledger.UpdateMember(name, func(r *store.MemberRecord) error { sid = r.NextID("s"); return nil })
	cwd := s.sessionDir(name, sid)
	if err := repo.AddWorktree(ctx, cwd); err != nil {
		return fmt.Errorf("session worktree: %w", err)
	}
	dispatchID := fmt.Sprintf("member:%s:home:%s:%s", name, sid, strconv.FormatInt(s.now().UnixNano(), 36))
	prompt := HomePrompt(s.Cfg(), name, rec, hPath, hText)
	agentID, err := s.Home.Launch(ctx, HomeSpec{Member: name, Session: sid, Dispatch: dispatchID, Cwd: cwd, Prompt: prompt})
	if err != nil {
		_ = repo.RemoveWorktree(ctx, cwd)
		return fmt.Errorf("launch home session: %w", err)
	}
	now := s.now()
	_ = s.Ledger.UpdateMember(name, func(r *store.MemberRecord) error {
		if r.Home != nil {
			r.Home.Ended = now
			r.Past = append(r.Past, r.Home)
			if len(r.Past) > 50 {
				r.Past = r.Past[len(r.Past)-50:]
			}
		}
		r.Home = &store.HomeSession{ID: sid, AgentID: agentID, Dispatch: dispatchID, Cwd: cwd, Started: now, Handoff: hPath}
		return nil
	})
	s.audit(map[string]any{"event": "member_session", "member": name, "session": sid, "agent": agentID, "reason": reason, "handoff": hPath})
	s.logf("%s: home session %s started (agent %s): %s", name, sid, agentID, reason)
	if old != nil {
		if err := s.Home.Retire(ctx, name, old.AgentID, old.Dispatch, old.Cwd); err != nil {
			s.logf("%s: retire session %s: %v", name, old.ID, err)
		}
		if err := repo.RemoveWorktree(ctx, old.Cwd); err != nil {
			s.logf("%s: remove session worktree %s: %v", name, old.Cwd, err)
		}
	}
	return nil
}

// collectHandoff makes sure a handoff note exists for the outgoing session:
// asked of the session itself when it is alive, written by conductor from
// the ledger when it is not (or does not answer).
func (s *Service) collectHandoff(ctx context.Context, name string, old *store.HomeSession, reason string) {
	repo := s.Repo(name)
	before, _ := repo.LatestHandoff()
	st, err := s.Home.State(ctx, name, old.AgentID)
	if err == nil && st != "gone" {
		to := s.HandoffTimeout
		if to <= 0 {
			to = 10 * time.Minute
		}
		hctx, cancel := context.WithTimeout(ctx, to)
		defer cancel()
		msg := fmt.Sprintf("[conductor] Your session is being rotated (%s). Write your handoff NOW so a fresh session can continue with no lost context: what you are in the middle of, what you promised whom, what to watch for, and anything you learned that is not yet in knowledge/. Run:\n  conductor member handoff --text \"…\"\nThen reply \"handoff written\".", reason)
		if err := s.Home.Send(hctx, name, old.AgentID, msg); err != nil {
			s.logf("%s: handoff request: %v", name, err)
		}
		// A send returns when the agent STOPS — which includes stopping on a
		// permission prompt mid-turn, not only finishing. Wait for the turn
		// to actually settle before deciding whether a note was written.
		s.waitSettled(hctx, name, old.AgentID)
	}
	if after, _ := repo.LatestHandoff(); after != "" && after != before {
		return
	}
	note := MechanicalHandoff(s.Ledger.Member(name), reason)
	if _, err := repo.Write(ctx, repo.HandoffPath(old.ID), note, "replace", "handoff: session "+old.ID+" (written by conductor)", map[string]string{"Member-Session": old.ID}); err != nil {
		s.logf("%s: mechanical handoff: %v", name, err)
	}
}

// waitSettled blocks until an agent is idle (or gone, or errored) or ctx
// ends.
func (s *Service) waitSettled(ctx context.Context, name, agentID string) {
	every := s.PollEvery
	if every <= 0 || every > 2*time.Second {
		every = 2 * time.Second
	}
	for {
		st, err := s.Home.State(ctx, name, agentID)
		if err == nil && (st == "idle" || st == "gone" || st == "error" || st == "closed") {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// ---- routing ----

// RequesterClass classifies who asked.
func RequesterClass(cfg *config.Config, member, requester string) (string, string) {
	// Matching is EXACT: a requester string that only resembles an operator's
	// name or alias (padding, case, a prefix) is a third party.
	r := requester
	if strings.TrimSpace(r) == "" {
		return "", ReqEvent
	}
	for _, op := range cfg.MemberOperators(member) {
		if r == op || r == "operator:"+op {
			return op, ReqOperator
		}
		for _, a := range cfg.Operators[op].Aliases {
			if r == a {
				return op, ReqOperator
			}
		}
	}
	return r, ReqUser
}

// Deliver routes an event into a member: a case, an inbox message, and —
// for an urgent event — a worker started immediately, so urgent work never
// waits behind the home conversation.
func (s *Service) Deliver(ctx context.Context, ev Event) (string, error) {
	if _, err := s.member(ev.Member); err != nil {
		return "", err
	}
	cfg := s.Cfg()
	who, class := RequesterClass(cfg, ev.Member, ev.Requester)
	title := oneLine(ev.Title, 200)
	if title == "" {
		title = oneLine(ev.Text, 120)
	}
	if title == "" {
		title = "event from " + ev.Source
	}
	src := ev.Source
	if src == "" {
		src = "unknown"
	}
	now := s.now()
	var c store.Case
	err := s.Ledger.UpdateMember(ev.Member, func(r *store.MemberRecord) error {
		id := r.NextID("c")
		if r.Cases == nil {
			r.Cases = map[string]*store.Case{}
		}
		cc := &store.Case{ID: id, Title: title, Status: "open", Urgent: ev.Urgent, Source: src,
			Requester: who, RequesterClass: class, Created: now, Updated: now}
		r.Cases[id] = cc
		c = *cc
		return nil
	})
	if err != nil {
		return "", err
	}
	s.audit(map[string]any{"event": "member_event", "member": ev.Member, "case": c.ID, "source": src, "requester": who, "requester_class": class, "urgent": ev.Urgent})
	note := ""
	if ev.Urgent {
		brief := "URGENT event — triage it now. Investigate, act within your autonomy, propose anything beyond it, and escalate per your charter if needed. Report what you found and did.\n\n" + EventMessage(c, ev.Text, "")
		w, err := s.Spawn(ctx, Actor{Member: ev.Member, Role: config.MemberRoleHome}, SpawnRequest{CaseID: c.ID, Brief: brief})
		if err != nil {
			note = "(conductor could not start a worker for this urgent event: " + err.Error() + " — handle it yourself.)"
		} else {
			note = fmt.Sprintf("(Urgent: conductor already started worker %s on this case; it will report here.)", w.ID)
		}
	}
	s.enqueue(ev.Member, c.ID, EventMessage(c, ev.Text, note))
	return c.ID, nil
}

// ---- workers ----

// SpawnRequest is a worker launch.
type SpawnRequest struct {
	CaseID  string
	Title   string // for a new case when CaseID is empty
	Brief   string
	Handoff bool // a paseo hand-off worker (explicit opt-in)
}

// Spawn starts a worker for a case. Only a home session (or conductor
// itself) spawns; a worker cannot start more workers.
func (s *Service) Spawn(ctx context.Context, c Actor, req SpawnRequest) (store.Worker, error) {
	m, err := s.member(c.Member)
	if err != nil {
		return store.Worker{}, err
	}
	if c.Role != config.MemberRoleHome {
		return store.Worker{}, errors.New("only the home session starts workers")
	}
	if strings.TrimSpace(req.Brief) == "" {
		return store.Worker{}, errors.New("a worker needs a brief")
	}
	if s.Workers == nil {
		return store.Worker{}, errors.New("no worker launcher on this daemon")
	}
	now := s.now()
	var w store.Worker
	var cs store.Case
	err = s.Ledger.UpdateMember(c.Member, func(r *store.MemberRecord) error {
		live := 0
		for _, x := range r.Workers {
			if x.Status == "starting" || x.Status == "running" {
				live++
			}
		}
		if live >= m.Worker.MaxWorkers() {
			return fmt.Errorf("member %s already has %d live workers (worker.max_concurrent)", c.Member, live)
		}
		caseID := req.CaseID
		if caseID == "" {
			caseID = r.NextID("c")
			title := oneLine(req.Title, 200)
			if title == "" {
				title = oneLine(req.Brief, 120)
			}
			if r.Cases == nil {
				r.Cases = map[string]*store.Case{}
			}
			r.Cases[caseID] = &store.Case{ID: caseID, Title: title, Status: "open", Source: "member",
				RequesterClass: ReqMember, Created: now, Updated: now}
		}
		cc := r.Cases[caseID]
		if cc == nil {
			return fmt.Errorf("no case %s", caseID)
		}
		if cc.Status != "open" {
			return fmt.Errorf("case %s is closed", caseID)
		}
		wid := r.NextID("w")
		hid := fmt.Sprintf("m-%s-%s-%s", c.Member, wid, strconv.FormatInt(now.UnixNano(), 36))
		w = store.Worker{ID: wid, CaseID: caseID, Brief: req.Brief, Handoff: req.Handoff, HistoryID: hid,
			Dispatch: hid + ":" + config.MemberWorkerStepID, Cwd: s.sessionDir(c.Member, wid), Status: "starting", Started: now}
		if r.Workers == nil {
			r.Workers = map[string]*store.Worker{}
		}
		r.Workers[wid] = &w
		cc.Workers = append(cc.Workers, wid)
		cc.Updated = now
		cs = *cc
		return nil
	})
	if err != nil {
		return store.Worker{}, err
	}
	fail := func(err error) (store.Worker, error) {
		_ = s.Ledger.UpdateMember(c.Member, func(r *store.MemberRecord) error {
			if x := r.Workers[w.ID]; x != nil {
				x.Status, x.Ended, x.Result = "failed", s.now(), err.Error()
			}
			return nil
		})
		return store.Worker{}, err
	}
	repo := s.Repo(c.Member)
	if err := repo.AddWorktree(ctx, w.Cwd); err != nil {
		return fail(fmt.Errorf("worker worktree: %w", err))
	}
	trig := config.MemberWorkerTrigger(c.Member)
	if req.Handoff {
		trig = config.MemberHandoffTrigger(c.Member)
	}
	inputs := map[string]any{
		"prompt":                   WorkerPrompt(s.Cfg(), c.Member, &cs, w.ID, req.Brief),
		"workdir":                  w.Cwd,
		"worker":                   w.ID,
		dispatch.MemberContextCase: w.CaseID,
	}
	if err := s.Workers.Fire(ctx, trig, w.HistoryID, inputs); err != nil {
		_ = repo.RemoveWorktree(ctx, w.Cwd)
		return fail(fmt.Errorf("start worker: %w", err))
	}
	s.audit(map[string]any{"event": "member_worker", "member": c.Member, "worker": w.ID, "case": w.CaseID, "handoff": req.Handoff, "action": "spawn"})
	s.logf("%s: worker %s started on case %s", c.Member, w.ID, w.CaseID)
	return w, nil
}

// findWorker locates a worker by dispatch id or history id across members.
func (s *Service) findWorker(dispatchID, historyID string) (string, store.Worker, bool) {
	for _, name := range s.Cfg().MemberNames() {
		rec := s.Ledger.Member(name)
		for _, w := range rec.Workers {
			if (dispatchID != "" && w.Dispatch == dispatchID) || (historyID != "" && w.HistoryID == historyID) {
				return name, *w, true
			}
		}
	}
	return "", store.Worker{}, false
}

// IdentityForCwd is the cwd→identity lookup the jail consults once per paseo
// (ACP) session at creation (controller.SetPaseoIdentity, #175): a member's
// live home session, or a live hand-off worker, is identified by the session
// it is — "member-<name>-<session or worker id>" — which keys its tool state
// and is its jail's AgentID, and therefore what a grant minted for it binds
// to. ok=false for any cwd that is not a live member session's, so a stale or
// foreign cwd never inherits a member identity. Every live member session has
// its own cwd, so the answer is unambiguous.
func (s *Service) IdentityForCwd(cwd string) (string, bool) {
	clean := filepath.Clean(cwd)
	if clean == "" || clean == "." {
		return "", false
	}
	for _, name := range s.Cfg().MemberNames() {
		rec := s.Ledger.Member(name)
		if h := rec.Home; h != nil && filepath.Clean(h.Cwd) == clean {
			return SessionIdentity(name, h.ID), true
		}
		for _, w := range rec.Workers {
			if w.Handoff && (w.Status == "starting" || w.Status == "running") && filepath.Clean(w.Cwd) == clean {
				return SessionIdentity(name, w.ID), true
			}
		}
	}
	return "", false
}

// SessionIdentity is a member session's jail identity (path-safe: member
// names and ids are [a-z0-9_-]).
func SessionIdentity(member, id string) string { return "member-" + member + "-" + id }

// IsMemberDispatch reports whether a dispatch id belongs to a member
// session, and which role.
func (s *Service) IsMemberDispatch(dispatchID string) (member, role string) {
	if dispatchID == "" {
		return "", ""
	}
	for _, name := range s.Cfg().MemberNames() {
		rec := s.Ledger.Member(name)
		if rec.Home != nil && rec.Home.Dispatch == dispatchID {
			return name, config.MemberRoleHome
		}
		for _, w := range rec.Workers {
			if w.Dispatch == dispatchID {
				return name, config.MemberRoleWorker
			}
		}
	}
	return "", ""
}

// WorkerDone records a worker's step.done: the ledger, the episodic log, and
// a report into the home inbox. handled=false for a dispatch that is not a
// member worker.
func (s *Service) WorkerDone(ctx context.Context, dispatchID, reason string, output any) bool {
	name, w, ok := s.findWorker(dispatchID, "")
	if !ok {
		return false
	}
	summary := summarizeOutput(reason, output)
	s.finishWorker(ctx, name, w, "done", summary)
	return true
}

// OnRunEvent observes a worker run's lifecycle: started → running; a run
// that ends without the worker having reported is recorded as such.
func (s *Service) OnRunEvent(ctx context.Context, typ, historyID, status, detail string) {
	if !strings.HasPrefix(historyID, "m-") {
		return
	}
	name, w, ok := s.findWorker("", historyID)
	if !ok {
		return
	}
	switch typ {
	case "run_started", "step_started":
		_ = s.Ledger.UpdateMember(name, func(r *store.MemberRecord) error {
			if x := r.Workers[w.ID]; x != nil && x.Status == "starting" {
				x.Status = "running"
			}
			return nil
		})
	case "run_done":
		if w.Handoff && status != "failed" {
			return // a background hand-off's run returns at launch
		}
		if w.Status == "starting" || w.Status == "running" {
			st := "exited"
			if status == "failed" {
				st = "failed"
			}
			s.finishWorker(ctx, name, w, st, oneLine("the run ended ("+status+") without the worker reporting step.done. "+detail, 600))
			return
		}
		if !w.Handoff {
			_ = s.Repo(name).RemoveWorktree(ctx, w.Cwd)
		}
	}
}

func (s *Service) finishWorker(ctx context.Context, name string, w store.Worker, status, summary string) {
	var cs store.Case
	already := false
	_ = s.Ledger.UpdateMember(name, func(r *store.MemberRecord) error {
		x := r.Workers[w.ID]
		if x == nil {
			return nil
		}
		if x.Status != "starting" && x.Status != "running" {
			already = true
			return nil
		}
		x.Status, x.Ended, x.Result = status, s.now(), summary
		if c := r.Cases[x.CaseID]; c != nil {
			c.Updated = s.now()
			cs = *c
		}
		return nil
	})
	if already {
		return
	}
	repo := s.Repo(name)
	created := cs.Created
	if created.IsZero() {
		created = s.now()
	}
	ep := fmt.Sprintf("## %s — worker %s %s\n\nBrief: %s\n\nResult: %s\n", s.now().UTC().Format(time.RFC3339), w.ID, status, oneLine(w.Brief, 600), summary)
	if _, err := repo.Write(ctx, repo.EpisodePath(w.CaseID, cs.Title, created), ep, "append",
		fmt.Sprintf("episode: case %s worker %s %s", w.CaseID, w.ID, status), map[string]string{"Member-Case": w.CaseID, "Member-Worker": w.ID}); err != nil {
		s.logf("%s: episode for worker %s: %v", name, w.ID, err)
	}
	s.audit(map[string]any{"event": "member_worker", "member": name, "worker": w.ID, "case": w.CaseID, "action": status})
	s.enqueue(name, w.CaseID, fmt.Sprintf("[conductor] worker %s on case %s finished (%s):\n%s", w.ID, w.CaseID, status, fence("WORKER REPORT", summary)))
	if !w.Handoff {
		_ = repo.RemoveWorktree(ctx, w.Cwd)
	}
}

func summarizeOutput(reason string, output any) string {
	switch o := output.(type) {
	case nil:
	case string:
		if strings.TrimSpace(o) != "" {
			return oneLine(o, 4000)
		}
	case map[string]any:
		var parts []string
		for _, k := range []string{"summary", "outcome", "result"} {
			if v, ok := o[k]; ok {
				parts = append(parts, fmt.Sprintf("%s: %v", k, v))
			}
		}
		if len(parts) > 0 {
			return oneLine(strings.Join(parts, " — "), 4000)
		}
		return oneLine(fmt.Sprintf("%v", o), 4000)
	default:
		return oneLine(fmt.Sprintf("%v", o), 4000)
	}
	if strings.TrimSpace(reason) != "" {
		return oneLine(reason, 2000)
	}
	return "(no result reported)"
}

// ---- agent ops ----

// WriteMemory writes a memory-repo file for a member session.
func (s *Service) WriteMemory(ctx context.Context, c Actor, path, text string, appendMode bool, caseID string) (string, error) {
	if _, err := s.member(c.Member); err != nil {
		return "", err
	}
	if strings.HasPrefix(path, DirHandoff+"/") {
		return "", errors.New("handoff notes are written with `conductor member handoff`")
	}
	mode := "replace"
	if appendMode {
		mode = "append"
	}
	if caseID == "" {
		caseID = c.CaseID
	}
	sha, err := s.Repo(c.Member).Write(ctx, path, text, mode, "memory: "+path,
		map[string]string{"Member-Case": caseID, "Member-Role": c.Role, "Member-Dispatch": c.Dispatch})
	if err == nil {
		s.audit(map[string]any{"event": "member_memory", "member": c.Member, "path": path, "commit": sha, "role": c.Role})
	}
	return sha, err
}

// Episode appends to a case's episode.
func (s *Service) Episode(ctx context.Context, c Actor, caseID, text string) (string, error) {
	if caseID == "" {
		caseID = c.CaseID
	}
	rec := s.Ledger.Member(c.Member)
	cs := rec.Cases[caseID]
	if cs == nil {
		return "", fmt.Errorf("no case %q", caseID)
	}
	repo := s.Repo(c.Member)
	entry := fmt.Sprintf("## %s — %s\n\n%s\n", s.now().UTC().Format(time.RFC3339), c.Role, strings.TrimSpace(text))
	return repo.Write(ctx, repo.EpisodePath(cs.ID, cs.Title, cs.Created), entry, "append", "episode: case "+cs.ID, map[string]string{"Member-Case": cs.ID, "Member-Role": c.Role})
}

// Handoff records the home session's handoff note.
func (s *Service) Handoff(ctx context.Context, c Actor, text string) (string, error) {
	if c.Role != config.MemberRoleHome {
		return "", errors.New("only the home session writes handoff notes")
	}
	rec := s.Ledger.Member(c.Member)
	sid := "unknown"
	if rec.Home != nil && rec.Home.Dispatch == c.Dispatch {
		sid = rec.Home.ID
	}
	repo := s.Repo(c.Member)
	if strings.TrimSpace(text) == "" {
		return "", errors.New("empty handoff")
	}
	return repo.Write(ctx, repo.HandoffPath(sid), "# Handoff from session "+sid+"\n\n"+text, "replace", "handoff: session "+sid, map[string]string{"Member-Session": sid})
}

// CaseOpen opens a case from the home session (the operator asked for
// something in conversation, or the member noticed something).
func (s *Service) CaseOpen(c Actor, title, summary string) (store.Case, error) {
	if c.Role != config.MemberRoleHome {
		return store.Case{}, errors.New("only the home session opens cases")
	}
	if strings.TrimSpace(title) == "" {
		return store.Case{}, errors.New("a case needs a title")
	}
	now := s.now()
	var out store.Case
	err := s.Ledger.UpdateMember(c.Member, func(r *store.MemberRecord) error {
		id := r.NextID("c")
		if r.Cases == nil {
			r.Cases = map[string]*store.Case{}
		}
		cc := &store.Case{ID: id, Title: oneLine(title, 200), Summary: summary, Status: "open", Source: "member",
			RequesterClass: ReqMember, Created: now, Updated: now}
		r.Cases[id] = cc
		out = *cc
		return nil
	})
	return out, err
}

// CaseNote appends to a case's running summary.
func (s *Service) CaseNote(c Actor, caseID, text string) error {
	if c.Role == config.MemberRoleWorker && caseID != c.CaseID {
		return errors.New("a worker notes only its own case")
	}
	return s.Ledger.UpdateMember(c.Member, func(r *store.MemberRecord) error {
		cc := r.Cases[caseID]
		if cc == nil {
			return fmt.Errorf("no case %q", caseID)
		}
		cc.Summary = strings.TrimSpace(cc.Summary + "\n" + s.now().UTC().Format("01-02 15:04") + " " + strings.TrimSpace(text))
		if len(cc.Summary) > 8000 {
			cc.Summary = cc.Summary[len(cc.Summary)-8000:]
		}
		cc.Updated = s.now()
		return nil
	})
}

// CaseClose closes a case and writes its outcome to the episodic log.
func (s *Service) CaseClose(ctx context.Context, c Actor, caseID, outcome string) error {
	if c.Role != config.MemberRoleHome {
		return errors.New("only the home session closes cases")
	}
	var cs store.Case
	err := s.Ledger.UpdateMember(c.Member, func(r *store.MemberRecord) error {
		cc := r.Cases[caseID]
		if cc == nil {
			return fmt.Errorf("no case %q", caseID)
		}
		cc.Status, cc.Outcome, cc.Updated = "closed", outcome, s.now()
		cs = *cc
		return nil
	})
	if err != nil {
		return err
	}
	repo := s.Repo(c.Member)
	entry := fmt.Sprintf("## %s — closed\n\nSource: %s · requester: %s (%s)\n\nOutcome: %s\n", s.now().UTC().Format(time.RFC3339), cs.Source, orDash(cs.Requester), cs.RequesterClass, strings.TrimSpace(outcome))
	_, err = repo.Write(ctx, repo.EpisodePath(cs.ID, cs.Title, cs.Created), entry, "append", "episode: case "+cs.ID+" closed", map[string]string{"Member-Case": cs.ID})
	return err
}

// Cases lists a member's cases (open first, newest first).
func (s *Service) Cases(member string, all bool) []store.Case {
	rec := s.Ledger.Member(member)
	var out []store.Case
	for _, c := range rec.Cases {
		if all || c.Status == "open" {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Status == "open") != (out[j].Status == "open") {
			return out[i].Status == "open"
		}
		return out[i].Created.After(out[j].Created)
	})
	return out
}

// ProposeCommand records a host-command proposal (e.g. a saved-plan
// terraform apply). The files it names are hashed NOW, from the caller's own
// working directory, so the operator approves exactly those bytes; the
// grant minted on approval pins them for the jail's broker to re-check.
func (s *Service) ProposeCommand(c Actor, tool string, argv, files []string, summary string) (store.Proposal, error) {
	m, err := s.member(c.Member)
	if err != nil {
		return store.Proposal{}, err
	}
	tool = strings.TrimSpace(tool)
	if tool == "" || strings.ContainsAny(tool, "/\\ ") {
		return store.Proposal{}, errors.New("tool must be a bare binary name")
	}
	if Classify(m, "command."+tool) == config.TierNever {
		return store.Proposal{}, fmt.Errorf("command.%s is classified never for member %s", tool, c.Member)
	}
	rec := s.Ledger.Member(c.Member)
	cwd := ""
	agentID := ""
	switch c.Role {
	// A grant binds to the identity the BROKER will present: for a paseo
	// session (the home, a hand-off worker) that is the jail identity the
	// cwd lookup gives it — paseo's own agent id never crosses ACP — and for
	// a cli worker its dispatch id (Dispatch, below).
	case config.MemberRoleHome:
		if rec.Home != nil && rec.Home.Dispatch == c.Dispatch {
			cwd, agentID = rec.Home.Cwd, SessionIdentity(c.Member, rec.Home.ID)
		}
	case config.MemberRoleWorker:
		for _, w := range rec.Workers {
			if w.Dispatch == c.Dispatch {
				cwd = w.Cwd
				if w.Handoff {
					agentID = SessionIdentity(c.Member, w.ID)
				}
			}
		}
	}
	if cwd == "" {
		return store.Proposal{}, errors.New("cannot resolve the caller's working directory")
	}
	sums := map[string]string{}
	for _, f := range files {
		abs, err := resolveWithin(cwd, f)
		if err != nil {
			return store.Proposal{}, err
		}
		sum, err := fileSHA(abs)
		if err != nil {
			return store.Proposal{}, fmt.Errorf("hash %s: %w", f, err)
		}
		sums[f] = sum
	}
	opts := map[string]any{"tool": tool, "argv": argv, "files": sums}
	digest, err := ActionDigest(c.Member, "command", "command."+tool, opts)
	if err != nil {
		return store.Proposal{}, err
	}
	now := s.now()
	status := "pending"
	if m.Autonomy.ShadowMode() {
		status = "shadow"
	}
	var out store.Proposal
	err = s.Ledger.UpdateMember(c.Member, func(r *store.MemberRecord) error {
		p := &store.Proposal{ID: r.NextID("p"), CaseID: c.CaseID, Kind: "command", Tier: config.TierPropose,
			Tool: tool, Argv: append([]string(nil), argv...), Files: sums, Summary: summary, Digest: digest,
			Role: c.Role, Agent: c.Agent, Dispatch: c.Dispatch, AgentID: agentID,
			Status: status, Created: now, Expires: now.Add(m.Autonomy.TTL()), RequesterClass: ReqMember}
		if cc := r.Cases[c.CaseID]; cc != nil {
			p.Requester, p.RequesterClass = cc.Requester, cc.RequesterClass
			cc.Proposals = append(cc.Proposals, p.ID)
		}
		if r.Proposals == nil {
			r.Proposals = map[string]*store.Proposal{}
		}
		r.Proposals[p.ID] = p
		out = *p
		return nil
	})
	if err == nil && s.Gate != nil && s.Gate.OnProposal != nil {
		s.Gate.OnProposal(c.Member, out)
	}
	return out, err
}

// Wait blocks until a proposal is decided (or timeout).
func (s *Service) Wait(ctx context.Context, member, id string, timeout time.Duration) (store.Proposal, error) {
	if timeout <= 0 || timeout > time.Hour {
		timeout = 30 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		p := s.Ledger.Member(member).Proposals[id]
		if p == nil {
			return store.Proposal{}, fmt.Errorf("no proposal %s", id)
		}
		if p.Status != "pending" || time.Now().After(deadline) {
			return *p, nil
		}
		select {
		case <-ctx.Done():
			return *p, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// OnProposal is wired as Gate.OnProposal: the home session hears about every
// proposal (a worker's included), so it can tell the operator.
func (s *Service) OnProposal(member string, p store.Proposal) {
	s.audit(map[string]any{"event": "member_proposal", "member": member, "proposal": p.ID, "tier": p.Tier, "status": p.Status, "uses": p.Uses, "tool": p.Tool, "role": p.Role})
	if p.Role == config.MemberRoleHome {
		return // the home session just made it; it already has the answer
	}
	verb := "is awaiting operator approval"
	if p.Status == "shadow" {
		verb = "was recorded as a shadow proposal"
	}
	s.enqueue(member, p.CaseID, fmt.Sprintf("[conductor] proposal %s (case %s, requested by %s/%s) %s: %s",
		p.ID, orDash(p.CaseID), orDash(p.Requester), p.RequesterClass, verb, ProposalAction(p)))
}

// OnDecision is wired as Executor.Notify.
func (s *Service) OnDecision(member string, p store.Proposal) {
	res := ""
	if p.Reason != "" {
		res = " — " + p.Reason
	}
	if p.Grant != "" {
		res += " — grant " + p.Grant + " (run the exact approved command now; it is single-use)"
	}
	s.enqueue(member, p.CaseID, fmt.Sprintf("[conductor] proposal %s was %s by operator %s%s", p.ID, p.Status, orDash(p.Approver), res))
}
