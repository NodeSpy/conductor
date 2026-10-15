package member

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/store"
)

// PROACTIVE SWEEPS. A member with `proactive:` looks around on its own every
// `every`: one cli worker (the synthesized member:<name>:sweep trigger) works
// through the operator's checklist and whatever the member's memory says is
// worth watching, and reports findings through step.done.
//
// Read-only is structural: the sweep worker's token is bound to its sweep,
// the gate attributes its calls to requester class `proactive`, and that class
// never runs anything above observe — a mutating call becomes a proposal (a
// shadow one in shadow mode), exactly as for a third party's request.
//
// Conductor, not the agent, decides what reaches the operator: each finding
// is deduplicated against open cases by its key; a new finding at or above
// notify_at opens a case and notifies (the home conversation, plus the
// conductor.member_notify lifecycle event for any connector to alert on);
// one below it is recorded in the ledger and memory and stays quiet. A sweep
// with no findings is one ledger line.
//
// A sweep never starts during quiet hours, while another sweep runs, or past
// the budget (runs and cost over a rolling 24 hours): it is skipped, and the
// skip is logged and audited once per reason.

// SweepWindow is the rolling window the proactive budget counts over.
const SweepWindow = 24 * time.Hour

// maxFindings bounds what one sweep can report.
const maxFindings = 20

// NotifyEvent is the conductor.* lifecycle event a notified finding emits.
const NotifyEvent = "member_notify"

// Notice is one proactive notification.
type Notice struct {
	Member, Sweep, CaseID, Severity, Title, Key, Text string
}

// ErrSweepSkipped marks a sweep that was not started by policy (quiet
// hours, budget, one already running) — not a failure.
var ErrSweepSkipped = errors.New("sweep skipped")

// maybeSweep starts a due sweep. Called from Tick.
func (s *Service) maybeSweep(ctx context.Context, name string, m config.MemberConfig) {
	p := m.Proactive
	if p == nil || s.Workers == nil {
		return
	}
	rec := s.Ledger.Member(name)
	if last := lastSweepStart(rec); !last.IsZero() && s.now().Sub(last) < p.Every.D() {
		return
	}
	if _, err := s.StartSweep(ctx, name, false); err != nil {
		s.noteSkip(name, err)
	}
}

func lastSweepStart(rec store.MemberRecord) time.Time {
	var t time.Time
	for _, sw := range rec.Sweeps {
		if sw.Started.After(t) {
			t = sw.Started
		}
	}
	return t
}

// noteSkip logs and audits a skipped sweep once per distinct reason, so a
// budget-exhausted member does not log every tick.
func (s *Service) noteSkip(name string, err error) {
	s.mu.Lock()
	if s.skipped == nil {
		s.skipped = map[string]string{}
	}
	same := s.skipped[name] == err.Error()
	s.skipped[name] = err.Error()
	s.mu.Unlock()
	if same {
		return
	}
	s.logf("%s: proactive sweep not started: %v", name, err)
	s.audit(map[string]any{"event": "member_sweep", "member": name, "action": "skipped", "reason": err.Error()})
}

// sweepBudget reports why a new sweep would exceed the budget ("" = fits).
func sweepBudget(p *config.MemberProactive, rec store.MemberRecord, now time.Time) string {
	runs, cost := 0, 0.0
	for _, sw := range rec.Sweeps {
		if now.Sub(sw.Started) < SweepWindow {
			runs++
			cost += sw.CostUSD
		}
	}
	if b := p.Budget.MaxRunsPerDay; b > 0 && runs >= b {
		return fmt.Sprintf("budget: %d sweeps in the last 24h (max_runs_per_day %d)", runs, b)
	}
	if b := p.Budget.MaxCostPerDay; b > 0 && cost >= b {
		return fmt.Sprintf("budget: sweeps cost $%.2f in the last 24h (max_cost_per_day $%.2f)", cost, b)
	}
	return ""
}

