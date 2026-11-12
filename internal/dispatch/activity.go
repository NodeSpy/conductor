package dispatch

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// AGENT ACTIVITY FOR A FOREGROUND PASEO DISPATCH.
//
// `paseo run` blocks until the agent's turn ends and streams nothing, so
// while it runs the dispatcher watches the agent from the side: it finds the
// agent by a label the launch carries, and every activityPoll reads the last
// few entries of its timeline (`paseo logs <id> --tail n`) and reports the
// most recent tool call it sees there as one short phrase through
// Request.Activity. Only when the request asks for it (a run whose progress
// is reported), and best-effort throughout: a read that fails is skipped.

// activityLabel names the agent a foreground dispatch launched, so the
// activity watcher can find it while `paseo run` still blocks.
const activityLabel = "conductor-activity"

// activityPoll is how often the watcher reads the agent's timeline; a var so
// tests can shorten it.
var activityPoll = 30 * time.Second

// activityTail is how many timeline entries one read takes.
const activityTail = 6

// watchActivity reports the activity of the agent labelled token through
// report until ctx ends. Run in its own goroutine.
func (d *Dispatcher) watchActivity(ctx context.Context, token string, report func(string)) {
	t := time.NewTicker(activityPoll)
	defer t.Stop()
	id, last := "", ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if id == "" {
			agents, err := d.backend().ListAgents(ctx, map[string]string{activityLabel: token})
			if err != nil || len(agents) == 0 {
				continue
			}
			id = agents[0].ID
		}
		text, err := d.backend().AgentLog(ctx, id, activityTail)
		if err != nil && text == "" {
			continue
		}
		if p := LastToolPhrase(text); p != "" && p != last {
			last = p
			report(p)
		}
	}
}

// timelineTool matches one tool-call entry of a paseo timeline: "[Name]
// summary" (or "[Name]" alone).
var timelineTool = regexp.MustCompile(`^\[([A-Za-z][A-Za-z0-9 _.:/-]{0,40})\](?:\s+(.*))?$`)

// notTools are the timeline's non-tool entry labels.
var notTools = map[string]bool{"user": true, "thought": true, "assistant": true}

// toolVerbs turns a coding agent's common tool names into words a reader of
// the status understands. Anything else keeps its own name.
var toolVerbs = map[string]string{
	"bash": "running", "shell": "running", "terminal": "running", "exec": "running",
	"edit": "editing", "multiedit": "editing", "write": "writing", "notebookedit": "editing",
	"read": "reading", "view": "reading",
	"grep": "searching", "glob": "searching", "search": "searching", "ls": "listing",
	"webfetch": "fetching", "fetch": "fetching", "websearch": "searching the web",
	"todowrite": "planning", "task": "delegating",
}

// LastToolPhrase is the most recent tool call in a paseo timeline tail, as a
// short phrase ("editing adapters/adapter.go", "running go test ./..."), or
// "" when the tail shows none. A tool whose summary is a JSON blob (an
// external tool's raw input) is reported by name only.
func LastToolPhrase(timeline string) string {
	lines := strings.Split(timeline, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		m := timelineTool.FindStringSubmatch(strings.TrimSpace(lines[i]))
		if m == nil || notTools[strings.ToLower(m[1])] {
			continue
		}
		name := strings.TrimSpace(m[1])
		verb, known := toolVerbs[strings.ToLower(strings.ReplaceAll(name, " ", ""))]
		if !known {
			verb = "using " + name
		}
		arg := strings.TrimSpace(m[2])
		if strings.HasPrefix(arg, "{") || strings.HasPrefix(arg, "[") {
			arg = ""
		}
		return strings.TrimSpace(verb + " " + oneLine(arg, 80))
	}
	return ""
}

// oneLine collapses s to a single line of at most max runes, dropping
// control and invisible format characters.
func oneLine(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}
