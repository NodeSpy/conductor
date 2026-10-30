package main

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/bot"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dashboard"
)

// Every write route the dashboard mounts from the bot API (the real list: the
// bot service's routes, the avatar and the wallet routes) has a rule at the
// dashboard's chokepoint, and every rule names a real route. A new write
// route fails here until it has one (and is refused at runtime meanwhile).
func TestEveryMountedBotWriteRouteHasAChokepointRule(t *testing.T) {
	cfg := &config.Config{}
	m := &botStack{svc: &bot.Service{Cfg: func() *config.Config { return cfg }}}
	routes, authority := dashboardBotRoutes(m)
	if authority == nil || len(routes) == 0 {
		t.Fatal("no routes")
	}
	mounted := map[string]bool{}
	for _, r := range routes {
		mounted[r.Pattern] = true
		method, _, _ := strings.Cut(r.Pattern, " ")
		if method == "" || strings.HasPrefix(method, "/") {
			t.Errorf("%q: a route with no method would take writes as reads", r.Pattern)
			continue
		}
		if method != "GET" && !dashboard.BotWriteRuled(r.Pattern) {
			t.Errorf("write route %q has no rule at the dashboard's chokepoint", r.Pattern)
		}
	}
	for _, p := range dashboard.BotWriteRulePatterns() {
		if !mounted[p] {
			t.Errorf("rule %q names no mounted route", p)
		}
	}
}
