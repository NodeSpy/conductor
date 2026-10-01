package connector

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/core"
)

// GitHub run progress (see progress.go for the contract). Two independent
// faces, each with its own off switch:
//
//   - reactions — on the comment or review the run is handling: 👀 when the
//     run starts (before any worktree or agent exists, so a run that dies
//     provisioning still shows it was picked up), then one outcome reaction
//     beside it: 🚀 pushed, 👍 finished without pushing, 😕 failed / gave up /
//     parked. 👀 stays: it records that the run happened, and the outcome
//     reads as its answer. Events with no comment or review (failing checks,
//     a merge conflict) carry no subject and get no reaction.
//   - status — a commit status on the PR's head, as you: `pending` while the
//     run works, then `success` / `failure`. Its context (the row's name on
//     the PR) is your login unless progress.status_context says otherwise —
//     never a tool name. A status belongs to a commit, so the final one goes
//     on the head as it is AFTER the run: on the new commit when it pushed,
//     with the old commit's pending resolved too, so nothing is left pending
//     on a commit the PR no longer shows.
//
// Both default ON for the "your PR" autopilot kinds (core.BranchFixKind) and
// OFF for everything else (a review run on someone else's PR must not stamp
// statuses on it). Precedence: the trigger's `options.progress` → the
// connector's `progress:` → that kind default.

// progressOptionDesc documents the trigger option (declared on every event).
const progressOptionDesc = "run progress on the PR: { reactions: bool, status: bool } — 👀 then 🚀/👍/😕 on the comment or review being handled, and a pending→success/failure commit status as you. Default on for the your-PR kinds (new_comment, changes_requested, failing_checks, merge_conflict, pr_behind), off otherwise"

// reactionSubjectsDesc documents the event context field.
const reactionSubjectsDesc = "what the run is handling, as [{kind, id}] for github.react: the review (kind review), the standalone comment (issue_comment | review_comment), or — sweep-recovered — each unresolved thread's opening comment (capped)"

// progressConf is the connector-level `progress:` block.
type progressConf struct {
	Reactions     *bool  `yaml:"reactions"`
	Status        *bool  `yaml:"status"`
	StatusContext string `yaml:"status_context"`
}

// checkProgressKeys rejects a `progress:` block with a key it doesn't know or
// a switch that isn't a boolean. status_context is connector-level only: one
// connector posts under one context, which its source's own-status guard
// must know.
func checkProgressKeys(m map[string]any, connection bool) error {
	for k, v := range m {
		switch k {
		case "reactions", "status":
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("%s: want true or false, got %v", k, v)
			}
		case "status_context":
			if !connection {
				return fmt.Errorf("status_context is set on the connector's progress: block, not per trigger")
			}
			if _, ok := v.(string); !ok {
				return fmt.Errorf("status_context: want a string, got %v", v)
			}
		default:
			return fmt.Errorf("unknown key %q (valid: reactions, status%s)", k, map[bool]string{true: ", status_context"}[connection])
		}
	}
	return nil
}

// Bounds on the API calls a lifecycle point may make: progress must never
// hold up provisioning, nor keep a finished run's goroutine around.
const (
	progressStartTimeout  = 10 * time.Second
	progressFinishTimeout = 20 * time.Second
	// progressForget is how long a finished run's record is kept for the
	// supersede check (see progressTracker).
	progressForget = 6 * time.Hour
)

// progressTracker decides who owns a PR's status row. The rule: the most
// recently STARTED run on a PR owns the row on the PR's current head. A run
// that finishes after a newer one started (on the same PR) never writes the
// current head — the newer run's pending, or its final word, stands — and
// only resolves its own starting commit, when that is not where the newer
// run's status sits. Last writer wins only among runs that each own the row
// at the moment they write. Process-local: a restart forgets, and the next
// run on the PR takes the row.
type progressTracker struct {
	mu   sync.Mutex
	next uint64
	prs  map[string]*prProgress
}

type prProgress struct {
	latest   uint64    // the newest started run
	sha      string    // the commit that run's pending is on
	finished time.Time // zero while the newest run is still going
}

// begin registers a run starting on key with its pending on sha.
func (pt *progressTracker) begin(key, sha string) uint64 {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	if pt.prs == nil {
		pt.prs = map[string]*prProgress{}
	}
	now := time.Now()
	for k, p := range pt.prs {
		if !p.finished.IsZero() && now.Sub(p.finished) > progressForget {
			delete(pt.prs, k)
		}
	}
	pt.next++
	pt.prs[key] = &prProgress{latest: pt.next, sha: sha}
	return pt.next
}

// finish reports whether run id is still the newest on key (it owns the
// row) and, when it is not, the commit the newer run's status is on.
func (pt *progressTracker) finish(key string, id uint64) (owner bool, newerSHA string) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	p := pt.prs[key]
	if p == nil || p.latest == id {
		if p != nil {
			p.finished = time.Now()
		}
		return true, ""
	}
	return false, p.sha
}

