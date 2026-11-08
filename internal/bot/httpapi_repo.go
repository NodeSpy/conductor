package bot

// The operator API for a bot's memory and a team's knowledge (design §4.4,
// the dashboard's repo browser): the same reads and writes the CLI makes over
// the control socket (`conductor bot memory|knowledge …`, OperatorOp's kinds,
// file, history, edit, new and consolidate), as HTTP routes. Every write is
// OperatorEdit — one commit authored by the operator, against the commit the
// operator read — so a stale base is refused (409, with the head) and nothing
// is silently overwritten. The operator is ?operator= (or "operator" in the
// body); the dashboard's chokepoint sets it to the signed-in session's.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// registerRepoRoutes adds the memory and knowledge routes to the operator API.
func (s *Service) registerRepoRoutes(mux *Routes) {
	for _, base := range []string{"/v1/bots/{bot}/memory", "/v1/teams/{team}/knowledge"} {
		mux.HandleFunc("GET "+base, s.apiRepo)
		mux.HandleFunc("GET "+base+"/file", s.apiRepoFile)
		mux.HandleFunc("GET "+base+"/history", s.apiRepoHistory)
		mux.HandleFunc("GET "+base+"/commits/{commit}", s.apiRepoCommit)
		mux.HandleFunc("GET "+base+"/blame", s.apiRepoBlame)
		mux.HandleFunc("POST "+base+"/edit", s.apiRepoEdit)
		mux.HandleFunc("POST "+base+"/new", s.apiRepoNew)
	}
	mux.HandleFunc("POST /v1/bots/{bot}/memory/consolidate", s.apiRepoConsolidate)
	mux.HandleFunc("GET /v1/teams/{team}/knowledge/proposals", s.apiKnowledgeProposals)
}

// repoTarget is the team or bot a repo route names, checked: an unknown one
// is a 404, and so is a team with no knowledge repo.
func (s *Service) repoTarget(w http.ResponseWriter, r *http.Request) (team, bot string, repo *Repo, ok bool) {
	team, bot = r.PathValue("team"), r.PathValue("bot")
	cfg := s.Cfg()
	if team != "" {
		if _, found := cfg.Teams[team]; !found {
			apiError(w, http.StatusNotFound, fmt.Errorf("no team %q", team))
			return "", "", nil, false
		}
	} else if _, err := s.bot(bot); err != nil {
		apiError(w, http.StatusNotFound, fmt.Errorf("no bot %q", bot))
		return "", "", nil, false
	}
	repo, _, _, err := s.editTarget(team, bot)
	if err != nil {
		apiError(w, http.StatusNotFound, err)
		return "", "", nil, false
	}
	if err := repo.Init(r.Context()); err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return "", "", nil, false
	}
	return team, bot, repo, true
}

// repoFileBudget caps the text a repo listing carries in all (the rest is
// read one file at a time); repoFileMax caps one file's.
const (
	repoFileBudget = 2 << 20
	repoFileMax    = 64 << 10
)

// apiRepo is a repo for the browser: its kinds (with whether an operator
// writes each), every file classified by kind with its title and — within a
// budget — its text, who reads it, and the head to write against.
func (s *Service) apiRepo(w http.ResponseWriter, r *http.Request) {
	team, bot, repo, ok := s.repoTarget(w, r)
	if !ok {
		return
	}
	cfg := s.Cfg()
	ks := repo.Kinds()
	var kinds []any
	for _, k := range ks.List() {
		d := k.Describe()
		d["template"] = k.Template
		d["free_paths"] = k.FreePaths
		d["builtin"] = k.Builtin
		d["operator"] = k.Writable(WriterOperator)
		d["annotate"] = k.Annotate
		if len(k.Legacy) > 0 {
			d["legacy"] = k.Legacy
		}
		kinds = append(kinds, d)
	}
	inv := repo.Inventory()
	sort.Slice(inv, func(i, j int) bool { return inv[i].Path < inv[j].Path })
	files := make([]any, 0, len(inv))
	budget := repoFileBudget
	repo.mu.Lock()
	for _, d := range inv {
		f := map[string]any{"path": d.Path, "kind": d.Kind, "status": d.Status}
		if d.Problem != "" {
			f["problem"] = d.Problem
		}
		if b, err := os.ReadFile(filepath.Join(repo.Dir, filepath.FromSlash(d.Path))); err == nil {
			text := string(b)
			f["bytes"] = len(b)
			if fm, _, has, err := SplitFrontmatter(text); err == nil && has {
				for _, key := range []string{"title", "what", "session"} {
					if t, _ := fm[key].(string); t != "" {
						f["title"] = t
						break
					}
				}
			}
			if len(b) <= repoFileMax && len(b) <= budget {
				f["text"] = text
				budget -= len(b)
			}
		}
		files = append(files, f)
	}
	repo.mu.Unlock()
	// repo names the repo by its directory's last two parts (team-knowledge/platform,
	// platform/knowledge): what an operator recognises, without the host's paths
	out := map[string]any{"head": repo.Head(r.Context()), "kinds": kinds, "files": files,
		"repo": filepath.ToSlash(filepath.Join(filepath.Base(filepath.Dir(repo.Dir)), filepath.Base(repo.Dir)))}
	if team != "" {
		t := cfg.Teams[team]
		out["target"], out["readers"], out["operators"] = "team:"+team, cfg.TeamBots(team), cfg.TeamOperators(team)
		if t.Knowledge != nil {
			out["promotion"] = t.Knowledge.PromotionMode()
		}
	} else {
		out["target"], out["readers"], out["operators"] = "bot:"+bot, []string{bot}, cfg.BotOperators(bot)
		if tn, _, ok := cfg.TeamOf(bot); ok {
			out["team"] = tn
			out["team_knowledge"] = s.TeamRepo(tn) != nil
		}
	}
	apiJSON(w, http.StatusOK, out)
}

