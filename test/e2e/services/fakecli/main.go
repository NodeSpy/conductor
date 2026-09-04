// Command fakecli is a hermetic stand-in for the bare-CLI agent tools conductor's
// `cli` transport controller (internal/controller/cli.go) drives — installed on
// PATH as both `claude` (claude-code recipe) and `codex` (codex recipe). Conductor
// execs it as a direct subprocess IN the conductor-provisioned PR worktree (the
// controller sets cmd.Dir) with the acts-as-the-user identity in its env, exactly
// as it would the real tool. It performs the shared fixer edit+commit+push (package
// fixer) so `forge_has_conductor_commit` passes for the cli:claude-code and
// cli:codex rows, with NO LLM and NO secrets.
//
// It matches the two built-in recipes in internal/controller/cli.go:
//
//	claude -p <prompt> --output-format json [--resume <id>]   → emits {"session_id":...}
//	codex exec <prompt>                                        → oneshot, plain output
//
// NOT part of the shipped product; harness-only.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NodeSpy/conductor/test/e2e/services/fixer"
)

func main() {
	tool := filepath.Base(os.Args[0])
	args := os.Args[1:]
	cwd, _ := os.Getwd()

	switch tool {
	case "codex":
		// codex exec <prompt>
		prompt := ""
		if len(args) >= 2 && args[0] == "exec" {
			prompt = args[1]
		}
		_ = fixer.Apply(cwd, "cli:codex", prompt)
		fmt.Println("codex: done")
	default: // claude / claude-code
		// claude -p <prompt> --output-format json [--resume <id>]
		prompt, resume := parseClaude(args)
		runtime := "cli:claude-code"
		if resume != "" {
			runtime = "cli:claude-code(resume)"
		}
		_ = fixer.Apply(cwd, runtime, prompt)
		// The controller parses session_id off this JSON to `--resume` a follow-up.
		fmt.Printf("{\"session_id\":%q,\"result\":\"claude: done\"}\n", "claude-session-1")
	}
}

func parseClaude(args []string) (prompt, resume string) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--print":
			if i+1 < len(args) {
				prompt = args[i+1]
				i++
			}
		case "--resume":
			if i+1 < len(args) {
				resume = args[i+1]
				i++
			}
		case "--output-format":
			i++ // skip its value
		default:
			if prompt == "" && !strings.HasPrefix(args[i], "-") {
				prompt = args[i]
			}
		}
	}
	return prompt, resume
}
