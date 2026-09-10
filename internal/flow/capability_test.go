package flow

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// The capability surface (docs/design/skill-capability-and-pack-interface.md
// §A/§B). The property these tests exist to protect is that the three
// agent-facing views of a grant cannot drift:
//
//	the CARD        — what the prompt tells the agent it can do
//	DISCOVER        — what `conductor discover` / the MCP tool list reports
//	ENFORCEMENT     — what RunSkillVerb will actually run
//
// All three come from one resolution (GrantedVerbs + matchAny over the same
// patterns), and TestGrantIsOneSourceOfTruth asserts they agree for every
// grant form.

const capCfg = `
connectors:
  svc:   { use: fake }
  other: { use: fake }
`

// capRunner builds a runner over two fake connectors.
func capRunner(t *testing.T) *Runner {
	t.Helper()
	cfg := loadConfig(t, capCfg)
	return newTestRunner(t, cfg, buildRegistry(t, cfg)).Runner
}

// discoverIDs is what `conductor discover` shows: the verb ids in the
// catalog the verb_list op returns.
func discoverIDs(r *Runner, patterns []string) []string {
	var out []string
	for _, tool := range r.SkillVerbCatalog(patterns) {
		out = append(out, tool["uses"].(string))
	}
	sort.Strings(out)
	return out
}

