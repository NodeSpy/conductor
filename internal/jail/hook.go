package jail

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
)

// Tool-call visibility (#154 §11). claude-code runs every tool call past
// conductor through PreToolUse/PostToolUse hooks (passed at launch with
// --settings): conductor records the call, and may refuse it with a reason
// the model sees. The optional intent rules live here. This layer is
// guidance plus visibility, not a wall — a shell can express an action in a
// way no tool-call rule recognizes; the guarantees stay with the jail, the
// host-command policy, and brokered git.

// HookSettings is the --settings JSON wiring claude-code's hooks to the
// shim at binDir.
func HookSettings(binDir string) string {
	cmd := func(ev string) []map[string]any {
		return []map[string]any{{
			"matcher": "*",
			"hooks":   []map[string]any{{"type": "command", "command": filepath.Join(binDir, ShimHook) + " " + ev, "timeout": 60}},
		}}
	}
	b, _ := json.Marshal(map[string]any{"hooks": map[string]any{
		"PreToolUse":  cmd("PreToolUse"),
		"PostToolUse": cmd("PostToolUse"),
	}})
	return string(b)
}

// hookInput is the part of claude-code's hook payload conductor reads.
type hookInput struct {
	Event     string          `json:"hook_event_name"`
	Tool      string          `json:"tool_name"`
	Input     json.RawMessage `json:"tool_input"`
	Response  json.RawMessage `json:"tool_response"`
	SessionID string          `json:"session_id"`
	Cwd       string          `json:"cwd"`
}

func (m *Manager) handleHook(_ context.Context, d *Dispatch, req Request, fw *frameWriter) {
	var in hookInput
	_ = json.Unmarshal(req.Hook, &in)
	if in.Event == "" {
		in.Event = req.Event
	}
	summary := toolSummary(in.Tool, in.Input)
	if in.Event == "PostToolUse" {
		st := "done"
		if isErrorResponse(in.Response) {
			st = "error"
		}
		m.emit(d, Event{Type: "tool_result", Status: st, Detail: summary})
		_ = fw.send(Reply{Done: true})
		return
	}
	if reason := intentCheck(d.Intent, d.Workspace, in.Tool, in.Input); reason != "" {
		m.emit(d, Event{Type: "tool_call", Status: "refused", Detail: summary, Reason: reason})
		_ = fw.send(Reply{Done: true, Decision: "deny", Reason: reason})
		return
	}
	m.emit(d, Event{Type: "tool_call", Status: "ok", Detail: summary})
	_ = fw.send(Reply{Done: true})
}

// toolSummary renders one tool call for watch: `Bash: git push …`,
// `Edit: src/x.go`.
func toolSummary(tool string, input json.RawMessage) string {
	var m map[string]any
	_ = json.Unmarshal(input, &m)
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	s := pick("command", "file_path", "notebook_path", "path", "url", "pattern", "query", "description")
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	if s == "" {
		return tool
	}
	return tool + ": " + s
}

func isErrorResponse(r json.RawMessage) bool {
	var m map[string]any
	if json.Unmarshal(r, &m) != nil {
		return false
	}
	if v, ok := m["is_error"].(bool); ok && v {
		return true
	}
	if v, ok := m["interrupted"].(bool); ok && v {
		return true
	}
	return false
}

// fileTools are the tool calls that edit a file (claude-code names).
var fileTools = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true}

