package config

import (
	"fmt"
	"strings"
)

// TICKETS (docs/wiki/Tickets.md): conductor's own ticket system. Config
// sets two things, per team and per bot:
//
//	teams:
//	  platform:
//	    tickets: { default_priority: normal, mark_own_ready: false }
//	bots:
//	  platform-sre:
//	    tickets: { mark_own_ready: true }   # a WIDENING: its own ideas may start without an operator
//
// Only an operator marks a ticket ready by default — ready is what lets work
// start. mark_own_ready lets a bot mark the tickets it created itself ready;
// it is off by default, a team's false is a ceiling a bot cannot lift, and
// `conductor validate` names it as a widening.
type TicketSettings struct {
	DefaultPriority string `yaml:"default_priority,omitempty"`
	MarkOwnReady    *bool  `yaml:"mark_own_ready,omitempty"`
}

// TicketPriorities are the priorities, lowest first.
var TicketPriorities = []string{"low", "normal", "high", "urgent"}

func (t TicketSettings) validate(team bool) error {
	if t.DefaultPriority != "" {
		if !team {
			return fmt.Errorf("tickets.default_priority is a team setting")
		}
		if !hasString(TicketPriorities, t.DefaultPriority) {
			return fmt.Errorf("tickets.default_priority: %q is not one of %s", t.DefaultPriority, strings.Join(TicketPriorities, ", "))
		}
	}
	return nil
}

// BotMarksOwnReady reports whether bot may mark the tickets it created
// ready: its own setting, else its team's; off by default.
func (c *Config) BotMarksOwnReady(bot string) bool {
	m := c.Bots[bot]
	if v := m.Tickets.MarkOwnReady; v != nil {
		return *v
	}
	if t, ok := c.Teams[string(m.Team)]; ok && t.Tickets.MarkOwnReady != nil {
		return *t.Tickets.MarkOwnReady
	}
	return false
}

// TicketDefaultPriority is a new ticket's priority for an assignee
// ("team:<name>", "bot:<name>" or ""): the team's default, else normal.
func (c *Config) TicketDefaultPriority(assignee string) string {
	kind, name, _ := strings.Cut(assignee, ":")
	team := name
	if kind == "bot" {
		team = string(c.Bots[name].Team)
	}
	if t, ok := c.Teams[team]; ok && t.Tickets.DefaultPriority != "" {
		return t.Tickets.DefaultPriority
	}
	return "normal"
}
