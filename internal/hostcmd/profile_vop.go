package hostcmd

import (
	"fmt"
	"path/filepath"
	"strings"
)

// vop (#paseo-hostcmd feature 2): a credential-injecting launcher — `vop exec
// <profile> -- <command>` fetches AWS credentials from 1Password and runs
// <command> with them. Judging it means judging TWO commands: vop's own
// subcommand (does it reveal or change credentials/config, open a shell?)
// and, for exec, the inner command it is about to launch as its own child —
// which is a host command in its own right and gets its own full Decide:
// its own built-in guardrails, its own configured rule
// (isolation.host.<inner>), its own path guard. vop is never a pass-through.
//
// vop's own subcommand surface (from `vop --help` / `vop skill`, v0.8.1):
//
//	refused outright, always (credentials/config revealed or changed, or an
//	unbounded surface):
//	  dump          prints active session credentials
//	  cred-process  prints credentials for AWS credential_process
//	  cat           prints the profiles config (even redacted, it names
//	                every configured account and its 1Password item)
//	  add/edit/rm   change profile config
//	  rotate        rotates AWS access keys
//	  migrate       changes profile config/state
//	  auth          pushes credentials to the credential server
//	  serve         opens a credential-serving listener on the host
//	  shell         an unbounded interactive shell — never
//	  unlock        clears a profile's rate-limit cooldown (vop's own skill
//	                text: don't run this unless the user asks)
//	  refresh       "refresh credentials in an active vop shell" — exec is
//	                stateless; there is no active shell session for it to
//	                refresh under the broker's one-shot model
//	read-only, no secret material in the output: ls, profile, show, version,
//	check, skill, test, completion, help — allowed
//	exec: judged below; every other/unknown subcommand is refused by default
//	bare `vop` (no subcommand): prints usage, harmless
var vopRefused = map[string]string{
	"dump":         "built-in: vop dump (prints active session credentials)",
	"cred-process": "built-in: vop cred-process (prints credentials for AWS credential_process)",
	"cat":          "built-in: vop cat (prints the profiles config)",
	"add":          "built-in: vop add (changes profile config)",
	"edit":         "built-in: vop edit (changes profile config)",
	"rm":           "built-in: vop rm (changes profile config)",
	"rotate":       "built-in: vop rotate (rotates AWS access keys)",
	"migrate":      "built-in: vop migrate (changes profile config/state)",
	"auth":         "built-in: vop auth (pushes credentials to the credential server)",
	"serve":        "built-in: vop serve (opens a credential-serving listener on the host)",
	"shell":        "built-in: vop shell (an unbounded interactive shell)",
	"unlock":       "built-in: vop unlock (clears a profile's rate-limit cooldown)",
	"refresh":      "built-in: vop refresh (vop exec is stateless; there is no active vop shell session to refresh)",
}

// vopReads are vop's read-only, secret-free subcommands.
var vopReads = set("ls", "profile", "show", "version", "check", "skill", "test", "completion", "help")

// vopInnerAllowed are the inner commands `vop exec` may launch: vop fetches
// AWS credentials, so only tools that use AWS credentials and carry their
// own built-in profile (their own guardrails, path guard, content-exec
// check) qualify — never a shell, an interpreter, `env`, `vop` itself, or an
// unprofiled binary, which would make vop an unbounded exec-as-the-operator
// primitive.
var vopInnerAllowed = set("aws", "terraform", "kubectl")

var vopSpec = flagSpec{value: set("--config")}

type vopProfile struct{}