// progressFlags resolves the two faces for one run.
func (g *githubImpl) progressFlags(t core.Trigger, opts map[string]any) (reactions, status bool) {
	reactions, status = core.BranchFixKind(t.Kind), core.BranchFixKind(t.Kind)
	if v := g.conn.Progress.Reactions; v != nil {
		reactions = *v
	}
	if v := g.conn.Progress.Status; v != nil {
		status = *v
	}
	if v, ok := opts["reactions"]; ok {
		reactions = truthy(v)
	}
	if v, ok := opts["status"]; ok {
		status = truthy(v)
	}
	return reactions, status
}

// ghProgress is one run's progress on one PR.
type ghProgress struct {
	g          *githubImpl
	t          core.Trigger
	key        string
	subjects   []any
	reactions  bool
	status     bool
	statusCtx  string // the context the pending went out under ("" = none posted)
	startSHA   string
	id         uint64
	finishOnce sync.Once
}

// StartProgress implements ProgressReporter.
func (g *githubImpl) StartProgress(ctx context.Context, run ProgressRun) Progress {
	t := run.Trigger
	if t.Source != "github" || t.Target.Repo == "" || t.Target.Number == 0 {
		return nil
	}
	reactions, status := g.progressFlags(t, run.Options)
	if !reactions && !status {
		return nil
	}
	p := &ghProgress{g: g, t: t, key: strings.ToLower(t.Target.Repo) + "#" + fmt.Sprint(t.Target.Number),
		reactions: reactions, status: status}
	if reactions {
		p.subjects = progressSubjects(append([]core.Trigger{t}, run.Batch...))
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), progressStartTimeout)
	defer cancel()
	// The head as it is now, not as the event saw it: a comment event carries
	// no head, and a run that waited for a slot may be on a newer commit.
	// It is also the baseline "did the run push?" compares against.
	sha, state, err := g.kit.PRHead(ctx, "me", t.Target.Repo, t.Target.Number)
	if err != nil {
		g.progressFailed(t, "read_head", err)
		sha = t.Target.HeadSHA
	} else if state != "" && state != "open" {
		return nil // nothing to show progress on
	}
	p.startSHA = sha
	if len(p.subjects) > 0 {
		p.react(ctx, "eyes")
	}
	if status && sha != "" {
		if c, err := g.statusContext(ctx, t.Target.Repo); err != nil {
			g.progressFailed(t, "status_context", err)
		} else if p.setStatus(ctx, c, sha, "pending", progressWorking(t)) {
			p.statusCtx = c
		}
	}
	p.id = g.progress.begin(p.key, p.startSHA)
	return p
}

// Finish implements Progress.
func (p *ghProgress) Finish(ctx context.Context, o RunOutcome) {
	p.finishOnce.Do(func() { p.finish(ctx, o) })
}

func (p *ghProgress) finish(ctx context.Context, o RunOutcome) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), progressFinishTimeout)
	defer cancel()
	owner, newerSHA := p.g.progress.finish(p.key, p.id)
	if o.Result == OutcomeStopped {
		// The PR closed under the run: no verdict to give, but don't leave
		// its last commit pending forever.
		if p.statusCtx != "" && p.startSHA != "" && (owner || p.startSHA != newerSHA) {
			p.setStatus(ctx, p.statusCtx, p.startSHA, "success", "stopped — the PR closed")
		}
		return
	}
	head := ""
	if h, _, err := p.g.kit.PRHead(ctx, "me", p.t.Target.Repo, p.t.Target.Number); err != nil {
		p.g.progressFailed(p.t, "read_head", err)
	} else {
		head = h
	}
	pushed := p.startSHA != "" && head != "" && head != p.startSHA
	if len(p.subjects) > 0 {
		switch {
		case o.Result != OutcomeOK:
			p.react(ctx, "confused")
		case pushed:
			p.react(ctx, "rocket")
		default:
			p.react(ctx, "+1")
		}
	}
	if !p.status {
		return
	}
	statusCtx := p.statusCtx
	if statusCtx == "" {
		// The pending never went out (the head or the login couldn't be
		// read then); the verdict still should.
		c, err := p.g.statusContext(ctx, p.t.Target.Repo)
		if err != nil {
			p.g.progressFailed(p.t, "status_context", err)
			return
		}
		statusCtx = c
	}
	state, desc := progressVerdict(o, pushed, head)
	if !owner {
		// A newer run on this PR owns the row: leave the current head (and
		// the commit its status is on) to it. Only this run's own starting
		// commit, if it is neither, still needs this run's word.
		if p.statusCtx != "" && p.startSHA != "" && p.startSHA != newerSHA && p.startSHA != head {
			p.setStatus(ctx, statusCtx, p.startSHA, state, desc)
		}
		return
	}
	target := head
	if target == "" {
		target = p.startSHA
	}
	if target == "" {
		return
	}
	p.setStatus(ctx, statusCtx, target, state, desc)
	if pushed && p.statusCtx != "" {
		// The pending sat on the commit the run started from; resolve it, or
		// it stays pending on a commit nobody looks at any more.
		p.setStatus(ctx, statusCtx, p.startSHA, "success", "pushed "+shortSHA(head))
	}
}

