// Package hostcmd decides the host commands of an agent jail (#154 §2–§3, §5).
//
// Inside the workspace jail a set of binaries — gh, aws, kubectl, docker, … —
// are shims: conductor's own binary at the tool's normal path. The agent types
// the command as usual; the shim hands {argv, cwd, stdin, env} to conductor,
// which decides here, runs the REAL binary on the operator's machine (with a
// copy-on-write home — see Runner), and streams the result back.
//
// Decide applies, in order:
//
//  1. Built-in guardrails. A profile parses the command (gh's `pr merge`,
//     kubectl's verb + resource, aws's service + operation) and refuses the
//     known auth/config subcommands by name, plus containers that mount the
//     host; a binary without a profile gets the keyword guard (an argument of
//     `login`, `token`, `auth`, … is refused unless an allow rule names it).
//     A path guard refuses arguments that point into the operator's home
//     outside the workspace — a host command can read what the agent cannot,
//     so it must not be handed ~/.ssh/id_rsa as a `--body-file`.
//  2. The configured rules (Resolve): step ∧ runtime ∧ global. A step can
//     narrow, never widen.
//  3. The profile's own write binding (binding.go): gh's writes are bound to
//     the dispatch's own PR, git's pushes (GitPush) to its own branch —
//     binary knowledge, configured only through isolation.host.gh / .git.
//     conductor's own verbs (`conductor call github.*`) are a separate
//     surface, governed by the verb grant and the connector's scopes; neither
//     surface consults the other.
//
// Shims are policy fronts, never pass-throughs: every host command is checked
// here and recorded by the caller.
package hostcmd

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
)

// Request is one host-command invocation from a jail shim.
type Request struct {
	Tool string   // binary name (the shim's argv[0] basename)
	Args []string // argv[1:]
	Cwd  string   // the agent's working directory (a path inside the workspace)
}

// Write is a write a command performs on the agent's behalf, for the
// profile's write binding. Kind is a small vocabulary: comment, review,
// reply, resolve_thread, push, create_pr, create_issue, merge, close,
// reopen, edit, other (a repository-level write), other_repo (anything on
// another repository). Number 0 = not target-scoped (or unknown).
type Write struct {
	Kind   string
	Repo   string
	Number int
	Branch string // push: the branch written
}

// Parsed is a profile's reading of one command line.
type Parsed struct {
	// Words is the canonical command for rule matching: the command path and
	// positional arguments, flags removed. `gh pr merge --squash 42` and
	// `gh pr merge 42 --squash` both read ["pr","merge","42"].
	Words []string
	// Flags maps each normalized long flag ("--repo") to its values ("" for
	// a boolean). `-R x`, `--repo x` and `--repo=x` all land here the same.
	Flags map[string][]string
	// Refuse is a built-in guardrail refusal ("" = none), e.g.
	// "built-in: gh auth *".
	Refuse string
	// Writes are the writes the command performs, for target binding.
	Writes []Write
	// Native runs the command inside the jail instead of on the host (npm
	// install and the like: only publish/auth paths need the host).
	Native bool
	// PathArgs are the arguments that name local files, for the path guard
	// (profiles report them precisely; the generic scan catches the rest).
	PathArgs []string
	// ExtraArgs are appended to the host invocation (e.g. npm publish
	// --ignore-scripts, so a lifecycle script cannot run on the host).
	ExtraArgs []string
}

