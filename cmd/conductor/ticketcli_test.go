package main

import (
	"strings"
	"testing"
)

// `conductor ticket …` parses to the ticket ops: repeatable labels, the
// ready switch, the status verbs.
func TestTicketRequest(t *testing.T) {
	parse := func(args ...string) (string, map[string]any) {
		pos, fl, _, _ := botFlags(args[1:])
		op, a, err := ticketRequest(args[0], pos, fl)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return op, a
	}
	op, a := parse("new", "rotate", "keys", "--assignee", "team:platform", "--label", "ops", "--label", "security", "--ready", "--due", "2026-11-01")
	if op != "ticket_new" || a["title"] != "rotate keys" || a["assignee"] != "team:platform" || a["ready"] != true ||
		strings.Join(a["labels"].([]string), ",") != "ops,security" || a["due"] != "2026-11-01" {
		t.Fatalf("new: %s %v", op, a)
	}
	if op, a = parse("done", "T-3", "all", "good"); op != "ticket_status" || a["to"] != "done" || a["note"] != "all good" || a["ticket"] != "T-3" {
		t.Fatalf("done: %s %v", op, a)
	}
	for verb, to := range map[string]string{"ready": "ready", "wait": "waiting", "decline": "declined", "draft": "draft", "reopen": "reopen", "resume": "in_progress"} {
		if _, a := parse(verb, "T-1"); a["to"] != to {
			t.Errorf("%s → %v", verb, a["to"])
		}
	}
	if _, a = parse("list", "--status", "draft,ready"); strings.Join(a["status"].([]string), ",") != "draft,ready" {
		t.Fatalf("list: %v", a)
	}
	if _, a = parse("list", "--all"); len(a["status"].([]string)) != 6 {
		t.Fatalf("list --all: %v", a)
	}
	if _, a = parse("edit", "T-1", "--no-labels", "--priority", "high"); len(a["labels"].([]string)) != 0 || a["priority"] != "high" {
		t.Fatalf("edit: %v", a)
	}
	if _, _, err := ticketRequest("frobnicate", nil, map[string]string{}); err == nil {
		t.Fatal("an unknown command")
	}
	t.Setenv("CONDUCTOR_SKILL_TOKEN", "x")
	if err := cmdTicket([]string{"list"}); err == nil || !strings.Contains(err.Error(), "conductor bot ticket") {
		t.Fatalf("in a session: %v", err)
	}
}
