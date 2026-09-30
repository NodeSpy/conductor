package hostcmd

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// The gh profile (#154 §5): reads on the dispatch's repository; writes only
// the target-bound kinds (comment, review, reply, resolve a thread), each
// reported for binding; the auth/config/alias/extension surface refused by
// name. Its host-side process runs with a clean GH_CONFIG_DIR (just the
// login), so an alias or extension cannot be hijacked.

var ghSpec = flagSpec{
	value: set("--repo", "--body", "--body-file", "--title", "--base", "--head", "--label",
		"--assignee", "--reviewer", "--milestone", "--project", "--method", "--raw-field",
		"--field", "--header", "--input", "--jq", "--template", "--json", "--limit",
		"--state", "--search", "--author", "--app", "--hostname", "--cache", "--preview",
		"--subject", "--match-head-commit", "--add-label", "--remove-label", "--add-reviewer",
		"--remove-reviewer", "--add-assignee", "--remove-assignee", "--add-project",
		"--remove-project", "--recover", "--branch", "--commit", "--workflow", "--event",
		"--status", "--user", "--job", "--attempt", "--name", "--pattern", "--dir",
		"--output", "--ref", "--env", "--org", "--visibility", "--description", "--color",
		"--sort", "--order", "--created", "--interval", "--key", "--notes", "--notes-file",
		"--target", "--tag", "--draft-of"),
	alias: map[string]string{
		"-R": "--repo", "-b": "--body", "-t": "--title", "-B": "--base", "-H": "--head",
		"-l": "--label", "-a": "--assignee", "-r": "--reviewer", "-m": "--milestone",
		"-p": "--project", "-X": "--method", "-f": "--raw-field", "-q": "--jq",
		"-L": "--limit", "-s": "--state", "-S": "--search", "-A": "--author",
		"-w": "--web", "-d": "--draft", "-e": "--editor", "-c": "--comment",
		"-y": "--yes", "-i": "--include", "-n": "--name", "-D": "--dir", "-O": "--output",
		"-u": "--user", "-j": "--job", "-g": "--dir",
	},
}

// ghFieldFlag is -F, whose meaning differs by command: --body-file for
// pr/issue comment/create, --field for api. It is resolved before parsing.
func ghPreprocess(args []string) []string {
	api := false
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			api = a == "api"
			break
		}
	}
	out := make([]string, 0, len(args))
	for _, a := range args {
		switch {
		case a == "-F":
			if api {
				out = append(out, "--field")
			} else {
				out = append(out, "--body-file")
			}
		case strings.HasPrefix(a, "-F") && len(a) > 2:
			v := strings.TrimPrefix(strings.TrimPrefix(a, "-F"), "=")
			if api {
				out = append(out, "--field="+v)
			} else {
				out = append(out, "--body-file="+v)
			}
		case a == "-h" && api:
			out = append(out, "--header")
		default:
			out = append(out, a)
		}
	}
	return out
}

type ghProfile struct{}

// ghReadOnly are the (group, sub) pairs that only read. A missing sub key
// ("") covers the whole group.
var ghReads = map[string]map[string]bool{
	"pr":          set("view", "diff", "checks", "list", "status"),
	"issue":       set("view", "list", "status"),
	"repo":        set("view", "list", "gitignore", "license"),
	"run":         set("list", "view", "watch", "download"),
	"workflow":    set("list", "view"),
	"release":     set("list", "view", "download"),
	"label":       set("list"),
	"cache":       set("list"),
	"ruleset":     set("list", "view", "check"),
	"attestation": set("verify", "download", "trusted-root"),
	"search":      set(""),
	"status":      set(""),
	"completion":  set(""),
	"version":     set(""),
	"help":        set(""),
	"secret":      set("list"),
	"variable":    set("list", "get"),
	"org":         set("list"),
	"ssh-key":     set("list"),
	"gpg-key":     set("list"),
}

