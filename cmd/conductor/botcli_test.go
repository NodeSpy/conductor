package main

import (
	"strings"
	"testing"
)

func botListFixture() map[string]any {
	b := func(name, folder, team string) any {
		m := map[string]any{"bot": name, "shadow": true, "open_cases": 0, "pending_proposals": 0, "live_workers": 0}
		if folder != "" {
			m["folder"] = folder
		}
		if team != "" {
			m["team"] = team
		}
		return m
	}
	return map[string]any{"bots": []any{
		b("solo", "", ""),
		b("platform-sre", "work/platform", "platform"),
		b("platform-data", "work/platform", "platform"),
		b("web-sre", "work/platform", ""),
		b("billing-sre", "work/billing", "billing"),
		b("home-bot", "personal/home", ""),
		b("ops-bot", "work", ""),
	}}
}

func lineNames(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		f := strings.Fields(l)
		if len(f) > 0 && f[0] == "team" {
			out = append(out, l[:len(l)-len(strings.TrimLeft(l, " "))]+"team "+f[1])
			continue
		}
		indent := l[:len(l)-len(strings.TrimLeft(l, " "))]
		out = append(out, indent+f[0])
	}
	return out
}

func TestBotListTree(t *testing.T) {
	got := strings.Join(lineNames(botListText(botListFixture(), "", false)), "\n")
	want := strings.Join([]string{
		"solo",
		"personal/",
		"  home/",
		"    home-bot",
		"work/",
		"  ops-bot",
		"  billing/",
		"    team billing",
		"      billing-sre",
		"  platform/",
		"    team platform",
		"      platform-data",
		"      platform-sre",
		"    web-sre",
	}, "\n")
	if got != want {
		t.Fatalf("tree:\n%s\nwant:\n%s\nraw:\n%s", got, want, botListText(botListFixture(), "", false))
	}
}

func TestBotListFolderFilterAndFlat(t *testing.T) {
	got := strings.Join(lineNames(botListText(botListFixture(), "work/platform", false)), "\n")
	want := "work/\n  platform/\n    team platform\n      platform-data\n      platform-sre\n    web-sre"
	if got != want {
		t.Fatalf("--folder work/platform:\n%s", got)
	}
	// Descendants are included; a name prefix is not a folder.
	w := botListText(botListFixture(), "work", false)
	for _, n := range []string{"ops-bot", "billing-sre", "platform-sre", "web-sre"} {
		if !strings.Contains(w, n) {
			t.Errorf("--folder work lost %s", n)
		}
	}
	if strings.Contains(w, "home-bot") || strings.Contains(w, "solo") {
		t.Errorf("--folder work kept bots outside it:\n%s", w)
	}
	if s := botListText(botListFixture(), "wor", false); !strings.Contains(s, "no bots in folder wor") {
		t.Errorf("a name prefix matched as a folder:\n%s", s)
	}
	flat := botListText(botListFixture(), "", true)
	lines := strings.Split(strings.TrimRight(flat, "\n"), "\n")
	if len(lines) != 7 {
		t.Fatalf("--flat: %d lines\n%s", len(lines), flat)
	}
	for _, l := range lines {
		if strings.HasPrefix(l, " ") || strings.HasSuffix(strings.Fields(l)[0], "/") {
			t.Errorf("--flat has tree structure: %q", l)
		}
	}
	if f := strings.Fields(lines[0]); f[0] != "solo" || f[1] != "/" || f[2] != "-" {
		t.Errorf("--flat first line: %q", lines[0])
	}
	if s := botListText(botListFixture(), "personal", true); strings.Count(s, "\n") != 1 || !strings.Contains(s, "home-bot") {
		t.Errorf("--flat --folder personal:\n%s", s)
	}
	if s := botListText(map[string]any{}, "", false); s != "no bots\n" {
		t.Errorf("empty: %q", s)
	}
}

// A bot session's shell cannot run the operator's repo edits (the agent
// socket has no such op either; this is the honest-case refusal).
func TestBotRepoEditRefusedInASession(t *testing.T) {
	t.Setenv("CONDUCTOR_SKILL_TOKEN", "tok")
	for _, sub := range []string{"knowledge", "memory"} {
		err := cmdBot([]string{sub, "put", "platform", "knowledge/x.md", "--text", "x", "--base", "abc1234"})
		if err == nil || !strings.Contains(err.Error(), "refusing: this shell is a conductor agent session") {
			t.Errorf("%s in a session: %v", sub, err)
		}
	}
}

func TestBotRepoEditNeedsABase(t *testing.T) {
	for _, args := range [][]string{
		{"put", "platform", "knowledge/x.md"},
		{"rm", "platform", "knowledge/x.md"},
		{"mv", "platform", "knowledge/x.md", "knowledge/y.md"},
		{"revert", "platform", "abc1234"},
	} {
		err := botRepoEdit(nil, "knowledge", args, map[string]string{"text": "x"})
		if err == nil || !strings.Contains(err.Error(), "--base <commit> is required") {
			t.Errorf("%v without --base: %v", args, err)
		}
	}
	if err := botRepoEdit(nil, "knowledge", []string{"put", "platform", "knowledge/x.md"}, map[string]string{"base": "abc1234"}); err == nil || !strings.Contains(err.Error(), "needs the content") {
		t.Errorf("put without content: %v", err)
	}
	if err := botRepoEdit(nil, "memory", []string{"bogus", "sre"}, map[string]string{}); err == nil || !strings.Contains(err.Error(), "conductor bot memory put <bot>") {
		t.Errorf("usage: %v", err)
	}
}

// A session reads memory as text: a recall's ranked list, a read's
// truncation note BEFORE the text and its sections after, an index as is.
func TestBotMemoryPlainText(t *testing.T) {
	for _, c := range []struct {
		sub  string
		pos  []string
		want string
	}{{"recall", nil, "recall"}, {"read", nil, "read"}, {"index", nil, "index"}, {"team", []string{"recall", "x"}, "recall"},
		{"team", []string{"promote"}, ""}, {"cases", nil, ""}} {
		if got := memView(c.sub, c.pos); got != c.want {
			t.Errorf("memView(%s %v) = %q, want %q", c.sub, c.pos, got, c.want)
		}
	}
	out, _ := captureStdout(t, func() error {
		printMem("read", map[string]any{"path": "knowledge/n.md", "bytes": 300, "text": "## A\nbody", "note": "TRUNCATED: read on with --offset 9.",
			"sections": []any{"A", "B"}})
		return nil
	})
	if !strings.HasPrefix(out, "== knowledge/n.md (300 bytes) ==\nTRUNCATED") || !strings.Contains(out, "## A\nbody\n== sections: A | B ==") {
		t.Fatalf("read:\n%s", out)
	}
	out, _ = captureStdout(t, func() error {
		printMem("recall", map[string]any{"hits": []any{map[string]any{"path": "log/x.md", "kind": "log", "title": "T", "snippet": "…s…", "archived": "abc123"}}, "note": "Best match first."})
		return nil
	})
	if !strings.Contains(out, "1. log/x.md (log) [archived abc123] — T\n   …s…\nBest match first.") {
		t.Fatalf("recall:\n%s", out)
	}
}
