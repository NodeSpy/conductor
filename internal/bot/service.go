package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/auditctx"
	"github.com/NodeSpy/conductor/internal/blob"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/store"
)

// THE BOT SERVICE: the daemon-side half of every bot. It owns the
// lifecycle the agents can't — launching and rotating the home session,
// delivering the inbox into it, routing events into cases, starting workers
// and recording what they report — and serves the agent-facing ops
// (`conductor bot …` from a bot session) and the operator ones.
//
// Launch seams: the HOME session goes through HomeLauncher (the paseo
// dispatcher, on the sandboxed provider entry when the sandbox is active).
// WORKERS go through WorkerLauncher, which fires the
// bot's synthesized `on: manual` trigger — the ordinary engine path to the
// cli runtime. Neither is a parallel launcher.

// HomeLauncher starts and drives home sessions.
type HomeLauncher interface {
	// Launch starts a home agent in spec.Cwd and returns its agent id.
	Launch(ctx context.Context, spec HomeSpec) (string, error)
	// Send delivers one message, with images attached when the runtime takes
	// them (AcceptsImages), and returns when the turn is done.
	Send(ctx context.Context, bot, agentID, text string, images []string) error
	// AcceptsImages reports whether the bot's home runtime takes images
	// with a message; otherwise an image is a file the session opens.
	AcceptsImages(ctx context.Context, bot string) bool
	// State is "running", "idle", … or "gone".
	State(ctx context.Context, bot, agentID string) (string, error)
	// Retire archives an agent conductor launched, revokes its session, and
	// (sandboxed) ends every ACP session opened in its cwd.
	Retire(ctx context.Context, bot, agentID, dispatchID, cwd string) error
}

// HomeSpec is one home launch.
type HomeSpec struct {
	Bot, Session, Dispatch, Cwd, Prompt string
}

// WorkerLauncher fires a bot worker run.
type WorkerLauncher interface {
	Fire(ctx context.Context, trigger, historyID string, inputs map[string]any) error
}

// Actor is the token-bound identity of an agent calling a bot op.
type Actor struct {
	Bot, Role, CaseID, Dispatch, Agent string
}

// RunUsage is one run's model usage, in every unit a budget caps.
type RunUsage struct {
	CostUSD       float64
	Tokens, Turns int
	ByModel       []store.ModelUsage
}

// Event is something routed into a bot.
type Event struct {
	Bot       string
	Source    string // "<connector>.<event>", "operator", "schedule"
	Requester string // who asked, as the event names them ("" = no human)
	Title     string
	Text      string // the payload (data)
	Urgent    bool
	// Attachments are files the event carried (an operator's): checked
	// against the bot's caps before the case opens, and refused with it.
	Attachments []AttachmentIn
	// ticket, when set, is the ready ticket this delivery picks up: the case
	// is that ticket, carried into the bot's ledger.
	ticket *store.Case
	// files fetches a connector event's files (flow.BotRoute.Files) once
	// the receiving bot is known. A chat user's files that can't be taken
	// don't refuse their message: it arrives with a note saying why.
	files func(ctx context.Context, maxFiles int, maxFile int64) ([]connector.File, error)
}

// Service is the bot service.
type Service struct {
	Cfg     func() *config.Config
	Ledger  Ledger
	Gate    *Gate
	Exec    *Executor
	Home    HomeLauncher
	Workers WorkerLauncher
	// Blobs stores attachments (nil: messages can't carry files).
	Blobs *blob.Store
	// Spend is the purchase ledger a ticket's spend limit is read against
	// (the browser's payments; nil: nothing known spent).
	Spend SpendLedger

	chat      chatHub
	ticketHub ticketHub
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
	// Terraform is the bot terraform runner (nil when no bot has a
	// terraform: block).
	Terraform *Terraform
	// RevokeToken kills a finished session's conductor token at once (the
	// broker's RevokeDispatch). Bot tokens are durable — they would
	// otherwise outlive a finished worker by days.
	RevokeToken func(dispatchID string)
	// RunUsage is a finished run's totals (the run history's), for the
	// proactive budget. Notify publishes a proactive finding to the
	// conductor.bot_notify lifecycle event. Both optional.
	RunUsage func(historyID string) (RunUsage, bool)
	Notify   func(ctx context.Context, n Notice)

	mu       sync.Mutex
	tplCfg   map[string]*config.Config // the config each bot's template was last checked against
	skipped  map[string]string         // last sweep-skip reason, per bot (logged once)
	failedAt map[string]time.Time      // last failed home launch, per bot (backoff)
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
		s.Log("bot: "+f, a...)
	}
}

func (s *Service) audit(e map[string]any) {
	if s.Audit != nil {
		s.Audit(e)
	}
}

// auditCtx is audit for a row written on a request: it adds the who-and-where
// the front end put on ctx (the dashboard's session, address and listener).
func (s *Service) auditCtx(ctx context.Context, e map[string]any) {
	auditctx.Merge(ctx, e)
	s.audit(e)
}

// Repo returns a bot's memory repo handle.
func (s *Service) Repo(name string) *Repo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.repos == nil {
		s.repos = map[string]*Repo{}
	}
	r := s.repos[name]
	if r == nil {
		r = &Repo{Dir: s.Cfg().BotRepoDir(name), Bot: name, Now: s.Now, Seed: s.Cfg().BotSeedDir(name),
			IndexPath:  s.Cfg().BotIndexPath(name),
			TemplateFn: func() *config.BotOrigin { return s.Cfg().Bots[name].Origin },
			LimitsFn:   func() config.MemoryLimits { return s.Cfg().Bots[name].Memory },
			KindsFn: func() *Kinds {
				m := s.Cfg().Bots[name]
				return NewKinds(false, m.Collections).WithLimits(m.Memory)
			}}
		s.repos[name] = r
	}
	return r
}

// lock serializes lifecycle work (launch, rotate, deliver) per bot.
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

// Kick wakes a bot's delivery loop.
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

func (s *Service) sessionDir(bot, id string) string {
	return filepath.Join(s.sessionRoot(), "bot-"+bot+"-"+id)
}

func (s *Service) bot(name string) (config.BotConfig, error) {
	m, ok := s.Cfg().Bots[name]
	if !ok || m.Disabled {
		return m, fmt.Errorf("unknown bot %q", name)
	}
	return m, nil
}

