package main

import (
	"fmt"
	"os"
	"strings"
)

const teamUsage = `usage:
  conductor team say <team> [--to <bot>]… [--everyone] [--as <operator>] [--attach <file>]… <text…>
      # default: the team's lead (else its routing); @bot in the text or --to reaches that member;
      # --everyone reaches every member (one reply each; chat.everyone_per_hour caps it)
  conductor team chat <team|bot:<name>> [--as <operator>] [--all]   # the transcript (new messages; --all: every one) — marks them read
  conductor team chats [--as <operator>]                            # every conversation: the last message, your unread count`

// cmdTeam is `conductor team …`: the operator's side of team chat.
func cmdTeam(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Println(teamUsage)
		return nil
	}
	if os.Getenv("CONDUCTOR_SKILL_TOKEN") != "" {
		return fmt.Errorf("refusing: this shell is a conductor agent session — a bot answers with `conductor bot reply`")
	}
	sub := args[0]
	cfg, rest, err := loadConfig(args[1:])
	if err != nil {
		return err
	}
	pos, fl, _, _ := botFlags(rest)
	a := map[string]any{"as": fl["as"]}
	switch sub {
	case "say":
		if len(pos) < 1 || (len(pos) < 2 && fl["attach"] == "") {
			return fmt.Errorf("%s", teamUsage)
		}
		a["conversation"] = "team:" + pos[0]
		a["text"] = strings.Join(pos[1:], " ")
		if fl["to"] != "" {
			a["to"] = strings.Split(fl["to"], "\x00")
		}
		a["everyone"] = fl["everyone"] == "true"
		if err := attachFiles(a, fl["attach"]); err != nil {
			return err
		}
		out, _, err := botControl(cfg, "chat_send", a)
		if err != nil {
			return err
		}
		fmt.Printf("#%v → %v (%v)\n", out["seq"], out["to"], out["mode"])
		return nil
	case "chat":
		if len(pos) != 1 {
			return fmt.Errorf("%s", teamUsage)
		}
		conv := pos[0]
		if !strings.Contains(conv, ":") {
			conv = "team:" + conv
		}
		a["conversation"] = conv
		if fl["all"] == "" {
			// Only what is new to this operator.
			list, _, err := botControl(cfg, "chats", map[string]any{"as": fl["as"]})
			if err != nil {
				return err
			}
			a["after"] = unreadFrom(list, conv)
		}
		out, _, err := botControl(cfg, "chat_read", a)
		if err != nil {
			return err
		}
		ms, _ := out["messages"].([]any)
		if len(ms) == 0 {
			fmt.Println("no new messages")
		}
		for _, x := range ms {
			m, _ := x.(map[string]any)
			fmt.Println(chatLineCLI(m))
		}
		return nil
	case "chats":
		out, _, err := botControl(cfg, "chats", a)
		if err != nil {
			return err
		}
		cs, _ := out["conversations"].([]any)
		for _, x := range cs {
			c, _ := x.(map[string]any)
			last := "-"
			if l, ok := c["last"].(map[string]any); ok {
				last = fmt.Sprintf("%v: %v", l["from"], oneLineCLI(fmt.Sprint(l["text"]), 60))
			}
			fmt.Printf("%-24v %3v unread  %s\n", c["id"], c["unread"], last)
		}
		return nil
	}
	return fmt.Errorf("unknown team command %q\n%s", sub, teamUsage)
}

// unreadFrom is the seq to read after: the operator's read mark, computed
// from the conversation's last seq and its unread count (0 reads all).
func unreadFrom(list map[string]any, conv string) int {
	cs, _ := list["conversations"].([]any)
	for _, x := range cs {
		c, _ := x.(map[string]any)
		if c["id"] != conv {
			continue
		}
		l, ok := c["last"].(map[string]any)
		if !ok {
			return 0
		}
		seq, _ := l["seq"].(float64)
		unread, _ := c["unread"].(float64)
		if unread == 0 {
			return int(seq)
		}
		return 0
	}
	return 0
}

func chatLineCLI(m map[string]any) string {
	line := fmt.Sprintf("#%v %v", m["seq"], m["from"])
	if r, ok := m["reply_to"].(float64); ok && r > 0 {
		line += fmt.Sprintf(" (reply to #%v)", r)
	}
	if to, ok := m["to"].([]any); ok && len(to) > 0 {
		var ns []string
		for _, t := range to {
			ns = append(ns, fmt.Sprint(t))
		}
		line += fmt.Sprintf(" → %s (%v)", strings.Join(ns, ", "), m["mode"])
	}
	return line + ": " + fmt.Sprint(m["text"])
}

func oneLineCLI(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