// ghRefused are the built-in refusals by name (#154 §3.1): gh's auth and
// config surface, and anything that would run other code on the host.
var ghRefused = map[string]string{
	"alias":      "built-in: gh alias *",
	"extension":  "built-in: gh extension *",
	"ext":        "built-in: gh extension *",
	"extensions": "built-in: gh extension *",
	"codespace":  "built-in: gh codespace *",
	"cs":         "built-in: gh codespace *",
	"browse":     "built-in: gh browse (no browser here)",
	"gist":       "built-in: gh gist * (not bound to the dispatch's repository)",
	"co":         "built-in: gh co / pr checkout (use git in the jail)",
	"agent-task": "built-in: gh agent-task *",
	"copilot":    "built-in: gh copilot *",
}

func (ghProfile) parse(args []string, ctx Context) Parsed {
	pos, flags := parseFlags(ghPreprocess(args), ghSpec)
	p := Parsed{Words: pos, Flags: flags}
	for _, k := range []string{"--body-file", "--input", "--notes-file"} {
		for _, v := range flags[k] {
			if v != "-" && v != "" {
				p.PathArgs = append(p.PathArgs, v)
			}
		}
	}
	for _, v := range flags["--field"] {
		if _, f, ok := strings.Cut(v, "=@"); ok && f != "-" {
			p.PathArgs = append(p.PathArgs, f)
		}
	}
	group, sub := p.word(0), p.word(1)
	if group == "" {
		return p // bare `gh` prints help
	}
	if r, ok := ghRefused[group]; ok {
		p.Refuse = r
		return p
	}
	switch group {
	case "auth":
		// `gh auth status` reports the login without the token; everything
		// else in the group changes or prints credentials.
		if sub != "status" || p.has("--show-token") || p.has("-t") {
			p.Refuse = "built-in: gh auth *"
		}
		return p
	case "config":
		if sub == "get" || sub == "list" {
			return p
		}
		p.Refuse = "built-in: gh config set|clear-cache|…"
		return p
	case "secret", "variable", "ssh-key", "gpg-key":
		if !ghReads[group][sub] {
			p.Refuse = "built-in: gh " + group + " " + sub + " (credential management)"
			return p
		}
	}
	repo := ctx.Repo
	if r := p.first("--repo"); r != "" {
		repo = normRepo(r)
	}
	// Every gh command is bound to the dispatch's repository, reads included.
	if group != "search" && group != "status" && group != "api" && group != "completion" &&
		group != "version" && group != "help" && group != "org" && group != "ssh-key" && group != "gpg-key" {
		if !strings.EqualFold(repo, ctx.Repo) {
			// Another repository, read or write: the gh binding refuses it
			// unless the operator's gh allow list names the command.
			p.Writes = append(p.Writes, Write{Kind: "other_repo", Repo: repo})
			return p
		}
	}
	if group == "api" {
		ghAPI(&p, ctx, repo)
		return p
	}
	if subs, ok := ghReads[group]; ok && (subs[""] || subs[sub]) {
		if group == "run" && sub == "download" {
			if d := p.first("--dir"); d != "" {
				p.PathArgs = append(p.PathArgs, d)
			}
		}
		return p
	}
	num := func() int { return ghTarget(p.word(2), ctx) }
	switch group + " " + sub {
	case "pr comment", "issue comment":
		p.Writes = append(p.Writes, Write{Kind: "comment", Repo: repo, Number: num()})
	case "pr review":
		if p.has("--approve") || p.has("-a") {
			p.Refuse = "built-in: an agent does not approve pull requests as the operator"
			return p
		}
		p.Writes = append(p.Writes, Write{Kind: "review", Repo: repo, Number: num()})
	case "pr create", "pr revert":
		p.Writes = append(p.Writes, Write{Kind: "create_pr", Repo: repo})
	case "issue create":
		p.Writes = append(p.Writes, Write{Kind: "create_issue", Repo: repo})
	case "pr merge":
		p.Writes = append(p.Writes, Write{Kind: "merge", Repo: repo, Number: num()})
	case "pr close", "issue close", "issue delete":
		p.Writes = append(p.Writes, Write{Kind: "close", Repo: repo, Number: num()})
	case "pr reopen", "issue reopen":
		p.Writes = append(p.Writes, Write{Kind: "reopen", Repo: repo, Number: num()})
	case "pr update-branch":
		// Server-side merge of the base into the head: a push to the head.
		n := num()
		br := ""
		if n == ctx.Number && ctx.IsPR {
			br = ctx.HeadBranch
		}
		p.Writes = append(p.Writes, Write{Kind: "push", Repo: repo, Number: n, Branch: br})
	case "pr checkout":
		p.Refuse = "built-in: gh pr checkout (use git in the jail)"
	default:
		// edit, ready, lock, develop, transfer, pin, repo create/delete/…,
		// run rerun, release create, label create, … — all writes, none of
		// them target-bound by default.
		kind := "other"
		if sub == "edit" || sub == "ready" || sub == "lock" || sub == "unlock" || sub == "pin" || sub == "unpin" {
			kind = "edit"
		}
		n := 0
		if group == "pr" || group == "issue" {
			n = num()
		}
		p.Writes = append(p.Writes, Write{Kind: kind, Repo: repo, Number: n})
	}
	return p
}