// Start initializes every bot and runs its loop until ctx ends.
func (s *Service) Start(ctx context.Context) {
	// Recover any cross-home reassignment a prior run was interrupted mid-way,
	// before dispatch can act on a half-moved ticket.
	s.reconcileTicketClaims()
	for _, team := range s.Cfg().TeamNames() {
		if repo := s.TeamRepo(team); repo != nil {
			if err := repo.Init(ctx); err != nil {
				s.logf("team %s: knowledge repo: %v", team, err)
				continue
			}
			s.reportInventory("team "+team, repo)
		}
	}
	for _, name := range s.Cfg().BotNames() {
		if err := s.Repo(name).Init(ctx); err != nil {
			s.logf("%s: memory repo: %v", name, err)
			continue
		}
		s.reportInventory("bot "+name, s.Repo(name))
		s.maybeCheckTemplate(ctx, name)
		s.recoverWorkers(name)
		if s.Terraform != nil {
			s.Terraform.Recover(name)
		}
		go s.loop(ctx, name)
	}
}

// reportInventory reports, once at start, what in a repo is not a valid
// document of a kind — skipped seed files, files outside every kind's home,
// files that fail their kind's schema. Nothing is deleted or moved: they stay
// readable, and the next write to one must be valid.
func (s *Service) reportInventory(what string, repo *Repo) {
	for _, sk := range repo.SeedSkipped {
		s.logf("%s: seed file not copied: %s", what, sk)
	}
	var issues []string
	for _, d := range repo.Inventory() {
		if d.Status == DocOK || d.Status == DocLegacy {
			continue
		}
		issues = append(issues, d.Path+" ("+d.Status+": "+d.Problem+")")
	}
	if len(issues) == 0 && len(repo.SeedSkipped) == 0 {
		return
	}
	for _, i := range issues {
		s.logf("%s: memory file not a valid document: %s", what, i)
	}
	s.audit(map[string]any{"event": "bot_memory_inventory", "repo": what, "unmapped_or_invalid": issues, "seed_skipped": repo.SeedSkipped})
}