// StartSweep starts one sweep now. byOperator (`conductor member sweep`)
// skips the quiet-hours check, never the budget or the one-at-a-time rule.
func (s *Service) StartSweep(ctx context.Context, name string, byOperator bool) (store.Sweep, error) {
	m, err := s.member(name)
	if err != nil {
		return store.Sweep{}, err
	}
	p := m.Proactive
	if p == nil {
		return store.Sweep{}, fmt.Errorf("member %s has no proactive: block", name)
	}
	if s.Workers == nil {
		return store.Sweep{}, errors.New("no worker launcher on this daemon")
	}
	now := s.now()
	if !byOperator && p.Quiet(now) {
		return store.Sweep{}, fmt.Errorf("%w: quiet hours (%s)", ErrSweepSkipped, p.QuietHours)
	}
	checklist, err := s.Cfg().MemberChecklist(name)
	if err != nil {
		return store.Sweep{}, err
	}
	var sw store.Sweep
	var w store.Worker
	var rec store.MemberRecord
	err = s.Ledger.UpdateMember(name, func(r *store.MemberRecord) error {
		for _, x := range r.Sweeps {
			if x.Status == "running" {
				return fmt.Errorf("%w: sweep %s is still running", ErrSweepSkipped, x.ID)
			}
		}
		if why := sweepBudget(p, *r, now); why != "" {
			return fmt.Errorf("%w: %s", ErrSweepSkipped, why)
		}
		live := 0
		for _, x := range r.Workers {
			if liveWorker(x) {
				live++
			}
		}
		if live >= m.Worker.MaxWorkers() {
			return fmt.Errorf("%w: %d live workers (worker.max_concurrent)", ErrSweepSkipped, live)
		}
		xid := r.NextID("x")
		wid := r.NextID("w")
		hid := fmt.Sprintf("m-%s-%s-%s", name, wid, strconv.FormatInt(now.UnixNano(), 36))
		w = store.Worker{ID: wid, Sweep: xid, Brief: "proactive sweep " + xid, HistoryID: hid,
			Dispatch: hid + ":" + config.MemberWorkerStepID, Cwd: s.sessionDir(name, wid), Status: "starting", Started: now,
			Parent: "conductor", Requester: "sweep " + xid, RequesterClass: ReqProactive, ReadOnly: true}
		sw = store.Sweep{ID: xid, Worker: wid, HistoryID: hid, Status: "running", Started: now}
		if r.Workers == nil {
			r.Workers = map[string]*store.Worker{}
		}
		if r.Sweeps == nil {
			r.Sweeps = map[string]*store.Sweep{}
		}
		r.Workers[wid] = &w
		r.Sweeps[xid] = &sw
		pruneSweeps(r)
		rec = *r
		return nil
	})
	if err != nil {
		return store.Sweep{}, err
	}
	s.mu.Lock()
	delete(s.skipped, name)
	s.mu.Unlock()
	fail := func(err error) (store.Sweep, error) {
		_ = s.Ledger.UpdateMember(name, func(r *store.MemberRecord) error {
			if x := r.Workers[w.ID]; x != nil {
				x.Status, x.Ended, x.Result = "failed", s.now(), err.Error()
			}
			if x := r.Sweeps[sw.ID]; x != nil {
				x.Status, x.Ended, x.Summary = "failed", s.now(), oneLine(err.Error(), 300)
			}
			return nil
		})
		s.logf("%s: proactive sweep %s failed to start: %v", name, sw.ID, err)
		return store.Sweep{}, err
	}
	repo := s.Repo(name)
	if err := repo.AddWorktree(ctx, w.Cwd); err != nil {
		return fail(fmt.Errorf("sweep worktree: %w", err))
	}
	inputs := map[string]any{
		"prompt":                   SweepPrompt(s.Cfg(), name, rec, sw.ID, w.ID, checklist),
		"workdir":                  w.Cwd,
		"worker":                   w.ID,
		dispatch.MemberContextCase: sw.ID,
	}
	if err := s.Workers.Fire(ctx, config.MemberSweepTrigger(name), w.HistoryID, inputs); err != nil {
		_ = repo.RemoveWorktree(ctx, w.Cwd)
		return fail(fmt.Errorf("start sweep: %w", err))
	}
	s.audit(map[string]any{"event": "member_sweep", "member": name, "sweep": sw.ID, "worker": w.ID, "action": "start", "by_operator": byOperator})
	s.logf("%s: proactive sweep %s started (worker %s)", name, sw.ID, w.ID)
	return sw, nil
}