// cardIDs is what the injected capability card names.
func cardIDs(r *Runner, patterns []string, universe []string) []string {
	card := r.CapabilityCard(patterns)
	var out []string
	for _, id := range universe {
		// "• <id> " / "• <id>\n" — anchored so svc.post does not match
		// svc.post_thing.
		if strings.Contains(card, "• "+id+" ") || strings.Contains(card, "• "+id+"\n") {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// gateDenials are RunSkillVerb's own refusal reasons. Anything else it
// returns means the call got PAST the gate and failed later (an unroutable
// connector, a missing option) — which is admission for our purposes.
var gateDenials = []string{
	"not available on the skill surface",
	"not allowed by this profile's skill.verbs",
	"gated behind human approval",
}

// enforcedIDs is what RunSkillVerb actually admits.
//
// It CALLS the real thing rather than restating its rules. A
// reimplementation here would pass while the gate itself drifted — and
// this test's whole job is to prove the card, discover, and enforcement
// agree, which is worth nothing if "enforcement" is a copy.
func enforcedIDs(t *testing.T, r *Runner, patterns []string, universe []string) []string {
	t.Helper()
	var out []string
	for _, id := range universe {
		_, err := r.RunSkillVerb(context.Background(), SkillIdentity{
			Agent: "probe", Verbs: patterns, Repo: "o/r", Number: 1,
		}, id, map[string]any{})
		denied := false
		if err != nil {
			for _, d := range gateDenials {
				if strings.Contains(err.Error(), d) {
					denied = true
					break
				}
			}
		}
		if !denied {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// verbUniverse is every verb the registry carries, whatever the grant.
func verbUniverse(r *Runner) []string {
	var out []string
	for _, g := range r.GrantedVerbs([]string{"*"}) {
		out = append(out, g.Uses)
	}
	sort.Strings(out)
	return out
}

// TestGrantIsOneSourceOfTruth is the load-bearing test of §A: for every grant
// form, the card, discover, and enforcement name exactly the same verbs.
func TestGrantIsOneSourceOfTruth(t *testing.T) {
	r := capRunner(t)
	universe := verbUniverse(r)
	if len(universe) < 4 {
		t.Fatalf("test fixture too small to be meaningful: %v", universe)
	}

	for _, patterns := range [][]string{
		{"*"},                        // everything
		{"svc.*"},                    // one connector
		{"svc.*", "other.ask"},       // a connector plus a specific verb
		{"svc.post"},                 // one verb
		{"svc.post", "svc.download"}, // several specific verbs
		{"nothing.*"},                // matches nothing
		{},                           // no grant at all
	} {
		name := strings.Join(patterns, ",")
		if name == "" {
			name = "(empty grant)"
		}
		t.Run(name, func(t *testing.T) {
			card := cardIDs(r, patterns, universe)
			disc := discoverIDs(r, patterns)
			enf := enforcedIDs(t, r, patterns, universe)
			if !equalIDs(disc, enf) {
				t.Fatalf("discover != enforcement\n  discover:    %v\n  enforcement: %v", disc, enf)
			}
			if !equalIDs(card, enf) {
				t.Fatalf("card != enforcement\n  card:        %v\n  enforcement: %v", card, enf)
			}
		})
	}
}

func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- §B grant forms --------------------------------------------------------

func TestGrantForms(t *testing.T) {
	r := capRunner(t)
	all := verbUniverse(r)

	tests := []struct {
		name     string
		patterns []string
		want     func(id string) bool
	}{
		{"all verbs, all connectors", []string{"*"}, func(string) bool { return true }},
		{"one connector", []string{"svc.*"}, func(id string) bool { return strings.HasPrefix(id, "svc.") }},
		{"several connectors", []string{"svc.*", "other.*"}, func(id string) bool {
			return strings.HasPrefix(id, "svc.") || strings.HasPrefix(id, "other.")
		}},
		{"a specific verb", []string{"svc.post"}, func(id string) bool { return id == "svc.post" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := discoverIDs(r, tc.patterns)
			var want []string
			for _, id := range all {
				if tc.want(id) {
					want = append(want, id)
				}
			}
			sort.Strings(want)
			if !equalIDs(got, want) {
				t.Fatalf("granted %v, want %v", got, want)
			}
		})
	}
}

// Deny-by-default: no block, no surface, nothing injected.
func TestEmptyGrantAdmitsNothing(t *testing.T) {
	r := capRunner(t)
	if got := r.GrantedVerbs(nil); len(got) != 0 {
		t.Fatalf("an absent grant must admit nothing, got %v", got)
	}
	if got := r.GrantedVerbs([]string{}); len(got) != 0 {
		t.Fatalf("an empty grant must admit nothing, got %v", got)
	}
	if card := r.CapabilityCard(nil); card != "" {
		t.Fatalf("no grant must inject no card, got %q", card)
	}
	if s := r.GrantSummary(nil); s != "no verbs" {
		t.Fatalf("summary = %q", s)
	}
}

// Reads are NOT open by default: a read verb outside the grant is denied
// exactly like a write.
func TestReadsAreNotOpenByDefault(t *testing.T) {
	r := capRunner(t)
	// `svc.download` is a read-shaped verb. A grant naming only svc.post
	// must not admit it, in any of the three views.
	patterns := []string{"svc.post"}
	if matchAny(patterns, "svc.download") {
		t.Fatal("enforcement admitted a read verb outside the grant")
	}
	if ids := discoverIDs(r, patterns); len(ids) != 1 || ids[0] != "svc.post" {
		t.Fatalf("discover leaked a read verb: %v", ids)
	}
	if card := r.CapabilityCard(patterns); strings.Contains(card, "svc.download") {
		t.Fatalf("the card leaked a read verb: %q", card)
	}
}

// A `["*"]` grant still cannot reach conductor's own orchestration — the
// invariant holds at any breadth.
func TestWildcardNeverReachesInternalOrchestration(t *testing.T) {
	r := capRunner(t)
	for _, g := range r.GrantedVerbs([]string{"*"}) {
		if strings.HasPrefix(g.Uses, "workflow.") || strings.HasPrefix(g.Uses, "conductor.") {
			t.Fatalf("a wildcard grant reached %q", g.Uses)
		}
	}
	// …and the enforcement point refuses them before the pattern gate.
	_, err := r.RunSkillVerb(context.Background(),
		SkillIdentity{Agent: "a", Verbs: []string{"*"}}, "conductor.pause", nil)
	if err == nil || !strings.Contains(err.Error(), "not available on the skill surface") {
		t.Fatalf("conductor.* must be refused at any breadth, got %v", err)
	}
	_, err = r.RunSkillVerb(context.Background(),
		SkillIdentity{Agent: "a", Verbs: []string{"*"}}, "workflow.save", nil)
	if err == nil || !strings.Contains(err.Error(), "not available on the skill surface") {
		t.Fatalf("workflow.* must be refused at any breadth, got %v", err)
	}
}

// --- §A the card itself ----------------------------------------------------

func TestCapabilityCardShape(t *testing.T) {
	r := capRunner(t)
	card := r.CapabilityCard([]string{"svc.post", "svc.ask"})

	for _, want := range []string{
		"## Conductor verbs available to you",
		"conductor call <verb> --<option> <value>",
		"• svc.post",
		"--text string (required)", // required options are rendered as flags
		"--channel string",         // optional ones too
		"• svc.ask",
	} {
		if !strings.Contains(card, want) {
			t.Fatalf("card missing %q:\n%s", want, card)
		}
	}
	// A verb's Usage hint wins over its Desc — that is the point of the
	// field: the verb describes itself once.
	if !strings.Contains(card, "ask a human and wait for their answer") {
		t.Fatalf("card should render the Usage hint:\n%s", card)
	}
	if strings.Contains(card, "a fake ask-capable verb") {
		t.Fatalf("Usage must win over Desc:\n%s", card)
	}
	// Required options sort ahead of optional ones within a verb.
	post := card[strings.Index(card, "• svc.post"):]
	if i, j := strings.Index(post, "--text"), strings.Index(post, "--channel"); i < 0 || j < 0 || i > j {
		t.Fatalf("required options should come first:\n%s", post)
	}
}

// The MCP tool description uses the same Usage precedence as the card, so an
// MCP agent and a CLI agent are told the same thing about a verb.
func TestMCPDescriptionMatchesCardDescription(t *testing.T) {
	r := capRunner(t)
	for _, tool := range r.SkillVerbCatalog([]string{"*"}) {
		uses := tool["uses"].(string)
		desc := tool["description"].(string)
		if uses == "svc.ask" || uses == "other.ask" {
			if desc != "ask a human and wait for their answer" {
				t.Fatalf("%s: MCP description = %q, want the Usage hint", uses, desc)
			}
		}
	}
	// And GrantedVerb.Description is the single place that precedence lives.
	for _, g := range r.GrantedVerbs([]string{"svc.ask", "svc.fail"}) {
		switch g.Uses {
		case "svc.ask":
			if g.Description() != "ask a human and wait for their answer" {
				t.Fatalf("Usage should win: %q", g.Description())
			}
		case "svc.fail":
			if g.Description() != "always errors" {
				t.Fatalf("Desc should be the fallback: %q", g.Description())
			}
		}
	}
}

// --- Layer 0 --------------------------------------------------------------

func TestCapabilityPreambleIsTransportAware(t *testing.T) {
	cli := CapabilityPreamble(true)
	mcp := CapabilityPreamble(false)
	for _, want := range []string{"CONDUCTOR VERBS", "conductor discover", "conductor call <connector.verb>"} {
		if !strings.Contains(cli, want) {
			t.Errorf("CLI preamble missing %q: %q", want, cli)
		}
	}
	if strings.Contains(mcp, "conductor call") || strings.Contains(mcp, "conductor discover") {
		t.Errorf("an MCP runtime already has the tools; do not tell it to shell the CLI: %q", mcp)
	}
	if !strings.Contains(mcp, "CONDUCTOR VERBS") {
		t.Errorf("MCP preamble should still carry the act-through-verbs guidance: %q", mcp)
	}
}

func TestGrantSummary(t *testing.T) {
	r := capRunner(t)
	got := r.GrantSummary([]string{"svc.post", "other.ask"})
	if !strings.Contains(got, "2 verb(s)") || !strings.Contains(got, "svc (1)") || !strings.Contains(got, "other (1)") {
		t.Fatalf("summary = %q", got)
	}
}

// A card is only rendered for verbs the REGISTRY actually carries: a grant
// naming a connector this daemon does not have promises nothing.
func TestCardOnlyRendersLiveRegistryVerbs(t *testing.T) {
	r := capRunner(t)
	if card := r.CapabilityCard([]string{"ghost.*"}); card != "" {
		t.Fatalf("a grant matching nothing must render no card, got %q", card)
	}
	card := r.CapabilityCard([]string{"svc.post", "ghost.anything"})
	if strings.Contains(card, "ghost") {
		t.Fatalf("card named a verb no connector provides:\n%s", card)
	}
	if !strings.Contains(card, "• svc.post") {
		t.Fatalf("card lost the verb that does exist:\n%s", card)
	}
}

// The grant a config declares is what reaches the surface — end to end
// through the config type, not just the pattern helper.
func TestSkillPolicyDrivesTheSurface(t *testing.T) {
	r := capRunner(t)
	sk := &config.SkillPolicy{Verbs: []string{"svc.*"}}
	granted := r.GrantedVerbs(sk.Verbs)
	if len(granted) == 0 {
		t.Fatal("a svc.* grant should admit the fake connector's verbs")
	}
	for _, g := range granted {
		if g.Connector != "svc" {
			t.Fatalf("svc.* admitted %q", g.Uses)
		}
	}
}