func (vopProfile) parse(args []string, _ Context) Parsed {
	pos, flags := parseFlags(args, vopSpec)
	p := Parsed{Words: pos, Flags: flags}
	cmd := p.word(0)
	if cmd == "" {
		return p // bare `vop` prints usage
	}
	if r, ok := vopRefused[cmd]; ok {
		p.Refuse = r
		return p
	}
	if vopReads[cmd] {
		return p
	}
	if cmd != "exec" {
		p.Refuse = "built-in: vop " + cmd + " (not recognized; refused by default)"
		return p
	}
	// From here on work off the RAW argv after "exec", not `pos`: parseFlags
	// treats "--" as its own end-of-flags marker and drops it, but for vop
	// "--" is the profile/command separator itself — whether it is there at
	// all is exactly what distinguishes an explicit profile from one vop
	// would resolve from the environment or a .vop file.
	rest := rawArgsAfter(args, "exec")
	if len(rest) == 0 || rest[0] == "--" {
		p.Refuse = "built-in: vop exec needs an explicit profile (vop exec <profile> -- …) — name it; " +
			"resolving one from AGENT_DECK_VOP_PROFILE/VOP_PROFILE/.vop is refused here"
		return p
	}
	profile := rest[0]
	inner := rest[1:]
	if len(inner) > 0 && inner[0] == "--" {
		inner = inner[1:]
	}
	if len(inner) == 0 {
		p.Refuse = "built-in: vop exec " + profile + " needs a command after --"
		return p
	}
	innerTool := filepath.Base(inner[0])
	if !vopInnerAllowed[innerTool] {
		p.Refuse = fmt.Sprintf("built-in: vop exec may only run aws, terraform, or kubectl as the inner command (got %q)", inner[0])
		return p
	}
	// Canonical words for rule matching are just "exec <profile>": the
	// operator's isolation.host.vop.allow names the profile, not the inner
	// command (that is isolation.host.<inner>'s own business).
	p.Words = []string{"exec", profile}
	p.InnerTool = innerTool
	p.InnerArgs = inner[1:]
	return p
}

// bind is vop's write/exposure binding (binder, see binding.go): `exec`
// opens real AWS credentials to whatever it runs, so the profile name must
// be named literally in the operator's own allow list (mirroring gh/git's
// namesCommand: two literal leading words, "exec" and the profile — a
// blanket "*" or "exec *" never opens it), and the inner command gets its
// own full Decide — its built-in guardrails, its configured
// isolation.host.<inner> rule, its path guard. A content-executing inner
// command (terraform plan, …) is refused rather than run confined: a
// confined run under vop (the child of a child, credentials already
// injected) is not supported in this version.
func (vopProfile) bind(p Parsed, rule Rule, ctx Context) string {
	if len(p.Words) == 0 || p.Words[0] != "exec" {
		return ""
	}
	profile := ""
	if len(p.Words) > 1 {
		profile = p.Words[1]
	}
	if !namesCommand(rule, p.Words, p.Flags, 2, 2) {
		return "vop: exec " + profile + " is refused unless the profile is named" + knob("vop", "exec "+profile+" *")
	}
	if p.InnerTool == "" {
		// parse() refuses every exec without a valid inner tool; this is
		// defensive, not reachable in practice.
		return "vop: exec " + profile + " has no inner command to run"
	}
	if ctx.RuleFor == nil {
		return fmt.Sprintf("vop: exec %s -- %s cannot be evaluated here (no inner-rule resolver wired)", profile, p.InnerTool)
	}
	innerRule := ctx.RuleFor(p.InnerTool)
	// A grant for the whole `vop exec <profile> -- <cmd>` is the operator
	// naming that exact command, inner write included.
	innerRule.Granted = rule.Granted
	innerDec := Decide(Request{Tool: p.InnerTool, Args: p.InnerArgs, Cwd: ctx.Cwd}, innerRule, ctx)
	switch {
	case innerDec.Native:
		return fmt.Sprintf("vop: exec %s -- %s: does not run under vop (it is a native-only command)", profile, p.InnerTool)
	case innerDec.Confine:
		return fmt.Sprintf("vop: exec %s -- %s %s executes workspace content on the host — confined runs are not supported under vop in this version",
			profile, p.InnerTool, strings.Join(innerDec.Parsed.Words, " "))
	case !innerDec.Allow:
		return fmt.Sprintf("vop: exec %s -- %s", profile, innerDec.Reason)
	}
	// Read-only by default: real AWS credentials are about to reach the
	// inner command, so a write runs only when the operator's own allow list
	// for that tool names it literally (a blanket "*" does not) — the same
	// bar gh's writes and content-executing commands clear.
	if what := vopInnerWrite(p.InnerTool, innerDec.Parsed); what != "" && !explicitlyAllowed(innerRule, innerDec.Parsed) {
		return fmt.Sprintf("vop: exec %s -- %s %s is a write, refused under vop unless your config names it%s",
			profile, p.InnerTool, what, knob(p.InnerTool, what+" *"))
	}
	return ""
}

