package main

import (
	"strings"
	"testing"
)

// `conductor team` is the operator's: a bot session answers with `bot reply`.
func TestTeamCommandRefusesASession(t *testing.T) {
	t.Setenv("CONDUCTOR_SKILL_TOKEN", "x")
	if err := cmdTeam([]string{"say", "platform", "hi"}); err == nil || !strings.Contains(err.Error(), "conductor bot reply") {
		t.Fatalf("in a session: %v", err)
	}
}

// A transcript line names the sender, who it reached and how, and what a
// reply answers.
func TestChatLineCLI(t *testing.T) {
	op := chatLineCLI(map[string]any{"seq": 3.0, "from": "operator:op", "to": []any{"a", "b"}, "mode": "everyone", "text": "roll call"})
	if op != "#3 operator:op → a, b (everyone): roll call" {
		t.Fatalf("operator line: %q", op)
	}
	if r := chatLineCLI(map[string]any{"seq": 4.0, "from": "bot:a", "reply_to": 3.0, "text": "here"}); r != "#4 bot:a (reply to #3): here" {
		t.Fatalf("reply line: %q", r)
	}
}

// --everyone is a switch: it never swallows the message.
func TestTeamSayEveryoneIsABoolFlag(t *testing.T) {
	pos, fl, _, _ := botFlags([]string{"platform", "--everyone", "roll", "call"})
	if fl["everyone"] != "true" || strings.Join(pos, " ") != "platform roll call" {
		t.Fatalf("pos %v, flags %v", pos, fl)
	}
}