// intentCheck evaluates the optional intent rules for one tool call ("" =
// allowed).
func intentCheck(r *config.IntentRules, ws, tool string, input json.RawMessage) string {
	if r == nil {
		return ""
	}
	for _, t := range r.DenyTools {
		if strings.EqualFold(t, tool) {
			return fmt.Sprintf("intent: tool %s is not used in this step (isolation.intent.deny_tools)", tool)
		}
	}
	var m map[string]any
	_ = json.Unmarshal(input, &m)
	str := func(k string) string { s, _ := m[k].(string); return s }
	if fileTools[tool] {
		p := firstNonEmpty(str("file_path"), str("notebook_path"))
		rel, ok := relIn(ws, p)
		if !ok {
			return fmt.Sprintf("intent: %s edits %s, outside the workspace", tool, p)
		}
		if len(r.AllowPaths) > 0 && !matchAnyGlob(r.AllowPaths, rel) {
			return fmt.Sprintf("intent: %s is outside the paths this step may edit (%s)", rel, strings.Join(r.AllowPaths, ", "))
		}
		if matchAnyGlob(r.DenyPaths, rel) {
			return fmt.Sprintf("intent: %s is a path this step must not edit (isolation.intent.deny_paths)", rel)
		}
		if tool == "Write" && r.MaxDeleteLines > 0 && strings.TrimSpace(str("content")) == "" {
			if n := lineCount(filepath.Join(ws, rel)); n > r.MaxDeleteLines {
				return fmt.Sprintf("intent: emptying %s (%d lines) is over the %d-line delete limit", rel, n, r.MaxDeleteLines)
			}
		}
	}
	if tool == "Bash" && r.MaxDeleteLines > 0 {
		for _, f := range rmTargets(str("command")) {
			rel, ok := relIn(ws, f)
			if !ok {
				continue
			}
			if n := lineCount(filepath.Join(ws, rel)); n > r.MaxDeleteLines {
				return fmt.Sprintf("intent: deleting %s (%d lines) is over the %d-line delete limit", rel, n, r.MaxDeleteLines)
			}
		}
	}
	if tool == "Bash" && (len(r.DenyPaths) > 0) {
		for _, f := range rmTargets(str("command")) {
			if rel, ok := relIn(ws, f); ok && matchAnyGlob(r.DenyPaths, rel) {
				return fmt.Sprintf("intent: %s is a path this step must not touch (isolation.intent.deny_paths)", rel)
			}
		}
	}
	return ""
}

func relIn(ws, p string) (string, bool) {
	if p == "" {
		return "", false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(ws, p)
	}
	p = filepath.Clean(p)
	if !within(p, ws) {
		return "", false
	}
	rel, err := filepath.Rel(ws, p)
	return rel, err == nil
}

var reRm = regexp.MustCompile(`(?:^|[;&|]\s*|\s)(?:rm|git\s+rm)\s+([^;&|]+)`)

// rmTargets pulls the file arguments of rm / git rm out of a shell command
// (best effort — this layer is guidance).
func rmTargets(cmd string) []string {
	var out []string
	for _, m := range reRm.FindAllStringSubmatch(cmd, -1) {
		for _, f := range strings.Fields(m[1]) {
			if !strings.HasPrefix(f, "-") {
				out = append(out, strings.Trim(f, `"'`))
			}
		}
	}
	return out
}

func lineCount(p string) int {
	f, err := os.Open(p)
	if err != nil {
		return 0
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		n++
	}
	return n
}

// matchAnyGlob matches workspace-relative globs where `**` spans
// directories and `*` does not.
func matchAnyGlob(pats []string, rel string) bool {
	for _, p := range pats {
		if globRe(p).MatchString(rel) {
			return true
		}
	}
	return false
}

func globRe(p string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(p); i++ {
		switch c := p[i]; c {
		case '*':
			if i+1 < len(p) && p[i+1] == '*' {
				b.WriteString(".*")
				i++
				if i+1 < len(p) && p[i+1] == '/' {
					i++
					b.WriteString("/?")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return regexp.MustCompile(`^\b$`)
	}
	return re
}

// RunHookShim is `conductor-hook <event>` inside the jail: claude-code's
// hook payload on stdin goes to the broker; a refusal is returned in the
// hook output format the model sees.
func RunHookShim(args []string) int {
	ev := "PreToolUse"
	if len(args) > 0 {
		ev = args[0]
	}
	raw, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if !json.Valid(raw) {
		raw = []byte("{}")
	}
	rep, err := Call(Request{Op: "hook", Event: ev, Hook: raw}, nil)
	if err != nil {
		// Visibility only: an unreachable broker does not block the agent
		// (host commands and git's network fail closed on their own).
		return 0
	}
	if rep.Decision == "deny" && ev == "PreToolUse" {
		out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": "conductor: " + rep.Reason,
		}})
		_, _ = os.Stdout.Write(append(out, '\n'))
	}
	return 0
}