// vopInnerWrite reports a write by the inner command — what it changes in
// the account or cluster — as the words naming it (for the refusal and the
// knob), or "" for a read. Unknown means write: the list names the reads.
func vopInnerWrite(tool string, p Parsed) string {
	w0, w1 := p.word(0), p.word(1)
	switch tool {
	case "aws":
		if w0 == "s3" {
			if w1 == "ls" {
				return ""
			}
			return "s3 " + w1
		}
		for _, pre := range []string{"describe-", "list-", "get-", "head-", "batch-get-", "lookup-", "search-", "query", "scan", "select-", "estimate-", "validate-", "simulate-", "test-"} {
			if strings.HasPrefix(w1, pre) {
				return ""
			}
		}
		if w1 == "" || w1 == "help" || w1 == "wait" {
			return ""
		}
		return strings.TrimSpace(w0 + " " + w1)
	case "terraform":
		switch w0 {
		case "show", "output", "validate", "version", "providers", "graph", "fmt", "-version", "help", "":
			return ""
		case "state":
			if w1 == "list" || w1 == "show" || w1 == "pull" {
				return ""
			}
			return "state " + w1
		case "workspace":
			if w1 == "list" || w1 == "show" {
				return ""
			}
			return "workspace " + w1
		}
		return w0
	case "kubectl":
		switch w0 {
		case "get", "describe", "logs", "top", "explain", "api-resources", "api-versions", "version", "cluster-info", "diff", "help", "":
			return ""
		case "auth":
			if w1 == "can-i" || w1 == "whoami" {
				return ""
			}
		case "config":
			if w1 == "view" || w1 == "get-contexts" || w1 == "current-context" || w1 == "get-clusters" {
				return ""
			}
			return "config " + w1
		}
		return w0
	}
	return tool
}

// rawArgsAfter returns the raw argv following the first bare occurrence of
// word (unprocessed: flags, "--", everything), or nil if word is not found.
func rawArgsAfter(args []string, word string) []string {
	for i, a := range args {
		if a == word {
			return args[i+1:]
		}
	}
	return nil
}

func init() {
	register("vop", vopProfile{}, homeView{
		// vop's own profile config, the 1Password CLI's config (`op`, which
		// vop shells out to or links against for the actual secret fetch),
		// and the 1Password desktop app's integration socket directory. Its
		// own directory is also persisted: `vop unlock` is refused, but its
		// rate-limit cooldown bookkeeping (what unlock clears) must still
		// take effect across the agent's repeated exec attempts, or that
		// anti-abuse mechanism is silently defeated by the copy-on-write
		// home discarding it every run.
		//
		// NOTE: if the operator's 1Password integration relies on the
		// desktop app's biometric unlock rather than a service-account
		// token, the live agent socket commonly lives under
		// $XDG_RUNTIME_DIR/1password (not a $HOME path at all) — that is not
		// wired here; such a setup needs a service-account token (an
		// environment/file vop reads from its own profile config) to work
		// from a host command at all, same as any other headless use of op.
		Paths:   []string{".config/vop", ".config/op", ".1password"},
		Persist: []string{".config/vop"},
	})
}
