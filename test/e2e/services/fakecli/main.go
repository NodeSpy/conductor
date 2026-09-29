// Command fakecli is a hermetic stand-in for the bare-CLI agent tools conductor's
// `cli` transport controller (internal/controller/cli.go) drives — installed on
// PATH as both `claude` (claude-code recipe) and `codex` (codex recipe). Conductor
// execs it as a direct subprocess IN the conductor-provisioned PR worktree (the
// controller sets cmd.Dir) with the acts-as-the-user identity in its env, exactly
// as it would the real tool. It performs the shared fixer edit+commit+push (package
// fixer) so `forge_has_conductor_commit` passes for the cli:claude-code and
// cli:codex rows, with NO LLM and NO secrets.
//
// It matches the two built-in recipes in internal/controller/cli.go, which
// send the prompt on stdin (as the real tools accept it), never in argv:
//
//	claude -p --output-format json [--resume <id>] < prompt   → emits {"session_id":...}
//	codex exec - < prompt                                      → oneshot, plain output
//
// A positional prompt argument is still honored, as the real tools do.
//
// NOT part of the shipped product; harness-only.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/NodeSpy/conductor/test/e2e/services/fixer"
)

func main() {
	tool := filepath.Base(os.Args[0])
	args := os.Args[1:]
	cwd, _ := os.Getwd()

	switch tool {
	case "codex":
		// codex exec - < prompt  (or codex exec <prompt>)
		prompt := ""
		if len(args) >= 1 && args[0] == "exec" {
			prompt = parseCodex(args[1:])
		}
		if prompt == "-" || prompt == "" {
			prompt = readStdin()
		}
		_ = fixer.Apply(cwd, "cli:codex", prompt)
		fmt.Println("codex: done")
	default: // claude / claude-code
		// claude -p --output-format json [--resume <id>] < prompt
		prompt, resume := parseClaude(args)
		if prompt == "" {
			prompt = readStdin()
		}
		runtime := "cli:claude-code"
		if resume != "" {
			runtime = "cli:claude-code(resume)"
		}
		_ = fixer.Apply(cwd, runtime, prompt)
		// A `[[reply {...}]]` marker is the scripted structured answer for the
		// output_schema-on-cli scenario (Group T): echo it into the `result`
		// field so conductor's controller path extracts + validates it, exactly
		// as it would a real claude reply. Absent the marker, the ordinary
		// fixer-shaped "done" result. The controller parses session_id off this
		// JSON to `--resume` a follow-up.
		result := "claude: done"
		if r, ok := replyDirective(prompt); ok {
			result = r
		}
		fmt.Printf("{\"session_id\":%q,\"result\":%q}\n", "claude-session-1", result)
	}
}

// replyDirective extracts a `[[reply {...json...}]]` marker from the prompt —
// the fake agent's scripted structured answer for output_schema scenarios,
// matching fakepaseo's own marker so a scenario reads identically on either
// runtime.
var replyDirectiveRe = regexp.MustCompile(`(?s)\[\[reply (\{.*?\})\]\]`)

func replyDirective(prompt string) (string, bool) {
	m := replyDirectiveRe.FindStringSubmatch(prompt)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// readStdin returns the prompt piped on stdin ("" when there is none —
// conductor leaves stdin at /dev/null for a launch with no stdin).
func readStdin() string {
	b, _ := io.ReadAll(os.Stdin)
	return string(b)
}

// parseClaude reads a positional prompt (if any) and --resume off claude's
// argv. `-p`/`--print` is a boolean flag, as in the real CLI; the flags that
// take a value have it skipped so it can't be mistaken for the prompt.
func parseClaude(args []string) (prompt, resume string) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--print":
		case "--resume":
			if i+1 < len(args) {
				resume = args[i+1]
				i++
			}
		case "--output-format", "--model", "--tools", "--system-prompt", "--json-schema":
			i++ // skip its value
		default:
			if prompt == "" && !strings.HasPrefix(args[i], "-") {
				prompt = args[i]
			}
		}
	}
	return prompt, resume
}

// parseCodex returns `codex exec`'s positional prompt argument ("-" means
// stdin), skipping the values of the flags conductor's recipes pass.
func parseCodex(args []string) string {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--model", "--output-schema", "--output-last-message", "--sandbox", "--cd":
			i++ // skip its value
		default:
			if args[i] == "-" || !strings.HasPrefix(args[i], "-") {
				return args[i]
			}
		}
	}
	return ""
}