// apiRepoFile is one file's text and the head to write against.
func (s *Service) apiRepoFile(w http.ResponseWriter, r *http.Request) {
	team, bot, _, ok := s.repoTarget(w, r)
	if !ok {
		return
	}
	out, err := s.RepoFile(r.Context(), team, bot, r.URL.Query().Get("path"), r.URL.Query().Get("kind"))
	if err != nil {
		apiError(w, http.StatusNotFound, err)
		return
	}
	apiJSON(w, http.StatusOK, out)
}

// apiRepoHistory is the repo's commits, or one file's (?path=), newest first.
func (s *Service) apiRepoHistory(w http.ResponseWriter, r *http.Request) {
	team, bot, _, ok := s.repoTarget(w, r)
	if !ok {
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	out, err := s.RepoHistory(r.Context(), team, bot, r.URL.Query().Get("path"), n)
	if err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	apiJSON(w, http.StatusOK, out)
}

// repoCommitFiles caps the files a commit's diff carries.
const repoCommitFiles = 30

// apiRepoCommit is one commit: who wrote it, its message and trailers, and
// each file it changed before and after (the browser draws the diff).
func (s *Service) apiRepoCommit(w http.ResponseWriter, r *http.Request) {
	_, _, repo, ok := s.repoTarget(w, r)
	if !ok {
		return
	}
	out, err := repo.CommitView(r.Context(), r.PathValue("commit"))
	if err != nil {
		apiError(w, http.StatusNotFound, err)
		return
	}
	apiJSON(w, http.StatusOK, out)
}

// CommitView is one commit of the repo with its changed files' text before
// and after it.
func (r *Repo) CommitView(ctx context.Context, sha string) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	full := r.resolveLocked(ctx, strings.TrimSpace(sha))
	if full == "" {
		return nil, fmt.Errorf("no commit %q in this repo", sha)
	}
	head, err := r.git(ctx, r.Dir, "show", "-s", "--format=%H%x1f%h%x1f%ae%x1f%aI%x1f%s%x1f%(trailers:only,unfold)", full)
	if err != nil {
		return nil, err
	}
	f := strings.SplitN(strings.TrimRight(head, "\n"), "\x1f", 6)
	if len(f) < 6 {
		return nil, errors.New("unreadable commit")
	}
	kind, who := authorKind(f[2])
	names, err := r.git(ctx, r.Dir, "show", "--no-renames", "--name-only", "--format=", full)
	if err != nil {
		return nil, err
	}
	parent := ""
	if out, err := r.git(ctx, r.Dir, "rev-parse", "--verify", "--quiet", full+"^"); err == nil {
		parent = strings.TrimSpace(out)
	}
	var files []any
	for _, p := range strings.Split(names, "\n") {
		if p = strings.TrimSpace(p); p == "" || strings.HasPrefix(filepath.Base(p), ".") {
			continue
		}
		if len(files) == repoCommitFiles {
			break
		}
		show := func(rev string) (string, bool) {
			if rev == "" {
				return "", false
			}
			out, err := r.git(ctx, r.Dir, "show", rev+":"+p)
			if err != nil {
				return "", false
			}
			return cutBytes(out, repoFileMax), true
		}
		before, hadBefore := show(parent)
		after, hadAfter := show(full)
		files = append(files, map[string]any{"path": p, "before": before, "after": after, "added": !hadBefore, "deleted": !hadAfter})
	}
	return map[string]any{"commit": f[0], "short": f[1], "kind": kind, "who": who, "date": f[3], "subject": f[4],
		"trailers": strings.TrimSpace(f[5]), "files": files}, nil
}