// ghTarget resolves a pr/issue selector (a number, a URL, a branch name, or
// nothing for "the current branch's PR") to a number; 0 = unknown.
func ghTarget(sel string, ctx Context) int {
	if sel == "" {
		if ctx.IsPR || ctx.Number > 0 {
			return ctx.Number
		}
		return 0
	}
	sel = strings.TrimPrefix(sel, "#")
	if n, err := strconv.Atoi(sel); err == nil {
		return n
	}
	if u, err := url.Parse(sel); err == nil && u.Host != "" {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 4 && (parts[2] == "pull" || parts[2] == "issues") {
			if !strings.EqualFold(parts[0]+"/"+parts[1], ctx.Repo) {
				return -1
			}
			if n, err := strconv.Atoi(parts[3]); err == nil {
				return n
			}
		}
		return 0
	}
	if ctx.IsPR && sel == ctx.HeadBranch {
		return ctx.Number
	}
	return 0
}

func normRepo(r string) string {
	r = strings.TrimSuffix(strings.TrimPrefix(r, "https://github.com/"), ".git")
	if strings.Count(r, "/") == 2 { // HOST/OWNER/REPO
		r = r[strings.IndexByte(r, '/')+1:]
	}
	return r
}

var (
	reIssueComment = regexp.MustCompile(`^repos/([^/]+/[^/]+)/issues/(\d+)/comments$`)
	rePullReviews  = regexp.MustCompile(`^repos/([^/]+/[^/]+)/pulls/(\d+)/reviews$`)
	rePullComments = regexp.MustCompile(`^repos/([^/]+/[^/]+)/pulls/(\d+)/comments$`)
	rePullReplies  = regexp.MustCompile(`^repos/([^/]+/[^/]+)/pulls/(\d+)/comments/\d+/replies$`)
	rePulls        = regexp.MustCompile(`^repos/([^/]+/[^/]+)/pulls$`)
	reIssues       = regexp.MustCompile(`^repos/([^/]+/[^/]+)/issues$`)
	rePullMerge    = regexp.MustCompile(`^repos/([^/]+/[^/]+)/pulls/(\d+)/merge$`)
	reRepoPath     = regexp.MustCompile(`^repos/([^/]+/[^/]+)(/|$)`)
	reNumbered     = regexp.MustCompile(`^repos/[^/]+/[^/]+/(?:issues|pulls)/(\d+)(/|$)`)
	reGQLThread    = regexp.MustCompile(`(?:threadId|pullRequestReviewThreadId)\s*:\s*"([^"]+)"`)
	reGQLOp        = regexp.MustCompile(`(?m)^\s*(mutation|query|subscription)\b`)
)

