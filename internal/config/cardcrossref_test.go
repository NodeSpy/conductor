package config

import (
	"strings"
	"testing"
)

// TestCardCrossRefWarning (M4): a bot's or team's purchases.cards.<name> that
// does not name a card in browser.cards must produce a load-time warning. At
// runtime the per-card limit is a plain map lookup (limits.go), so a mistyped
// name silently drops that tighter layer and only the looser overall cap binds
// — previously with no sign at load or `conductor validate`.
func TestCardCrossRefWarning(t *testing.T) {
	lim := CardLimits{PerPurchase: 20, PerDay: 50, PerMonth: 100}
	overall := CardLimits{PerPurchase: 40, PerDay: 100, PerMonth: 300}
	cfg := &Config{
		Browser: &BrowserConfig{Cards: map[string]BrowserCard{"visa": {}}},
		Bots: map[string]BotConfig{
			"m": {Purchases: &PurchaseLimits{
				CardLimits: overall,
				Cards:      map[string]CardLimits{"visa": lim, "viza": lim},
			}},
		},
		Teams: map[string]TeamConfig{
			"t": {Budget: TeamBudget{Purchases: &PurchaseLimits{
				CardLimits: overall,
				Cards:      map[string]CardLimits{"amex": lim},
			}}},
		},
	}
	w := strings.Join(cfg.BotWarnings(), "\n")
	if strings.Contains(w, "cards.visa:") {
		t.Fatalf("M4: a card that exists in browser.cards was flagged: %s", w)
	}
	if !strings.Contains(w, "bots.m.purchases.cards.viza:") {
		t.Fatalf("M4: a mistyped bot card was not flagged: %s", w)
	}
	if !strings.Contains(w, "teams.t.budget.purchases.cards.amex:") {
		t.Fatalf("M4: a missing team card was not flagged: %s", w)
	}

	// With no browser.cards block at all, every referenced card is flagged.
	none := &Config{Bots: map[string]BotConfig{
		"m": {Purchases: &PurchaseLimits{CardLimits: overall, Cards: map[string]CardLimits{"visa": lim}}},
	}}
	if !strings.Contains(strings.Join(none.BotWarnings(), "\n"), "no browser.cards are configured") {
		t.Fatalf("M4: a card ref with no browser.cards was not flagged: %v", none.BotWarnings())
	}

	// A config whose per-card keys all resolve emits no card cross-ref note.
	clean := &Config{
		Browser: &BrowserConfig{Cards: map[string]BrowserCard{"visa": {}, "amex": {}}},
		Bots:    map[string]BotConfig{"m": {Purchases: &PurchaseLimits{CardLimits: overall, Cards: map[string]CardLimits{"visa": lim}}}},
	}
	if got := strings.Join(clean.BotWarnings(), "\n"); strings.Contains(got, "per-card limit") {
		t.Fatalf("M4: a valid per-card limit was flagged: %s", got)
	}
}