// apiRepoBlame is "who wrote this": the file at the head, in runs of lines
// each beside the commit that last changed them.
func (s *Service) apiRepoBlame(w http.ResponseWriter, r *http.Request) {
	_, _, repo, ok := s.repoTarget(w, r)
	if !ok {
		return
	}
	groups, err := repo.Blame(r.Context(), r.URL.Query().Get("path"))
	if err != nil {
		apiError(w, http.StatusNotFound, err)
		return
	}
	apiJSON(w, http.StatusOK, map[string]any{"path": r.URL.Query().Get("path"), "groups": groups})
}

// BlameGroup is a run of lines last changed by one commit.
type BlameGroup struct {
	Commit string   `json:"commit"`
	Short  string   `json:"short"`
	Kind   string   `json:"kind"`
	Who    string   `json:"who"`
	Date   string   `json:"date"`
	Lines  []string `json:"lines"`
}

// Blame is a file's lines grouped by the commit that last changed them.
func (r *Repo) Blame(ctx context.Context, path string) ([]BlameGroup, error) {
	c, err := r.cleanPath(path)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	out, err := r.git(ctx, r.Dir, "blame", "--line-porcelain", "HEAD", "--", c)
	r.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("no file %s at the head", c)
	}
	var groups []BlameGroup
	var sha, mail string
	var at int64
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "\t"):
			if n := len(groups); n > 0 && groups[n-1].Commit == sha {
				groups[n-1].Lines = append(groups[n-1].Lines, l[1:])
				continue
			}
			kind, who := authorKind(strings.Trim(mail, "<>"))
			short := sha
			if len(short) > 7 {
				short = short[:7]
			}
			groups = append(groups, BlameGroup{Commit: sha, Short: short, Kind: kind, Who: who,
				Date: time.Unix(at, 0).UTC().Format(time.RFC3339), Lines: []string{l[1:]}})
		case strings.HasPrefix(l, "author-mail "):
			mail = strings.TrimPrefix(l, "author-mail ")
		case strings.HasPrefix(l, "author-time "):
			at, _ = strconv.ParseInt(strings.TrimPrefix(l, "author-time "), 10, 64)
		default:
			if f := strings.Fields(l); len(f) >= 3 && hexSHA.MatchString(f[0]) && len(f[0]) == 40 {
				sha = f[0]
			}
		}
	}
	return groups, nil
}

// repoWriteErr answers a refused write: a stale base is 409 with the head to
// re-read; anything else (a schema the document fails, a path of another
// kind, an operator who may not write here) is 400 or 403.
func repoWriteErr(w http.ResponseWriter, err error) {
	var stale *StaleBaseError
	if errors.As(err, &stale) {
		apiJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "head": stale.Head})
		return
	}
	if strings.Contains(err.Error(), "is not an operator of") || strings.Contains(err.Error(), "say which operator") {
		apiError(w, http.StatusForbidden, err)
		return
	}
	apiError(w, http.StatusBadRequest, err)
}

// operatorOf is the request's operator: the query's (the dashboard sets it),
// else the body's.
func operatorOf(r *http.Request, args map[string]any) string {
	if op := r.URL.Query().Get("operator"); op != "" {
		return op
	}
	return str(args, "operator")
}

// apiRepoEdit is one operator write: {op: put|rm|mv|revert|annotate|flag,
// path, to, text, kind, create, commit, base, message, case}.
func (s *Service) apiRepoEdit(w http.ResponseWriter, r *http.Request) {
	team, bot, _, ok := s.repoTarget(w, r)
	if !ok {
		return
	}
	args, ok := repoArgs(w, r)
	if !ok {
		return
	}
	out, err := s.OperatorEdit(r.Context(), team, bot, operatorOf(r, args), str(args, "case"), Edit{
		Op: str(args, "op"), Path: str(args, "path"), To: str(args, "to"), Text: str(args, "text"),
		Kind: str(args, "kind"), Create: boolArg(args, "create"),
		Commit: str(args, "commit"), Base: str(args, "base"), Subject: str(args, "message")})
	if err != nil {
		repoWriteErr(w, err)
		return
	}
	apiJSON(w, http.StatusOK, out)
}