// ghAPI classifies `gh api`: the method (explicit -X, else POST when fields
// or --input are given, else GET) and the endpoint path decide the write.
func ghAPI(p *Parsed, ctx Context, repo string) {
	ep := ""
	if len(p.Words) > 1 {
		ep = p.Words[1]
	}
	owner, name, _ := strings.Cut(repo, "/")
	ep = strings.NewReplacer("{owner}", owner, "{repo}", name, ":owner", owner, ":repo", name).Replace(ep)
	ep = strings.TrimPrefix(ep, "/")
	if i := strings.IndexByte(ep, '?'); i >= 0 {
		ep = ep[:i]
	}
	if u, err := url.Parse(ep); err == nil && u.Host != "" {
		ep = strings.TrimPrefix(u.Path, "/")
	}
	method := strings.ToUpper(p.first("--method"))
	if method == "" {
		if p.has("--field") || p.has("--raw-field") || p.has("--input") {
			method = "POST"
		} else {
			method = "GET"
		}
	}
	if p.first("--hostname") != "" && !strings.EqualFold(p.first("--hostname"), "github.com") {
		p.Refuse = "target: gh api --hostname " + p.first("--hostname") + " is not the dispatch's host"
		return
	}
	if ep == "graphql" {
		ghGraphQL(p, ctx)
		return
	}
	if m := reRepoPath.FindStringSubmatch(ep); m != nil && !strings.EqualFold(m[1], ctx.Repo) {
		p.Writes = append(p.Writes, Write{Kind: "other_repo", Repo: m[1]})
		return
	}
	if method == "GET" || method == "HEAD" {
		if strings.HasPrefix(ep, "repos/") || ep == "user" || ep == "rate_limit" || strings.HasPrefix(ep, "search/") || ep == "meta" || ep == "octocat" || ep == "zen" {
			return
		}
		p.Refuse = "target: gh api GET " + ep + " is outside the dispatch's repository"
		return
	}
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	w := Write{Kind: "other", Repo: ctx.Repo}
	switch {
	case method == "POST" && reIssueComment.MatchString(ep):
		m := reIssueComment.FindStringSubmatch(ep)
		w = Write{Kind: "comment", Repo: m[1], Number: atoi(m[2])}
	case method == "POST" && rePullReviews.MatchString(ep):
		m := rePullReviews.FindStringSubmatch(ep)
		if ev := fieldValue(p, "event"); strings.EqualFold(ev, "APPROVE") {
			p.Refuse = "built-in: an agent does not approve pull requests as the operator"
			return
		}
		w = Write{Kind: "review", Repo: m[1], Number: atoi(m[2])}
	case method == "POST" && rePullComments.MatchString(ep):
		m := rePullComments.FindStringSubmatch(ep)
		w = Write{Kind: "review", Repo: m[1], Number: atoi(m[2])}
	case method == "POST" && rePullReplies.MatchString(ep):
		m := rePullReplies.FindStringSubmatch(ep)
		w = Write{Kind: "reply", Repo: m[1], Number: atoi(m[2])}
	case method == "POST" && rePulls.MatchString(ep):
		w = Write{Kind: "create_pr", Repo: rePulls.FindStringSubmatch(ep)[1]}
	case method == "POST" && reIssues.MatchString(ep):
		w = Write{Kind: "create_issue", Repo: reIssues.FindStringSubmatch(ep)[1]}
	case method == "PUT" && rePullMerge.MatchString(ep):
		m := rePullMerge.FindStringSubmatch(ep)
		w = Write{Kind: "merge", Repo: m[1], Number: atoi(m[2])}
	default:
		if m := reNumbered.FindStringSubmatch(ep); m != nil {
			w.Number = atoi(m[1])
			if method == "PATCH" && strings.EqualFold(fieldValue(p, "state"), "closed") {
				w.Kind = "close"
			} else {
				w.Kind = "edit"
			}
		}
	}
	p.Writes = append(p.Writes, w)
}

// fieldValue reads one -f/-F key=value field.
func fieldValue(p *Parsed, key string) string {
	for _, f := range []string{"--raw-field", "--field"} {
		for _, v := range p.Flags[f] {
			if k, val, ok := strings.Cut(v, "="); ok && k == key {
				return val
			}
		}
	}
	return ""
}

// ghThreadMutations are the GraphQL mutations a fixer may run on its own
// PR's review threads.
var ghThreadMutations = map[string]string{
	"resolveReviewThread":             "resolve_thread",
	"unresolveReviewThread":           "resolve_thread",
	"addPullRequestReviewThreadReply": "reply",
}