// Context is what Decide knows about the dispatch.
type Context struct {
	Repo       string // the dispatch's repository, owner/name
	Number     int    // its PR or issue number (0 = none)
	IsPR       bool
	HeadBranch string // the branch it may push (a PR's head, a branch-off's own)
	Workspace  string // the worktree, as seen at the same path on the host
	TmpDir     string // the dispatch's scratch /tmp, as seen on the host
	Home       string // the operator's home
	// Sensitive are extra paths no host command may be pointed at (the
	// daemon's state and config dirs), whether or not they are under Home.
	Sensitive []string
	// ReadFile reads a workspace file the command references (gh api
	// -F query=@q.graphql), bounded; nil → such files are treated as unknown.
	ReadFile func(path string) ([]byte, error)
	// ThreadTarget resolves a review-thread node id to the PR it belongs to
	// (for gh api graphql resolveReviewThread); nil → unresolvable.
	ThreadTarget func(nodeID string) (repo string, number int, err error)
	// LocalImage reports a docker image that was built on this machine
	// rather than pulled (docker run of it executes local content); nil →
	// treated as pulled.
	LocalImage func(ref string) bool
	// ReadOnly: a review step — the gh and git profiles refuse its writes
	// unless the operator's allow list names them.
	ReadOnly bool
	// TargetClosed reports why the dispatch's own target takes no more
	// writes ("" = it's open; nil = unknown): the gh and git profiles refuse
	// every write for a closed target.
	TargetClosed func() string
}

// Rule is one binary's effective configured rule, after Resolve.
type Rule struct {
	Tool     string
	Disabled bool
	// Allow holds one allow list per configuring layer; a command must match
	// every non-empty layer (a step's list can only narrow the runtime's).
	Allow [][]string
	// OperatorAllow are the allow patterns of the global and runtime layers
	// only: what may explicitly permit a content-executing command (a step
	// cannot widen).
	OperatorAllow []string
	Deny          []string
	Env           map[string]string
	Persist       []string
	Network       *config.IsolationNetwork
	// Profiled reports a built-in profile (parsed matching) vs argv matching.
	Profiled bool
}

// Decision is Decide's verdict.
type Decision struct {
	Allow  bool
	Reason string // why it was refused; shown to the agent and in watch
	Parsed Parsed
	// Native: run in the jail, not on the host.
	Native bool
	// Confine: the command executes workspace content and the operator's
	// config allows it — it runs in a host-side jail (see content.go).
	Confine bool
}

// Decide applies the guardrails, the rule, and the profile's write binding
// to req.
func Decide(req Request, rule Rule, ctx Context) Decision {
	if rule.Disabled {
		return Decision{Reason: fmt.Sprintf("%s is disabled for this dispatch (isolation.host.%s: false)", req.Tool, req.Tool)}
	}
	prof := profileFor(req.Tool)
	var p Parsed
	if prof != nil {
		p = prof.parse(req.Args, ctx)
	} else {
		p = parseGeneric(req.Args)
	}
	d := Decision{Parsed: p, Native: p.Native}
	if p.Native {
		// A native command runs in the jail like any other binary there;
		// only the configured rules apply (the jail is the boundary).
		if r := matchRules(rule, p, req.Args); r != "" {
			d.Reason = r
			return d
		}
		d.Allow = true
		return d
	}
	if p.Refuse != "" {
		d.Reason = "denied (" + p.Refuse + ")"
		return d
	}
	if prof == nil {
		if kw := keywordGuard(req.Args); kw != "" && !allowNames(rule, p, req.Args) {
			d.Reason = fmt.Sprintf("denied (built-in keyword guard: %q — an auth/credential action; name it in isolation.host.%s.allow if it is safe)", kw, req.Tool)
			return d
		}
	}
	if r := pathGuard(req, p, ctx); r != "" {
		d.Reason = r
		return d
	}
	if why, knob := contentExec(req.Tool, p, ctx); why != "" {
		if !explicitlyAllowed(rule, p) {
			d.Reason = fmt.Sprintf("denied (%s %s executes workspace content on the host: %s. Allow it in your config — isolation.host.%s.allow: [%q] — and it runs in a host-side jail)",
				req.Tool, strings.TrimSuffix(knob, " *"), why, req.Tool, knob)
			return d
		}
		d.Confine = true
	}
	if r := matchRules(rule, p, req.Args); r != "" {
		d.Reason = r
		return d
	}
	if b, ok := prof.(binder); ok {
		if r := b.bind(p, rule, ctx); r != "" {
			d.Reason = r
			return d
		}
	}
	d.Allow = true
	return d
}

