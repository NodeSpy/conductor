package bot

// Read models for the dashboard's Bots screens (design §4.4): the bot roster
// (teams are the page), one bot's header and tabs, a bot's proposals, and every
// pending proposal across bots for the Needs-you / Approvals surface. These wrap
// the same ledger + config the CLI reads; they add no state.

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/store"
)

// registerViewRoutes adds the bot read models to the operator API mux.
func (s *Service) registerViewRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/bots", s.apiBots)
	mux.HandleFunc("GET /v1/bots/{bot}", s.apiBot)
	mux.HandleFunc("GET /v1/bots/{bot}/proposals", s.apiBotProposals)
	mux.HandleFunc("GET /v1/proposals", s.apiProposals)
}

// botSummary is a bot's row on the Bots screen: what it is, its facts line, and
// the counts its one quiet status mark needs.
func (s *Service) botSummary(name string) map[string]any {
	cfg := s.Cfg()
	m := cfg.Bots[name]
	rec := s.Ledger.Bot(name)
	open, pending, workers := 0, 0, 0
	for _, c := range rec.Cases {
		if store.Active(c.Status) {
			open++
		}
	}
	for _, p := range rec.Proposals {
		if p.Status == "pending" {
			pending++
		}
	}
	for _, w := range rec.Workers {
		if w.Status == "starting" || w.Status == "running" {
			workers++
		}
	}
	return map[string]any{
		"bot": name, "description": m.Description, "team": string(m.Team),
		"shadow": m.Autonomy.ShadowMode(), "disabled": m.Disabled,
		"home_runtime": cfg.BotHomeRuntime(name), "identity": m.Identity.View(name, m.IdentityFrom),
		"open_cases": open, "pending_proposals": pending, "live_workers": workers,
		"capabilities": s.capabilityWords(name),
	}
}

// capabilityWords is "Does on its own / Asks you first / Never" for a bot,
// computed from its grants, effective tier and promotions (design §4.4). In
// shadow (practice) mode nothing runs on its own.
func (s *Service) capabilityWords(name string) map[string]any {
	cfg := s.Cfg()
	m := cfg.Bots[name]
	shadow := m.Autonomy.ShadowMode()
	var does, asks []string
	if m.Skill != nil {
		for _, v := range m.Skill.Verbs {
			tier := Classify(m, v)
			promoted := !shadow && tier == config.TierReversible && matchAny(m.Autonomy.Promote, v)
			if !shadow && (tier == config.TierObserve || promoted) {
				does = append(does, v)
			} else {
				asks = append(asks, v)
			}
		}
	}
	sort.Strings(does)
	sort.Strings(asks)
	return map[string]any{"does": does, "asks": asks, "shadow": shadow}
}

func matchAny(pats []string, uses string) bool {
	for _, p := range pats {
		if p == uses || p == "*" {
			return true
		}
	}
	return false
}

func (s *Service) apiBots(w http.ResponseWriter, r *http.Request) {
	cfg := s.Cfg()
	names := make([]string, 0, len(cfg.Bots))
	for n := range cfg.Bots {
		names = append(names, n)
	}
	sort.Strings(names)
	bots := make([]any, 0, len(names))
	for _, n := range names {
		bots = append(bots, s.botSummary(n))
	}
	teams := []any{}
	for _, t := range cfg.TeamNames() {
		teams = append(teams, s.teamStatus(r.Context(), t, false))
	}
	apiJSON(w, http.StatusOK, map[string]any{"bots": bots, "teams": teams})
}

func (s *Service) apiBot(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("bot")
	if _, ok := s.Cfg().Bots[name]; !ok {
		apiError(w, http.StatusNotFound, fmt.Errorf("unknown bot %q", name))
		return
	}
	out := s.statusOf(name)
	out["capabilities"] = s.capabilityWords(name)
	out["team"] = string(s.Cfg().Bots[name].Team)
	apiJSON(w, http.StatusOK, out)
}

func (s *Service) apiBotProposals(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("bot")
	if _, ok := s.Cfg().Bots[name]; !ok {
		apiError(w, http.StatusNotFound, fmt.Errorf("unknown bot %q", name))
		return
	}
	ps := s.Proposals(name, true)
	out := make([]any, 0, len(ps))
	for _, p := range ps {
		out = append(out, proposalRow(name, p))
	}
	apiJSON(w, http.StatusOK, map[string]any{"bot": name, "proposals": out})
}

// apiProposals is every pending proposal across every bot — the Needs-you queue
// and the Approvals surface.
func (s *Service) apiProposals(w http.ResponseWriter, r *http.Request) {
	cfg := s.Cfg()
	names := make([]string, 0, len(cfg.Bots))
	for n := range cfg.Bots {
		names = append(names, n)
	}
	sort.Strings(names)
	out := []any{}
	for _, n := range names {
		for _, p := range s.Proposals(n, false) {
			if p.Status == "pending" {
				out = append(out, proposalRow(n, p))
			}
		}
	}
	apiJSON(w, http.StatusOK, map[string]any{"proposals": out})
}

// proposalRow is a proposal as the dashboard lists it: enough for the row and the
// approval screen's header; the canonical payload + digest come from the approval
// endpoint behind the step-up.
func proposalRow(bot string, p store.Proposal) map[string]any {
	row := map[string]any{
		"bot": bot, "proposal": p.ID, "kind": p.Kind, "tier": p.Tier, "uses": p.Uses,
		"status": p.Status, "digest": p.Digest, "case": p.CaseID,
		"requester": p.Requester, "requester_class": p.RequesterClass,
		"summary": p.Summary, "action": ProposalAction(p),
	}
	if !p.Created.IsZero() {
		row["created"] = p.Created
	}
	if !p.Expires.IsZero() {
		row["expires"] = p.Expires
	}
	if p.Options != nil {
		row["options"] = p.Options
	}
	if tf := p.Terraform; tf != nil {
		row["terraform"] = map[string]any{
			"repo": tf.Repo, "dir": tf.Dir, "workspace": tf.Workspace,
			"add": tf.Add, "change": tf.Change, "destroy": tf.Destroy,
			"plan": tf.PlanText, "plan_sha256": tf.PlanSHA256,
			"replaced": tf.Replaced, "destroyed": tf.Destroyed,
		}
	}
	return row
}