// ghGraphQL: a query is a read (any repository — GraphQL reads cannot be
// bound by path, and reads are not writes); a mutation must consist only of
// review-thread operations, each resolved to its PR for target binding.
func ghGraphQL(p *Parsed, ctx Context) {
	q := fieldValue(p, "query")
	if strings.HasPrefix(q, "@") {
		if ctx.ReadFile == nil {
			p.Refuse = "target: gh api graphql with a query file cannot be checked here"
			return
		}
		b, err := ctx.ReadFile(strings.TrimPrefix(q, "@"))
		if err != nil {
			p.Refuse = "target: gh api graphql query file unreadable: " + err.Error()
			return
		}
		q = string(b)
	}
	if p.has("--input") {
		p.Refuse = "target: gh api graphql --input cannot be checked; pass the query with -f query=…"
		return
	}
	trimmed := strings.TrimSpace(q)
	isMutation := false
	if m := reGQLOp.FindStringSubmatch(trimmed); m != nil {
		isMutation = m[1] == "mutation"
	}
	if !isMutation {
		if strings.Contains(trimmed, "mutation") {
			// A mutation keyword anywhere but the operation head (several
			// operations in one document, a fragment trick): refuse rather
			// than guess.
			p.Refuse = "target: gh api graphql document mixes a mutation into a query"
		}
		return
	}
	ops := gqlTopFields(trimmed)
	if len(ops) == 0 {
		p.Refuse = "target: gh api graphql mutation has no recognizable operation"
		return
	}
	ids := reGQLThread.FindAllStringSubmatch(q, -1)
	var idVals []string
	for _, m := range ids {
		idVals = append(idVals, m[1])
	}
	for _, k := range []string{"threadId", "pullRequestReviewThreadId", "id"} {
		if v := fieldValue(p, k); v != "" {
			idVals = append(idVals, v)
		}
	}
	for _, op := range ops {
		kind, ok := ghThreadMutations[op]
		if !ok {
			p.Writes = append(p.Writes, Write{Kind: "other", Repo: ctx.Repo})
			p.Refuse = "target: gh api graphql mutation " + op + " is not a review-thread operation on the dispatch's PR"
			return
		}
		if len(idVals) != 1 || ctx.ThreadTarget == nil {
			p.Refuse = "target: gh api graphql " + op + ": the review thread cannot be resolved to a PR (pass exactly one threadId)"
			return
		}
		repo, num, err := ctx.ThreadTarget(idVals[0])
		if err != nil {
			p.Refuse = "target: gh api graphql " + op + ": " + err.Error()
			return
		}
		p.Writes = append(p.Writes, Write{Kind: kind, Repo: repo, Number: num})
	}
}

func init() {
	register("gh", ghProfile{}, homeView{
		Paths: []string{".config/gh"},
		Env: map[string]string{
			"GH_PROMPT_DISABLED": "1", "GH_NO_UPDATE_NOTIFIER": "1", "GH_PAGER": "cat",
			"NO_COLOR": "1", "GH_SPINNER_DISABLED": "1",
		},
	})
}

// gqlTopFields returns the top-level fields of a GraphQL operation's
// selection set (the mutations it runs), skipping aliases (`a: field(…)`),
// argument lists, and nested selections.
func gqlTopFields(doc string) []string {
	start := strings.IndexByte(doc, '{')
	if start < 0 {
		return nil
	}
	body := doc[start+1:]
	isIdent := func(c byte) bool {
		return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
	}
	var out []string
	braces, parens := 0, 0
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '"':
			// skip a string literal
			for i++; i < len(body) && body[i] != '"'; i++ {
				if body[i] == '\\' {
					i++
				}
			}
			continue
		case c == '(':
			parens++
			continue
		case c == ')':
			parens--
			continue
		case c == '{':
			braces++
			continue
		case c == '}':
			if braces == 0 {
				return out
			}
			braces--
			continue
		}
		if braces != 0 || parens != 0 || !isIdent(c) || (i > 0 && isIdent(body[i-1])) || c >= '0' && c <= '9' {
			continue
		}
		j := i
		for j < len(body) && isIdent(body[j]) {
			j++
		}
		name := body[i:j]
		k := j
		for k < len(body) && (body[k] == ' ' || body[k] == '\t' || body[k] == '\n' || body[k] == '\r' || body[k] == ',') {
			k++
		}
		if k < len(body) && body[k] == ':' {
			i = k // an alias; the real field follows
			continue
		}
		out = append(out, name)
		i = j - 1
	}
	return out
}
