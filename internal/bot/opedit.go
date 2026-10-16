package bot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
)

// OPERATOR AUTHORING. An operator writes a team knowledge repo or a bot's
// memory repo directly — create, update, move, delete, revert — each as one
// commit authored by the operator, with no proposal. Only the operator
// control socket reaches it (OperatorOp; AgentOp refuses every one of
// these ops), and every write names the commit it was made against: a stale
// base is refused with the current head, so nothing is silently
// overwritten. Bots keep their own paths: proposals into team knowledge,
// and writes to their own memory.

// StaleBaseError is a write against a commit that is no longer the repo's
// head.
type StaleBaseError struct{ Base, Head string }

func (e *StaleBaseError) Error() string {
	if e.Base == "" {
		return fmt.Sprintf("a write needs the commit it was made against (--base); the repo is at %s", e.Head)
	}
	return fmt.Sprintf("stale base %s: the repo is now at %s — re-read it and retry against that commit", e.Base, e.Head)
}

// Edit operations.
const (
	EditPut    = "put"
	EditRm     = "rm"
	EditMv     = "mv"
	EditRevert = "revert"
)

// Edit is one operator change to a repo.
type Edit struct {
	Op   string // put | rm | mv | revert
	Path string // put / rm / mv: the file; revert: unused
	To   string // mv: the new path
	Text string // put: the full new content
	// Commit is a revert's target.
	Commit string
	// Base is the commit the operator read: it must be the head.
	Base     string
	Subject  string
	Trailers map[string]string
}