// matchRules applies deny then allow. It returns the refusal reason or "".
func matchRules(rule Rule, p Parsed, argv []string) string {
	words, flags := p.Words, p.Flags
	if !rule.Profiled {
		words, flags = argvWords(argv)
	}
	for _, pat := range rule.Deny {
		if MatchRule(pat, words, flags) {
			return fmt.Sprintf("denied by %s.deny %q", rule.Tool, pat)
		}
	}
	for _, layer := range rule.Allow {
		if len(layer) == 0 {
			continue
		}
		ok := false
		for _, pat := range layer {
			if MatchRule(pat, words, flags) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Sprintf("denied: not in %s.allow %q", rule.Tool, layer)
		}
	}
	return ""
}

// allowNames reports whether an allow rule explicitly matches the command —
// the override for a keyword-guard false positive.
func allowNames(rule Rule, p Parsed, argv []string) bool {
	words, flags := argvWords(argv)
	any := false
	for _, layer := range rule.Allow {
		if len(layer) == 0 {
			continue
		}
		any = true
		ok := false
		for _, pat := range layer {
			if MatchRule(pat, words, flags) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return any
}

// MatchRule matches one rule pattern against a parsed command. The pattern
// is split into words; each word is a glob (`*` matches any run of
// characters, `/` included) matched against the command word at the same
// position, and a final lone `*` matches any number of remaining words (zero
// included). A word starting with `-` is a flag condition instead: it must be
// present among the command's flags wherever it appeared (`--admin`, or
// `--profile=prod` for a value) — flag order and spelling cannot dodge it.
func MatchRule(pattern string, words []string, flags map[string][]string) bool {
	var pw []string
	var pf []string
	for _, w := range strings.Fields(pattern) {
		if strings.HasPrefix(w, "-") && w != "-" {
			pf = append(pf, w)
		} else {
			pw = append(pw, w)
		}
	}
	for _, f := range pf {
		if !flagPresent(f, flags) {
			return false
		}
	}
	return matchWords(pw, words)
}

func matchWords(pw, words []string) bool {
	for i, p := range pw {
		if p == "*" && i == len(pw)-1 {
			return true
		}
		if i >= len(words) {
			return false
		}
		if !globMatch(p, words[i]) {
			return false
		}
	}
	return len(pw) == len(words)
}

// globMatch is path.Match with `*` crossing `/` (rules match URLs and paths
// as plain words).
func globMatch(pat, s string) bool {
	if pat == "*" {
		return true
	}
	// Escape '/' handling: path.Match's * stops at '/', so match segment by
	// segment when the pattern has no '/', else compare with a '/'-free
	// rewrite.
	p := strings.ReplaceAll(pat, "/", "\x00")
	t := strings.ReplaceAll(s, "/", "\x00")
	ok, err := path.Match(p, t)
	return err == nil && ok
}

func flagPresent(spec string, flags map[string][]string) bool {
	name, val, hasVal := strings.Cut(spec, "=")
	name = normFlag(name)
	vals, ok := flags[name]
	if !ok && len(name) > 2 && name[0] == '-' && name[1] != '-' {
		// A single-dash long flag (terraform's -destroy, -auto-approve):
		// profiles that accept either spelling record it as --name.
		vals, ok = flags["-"+name]
	}
	if !ok {
		return false
	}
	if !hasVal {
		return true
	}
	for _, v := range vals {
		if globMatch(val, v) {
			return true
		}
	}
	return false
}

// keywords is the built-in guard for binaries without a profile (#154 §3.2).
var keywords = map[string]bool{
	"login": true, "logout": true, "signin": true, "signout": true, "sign-in": true, "sign-out": true,
	"auth": true, "revoke": true, "configure": true, "credentials": true, "credential": true,
	"token": true, "tokens": true, "adduser": true,
}

// keywordGuard returns the first argument that is an auth/credential keyword.
func keywordGuard(args []string) string {
	for _, a := range args {
		k := strings.ToLower(strings.TrimLeft(a, "-"))
		if i := strings.IndexByte(k, '='); i >= 0 {
			k = k[:i]
		}
		if keywords[k] {
			return a
		}
	}
	return ""
}

// pathGuard refuses arguments that name files the agent cannot read itself:
// anywhere under the operator's home outside the workspace and the
// dispatch's scratch dir, and the daemon's own state/config wherever they
// live. A host command can read what the jail hides, so an argument like
// `--body-file ~/.ssh/id_rsa` would turn it into an exfiltration channel.
func pathGuard(req Request, p Parsed, ctx Context) string {
	cands := append([]string(nil), p.PathArgs...)
	for _, a := range req.Args {
		cands = append(cands, pathCandidates(a)...)
	}
	for _, c := range cands {
		abs := resolveArgPath(c, req.Cwd, ctx.Home)
		if abs == "" {
			continue
		}
		if r := forbiddenPath(abs, ctx); r != "" {
			return fmt.Sprintf("denied (path %q %s)", c, r)
		}
	}
	return ""
}

// pathCandidates extracts the file-path-looking parts of one argument:
// the argument itself, a `--flag=value` value, an `@file` reference, a
// `file://` URL, and each `k=v` / `src=`/`source=` part of a mount spec.
func pathCandidates(a string) []string {
	var out []string
	add := func(s string) {
		s = strings.TrimPrefix(s, "@")
		for _, pre := range []string{"fileb://", "file://"} {
			s = strings.TrimPrefix(s, pre)
		}
		if looksLikePath(s) {
			out = append(out, s)
		}
	}
	add(a)
	if strings.HasPrefix(a, "-") {
		if _, v, ok := strings.Cut(a, "="); ok {
			add(v)
		}
	}
	if _, v, ok := strings.Cut(a, "=@"); ok {
		add(v)
	}
	if strings.Contains(a, ",") || strings.Contains(a, "=") {
		for _, part := range strings.Split(a, ",") {
			if _, v, ok := strings.Cut(part, "="); ok {
				add(v)
			}
		}
	}
	return out
}

func looksLikePath(s string) bool {
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") ||
		strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") || s == ".." ||
		strings.Contains(s, "/../")
}

// resolveArgPath turns a candidate into an absolute, cleaned path ("" if it
// is not a local path at all).
func resolveArgPath(c, cwd, home string) string {
	switch {
	case c == "~" || strings.HasPrefix(c, "~/"):
		if home == "" {
			return ""
		}
		c = filepath.Join(home, strings.TrimPrefix(c, "~"))
	case strings.HasPrefix(c, "~"):
		// ~otheruser/… — never inside the workspace.
		return "/~" + c
	case !filepath.IsAbs(c):
		if cwd == "" {
			return ""
		}
		c = filepath.Join(cwd, c)
	}
	return filepath.Clean(c)
}

func forbiddenPath(abs string, ctx Context) string {
	within := func(root string) bool {
		if root == "" {
			return false
		}
		root = filepath.Clean(root)
		return abs == root || strings.HasPrefix(abs, root+string(filepath.Separator))
	}
	for _, s := range ctx.Sensitive {
		if within(s) && !within(ctx.Workspace) {
			return "is conductor's own state/config"
		}
	}
	if strings.HasPrefix(abs, "/~") {
		return "is another user's home"
	}
	switch abs {
	case "/dev/null", "/dev/stdin", "/dev/stdout", "/dev/stderr", "/dev/zero", "/dev/urandom", "/dev/random":
		return ""
	}
	// Kernel and session interfaces: a host command must not be pointed at
	// another process's environment (/proc/<pid>/environ — the daemon's own
	// secrets) or the session's sockets (/run/user/<uid>: agents, keyrings).
	for _, k := range []string{"/proc", "/sys", "/dev", "/run/user", "/var/run/user"} {
		if within(k) {
			return "is a kernel/session interface"
		}
	}
	if within(ctx.Workspace) || within(ctx.TmpDir) {
		return ""
	}
	if within(ctx.Home) {
		return "is outside the workspace (the operator's home is not readable from the jail)"
	}
	return ""
}

// Resolve folds the configuring layers — global, runtime, step, in that
// order; nil layers are skipped — into tool's effective rule. step reports
// whether the LAST layer is a step's block: a step can narrow (false, a
// stricter allow, more denies) but never widen — it cannot name a binary
// that the layers above and the built-ins do not make a host command, and it
// cannot set env/persist (validate refuses those).
func Resolve(tool string, layers []*config.IsolationConfig, step bool) Rule {
	r := Rule{Tool: tool, Profiled: profileFor(tool) != nil}
	for i, l := range layers {
		if l == nil || l.Host == nil {
			continue
		}
		h, ok := l.Host[tool]
		if !ok || h == nil {
			continue
		}
		isStep := step && i == len(layers)-1
		if h.Disabled {
			r.Disabled = true
			continue
		}
		if len(h.Allow) > 0 {
			r.Allow = append(r.Allow, append([]string(nil), h.Allow...))
			if !isStep {
				r.OperatorAllow = append(r.OperatorAllow, h.Allow...)
			}
		}
		r.Deny = append(r.Deny, h.Deny...)
		if !isStep {
			for k, v := range h.Env {
				if r.Env == nil {
					r.Env = map[string]string{}
				}
				r.Env[k] = v
			}
			r.Persist = append(r.Persist, h.Persist...)
		}
		if h.Network != nil {
			r.Network = stricterNetwork(r.Network, h.Network)
		}
	}
	return r
}

// netRank orders network modes by restrictiveness.
func netRank(n *config.IsolationNetwork) int {
	switch {
	case n == nil || n.Mode == config.NetOpen:
		return 0
	case n.Mode == config.NetAudit:
		return 1
	case n.Mode == config.NetDeny || (n.Deny && len(n.Egress) == 0):
		return 3
	default:
		return 2 // an egress allowlist
	}
}

// stricterNetwork keeps the more restrictive of two network blocks; two
// allowlists intersect, so a later layer can drop hosts but never add one.
func stricterNetwork(a, b *config.IsolationNetwork) *config.IsolationNetwork {
	if a == nil {
		return b
	}
	ra, rb := netRank(a), netRank(b)
	if ra == 2 && rb == 2 {
		keep := map[string]bool{}
		for _, e := range b.Egress {
			keep[e] = true
		}
		var both []string
		for _, e := range a.Egress {
			if keep[e] {
				both = append(both, e)
			}
		}
		return &config.IsolationNetwork{Egress: both, Deny: true}
	}
	if rb > ra {
		return b
	}
	return a
}

// Builtins are the host commands conductor knows how to profile — each a
// host command when installed on the machine (#154 §2). git is not a shim:
// its network and signing side reach conductor through the remote helper
// and the signing shim instead (§4).
var Builtins = []string{"gh", "aws", "kubectl", "docker", "terraform", "gcloud", "az", "ssh", "scp", "npm", "pnpm"}

// HostSet is the dispatch's host set: the built-ins installed on the machine
// plus any binary an operator layer names, minus any set to false. It
// returns the enabled names and, separately, the disabled ones (which the
// jail still shims — with a refusal — so the real binary stays unreachable).
func HostSet(layers []*config.IsolationConfig, step bool, lookPath func(string) (string, error)) (enabled, disabled []string) {
	seen := map[string]bool{}
	var names []string
	for _, b := range Builtins {
		if _, err := lookPath(b); err == nil {
			names = append(names, b)
			seen[b] = true
		}
	}
	for i, l := range layers {
		if l == nil {
			continue
		}
		isStep := step && i == len(layers)-1
		for n, h := range l.Host {
			if seen[n] || h == nil || h.Disabled || n == "git" {
				// git is never a shim: its network and signing side are
				// brokered, and host.git configures its push binding.
				continue
			}
			if isStep {
				continue // a step cannot add a host command
			}
			if _, err := lookPath(n); err != nil {
				continue
			}
			names = append(names, n)
			seen[n] = true
		}
	}
	all := map[string]bool{}
	for _, n := range names {
		all[n] = true
	}
	for _, l := range layers {
		if l == nil {
			continue
		}
		for n, h := range l.Host {
			if h != nil && h.Disabled {
				all[n] = true
			}
		}
	}
	for n := range all {
		r := Resolve(n, layers, step)
		if r.Disabled {
			disabled = append(disabled, n)
		} else if seen[n] {
			enabled = append(enabled, n)
		}
	}
	sort.Strings(enabled)
	sort.Strings(disabled)
	return enabled, disabled
}