func (p *ghProgress) react(ctx context.Context, content string) {
	_, err := p.g.kit.Invoke(ctx, "react", map[string]any{
		"repo": p.t.Target.Repo, "pr": p.t.Target.Number, "subjects": p.subjects, "content": content,
	})
	if err != nil {
		p.g.progressFailed(p.t, "react:"+content, err)
	}
}

func (p *ghProgress) setStatus(ctx context.Context, statusCtx, sha, state, desc string) bool {
	_, err := p.g.kit.Invoke(ctx, "set_status", map[string]any{
		"repo": p.t.Target.Repo, "sha": sha, "state": state, "description": desc, "context": statusCtx,
	})
	if err != nil {
		p.g.progressFailed(p.t, "status:"+state, err)
		return false
	}
	return true
}

// statusContext is the context progress statuses go out under: the
// configured one, else the login of the identity they are posted as. The
// result also joins the source's own-status set, so a status conductor wrote
// can never come back to it as a CI signal.
func (g *githubImpl) statusContext(ctx context.Context, repo string) (string, error) {
	c := g.conn.Progress.StatusContext
	if c == "" {
		tok, err := g.kit.TokenFor(ctx, "me", repo)
		if err != nil {
			return "", err
		}
		if c, err = g.kit.Login(ctx, tok); err != nil {
			return "", err
		}
	}
	g.srcMu.Lock()
	src := g.src
	g.srcMu.Unlock()
	if src != nil {
		src.NoteOwnStatusContext(c)
	}
	return c, nil
}

// progressFailed logs and audits a progress write that didn't land. Never
// more than that: progress is best-effort.
func (g *githubImpl) progressFailed(t core.Trigger, what string, err error) {
	msg := err.Error()
	if g.deps.Secrets != nil {
		msg = g.deps.Secrets.Redact(msg)
	}
	if g.deps.Log != nil {
		g.deps.Log("github[%s]: %s#%d progress %s failed (best-effort): %s", g.name, t.Target.Repo, t.Target.Number, what, msg)
	}
	if g.deps.Audit != nil {
		g.deps.Audit(map[string]any{"event": "progress", "connector": g.name, "repo": t.Target.Repo,
			"number": t.Target.Number, "kind": t.Kind, "what": what, "outcome": "failed", "error": msg})
	}
}

// maxProgressSubjects caps how many objects one run reacts to — a sweep run
// over many unresolved threads shouldn't fire dozens of API calls each way.
const maxProgressSubjects = 10

// progressSubjects collects the reaction subjects across a run's events,
// de-duplicated, capped.
func progressSubjects(ts []core.Trigger) []any {
	var out []any
	seen := map[string]bool{}
	for _, t := range ts {
		xs, _ := t.Context["reaction_subjects"].([]any)
		for _, x := range xs {
			m, ok := x.(map[string]any)
			if !ok {
				continue
			}
			k := fmt.Sprint(m["kind"], ":", m["id"])
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, m)
			if len(out) == maxProgressSubjects {
				return out
			}
		}
	}
	return out
}

// progressWorking is the pending status's description: what the run is doing.
func progressWorking(t core.Trigger) string {
	author, _ := t.Context["author"].(string)
	who := "a"
	if author != "" {
		who = author + "'s"
	}
	_, isReview := t.Context["review_id"]
	switch t.Kind {
	case "changes_requested":
		if !isReview {
			return "addressing unresolved review threads"
		}
		return "addressing " + who + " review"
	case "new_comment":
		if isReview {
			return "replying to " + who + " review"
		}
		return "replying to " + who + " comment"
	case "failing_checks":
		if c, _ := t.Context["failing_check"].(string); c != "" {
			return "fixing failing check " + c
		}
		return "fixing failing checks"
	case "merge_conflict":
		return "resolving merge conflict"
	case "pr_behind":
		return "updating the branch with its base"
	}
	return "working on this PR"
}

// progressVerdict is the final status for an outcome.
func progressVerdict(o RunOutcome, pushed bool, head string) (state, desc string) {
	if o.Result != OutcomeOK {
		reason := o.Reason
		if reason == "" {
			reason = "the run failed"
		}
		return "failure", "gave up: " + reason
	}
	if pushed {
		return "success", "pushed " + shortSHA(head)
	}
	return "success", "done — no changes pushed"
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

var _ ProgressReporter = (*githubImpl)(nil)
