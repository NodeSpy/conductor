package store

import (
	"path/filepath"
	"testing"
	"time"
)

// A conversation persists across a reopen, numbers its messages, bounds its
// transcript, and counts unread per operator (one's own messages are read).
func TestConversations(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	s, err := Open(Options{StatePath: p})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	err = s.UpdateConversation("team:platform", func(c *Conversation) error {
		c.Append(ChatMessage{At: now, From: "operator:op", Text: "hi", To: []string{"lead"}, Mode: "lead"})
		c.Append(ChatMessage{At: now, From: "bot:lead", Text: "hello", ReplyTo: 1})
		c.ReadUpTo = map[string]int{"op": 1}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := Open(Options{StatePath: p})
	if err != nil {
		t.Fatal(err)
	}
	c := s2.Conversation("team:platform")
	if c.Seq != 2 || len(c.Messages) != 2 || c.Messages[1].Seq != 2 || c.Messages[1].ReplyTo != 1 {
		t.Fatalf("after a reopen: %+v", c)
	}
	// One's own message is never unread, even below no read mark.
	c0 := c
	c0.ReadUpTo = nil
	if c0.Unread("op") != 1 {
		t.Fatalf("op's own message counted unread: %d", c0.Unread("op"))
	}
	if c.Unread("op") != 1 || c.Unread("op2") != 2 {
		t.Fatalf("unread: op %d, op2 %d", c.Unread("op"), c.Unread("op2"))
	}
	if ids := s2.ConversationIDs(); len(ids) != 1 || ids[0] != "team:platform" {
		t.Fatalf("ids: %v", ids)
	}
	// A copy is a copy.
	c.Messages[0].Text = "changed"
	if s2.Conversation("team:platform").Messages[0].Text != "hi" {
		t.Fatal("a reader mutated the store")
	}
	// Bounded.
	_ = s2.UpdateConversation("bot:sre", func(c *Conversation) error {
		for i := 0; i < ConversationRetention+10; i++ {
			c.Append(ChatMessage{From: "operator:op", Text: "x"})
		}
		return nil
	})
	if c := s2.Conversation("bot:sre"); len(c.Messages) != ConversationRetention || c.Messages[0].Seq != 11 || c.Seq != ConversationRetention+10 {
		t.Fatalf("retention: %d messages, first #%d", len(c.Messages), c.Messages[0].Seq)
	}
	if e := s2.Conversation("bot:none"); e.ID != "bot:none" || e.Seq != 0 {
		t.Fatalf("an unused conversation: %+v", e)
	}
}

// Pins are per operator, ordered, and persist; an empty list removes the
// operator's entry.
func TestChatPinsStore(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(Options{StatePath: p})
	_ = s.UpdateChatPins("op", func([]string) ([]string, error) { return []string{"team:platform", "bot:sre"}, nil })
	_ = s.UpdateChatPins("op2", func([]string) ([]string, error) { return []string{"bot:sre"}, nil })
	s2, _ := Open(Options{StatePath: p})
	if got := s2.ChatPins("op"); len(got) != 2 || got[0] != "team:platform" || got[1] != "bot:sre" {
		t.Fatalf("op's pins after a reopen: %v", got)
	}
	if got := s2.ChatPins("op2"); len(got) != 1 {
		t.Fatalf("op2's pins: %v", got)
	}
	got := s2.ChatPins("op")
	got[0] = "changed"
	if s2.ChatPins("op")[0] != "team:platform" {
		t.Fatal("a reader mutated the store")
	}
	_ = s2.UpdateChatPins("op", func([]string) ([]string, error) { return nil, nil })
	s3, _ := Open(Options{StatePath: p})
	if len(s3.ChatPins("op")) != 0 || len(s3.ChatPins("op2")) != 1 {
		t.Fatalf("after unpinning everything: %v %v", s3.ChatPins("op"), s3.ChatPins("op2"))
	}
}