// pruneSweeps keeps the ledger's sweep history bounded (the newest 200).
func pruneSweeps(r *store.MemberRecord) {
	if len(r.Sweeps) <= 200 {
		return
	}
	ids := make([]string, 0, len(r.Sweeps))
	for id := range r.Sweeps {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return r.Sweeps[ids[i]].Started.Before(r.Sweeps[ids[j]].Started) })
	for _, id := range ids[:len(ids)-200] {
		if r.Sweeps[id].Status != "running" {
			delete(r.Sweeps, id)
		}
	}
}

// recordSweepCost attaches a finished sweep run's cost (the run history's
// total) to the sweep — what the daily cost budget counts.
func (s *Service) recordSweepCost(name string, w store.Worker, historyID string) {
	if w.Sweep == "" || s.RunCost == nil {
		return
	}
	cost, ok := s.RunCost(historyID)
	if !ok {
		return
	}
	_ = s.Ledger.UpdateMember(name, func(r *store.MemberRecord) error {
		if sw := r.Sweeps[w.Sweep]; sw != nil {
			sw.CostUSD = cost
		}
		return nil
	})
}

// parseFindings reads a sweep's step.done output: {"summary": …,
// "findings": [{"key","title","severity","detail"}]}. Malformed entries are
// kept as best they can be: a missing key derives from the title, an unknown
// severity reads as medium.
func parseFindings(output json.RawMessage) (summary string, out []store.Finding) {
	var o struct {
		Summary  any              `json:"summary"`
		Findings []map[string]any `json:"findings"`
	}
	if len(output) == 0 || json.Unmarshal(output, &o) != nil {
		return "", nil
	}
	if o.Summary != nil {
		summary = oneLine(fmt.Sprint(o.Summary), 300)
	}
	for _, f := range o.Findings {
		if len(out) == maxFindings {
			break
		}
		title := oneLine(fmt.Sprint(orEmpty(f["title"])), 200)
		key := oneLine(fmt.Sprint(orEmpty(f["key"])), 120)
		if key == "" {
			key = Slug(title)
		}
		if title == "" && key == "" {
			continue
		}
		if title == "" {
			title = key
		}
		sev := strings.ToLower(strings.TrimSpace(fmt.Sprint(orEmpty(f["severity"]))))
		if config.SeverityRank(sev) < 0 {
			sev = "medium"
		}
		detail := strings.TrimSpace(fmt.Sprint(orEmpty(f["detail"])))
		if len(detail) > 2000 {
			detail = detail[:2000] + "…"
		}
		out = append(out, store.Finding{Key: key, Title: title, Severity: sev, Detail: detail})
	}
	return summary, out
}

func orEmpty(v any) any {
	if v == nil {
		return ""
	}
	return v
}