// hexSHA is what a base or a revert target must look like: a commit id, not
// a ref — a name like HEAD or main would always "match" and defeat the check.
var hexSHA = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// resolveLocked resolves a commit id to its full hash ("" when it is not a
// commit of this repo).
func (r *Repo) resolveLocked(ctx context.Context, sha string) string {
	if !hexSHA.MatchString(sha) {
		return ""
	}
	out, err := r.git(ctx, r.Dir, "rev-parse", "--verify", "--quiet", sha+"^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// Commit applies one edit as one commit authored by id. It returns the new
// head (the old one, with changed false, when a put changes nothing).
func (r *Repo) Commit(ctx context.Context, id Ident, e Edit) (head string, changed bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, _ := r.git(ctx, r.Dir, "rev-parse", "HEAD")
	cur = strings.TrimSpace(cur)
	if base := r.resolveLocked(ctx, strings.TrimSpace(e.Base)); base == "" || base != cur {
		return "", false, &StaleBaseError{Base: strings.TrimSpace(e.Base), Head: r.headLocked(ctx)}
	}
	var paths []string
	subject := e.Subject
	switch e.Op {
	case EditPut:
		c, err := r.cleanPath(e.Path)
		if err != nil {
			return "", false, err
		}
		if len(e.Text) > 256<<10 {
			return "", false, fmt.Errorf("file too large (%d bytes, max 256KiB)", len(e.Text))
		}
		text := e.Text
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		abs := filepath.Join(r.Dir, c)
		if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
			return "", false, err
		}
		if err := os.WriteFile(abs, []byte(text), 0o600); err != nil {
			return "", false, err
		}
		if _, err := r.git(ctx, r.Dir, "add", "--", c); err != nil {
			return "", false, err
		}
		paths = []string{c}
		if subject == "" {
			subject = "update " + c
		}
	case EditRm:
		c, err := r.cleanPath(e.Path)
		if err != nil {
			return "", false, err
		}
		if !r.trackedLocked(ctx, c) {
			return "", false, fmt.Errorf("no file %s", c)
		}
		if _, err := r.git(ctx, r.Dir, "rm", "-q", "--", c); err != nil {
			return "", false, err
		}
		paths = []string{c}
		if subject == "" {
			subject = "delete " + c
		}
	case EditMv:
		from, err := r.cleanPath(e.Path)
		if err != nil {
			return "", false, err
		}
		to, err := r.cleanPath(e.To)
		if err != nil {
			return "", false, err
		}
		if !r.trackedLocked(ctx, from) {
			return "", false, fmt.Errorf("no file %s", from)
		}
		// (git mv refuses an existing destination itself.)
		if err := os.MkdirAll(filepath.Dir(filepath.Join(r.Dir, to)), 0o700); err != nil {
			return "", false, err
		}
		if _, err := r.git(ctx, r.Dir, "mv", "--", from, to); err != nil {
			return "", false, err
		}
		paths = []string{from, to}
		if subject == "" {
			subject = "move " + from + " to " + to
		}
	case EditRevert:
		target := r.resolveLocked(ctx, strings.TrimSpace(e.Commit))
		if target == "" {
			return "", false, fmt.Errorf("no commit %q in this repo", e.Commit)
		}
		files, err := r.revertableLocked(ctx, target)
		if err != nil {
			return "", false, err
		}
		if _, err := r.gitIdent(ctx, OperatorIdent("-"), r.Dir, "revert", "--no-commit", target); err != nil {
			_, _ = r.git(ctx, r.Dir, "revert", "--abort")
			_, _ = r.git(ctx, r.Dir, "reset", "-q", "--hard", cur)
			return "", false, fmt.Errorf("commit %s does not revert cleanly onto the head (a later commit changed the same lines) — edit the file instead: %v", short(target), err)
		}
		paths = files
		if subject == "" {
			subj, _ := r.git(ctx, r.Dir, "log", "-1", "--pretty=format:%s", target)
			subject = fmt.Sprintf("revert %s (%s)", short(target), strings.TrimSpace(subj))
		}
		if e.Trailers == nil {
			e.Trailers = map[string]string{}
		}
		e.Trailers["Reverts"] = target
	default:
		return "", false, fmt.Errorf("unknown edit %q (want put, rm, mv or revert)", e.Op)
	}
	if out, _ := r.git(ctx, r.Dir, append([]string{"diff", "--cached", "--name-only", "--"}, paths...)...); strings.TrimSpace(out) == "" {
		return r.headLocked(ctx), false, nil // nothing changed
	}
	args := append([]string{"commit", "-q", "-m", commitMessage(subject, e.Trailers), "--"}, paths...)
	if _, err := r.gitIdent(ctx, id, r.Dir, args...); err != nil {
		_, _ = r.git(ctx, r.Dir, "reset", "-q", "--hard", cur)
		return "", false, err
	}
	head = r.headLocked(ctx)
	r.refreshLocked(ctx)
	return head, true, nil
}

func (r *Repo) trackedLocked(ctx context.Context, path string) bool {
	out, err := r.git(ctx, r.Dir, "ls-files", "--error-unmatch", "--", path)
	return err == nil && strings.TrimSpace(out) != ""
}

// revertableLocked lists the files a commit touched, refusing one that
// touched anything outside the repo's writable layout — which refuses the
// repo's first commit (it holds the README) — or none (diff-tree lists no
// files for a merge, so a merge is refused here too).
func (r *Repo) revertableLocked(ctx context.Context, sha string) ([]string, error) {
	out, err := r.git(ctx, r.Dir, "diff-tree", "--no-commit-id", "--name-only", "-r", "--no-renames", sha)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(strings.TrimSpace(out), "\n") {
		if f == "" {
			continue
		}
		if _, err := r.cleanPath(f); err != nil {
			return nil, fmt.Errorf("commit %s touches %s, outside what may be edited — not revertable", short(sha), f)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("commit %s changes no files", short(sha))
	}
	return files, nil
}

// HistoryEntry is one commit, for the operator: who wrote it (an operator,
// a bot, or conductor for the team repo's own first commit) and what it
// is linked to.
type HistoryEntry struct {
	Commit   string   `json:"commit"`
	Short    string   `json:"short"`
	Author   string   `json:"author"`
	Kind     string   `json:"kind"` // operator | bot | team | other
	Who      string   `json:"who"`  // the operator's or bot's name
	Date     string   `json:"date"`
	Subject  string   `json:"subject"`
	Case     string   `json:"case,omitempty"`
	Proposal string   `json:"proposal,omitempty"`
	Bot      string   `json:"bot,omitempty"` // the bot behind an operator-edited promotion
	Reverts  string   `json:"reverts,omitempty"`
	Files    []string `json:"files,omitempty"`
}

// History lists a repo's commits, newest first — all of them, or one
// file's (following renames). limit is capped at 200.
func (r *Repo) History(ctx context.Context, path string, limit int) ([]HistoryEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = min(max(limit, 20), 200)
	}
	args := []string{"log", "-n", strconv.Itoa(limit), "--name-only",
		"--pretty=format:%x1e%H%x1f%h%x1f%an%x1f%ae%x1f%aI%x1f%s%x1f%(trailers:key=Bot-Case,valueonly,separator=%x2C)%x1f%(trailers:key=Bot-Proposal,valueonly,separator=%x2C)%x1f%(trailers:key=Bot,valueonly,separator=%x2C)%x1f%(trailers:key=Reverts,valueonly,separator=%x2C)%x1f"}
	if path != "" {
		c, err := r.cleanPath(path)
		if err != nil {
			return nil, err
		}
		args = append(args, "--follow", "--", c)
	}
	r.mu.Lock()
	out, err := r.git(ctx, r.Dir, args...)
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	var hs []HistoryEntry
	for _, rec := range strings.Split(out, "\x1e") {
		f := strings.Split(rec, "\x1f")
		if len(f) < 11 {
			continue
		}
		h := HistoryEntry{Commit: f[0], Short: f[1], Author: f[2], Date: f[4], Subject: f[5],
			Case: strings.TrimSpace(f[6]), Proposal: strings.TrimSpace(f[7]), Bot: strings.TrimSpace(f[8]), Reverts: strings.TrimSpace(f[9])}
		h.Kind, h.Who = authorKind(f[3])
		for _, l := range strings.Split(f[10], "\n") {
			if l = strings.TrimSpace(l); l != "" {
				h.Files = append(h.Files, l)
			}
		}
		hs = append(hs, h)
	}
	return hs, nil
}

func authorKind(email string) (string, string) {
	for _, d := range []struct{ domain, kind string }{{operatorDomain, "operator"}, {botDomain, "bot"}, {teamDomain, "team"}} {
		if who, ok := strings.CutSuffix(email, d.domain); ok {
			return d.kind, who
		}
	}
	return "other", email
}

// ---- the service side ----

// editTarget resolves an operator edit's repo: a team's knowledge repo or
// a bot's memory, and the operators who may edit it.
func (s *Service) editTarget(team, bot string) (*Repo, []string, string, error) {
	cfg := s.Cfg()
	switch {
	case team != "" && bot != "":
		return nil, nil, "", errors.New("name a team or a bot, not both")
	case team != "":
		if _, ok := cfg.Teams[team]; !ok {
			return nil, nil, "", fmt.Errorf("unknown team %q", team)
		}
		repo := s.TeamRepo(team)
		if repo == nil {
			return nil, nil, "", fmt.Errorf("team %s has no knowledge repo (teams.%s.knowledge)", team, team)
		}
		return repo, cfg.TeamOperators(team), "team " + team, nil
	case bot != "":
		if _, err := s.bot(bot); err != nil {
			return nil, nil, "", err
		}
		return s.Repo(bot), cfg.BotOperators(bot), "bot " + bot, nil
	}
	return nil, nil, "", errors.New("name a team (knowledge) or a bot (memory)")
}

// pickOperator is the operator an edit is authored as: the named one, which
// must be allowed here, or the only one allowed.
func pickOperator(cfg *config.Config, allowed []string, as, what string) (string, error) {
	if as == "" {
		if len(allowed) != 1 {
			return "", fmt.Errorf("say which operator you are with --as (operators of %s: %s)", what, strings.Join(allowed, ", "))
		}
		return allowed[0], nil
	}
	if _, ok := cfg.Operators[as]; !ok {
		return "", fmt.Errorf("operator %q is not configured", as)
	}
	for _, op := range allowed {
		if op == as {
			return as, nil
		}
	}
	return "", fmt.Errorf("%q is not an operator of %s", as, what)
}

// OperatorEdit is an operator's direct write to a team's knowledge or a
// bot's memory: one commit, authored by the operator, against Base.
func (s *Service) OperatorEdit(ctx context.Context, team, bot, as, caseID string, e Edit) (map[string]any, error) {
	repo, allowed, what, err := s.editTarget(team, bot)
	if err != nil {
		return nil, err
	}
	op, err := pickOperator(s.Cfg(), allowed, as, what)
	if err != nil {
		return nil, err
	}
	if err := repo.Init(ctx); err != nil {
		return nil, err
	}
	if caseID != "" {
		if bot == "" {
			return nil, errors.New("--case links a bot's case: only for a bot's memory")
		}
		if s.Ledger.Bot(bot).Cases[caseID] == nil {
			return nil, fmt.Errorf("bot %s has no case %q", bot, caseID)
		}
	}
	e.Trailers = map[string]string{"Operator": op, "Operator-Edit": e.Op, "Bot-Case": caseID}
	if team != "" {
		e.Trailers["Bot-Team"] = team
	}
	head, changed, err := repo.Commit(ctx, OperatorIdent(op), e)
	ae := map[string]any{"event": "bot_operator_edit", "operator": op, "team": team, "bot": bot, "op": e.Op,
		"path": e.Path, "to": e.To, "revert": e.Commit, "base": e.Base, "case": caseID}
	if err != nil {
		ae["outcome"], ae["error"] = "refused", err.Error()
		var stale *StaleBaseError
		if errors.As(err, &stale) {
			ae["head"] = stale.Head
		}
		s.audit(ae)
		return nil, err
	}
	ae["outcome"], ae["commit"], ae["changed"] = "ok", head, changed
	s.audit(ae)
	if changed && bot != "" {
		// The bot's memory changed under it: say so (no content — it reads
		// the file through conductor).
		what := e.Path
		switch e.Op {
		case EditMv:
			what = e.Path + " → " + e.To
		case EditRevert:
			what = "commit " + short(e.Commit) + " reverted"
		}
		s.enqueue(bot, caseID, fmt.Sprintf("[conductor] operator %s changed your memory (%s %s, commit %s). Re-read it (`conductor bot read <path>`) before relying on what you remember of it.", op, e.Op, what, head))
	}
	return map[string]any{"commit": head, "changed": changed, "operator": op, "repo": what}, nil
}

// RepoFile is an operator's read of one file, with the head to write
// against.
func (s *Service) RepoFile(ctx context.Context, team, bot, path string) (map[string]any, error) {
	repo, _, what, err := s.editTarget(team, bot)
	if err != nil {
		return nil, err
	}
	if err := repo.Init(ctx); err != nil {
		return nil, err
	}
	head := repo.Head(ctx)
	if path == "" {
		files := append(repo.List(DirKnowledge), repo.List(DirRunbooks)...)
		if bot != "" {
			files = append(files, append(repo.List(DirEpisodes), repo.List(DirHandoff)...)...)
		}
		return map[string]any{"repo": what, "head": head, "files": files}, nil
	}
	text, err := repo.Read(path)
	if err != nil {
		return nil, err
	}
	return map[string]any{"repo": what, "head": head, "path": path, "text": text}, nil
}

// RepoHistory lists a repo's or one file's commits for the operator.
func (s *Service) RepoHistory(ctx context.Context, team, bot, path string, limit int) (map[string]any, error) {
	repo, _, what, err := s.editTarget(team, bot)
	if err != nil {
		return nil, err
	}
	if err := repo.Init(ctx); err != nil {
		return nil, err
	}
	hs, err := repo.History(ctx, path, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"repo": what, "head": repo.Head(ctx), "path": path, "commits": toList(hs)}, nil
}

// operatorOnlyOps are the operator-authoring ops: never on the agent socket.
// (The ops are generic — edit, file, history — so no spelling of them is
// reachable from a bot session.)
var operatorOnlyOps = map[string]bool{"edit": true, "file": true, "history": true}