// recoverWorkers: a cli worker is a child of the daemon, so a restart ended
// it. Say so in the ledger and the inbox rather than leave it "running".
func (s *Service) recoverWorkers(name string) {
	var lost []store.Worker
	type report struct{ caseID, text string }
	var reports []report
	_ = s.Ledger.UpdateBot(name, func(r *store.BotRecord) error {
		for _, w := range r.Workers {
			if liveWorker(w) && !w.Handoff {
				w.Status, w.Ended, w.Result = "interrupted", s.now(), "the daemon restarted while this worker ran"
				lost = append(lost, *w)
				if sw := r.Sweeps[w.Sweep]; sw != nil && sw.Status == "running" {
					sw.Status, sw.Ended, sw.Summary = "interrupted", s.now(), "the daemon restarted while this sweep ran"
				}
			}
		}
		// A group whose last live worker was just interrupted reports now.
		for _, g := range r.Groups {
			if !g.Reported && groupDone(r, g) {
				g.Reported = true
				var ws []store.Worker
				for _, id := range g.Workers {
					if w := r.Workers[id]; w != nil {
						ws = append(ws, *w)
					}
				}
				reports = append(reports, report{g.CaseID, GroupReport(*g, ws)})
			}
		}
		return nil
	})
	for _, w := range lost {
		s.revoke(w.Dispatch)
		_ = s.Repo(name).RemoveWorktree(context.Background(), w.Cwd)
		switch {
		case w.Sweep != "":
			s.logf("%s: proactive sweep %s was interrupted by a daemon restart", name, w.Sweep)
		case w.Group == "":
			s.enqueue(name, w.CaseID, fmt.Sprintf("[conductor] worker %s (case %s) was interrupted by a daemon restart before it reported. Respawn it if the work is still needed.", w.ID, w.CaseID))
		}
	}
	for _, rp := range reports {
		s.enqueue(name, rp.caseID, rp.text)
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

// Tick is one pass of a bot's loop: keep the home session alive and
// fresh, expire stale proposals, deliver the inbox.
func (s *Service) Tick(ctx context.Context, name string) {
	m, err := s.bot(name)
	if err != nil {
		return
	}
	s.expireTriage(ctx, name)
	s.sweepAttachments(name, m.Attachments)
	s.maybeCheckTemplate(ctx, name)
	if err := s.sandboxGate(name, m); err != nil {
		// Nothing of this bot starts until the sandbox does.
		s.noteSkip(name, err)
		return
	}
	s.maybeSweep(ctx, name, m)
	if s.Home == nil {
		return
	}
	if s.Exec != nil {
		s.Exec.ExpireProposals(name)
	}
	rec := s.Ledger.Bot(name)
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

func (s *Service) revoke(dispatchID string) {
	if s.RevokeToken != nil && dispatchID != "" {
		s.RevokeToken(dispatchID)
	}
}

// enqueue adds a message to the home inbox (durable) and wakes delivery.
func (s *Service) enqueue(name, caseID, text string) {
	s.enqueueMsg(name, store.InboxMsg{CaseID: caseID, Text: text})
}

// enqueueMsg queues a message (its ID reserved already, or minted here).
func (s *Service) enqueueMsg(name string, msg store.InboxMsg) {
	_ = s.Ledger.UpdateBot(name, func(r *store.BotRecord) error {
		if msg.ID == "" {
			msg.ID = r.NextID("m")
		}
		msg.Created = s.now()
		r.Inbox = append(r.Inbox, &msg)
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
	_, err := s.SayWith(name, text, nil, "")
	return err
}

// SayWith is Say with files, optionally on one of the bot's open cases (the
// files are then removed when it closes; otherwise after attachments.ttl).
// It returns the message's attachments.
func (s *Service) SayWith(name, text string, files []AttachmentIn, caseID string) ([]*store.Attachment, error) {
	return s.SayAs("", name, text, files, caseID)
}

// SayAs is SayWith as a named operator (""; the only one, or an unnamed
// operator when several are configured): recorded in the bot's
// conversation, delivered through Chat.
func (s *Service) SayAs(op, name, text string, files []AttachmentIn, caseID string) ([]*store.Attachment, error) {
	return s.sayAs(context.Background(), op, name, text, files, caseID)
}

func (s *Service) sayAs(ctx context.Context, op, name, text string, files []AttachmentIn, caseID string) ([]*store.Attachment, error) {
	m, err := s.Chat(ctx, ChatSend{Conversation: BotConversation(name), Operator: op, Text: text, Files: files,
		CaseID: caseID, anyOperator: op == ""})
	if err != nil {
		return nil, err
	}
	var out []*store.Attachment
	for _, a := range liveAttachments(s.Ledger.Bot(name), m.Attachments) {
		a := a
		out = append(out, &a)
	}
	return out, nil
}

func attachmentIDs(atts []*store.Attachment) []string {
	out := make([]string, len(atts))
	for i, a := range atts {
		out[i] = a.ID
	}
	return out
}

// deliver sends every pending inbox message as one turn, when the home
// agent is idle. A busy agent (the operator mid-conversation) is never
// interrupted: delivery waits for the next idle moment.
func (s *Service) deliver(ctx context.Context, name string) {
	l := s.lock(name)
	l.Lock()
	defer l.Unlock()
	rec := s.Ledger.Bot(name)
	if rec.Home == nil {
		return
	}
	var ids []string
	var pending []*store.InboxMsg
	for _, m := range rec.Inbox {
		if m.Delivered.IsZero() {
			ids = append(ids, m.ID)
			pending = append(pending, m)
		}
	}
	if len(ids) == 0 {
		return
	}
	st, err := s.Home.State(ctx, name, rec.Home.AgentID)
	if err != nil || st == "running" || st == "gone" {
		return
	}
	// A message's files: images go to the runtime as images where it takes
	// them; every file is listed, fenced, with its read-only path.
	takesImages := s.Home.AcceptsImages(ctx, name)
	var parts, images []string
	for _, m := range pending {
		atts := liveAttachments(rec, m.Attachments)
		asImg := map[string]bool{}
		for _, a := range atts {
			if takesImages && a.Removed.IsZero() && imageInput(a.MediaType) {
				asImg[a.ID] = true
				images = append(images, a.Path)
			}
		}
		parts = append(parts, m.Text+AttachmentSection(atts, asImg, ""))
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
	err = s.Home.Send(sctx, name, rec.Home.AgentID, text, images)
	_ = s.Ledger.UpdateBot(name, func(r *store.BotRecord) error {
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
	m, err := s.bot(name)
	if err != nil {
		return err
	}
	if err := s.sandboxGate(name, m); err != nil {
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
	rec := s.Ledger.Bot(name)
	old := rec.Home
	if old != nil {
		s.collectHandoff(ctx, name, old, reason)
		rec = s.Ledger.Bot(name)
	}
	hPath, hText := repo.LatestHandoff()
	var sid string
	_ = s.Ledger.UpdateBot(name, func(r *store.BotRecord) error { sid = r.NextID("s"); return nil })
	cwd := s.sessionDir(name, sid)
	if err := repo.AddWorktree(ctx, cwd); err != nil {
		return fmt.Errorf("session worktree: %w", err)
	}
	dispatchID := fmt.Sprintf("bot:%s:home:%s:%s", name, sid, strconv.FormatInt(s.now().UnixNano(), 36))
	prompt := HomePrompt(s.Cfg(), name, rec, hPath, hText, s.MemoryIndex(ctx, name, homeQuery(rec)))
	agentID, err := s.Home.Launch(ctx, HomeSpec{Bot: name, Session: sid, Dispatch: dispatchID, Cwd: cwd, Prompt: prompt})
	if err != nil {
		_ = repo.RemoveWorktree(ctx, cwd)
		return fmt.Errorf("launch home session: %w", err)
	}
	now := s.now()
	_ = s.Ledger.UpdateBot(name, func(r *store.BotRecord) error {
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
	s.audit(map[string]any{"event": "bot_session", "bot": name, "session": sid, "agent": agentID, "reason": reason, "handoff": hPath})
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
		msg := fmt.Sprintf("[conductor] Your session is being rotated (%s). Write your handoff NOW so a fresh session can continue with no lost context: what you are in the middle of, what you promised whom, what to watch for, and anything you learned that is not yet in knowledge/. Run:\n  conductor bot handoff --text \"…\"\nThen reply \"handoff written\".", reason)
		if err := s.Home.Send(hctx, name, old.AgentID, msg, nil); err != nil {
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
	note := MechanicalHandoff(s.Ledger.Bot(name), reason)
	if max := s.Cfg().Bots[name].Memory.MaxBytesOf("handoff"); len(note) > max {
		note = strings.TrimRight(cutBytes(note, max), "\n") + "\n\n[… cut at the handoff cap; `conductor bot cases` has all open work]\n"
	}
	if _, err := repo.AppendLog(ctx, repo.HandoffPath(old.ID), map[string]any{"session": old.ID}, note, "handoff: session "+old.ID+" (written by conductor)", map[string]string{"Bot-Session": old.ID}); err != nil {
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
func RequesterClass(cfg *config.Config, bot, requester string) (string, string) {
	// Matching is EXACT: a requester string that only resembles an operator's
	// name or alias (padding, case, a prefix) is a third party.
	return classifyAgainst(cfg, cfg.BotOperators(bot), requester)
}

// RequesterClassFor classifies an intake that carries event provenance (a
// source): an empty requester is `event` only when the source vouches for it;
// a requester-less and source-less intake drops to the least trusted class.
func RequesterClassFor(cfg *config.Config, bot, requester, source string) (string, string) {
	return sourcedClass(cfg, cfg.BotOperators(bot), requester, source)
}

// Deliver routes an event into a bot: a case, an inbox message, and —
// for an urgent event — a worker started immediately, so urgent work never
// waits behind the home conversation.
func (s *Service) Deliver(ctx context.Context, ev Event) (string, error) {
	if _, err := s.bot(ev.Bot); err != nil {
		return "", err
	}
	who, class := RequesterClassFor(s.Cfg(), ev.Bot, ev.Requester, ev.Source)
	return s.deliverCase(ctx, ev, who, class, nil, "")
}

// eventAttachments checks an event's files against the bot's caps — before
// its case opens, so a refused file refuses the event — and reserves the
// message they will arrive with.
func (s *Service) eventAttachments(ev Event) (types []string, msg string, err error) {
	if len(ev.Attachments) == 0 {
		return nil, "", nil
	}
	m, err := s.bot(ev.Bot)
	if err != nil {
		return nil, "", err
	}
	if types, err = checkAttachments(m.Attachments, ev.Attachments); err != nil {
		return nil, "", err
	}
	msg, err = s.reserveMessage(ev.Bot)
	return types, msg, err
}

// deliverCase opens a case attributed to (who, class) — already decided by
// the caller, never by the event — with its routing record, if team-routed,
// and delivers it: the inbox, and a worker at once for an urgent event. A
// pre is a conductor line shown with the event (a lead's fenced note).
func (s *Service) deliverCase(ctx context.Context, ev Event, who, class string, rt *store.Routing, pre string) (string, error) {
	// Intake invariant (M2): a provenance-less delivery — an autonomy class with
	// neither a requester NOR a source to vouch for it — is never written as
	// trusted, whatever the caller decided. classifyAgainst("") is `event` by its
	// documented contract, and sourcedClass already drops a requester-less,
	// source-less intake at the classified entries (Deliver / RouteTeam). This is
	// that same drop at the shared delivery sink, so a caller that reaches here on
	// another path (a ready ticket carrying a stored class, or any future one)
	// cannot smuggle an untrusted, sourceless event into an autonomous tier. The
	// raw ev.Source is checked, not srcOr's "unknown" placeholder.
	if autonomyClass[class] && strings.TrimSpace(who) == "" && strings.TrimSpace(ev.Source) == "" {
		who, class = "", ReqUser
	}
	title := eventTitle(ev.Title, ev.Text, ev.Source)
	src := srcOr(ev.Source)
	filesNote := ""
	if ev.files != nil && len(ev.Attachments) == 0 {
		lim := s.Cfg().Bots[ev.Bot].Attachments
		var err error
		if lim.Off {
			err = errors.New("this bot takes no attachments")
		} else {
			var got []connector.File
			if got, err = ev.files(ctx, lim.MaxFilesOr(), lim.MaxFileOr()); err == nil {
				for _, f := range got {
					ev.Attachments = append(ev.Attachments, AttachmentIn{Name: f.Name, Data: f.Data})
				}
				if _, err = checkAttachments(lim, ev.Attachments); err != nil {
					ev.Attachments = nil
				}
			}
		}
		if err != nil {
			filesNote = "(The files that came with this event were not accepted: " + oneLine(err.Error(), 300) + ")"
		}
	}
	types, msgID, err := s.eventAttachments(ev)
	if err != nil {
		return "", err
	}
	now := s.now()
	var c store.Case
	err = s.Ledger.UpdateBot(ev.Bot, func(r *store.BotRecord) error {
		id := r.NextID("c")
		if r.Cases == nil {
			r.Cases = map[string]*store.Case{}
		}
		cc := &store.Case{ID: id, Title: title, Status: store.TicketInProgress, Urgent: ev.Urgent, Source: src,
			Requester: who, RequesterClass: class, Created: now, Updated: now}
		if t := ev.ticket; t != nil {
			carryTicket(cc, t, ev.Bot, now)
		} else {
			// An event's ticket: created ready, picked up at once.
			addHistory(cc, now, src, "created ready and picked up by bot:"+ev.Bot)
		}
		if rt != nil {
			cp := *rt
			cp.AssigneeCase = id
			cc.Team, cc.Routing = rt.Team, &cp
		}
		r.Cases[id] = cc
		c = *cc
		return nil
	})
	if err != nil {
		return "", err
	}
	ae := map[string]any{"event": "bot_event", "bot": ev.Bot, "case": c.ID, "source": src, "requester": who, "requester_class": class, "urgent": ev.Urgent}
	if rt != nil {
		ae["team"], ae["via"] = rt.Team, rt.Via
	}
	s.audit(ae)
	note := filesNote
	atts, err := s.storeAttachments(ev.Bot, msgID, c.ID, ev.Attachments, types, attachOrigin{Source: src, Requester: who, Class: class})
	if err != nil {
		note = strings.TrimSpace(note + "\n(The event's files could not be stored: " + err.Error() + ")")
	}
	if ev.Urgent {
		brief := "URGENT event — triage it now. Investigate, act within your autonomy, propose anything beyond it, and escalate per your charter if needed. Report what you found and did.\n\n" + EventMessage(c, ev.Text, "")
		w, err := s.Spawn(ctx, Actor{Bot: ev.Bot, Role: config.BotRoleHome}, SpawnRequest{CaseID: c.ID, Brief: brief, byConductor: true, Attachments: attachmentIDs(atts)})
		if err != nil {
			note = strings.TrimSpace(note + "\n(conductor could not start a worker for this urgent event: " + err.Error() + " — handle it yourself.)")
		} else {
			note = strings.TrimSpace(note + fmt.Sprintf("\n(Urgent: conductor already started worker %s on this case; it will report here.)", w.ID))
		}
	}
	if rt != nil {
		note = strings.TrimSpace(RoutingNote(c) + "\n" + pre + "\n" + note)
	}
	if fresh := s.Ledger.Bot(ev.Bot).Cases[c.ID]; fresh != nil {
		c = *fresh // with its ticket id, minted when it was stored
	}
	text := EventMessage(c, ev.Text, note)
	if ev.ticket != nil && ev.Source == "ticket" {
		if c.Description == "" && ev.Text != "" {
			c.Description = ev.Text
		}
		text = TicketMessage(c, "assigned to you") + strings.TrimRight("\n"+note, "\n")
	}
	s.enqueueMsg(ev.Bot, store.InboxMsg{ID: msgID, CaseID: c.ID, Text: text, Attachments: attachmentIDs(atts)})
	return c.ID, nil
}

// ---- workers ----

// SpawnRequest is a worker launch.
type SpawnRequest struct {
	CaseID  string
	Title   string // for a new case when CaseID is empty
	Brief   string
	Handoff bool // a paseo hand-off worker (explicit opt-in)
	// Group is a fan-out label: workers spawned on the same case under the
	// same label form one group, which `conductor bot join` waits on and
	// which reports back as one combined result.
	Group string
	// Verbs narrows the child's verbs (patterns; the bot's own policy
	// still applies — this can only take away). ReadOnly holds it to
	// observe: anything else it calls becomes a proposal.
	Verbs    []string
	ReadOnly bool
	// Attachments are attachment ids the parent passes on: the worker sees
	// these files (read-only, fenced with their provenance) and no others.
	Attachments []string
	// consolidates are the log entries a consolidation worker is given
	// (archive.go).
	consolidates map[string]string
	// byConductor marks a worker conductor starts itself (an urgent
	// event's, a consolidation's): its parent is conductor, not a possibly
	// tainted home session, so it takes its case's provenance alone. Not
	// settable from an agent op.
	byConductor bool
}

// Spawn starts a worker for a case. Only a home session (or conductor
// itself) spawns; a worker cannot start more workers.
func (s *Service) Spawn(ctx context.Context, c Actor, req SpawnRequest) (store.Worker, error) {
	m, err := s.bot(c.Bot)
	if err != nil {
		return store.Worker{}, err
	}
	if c.Role != config.BotRoleHome {
		return store.Worker{}, errors.New("only the home session starts workers")
	}
	if strings.TrimSpace(req.Brief) == "" {
		return store.Worker{}, errors.New("a worker needs a brief")
	}
	if s.Workers == nil {
		return store.Worker{}, errors.New("no worker launcher on this daemon")
	}
	if req.Group != "" && !validLabel(req.Group) {
		return store.Worker{}, fmt.Errorf("group %q: use a short label of lowercase letters, digits, - or _", req.Group)
	}
	for _, v := range req.Verbs {
		if _, err := filepath.Match(v, "x"); err != nil || strings.TrimSpace(v) == "" {
			return store.Worker{}, fmt.Errorf("--verbs: bad pattern %q", v)
		}
	}
	if req.Group != "" && req.Handoff {
		return store.Worker{}, errors.New("a hand-off worker is not part of a fan-out group")
	}
	if req.Handoff && !m.Isolation.HandoffAllowed() {
		return store.Worker{}, fmt.Errorf("hand-off workers are off for bot %s (isolation.handoff_workers: false%s) — spawn a cli worker", c.Bot, teamSuffix(m))
	}
	if err := s.sandboxGate(c.Bot, m); err != nil {
		return store.Worker{}, err
	}
	now := s.now()
	var w store.Worker
	var cs store.Case
	err = s.Ledger.UpdateBot(c.Bot, func(r *store.BotRecord) error {
		live := 0
		for _, x := range r.Workers {
			if liveWorker(x) {
				live++
			}
		}
		if live >= m.Worker.MaxWorkers() {
			return fmt.Errorf("bot %s already has %d live workers (worker.max_concurrent)", c.Bot, live)
		}
		// The parent's own trust right now: a home session acts on the
		// least-trusted open request (homeAttribution). The child inherits
		// the less trusted of that and its case's requester — never more.
		pClass, pWho, _ := homeAttribution(r)
		if req.byConductor {
			pClass, pWho = ReqBot, ""
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
			r.Cases[caseID] = &store.Case{ID: caseID, Title: title, Status: store.TicketInProgress, Source: "bot",
				Requester: pWho, RequesterClass: pClass, Created: now, Updated: now}
		}
		cc := r.Cases[caseID]
		if cc == nil {
			return fmt.Errorf("no case %s", caseID)
		}
		// THE DISPATCH GATE: no worker starts on a ticket that isn't ready.
		if err := dispatchable(cc); err != nil {
			return err
		}
		for _, id := range req.Attachments {
			if a := liveAttachments(*r, []string{id}); len(a) == 0 || !a[0].Removed.IsZero() {
				return fmt.Errorf("--attach %s: no such attachment on bot %s (or it was removed)", id, c.Bot)
			}
		}
		onCase := 0
		for _, x := range r.Workers {
			if x.CaseID == caseID && liveWorker(x) {
				onCase++
			}
		}
		if onCase >= m.Worker.PerCase() {
			return fmt.Errorf("case %s already has %d live workers (worker.max_per_case)", caseID, onCase)
		}
		wid := r.NextID("w")
		hid := fmt.Sprintf("m-%s-%s-%s", c.Bot, wid, strconv.FormatInt(now.UnixNano(), 36))
		tClass, tWho := lessTrusted(cc.RequesterClass, cc.Requester, pClass, pWho)
		parent := "conductor"
		if !req.byConductor {
			parent = "home"
			if r.Home != nil && r.Home.Dispatch == c.Dispatch {
				parent = "home:" + r.Home.ID
			}
		}
		w = store.Worker{ID: wid, CaseID: caseID, Brief: req.Brief, Handoff: req.Handoff, HistoryID: hid,
			Dispatch: hid + ":" + config.BotWorkerStepID, Cwd: s.sessionDir(c.Bot, wid), Status: "starting", Started: now,
			Parent: parent, Requester: tWho, RequesterClass: tClass, Verbs: req.Verbs, ReadOnly: req.ReadOnly,
			Consolidates: req.consolidates, Attachments: req.Attachments}
		if r.Workers == nil {
			r.Workers = map[string]*store.Worker{}
		}
		if req.Group != "" {
			g := openGroup(r, caseID, req.Group)
			if g == nil {
				g = &store.Group{ID: r.NextID("g"), CaseID: caseID, Label: req.Group, Created: now}
				if r.Groups == nil {
					r.Groups = map[string]*store.Group{}
				}
				r.Groups[g.ID] = g
			}
			g.Workers = append(g.Workers, wid)
			w.Group = g.ID
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
		_ = s.Ledger.UpdateBot(c.Bot, func(r *store.BotRecord) error {
			if x := r.Workers[w.ID]; x != nil {
				x.Status, x.Ended, x.Result = "failed", s.now(), err.Error()
			}
			return nil
		})
		return store.Worker{}, err
	}
	repo := s.Repo(c.Bot)
	if err := repo.AddWorktree(ctx, w.Cwd); err != nil {
		return fail(fmt.Errorf("worker worktree: %w", err))
	}
	trig := config.BotWorkerTrigger(c.Bot)
	if req.Handoff {
		trig = config.BotHandoffTrigger(c.Bot)
	}
	prompt := WorkerPrompt(s.Cfg(), c.Bot, &cs, w.ID, req.Brief, s.MemoryIndex(ctx, c.Bot, cs.Title+" "+req.Brief))
	if len(req.Attachments) > 0 {
		by := "Your parent, the home session,"
		if req.byConductor {
			by = "Conductor"
		}
		prompt += "\n## Files passed to you\n" + AttachmentSection(liveAttachments(s.Ledger.Bot(c.Bot), req.Attachments), nil, by)
	}
	inputs := map[string]any{
		"prompt":                prompt,
		"workdir":               w.Cwd,
		"worker":                w.ID,
		dispatch.BotContextCase: w.CaseID,
	}
	if err := s.Workers.Fire(ctx, trig, w.HistoryID, inputs); err != nil {
		_ = repo.RemoveWorktree(ctx, w.Cwd)
		return fail(fmt.Errorf("start worker: %w", err))
	}
	s.audit(map[string]any{"event": "bot_worker", "bot": c.Bot, "worker": w.ID, "case": w.CaseID, "handoff": req.Handoff, "group": w.Group,
		"parent": w.Parent, "requester": w.Requester, "requester_class": w.RequesterClass, "verbs": w.Verbs, "read_only": w.ReadOnly,
		"attachments": w.Attachments, "action": "spawn"})
	s.logf("%s: worker %s started on case %s", c.Bot, w.ID, w.CaseID)
	return w, nil
}

// findWorker locates a worker by dispatch id or history id across bots.
func (s *Service) findWorker(dispatchID, historyID string) (string, store.Worker, bool) {
	for _, name := range s.Cfg().BotNames() {
		rec := s.Ledger.Bot(name)
		for _, w := range rec.Workers {
			if (dispatchID != "" && w.Dispatch == dispatchID) || (historyID != "" && w.HistoryID == historyID) {
				return name, *w, true
			}
		}
	}
	return "", store.Worker{}, false
}

// SessionBinding is what the sandbox needs to start a bot session's agent
// in a cwd: who it is (Identity, keying its sandbox state), and the binding
// the conductor token it mints for this launch must carry — bot, role,
// case, and the DISPATCH, which is what a rotation or a finish revokes by.
type SessionBinding struct {
	Identity, Bot, Role, Session, Dispatch, CaseID string
	// ReadOnly are paths the sandbox binds into the session READ-ONLY: a
	// home session's attachments dir; a worker's passed attachments only.
	ReadOnly []string
}

// SessionForCwd is the cwd → session lookup conductor's sandbox endpoint
// consults per launch (`conductor sandbox exec` resolves its cwd here and
// mints a fresh token bound to the answer): a bot's LIVE home session, or
// a live hand-off worker. ok=false for any other cwd — a retired session's
// included — so a stale or foreign cwd never inherits a bot binding, and
// a token minted for a retired session cannot be minted again. Every live
// bot session has its own cwd, so the answer is unambiguous.
func (s *Service) SessionForCwd(cwd string) (SessionBinding, bool) {
	clean := filepath.Clean(cwd)
	if clean == "" || clean == "." {
		return SessionBinding{}, false
	}
	for _, name := range s.Cfg().BotNames() {
		rec := s.Ledger.Bot(name)
		if h := rec.Home; h != nil && filepath.Clean(h.Cwd) == clean {
			return SessionBinding{Identity: SessionIdentity(name, h.ID), Bot: name, Role: config.BotRoleHome,
				Session: h.ID, Dispatch: h.Dispatch, ReadOnly: []string{s.Cfg().BotAttachmentsDir(name)}}, true
		}
		for _, w := range rec.Workers {
			if w.Handoff && (w.Status == "starting" || w.Status == "running") && filepath.Clean(w.Cwd) == clean {
				var ro []string
				for _, a := range liveAttachments(rec, w.Attachments) {
					if a.Removed.IsZero() {
						ro = append(ro, a.Path)
					}
				}
				return SessionBinding{Identity: SessionIdentity(name, w.ID), Bot: name, Role: config.BotRoleWorker,
					Session: w.ID, Dispatch: w.Dispatch, CaseID: w.CaseID, ReadOnly: ro}, true
			}
		}
	}
	return SessionBinding{}, false
}

// IdentityForCwd is SessionForCwd's identity alone.
func (s *Service) IdentityForCwd(cwd string) (string, bool) {
	b, ok := s.SessionForCwd(cwd)
	return b.Identity, ok
}

// SessionIdentity is a bot session's sandbox identity (path-safe: bot
// names and ids are [a-z0-9_-]).
func SessionIdentity(bot, id string) string { return "bot-" + bot + "-" + id }

// IsBotDispatch reports whether a dispatch id belongs to a bot
// session, and which role.
func (s *Service) IsBotDispatch(dispatchID string) (bot, role string) {
	if dispatchID == "" {
		return "", ""
	}
	for _, name := range s.Cfg().BotNames() {
		rec := s.Ledger.Bot(name)
		if rec.Home != nil && rec.Home.Dispatch == dispatchID {
			return name, config.BotRoleHome
		}
		for _, w := range rec.Workers {
			if w.Dispatch == dispatchID {
				return name, config.BotRoleWorker
			}
		}
	}
	return "", ""
}

// WorkerDone records a worker's step.done: the ledger, the episodic log, and
// a report into the home inbox. handled=false for a dispatch that is not a
// bot worker.
func (s *Service) WorkerDone(ctx context.Context, dispatchID, reason string, output any) bool {
	name, w, ok := s.findWorker(dispatchID, "")
	if !ok {
		return false
	}
	summary := summarizeOutput(reason, output)
	s.finishWorker(ctx, name, w, "done", summary, boundedOutput(output))
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
		_ = s.Ledger.UpdateBot(name, func(r *store.BotRecord) error {
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
			s.finishWorker(ctx, name, w, st, oneLine("the run ended ("+status+") without the worker reporting step.done. "+detail, 600), nil)
			s.recordSweepCost(name, w, historyID)
			return
		}
		s.recordSweepCost(name, w, historyID)
		if !w.Handoff {
			_ = s.Repo(name).RemoveWorktree(ctx, w.Cwd)
		}
	}
}

func (s *Service) finishWorker(ctx context.Context, name string, w store.Worker, status, summary string, output json.RawMessage) {
	var cs store.Case
	already := false
	var group *store.Group // set when this finish completes its group
	var bots []store.Worker
	_ = s.Ledger.UpdateBot(name, func(r *store.BotRecord) error {
		x := r.Workers[w.ID]
		if x == nil {
			return nil
		}
		if !liveWorker(x) {
			already = true
			return nil
		}
		x.Status, x.Ended, x.Result, x.Output = status, s.now(), summary, output
		if c := r.Cases[x.CaseID]; c != nil {
			c.Updated = s.now()
			cs = *c
		}
		// The last of a group to finish reports for all of them — decided
		// inside this transaction, so exactly one finish does.
		if g := r.Groups[x.Group]; g != nil && !g.Reported && groupDone(r, g) {
			g.Reported = true
			cp := *g
			group = &cp
			for _, id := range g.Workers {
				if gw := r.Workers[id]; gw != nil {
					bots = append(bots, *gw)
				}
			}
		}
		return nil
	})
	if already {
		return
	}
	// The worker is finished: its token goes now, not when it ages out.
	s.revoke(w.Dispatch)
	if w.Sweep != "" {
		s.finishSweep(ctx, name, w, status, summary, output)
		_ = s.Repo(name).RemoveWorktree(ctx, w.Cwd)
		return
	}
	repo := s.Repo(name)
	created := cs.Created
	if created.IsZero() {
		created = s.now()
	}
	ep := fmt.Sprintf("## %s — worker %s %s\n\nBrief: %s\n\nResult: %s\n", s.now().UTC().Format(time.RFC3339), w.ID, status, oneLine(w.Brief, 600), summary)
	if _, err := repo.AppendLog(ctx, repo.EpisodePath(w.CaseID, cs.Title, created), logFM(cs), ep,
		fmt.Sprintf("episode: case %s worker %s %s", w.CaseID, w.ID, status), map[string]string{"Bot-Case": w.CaseID, "Bot-Worker": w.ID}); err != nil {
		s.logf("%s: episode for worker %s: %v", name, w.ID, err)
	}
	s.audit(map[string]any{"event": "bot_worker", "bot": name, "worker": w.ID, "case": w.CaseID, "group": w.Group, "action": status})
	s.consolidationDone(ctx, name, w, status)
	switch {
	case isTriage(cs):
		// A triage worker reports by assigning; the lead's conversation is
		// not told about every routed event. Unassigned, it falls back.
		if cs.Routing.State == "triage" {
			s.triageFallback(ctx, name, cs.ID, "the triage worker finished ("+status+") without assigning")
		}
	case w.Group == "":
		s.enqueue(name, w.CaseID, fmt.Sprintf("[conductor] worker %s on case %s finished (%s):\n%s", w.ID, w.CaseID, status, fence("WORKER REPORT", summary)))
	case group != nil:
		s.enqueue(name, w.CaseID, GroupReport(*group, bots))
	}
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

// logFM is a case's log entry frontmatter.
func logFM(cs store.Case) map[string]any {
	t := cs.Title
	if t == "" {
		t = "case " + cs.ID
	}
	return map[string]any{"title": oneLine(t, 200), "case": cs.ID}
}

// MemoryWrite is a bot session's `conductor bot remember`: a document of a
// kind. Path, when empty, is derived from the kind and a slug (Slug, else
// the title — the frontmatter's or Title).
type MemoryWrite struct {
	Kind, Path, Slug, Title, Text string
	Append                        bool
	CaseID                        string
}

// WriteMemory writes a memory document for a bot session, through the
// kinds' chokepoint as WriterBot.
func (s *Service) WriteMemory(ctx context.Context, c Actor, mw MemoryWrite) (string, string, error) {
	if _, err := s.bot(c.Bot); err != nil {
		return "", "", err
	}
	repo := s.Repo(c.Bot)
	path, err := resolveDocPath(repo.Kinds(), mw.Kind, mw.Path, mw.Slug, mw.Title, mw.Text)
	if err != nil {
		return "", "", err
	}
	if strings.HasPrefix(path, DirHandoff+"/") {
		return "", "", errors.New("handoff notes are written with `conductor bot handoff`")
	}
	if strings.HasPrefix(path, DirLog+"/") || strings.HasPrefix(path, DirEpisodes+"/") {
		return "", "", errors.New("the log is written with `conductor bot episode --case <id>`")
	}
	if _, err := repo.cleanPath(path); err != nil {
		return "", "", err
	}
	if k, ok := repo.Kinds().Of(path); ok && mw.Kind != "" && k.Name != mw.Kind {
		return "", "", fmt.Errorf("%s is a %s path (%s/); a %s document lives elsewhere", path, k.Name, k.Dir, mw.Kind)
	}
	mode := "replace"
	if mw.Append {
		mode = "append"
	}
	caseID := mw.CaseID
	if caseID == "" {
		caseID = c.CaseID
	}
	sha, err := repo.Write(ctx, WriterBot, path, mw.Text, mode, "memory: "+path,
		map[string]string{"Bot-Case": caseID, "Bot-Role": c.Role, "Bot-Dispatch": c.Dispatch})
	if err == nil {
		s.audit(map[string]any{"event": "bot_memory", "bot": c.Bot, "path": path, "commit": sha, "role": c.Role})
	}
	return path, sha, err
}

// resolveDocPath is where a document goes: the path given (checked against
// the kind named, if any), or one derived from the kind and a slug — the
// slug given, else the title (given, or the frontmatter's).
func resolveDocPath(ks *Kinds, kind, path, slug, title, text string) (string, error) {
	if path != "" {
		c, err := CleanPath(path)
		if err != nil {
			return "", err
		}
		if kind != "" {
			k, err := ks.Get(kind)
			if err != nil {
				return "", err
			}
			if err := k.CheckPath(c); err != nil {
				return "", err
			}
		}
		return c, nil
	}
	if kind == "" {
		return "", errors.New("name the document: --kind <kind> (and a --title or --slug), or --path")
	}
	k, err := ks.Get(kind)
	if err != nil {
		return "", err
	}
	if slug == "" {
		t := title
		if t == "" {
			if fm, _, ok, _ := SplitFrontmatter(text); ok {
				t, _ = fm["title"].(string)
				if t == "" {
					t, _ = fm["what"].(string)
				}
			}
		}
		slug = Slug(t)
	}
	if slug == "" {
		return "", fmt.Errorf("a %s document needs a --slug or a title to derive its path from", k.Name)
	}
	return k.Derive(slug)
}

// Episode appends to a case's episode.
func (s *Service) Episode(ctx context.Context, c Actor, caseID, text string) (string, error) {
	if caseID == "" {
		caseID = c.CaseID
	}
	rec := s.Ledger.Bot(c.Bot)
	cs := rec.Cases[caseID]
	if cs == nil {
		return "", fmt.Errorf("no case %q", caseID)
	}
	if max := s.Cfg().Bots[c.Bot].Memory.MaxBytesOf("episode"); len(text) > max {
		return "", fmt.Errorf("episode entry is %d bytes, over its cap of %d bytes (memory.max_bytes.episode) — write what happened in fewer words; durable facts belong in knowledge/ (`conductor bot remember`)", len(text), max)
	}
	repo := s.Repo(c.Bot)
	entry := fmt.Sprintf("## %s — %s\n\n%s\n", s.now().UTC().Format(time.RFC3339), c.Role, strings.TrimSpace(text))
	return repo.AppendLog(ctx, repo.EpisodePath(cs.ID, cs.Title, cs.Created), logFM(*cs), entry, "episode: case "+cs.ID, map[string]string{"Bot-Case": cs.ID, "Bot-Role": c.Role})
}

// Handoff records the home session's handoff note.
func (s *Service) Handoff(ctx context.Context, c Actor, text string) (string, error) {
	if c.Role != config.BotRoleHome {
		return "", errors.New("only the home session writes handoff notes")
	}
	rec := s.Ledger.Bot(c.Bot)
	sid := "unknown"
	if rec.Home != nil && rec.Home.Dispatch == c.Dispatch {
		sid = rec.Home.ID
	}
	repo := s.Repo(c.Bot)
	if strings.TrimSpace(text) == "" {
		return "", errors.New("empty handoff")
	}
	if max := s.Cfg().Bots[c.Bot].Memory.MaxBytesOf("handoff"); len(text) > max {
		return "", fmt.Errorf("handoff is %d bytes, over its cap of %d bytes (memory.max_bytes.handoff) — rewrite it shorter and run `conductor bot handoff` again: what is in flight, what you promised whom, what to watch; durable facts belong in knowledge/ (`conductor bot remember`) and are in your next session's index", len(text), max)
	}
	return repo.AppendLog(ctx, repo.HandoffPath(sid), map[string]any{"session": sid}, "# Handoff from session "+sid+"\n\n"+text, "handoff: session "+sid, map[string]string{"Bot-Session": sid})
}

// CaseOpen opens a case from the home session (the operator asked for
// something in conversation, or the bot noticed something).
func (s *Service) CaseOpen(c Actor, title, summary string) (store.Case, error) {
	if c.Role != config.BotRoleHome {
		return store.Case{}, errors.New("only the home session opens cases")
	}
	if strings.TrimSpace(title) == "" {
		return store.Case{}, errors.New("a case needs a title")
	}
	now := s.now()
	var out store.Case
	err := s.Ledger.UpdateBot(c.Bot, func(r *store.BotRecord) error {
		id := r.NextID("c")
		if r.Cases == nil {
			r.Cases = map[string]*store.Case{}
		}
		cc := &store.Case{ID: id, Title: oneLine(title, 200), Summary: summary, Status: store.TicketInProgress, Source: "bot",
			RequesterClass: ReqBot, Created: now, Updated: now}
		r.Cases[id] = cc
		out = *cc
		return nil
	})
	return out, err
}

// CaseNote appends to a case's running summary.
func (s *Service) CaseNote(c Actor, caseID, text string) error {
	if c.Role == config.BotRoleWorker && caseID != c.CaseID {
		return errors.New("a worker notes only its own case")
	}
	return s.Ledger.UpdateBot(c.Bot, func(r *store.BotRecord) error {
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
// A delegated case's outcome goes back to its delegator.
func (s *Service) CaseClose(ctx context.Context, c Actor, caseID, outcome string) error {
	if c.Role != config.BotRoleHome {
		return errors.New("only the home session closes cases")
	}
	cs, err := s.closeCase(ctx, c.Bot, caseID, outcome, store.TicketDone, "bot:"+c.Bot)
	if cs.ID != "" { // closed, even if its episode could not be written
		s.reportDelegation(c.Bot, cs, DelegationDone, outcome)
	}
	return err
}

// closeCase closes a case and writes its outcome to the episodic log.
func (s *Service) closeCase(ctx context.Context, bot, caseID, outcome, status, by string) (store.Case, error) {
	var cs store.Case
	err := s.Ledger.UpdateBot(bot, func(r *store.BotRecord) error {
		cc := r.Cases[caseID]
		if cc == nil {
			return fmt.Errorf("no case %q", caseID)
		}
		if store.Closed(cc.Status) {
			// Once: a second close would report a delegation twice.
			return fmt.Errorf("case %s (ticket %s) is already %s", caseID, cc.Ticket, cc.Status)
		}
		if cc.Status != status {
			addHistory(cc, s.now(), by, cc.Status+" → "+status+": "+oneLine(outcome, 300))
		}
		cc.Status, cc.Outcome, cc.Updated = status, outcome, s.now()
		// A closed case's spend limit allows nothing; reopened, an operator
		// confirms it again.
		cc.SpendFor = ""
		cs = *cc
		return nil
	})
	if err != nil {
		return store.Case{}, err
	}
	// A closed ticket's purchases count toward no open cap: reclaim them from the
	// spend ledger so the ledger does not grow a record per purchase forever (#1).
	// Keyed by the ticket's stable id; a plain (non-ticket) case has none.
	if s.Spend != nil && cs.Ticket != "" {
		if err := s.Spend.CloseTicket(cs.Ticket); err != nil {
			s.logf("%s: prune purchases for closed ticket %s: %v", bot, cs.Ticket, err)
		}
	}
	repo := s.Repo(bot)
	entry := fmt.Sprintf("## %s — closed\n\nSource: %s · requester: %s (%s)\n\nOutcome: %s\n", s.now().UTC().Format(time.RFC3339), cs.Source, orDash(cs.Requester), cs.RequesterClass, strings.TrimSpace(outcome))
	_, err = repo.AppendLog(ctx, repo.EpisodePath(cs.ID, cs.Title, cs.Created), logFM(cs), entry, "episode: case "+cs.ID+" closed", map[string]string{"Bot-Case": cs.ID})
	return cs, err
}

// Cases lists a bot's cases (open first, newest first).
func (s *Service) Cases(bot string, all bool) []store.Case {
	rec := s.Ledger.Bot(bot)
	var out []store.Case
	for _, c := range rec.Cases {
		if all || !store.Closed(c.Status) {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if store.Closed(out[i].Status) != store.Closed(out[j].Status) {
			return !store.Closed(out[i].Status)
		}
		return out[i].Created.After(out[j].Created)
	})
	return out
}

// Unsettled reports a proposal state that will still change on its own:
// being planned, awaiting a decision, or approved and being applied.
func Unsettled(status string) bool {
	return status == "planning" || status == "pending" || status == "approved"
}

// Wait blocks until a proposal is decided (or timeout).
func (s *Service) Wait(ctx context.Context, bot, id string, timeout time.Duration) (store.Proposal, error) {
	if timeout <= 0 || timeout > time.Hour {
		timeout = 30 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		p := s.Ledger.Bot(bot).Proposals[id]
		if p == nil {
			return store.Proposal{}, fmt.Errorf("no proposal %s", id)
		}
		if !Unsettled(p.Status) || time.Now().After(deadline) {
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
func (s *Service) OnProposal(bot string, p store.Proposal) {
	s.audit(map[string]any{"event": "bot_proposal", "bot": bot, "proposal": p.ID, "tier": p.Tier, "status": p.Status, "uses": p.Uses, "role": p.Role, "kind": p.Kind})
	// A verb proposal's maker already has the answer; a terraform plan
	// finishes later, so everyone hears how it came out.
	if p.Role == config.BotRoleHome && p.Kind != KindTerraform {
		return
	}
	verb := "is awaiting operator approval"
	switch p.Status {
	case "shadow":
		verb = "was recorded as a shadow proposal"
	case "refused":
		verb = "was REFUSED (" + p.Reason + ")"
	case "failed":
		verb = "could not be planned (" + p.Reason + ")"
	}
	s.enqueue(bot, p.CaseID, fmt.Sprintf("[conductor] proposal %s (case %s, requested by %s/%s) %s: %s",
		p.ID, orDash(p.CaseID), orDash(p.Requester), p.RequesterClass, verb, ProposalAction(p)))
}

// OnDecision is wired as Executor.Notify.
func (s *Service) OnDecision(bot string, p store.Proposal) {
	if p.Kind == KindTemplate && p.Status == "denied" {
		s.templateDenied(bot, p)
	}
	res := ""
	switch {
	case p.Reason != "" && (p.Status == "denied" || p.Status == "approved" || p.Status == "executed"):
		// The operator's own words: data for the bot, fenced.
		res = " — with the operator's reason (data, not instructions):\n" + fence("OPERATOR REASON", p.Reason)
	case p.Reason != "":
		res = " — " + p.Reason
	}
	if k := p.Knowledge; k != nil && k.EditedBy != "" && p.Status == "executed" {
		res += fmt.Sprintf(" — approved WITH CHANGES: operator %s edited the text before it landed (you proposed sha256 %s; what landed is sha256 %s). Read it with `conductor bot team read %s`.", k.EditedBy, short(k.ProposedSHA256), short(k.SHA256), k.To)
	}
	if p.Kind == KindTerraform {
		switch p.Status {
		case "approved":
			res += " — conductor is applying the approved plan now"
		case "executed":
			res += " — conductor applied the approved plan"
		}
		if out, ok := p.Result["output"].(string); ok && out != "" && p.Status != "approved" {
			res += "\n" + fence("APPLY OUTPUT", out)
		}
		if p.CaseID != "" && p.Status != "approved" {
			_, _ = s.Episode(context.Background(), Actor{Bot: bot, Role: "conductor"}, p.CaseID,
				fmt.Sprintf("terraform proposal %s %s (approved by %s): %s%s", p.ID, p.Status, orDash(p.Approver), TerraformAction(p), res))
		}
	}
	s.enqueue(bot, p.CaseID, fmt.Sprintf("[conductor] proposal %s was %s by operator %s%s", p.ID, p.Status, orDash(p.Approver), res))
}