// finishSweep turns a finished sweep worker's report into ledger state:
// findings deduplicated against open cases by key, a case + notification
// for each new (or escalated) finding at or above the threshold, a memory
// record when there were findings, and one ledger line either way.
func (s *Service) finishSweep(ctx context.Context, name string, w store.Worker, status, summary string, output json.RawMessage) {
	m, err := s.member(name)
	if err != nil || m.Proactive == nil {
		return
	}
	threshold := config.SeverityRank(m.Proactive.NotifyLevel())
	sum, findings := parseFindings(output)
	if sum == "" {
		sum = oneLine(summary, 300)
	}
	now := s.now()
	var notices []Notice
	_ = s.Ledger.UpdateMember(name, func(r *store.MemberRecord) error {
		sw := r.Sweeps[w.Sweep]
		if sw == nil {
			return nil
		}
		for i := range findings {
			f := &findings[i]
			rank := config.SeverityRank(f.Severity)
			if c := openCaseByKey(r, f.Key); c != nil {
				// Already tracked: note it; notify only on an escalation
				// past the threshold.
				f.CaseID = c.ID
				c.Summary = strings.TrimSpace(c.Summary + "\n" + now.UTC().Format("01-02 15:04") + " sweep " + sw.ID + " saw it again (" + f.Severity + ")")
				c.Updated = now
				if rank > config.SeverityRank(c.Severity) {
					c.Severity = f.Severity
					if rank >= threshold {
						f.Notified = true
						notices = append(notices, Notice{Member: name, Sweep: sw.ID, CaseID: c.ID, Severity: f.Severity, Title: f.Title, Key: f.Key,
							Text: fmt.Sprintf("escalated to %s on case %s", strings.ToUpper(f.Severity), c.ID)})
					}
				}
				continue
			}
			if rank < threshold {
				continue // recorded quietly
			}
			cid := r.NextID("c")
			if r.Cases == nil {
				r.Cases = map[string]*store.Case{}
			}
			r.Cases[cid] = &store.Case{ID: cid, Title: oneLine("["+f.Severity+"] "+f.Title, 200), Status: "open", Source: "proactive",
				Requester: "sweep " + sw.ID, RequesterClass: ReqProactive, Summary: oneLine(f.Detail, 2000),
				Key: f.Key, Severity: f.Severity, Created: now, Updated: now}
			f.CaseID, f.Notified = cid, true
			notices = append(notices, Notice{Member: name, Sweep: sw.ID, CaseID: cid, Severity: f.Severity, Title: f.Title, Key: f.Key,
				Text: "case " + cid + " opened"})
		}
		sw.Findings, sw.Summary, sw.Ended = findings, sum, now
		switch {
		case status != "done":
			sw.Status = status
		case len(findings) > 0:
			sw.Status = "findings"
		default:
			sw.Status = "quiet"
		}
		return nil
	})
	s.audit(map[string]any{"event": "member_sweep", "member": name, "sweep": w.Sweep, "action": "finish", "status": status,
		"findings": len(findings), "notified": len(notices)})
	if len(findings) == 0 {
		// The quiet path: one ledger line (above), one journal line, nothing
		// reaches the operator.
		outcome := "quiet"
		if status != "done" {
			outcome = status
		}
		s.logf("%s: proactive sweep %s %s: %s", name, w.Sweep, outcome, oneLine(sum, 200))
		return
	}
	repo := s.Repo(name)
	var b strings.Builder
	fmt.Fprintf(&b, "## %s — proactive sweep %s (%s)\n\n%s\n\n", now.UTC().Format(time.RFC3339), w.Sweep, status, sum)
	for _, f := range findings {
		where := "recorded"
		if f.CaseID != "" {
			where = "case " + f.CaseID
		}
		fmt.Fprintf(&b, "- [%s] %s (key %s; %s)\n", f.Severity, f.Title, f.Key, where)
		if f.Detail != "" {
			fmt.Fprintf(&b, "  %s\n", oneLine(f.Detail, 600))
		}
	}
	path := fmt.Sprintf("%s/proactive/%s/%s.md", DirEpisodes, now.Format("2006/01"), now.Format("2006-01-02"))
	if _, err := repo.Write(ctx, path, b.String(), "append", "episode: proactive sweep "+w.Sweep, map[string]string{"Member-Sweep": w.Sweep}); err != nil {
		s.logf("%s: sweep %s memory: %v", name, w.Sweep, err)
	}
	for _, n := range notices {
		s.notifyFinding(ctx, n, findingDetail(findings, n.Key))
	}
}

func findingDetail(fs []store.Finding, key string) string {
	for _, f := range fs {
		if f.Key == key {
			return f.Detail
		}
	}
	return ""
}

// openCaseByKey finds the open case a finding key already tracks.
func openCaseByKey(r *store.MemberRecord, key string) *store.Case {
	if key == "" {
		return nil
	}
	for _, c := range r.Cases {
		if c.Status == "open" && c.Key == key {
			return c
		}
	}
	return nil
}

// notifyFinding tells the operator: into the home conversation, and as the
// conductor.member_notify lifecycle event (a trigger can alert anywhere).
func (s *Service) notifyFinding(ctx context.Context, n Notice, detail string) {
	body := n.Title
	if strings.TrimSpace(detail) != "" {
		body += "\n\n" + detail
	}
	s.enqueue(n.Member, n.CaseID, fmt.Sprintf("[conductor] proactive sweep %s found something (%s) — %s:\n%s",
		n.Sweep, strings.ToUpper(n.Severity), n.Text, fence("FINDING", body)))
	s.audit(map[string]any{"event": "member_notify", "member": n.Member, "sweep": n.Sweep, "case": n.CaseID, "severity": n.Severity, "key": n.Key, "title": n.Title})
	if s.Notify != nil {
		s.Notify(ctx, n)
	}
}

// Sweeps lists a member's sweeps, newest first.
func (s *Service) Sweeps(member string, limit int) []store.Sweep {
	rec := s.Ledger.Member(member)
	out := make([]store.Sweep, 0, len(rec.Sweeps))
	for _, sw := range rec.Sweeps {
		out = append(out, *sw)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