// apiRepoNew creates a document of a kind: {kind, title, slug, text, fields,
// base, message}; its path follows from the kind and the title.
func (s *Service) apiRepoNew(w http.ResponseWriter, r *http.Request) {
	team, bot, _, ok := s.repoTarget(w, r)
	if !ok {
		return
	}
	args, ok := repoArgs(w, r)
	if !ok {
		return
	}
	out, err := s.OperatorNew(r.Context(), team, bot, operatorOf(r, args), str(args, "case"), NewDoc{Kind: str(args, "kind"),
		Title: str(args, "title"), Slug: str(args, "slug"), Text: str(args, "text"), Base: str(args, "base"),
		Subject: str(args, "message"), Fields: fieldArgs(args)})
	if err != nil {
		repoWriteErr(w, err)
		return
	}
	apiJSON(w, http.StatusOK, out)
}

// apiRepoConsolidate starts consolidating a bot's log into digests now.
func (s *Service) apiRepoConsolidate(w http.ResponseWriter, r *http.Request) {
	_, bot, _, ok := s.repoTarget(w, r)
	if !ok {
		return
	}
	if op := r.URL.Query().Get("operator"); op != "" && !s.Operates(op, "bot:"+bot) {
		apiError(w, http.StatusForbidden, fmt.Errorf("%q is not an operator of bot %s", op, bot))
		return
	}
	wk, err := s.Consolidate(r.Context(), bot)
	if err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	if wk.ID == "" {
		apiJSON(w, http.StatusOK, map[string]any{"skipped": "no new log entries since the last consolidation"})
		return
	}
	apiJSON(w, http.StatusOK, map[string]any{"worker": wk.ID, "case": wk.CaseID})
}

func repoArgs(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	args := map[string]any{}
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		apiError(w, bodyErrCode(err), fmt.Errorf("want a JSON object: %w", err))
		return nil, false
	}
	return args, true
}

// apiKnowledgeProposals is what a team's bots have proposed for its
// knowledge, newest first: the pending ones with their full text, and the
// recently decided ones.
func (s *Service) apiKnowledgeProposals(w http.ResponseWriter, r *http.Request) {
	team, _, repo, ok := s.repoTarget(w, r)
	if !ok {
		return
	}
	cfg := s.Cfg()
	type row struct {
		at  time.Time
		out map[string]any
	}
	var rows []row
	for _, n := range cfg.TeamBots(team) {
		rec := s.Ledger.Bot(n)
		for _, p := range rec.Proposals {
			if p.Kind != KindKnowledge || p.Knowledge == nil || p.Knowledge.Team != team {
				continue
			}
			k := p.Knowledge
			kind := ""
			if ck, ok := repo.Kinds().Of(k.To); ok {
				kind = ck.Name
			}
			o := map[string]any{"bot": n, "proposal": p.ID, "status": p.Status, "summary": p.Summary, "from": k.From, "to": k.To,
				"kind": kind, "note": k.Note, "created": p.Created.Format(time.RFC3339), "expires": p.Expires.Format(time.RFC3339),
				"approvers": cfg.TeamOperators(team)}
			if p.Status == "pending" {
				o["text"] = k.Text
				if cur, err := repo.Read(k.To); err == nil {
					o["current"] = cur
				}
			}
			if p.CaseID != "" {
				o["case"] = p.CaseID
				if c := rec.Cases[p.CaseID]; c != nil {
					o["case_title"] = c.Title
				}
			}
			if p.Approver != "" {
				o["approver"] = p.Approver
			}
			if p.Reason != "" {
				o["reason"] = p.Reason
			}
			if !p.Decided.IsZero() {
				o["decided"] = p.Decided.Format(time.RFC3339)
			}
			if k.EditedBy != "" {
				o["edited_by"] = k.EditedBy
			}
			if k.Landed != "" {
				o["landed"] = k.Landed
			}
			rows = append(rows, row{p.Created, o})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		pi, pj := rows[i].out["status"] == "pending", rows[j].out["status"] == "pending"
		if pi != pj {
			return pi
		}
		return rows[i].at.After(rows[j].at)
	})
	out := make([]any, 0, len(rows))
	for i, x := range rows {
		if i == 50 {
			break
		}
		out = append(out, x.out)
	}
	apiJSON(w, http.StatusOK, map[string]any{"team": team, "proposals": out})
}
