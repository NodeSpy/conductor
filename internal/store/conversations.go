package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// CONVERSATIONS: an operator's chat with a bot ("bot:<name>") or a team
// ("team:<name>"), one transcript conductor records — the operator's
// messages and the bots' replies — with each operator's read mark. Kept
// beside the bot ledger in conversations.json, bounded.

// ConversationRetention bounds a transcript (oldest dropped).
const ConversationRetention = 500

// Conversation is one transcript.
type Conversation struct {
	ID       string        `json:"id"`
	Messages []ChatMessage `json:"messages,omitempty"`
	// Seq is the last message number; ReadUpTo is each operator's last
	// read message number (unread = messages after it from anyone else).
	Seq      int            `json:"seq"`
	ReadUpTo map[string]int `json:"read_up_to,omitempty"`
	// Slots are the replies still owed: one per (operator message, bot)
	// conductor delivered it to — "<seq>:<bot>". A reply spends its slot;
	// a bot with none has nothing to answer.
	Slots map[string]bool `json:"slots,omitempty"`
	// Fanouts are when the team's `everyone` messages were sent (bounded,
	// for the per-hour cap).
	Fanouts []time.Time `json:"fanouts,omitempty"`
}

// ChatMessage is one transcript entry.
type ChatMessage struct {
	Seq  int       `json:"seq"`
	At   time.Time `json:"at"`
	From string    `json:"from"` // "operator:<name>" | "bot:<name>"
	Text string    `json:"text"`
	// To are the bots an operator message was delivered to, and Mode how
	// they were chosen: lead | routing | mention | to | everyone | bot.
	To   []string `json:"to,omitempty"`
	Mode string   `json:"mode,omitempty"`
	// ReplyTo is the operator message a bot's reply answers.
	ReplyTo int `json:"reply_to,omitempty"`
	// Cases are the bots' cases the message lives on (bot → case).
	Cases       map[string]string `json:"cases,omitempty"`
	Attachments []string          `json:"attachments,omitempty"`
}

func (s *Store) convsPath() string {
	if s.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.path), "conversations.json")
}

func (s *Store) loadConversations() {
	s.convs = map[string]*Conversation{}
	p := s.convsPath()
	if p == "" {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	if err := json.Unmarshal(b, &s.convs); err != nil || s.convs == nil {
		_ = os.Rename(p, fmt.Sprintf("%s.corrupt-%d", p, time.Now().Unix()))
		s.convs = map[string]*Conversation{}
	}
}

func cloneConv(c *Conversation) *Conversation {
	b, _ := json.Marshal(c)
	var out Conversation
	_ = json.Unmarshal(b, &out)
	return &out
}

// Conversation is a copy of one transcript (an empty one when never used).
func (s *Store) Conversation(id string) Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.convs[id]; c != nil {
		return *cloneConv(c)
	}
	return Conversation{ID: id}
}

// ConversationIDs lists every conversation with a transcript, sorted.
func (s *Store) ConversationIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.convs))
	for id := range s.convs {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// UpdateConversation edits a copy of one transcript under its lock and
// persists it before returning (transactional, like UpdateBot).
func (s *Store) UpdateConversation(id string, fn func(*Conversation) error) error {
	ml := s.botLock("conversation:" + id)
	ml.Lock()
	defer ml.Unlock()
	s.mu.Lock()
	work := &Conversation{ID: id}
	if cur := s.convs[id]; cur != nil {
		work = cloneConv(cur)
	}
	if err := fn(work); err != nil {
		s.mu.Unlock()
		return err
	}
	if n := len(work.Messages); n > ConversationRetention {
		work.Messages = work.Messages[n-ConversationRetention:]
	}
	if s.convs == nil {
		s.convs = map[string]*Conversation{}
	}
	s.convs[id] = work
	s.mu.Unlock()
	return s.persist(func() ([]byte, string, error) {
		b, err := json.MarshalIndent(s.convs, "", " ")
		return b, s.convsPath(), err
	})
}

// Append adds a message, numbering it.
func (c *Conversation) Append(m ChatMessage) ChatMessage {
	c.Seq++
	m.Seq = c.Seq
	c.Messages = append(c.Messages, m)
	return m
}

// Unread counts the messages after op's read mark that op did not write.
func (c Conversation) Unread(op string) int {
	n := 0
	for _, m := range c.Messages {
		if m.Seq > c.ReadUpTo[op] && m.From != "operator:"+op {
			n++
		}
	}
	return n
}
