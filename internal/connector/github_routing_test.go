package connector

import (
	"strings"
	"testing"

	ghint "github.com/NodeSpy/conductor/internal/integrations/github"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// The `repo` / `not_repo` routing keys (docs/design/unified-filter-phase2.md
// §2). They are the one-key replacement for `filters: {repos, exclude_repos}`,
// and they have to reach TWO places that no keep-condition can serve:
//
//   - emit()'s per-variant repo gate, which runs before any event is evaluated;
//   - the sweep and the stuck-checks poller, which iterate a repo LIST and
//     have no event to evaluate a filter against at all.
//
// Both read Action.Repos / Action.ExcludeRepos, so the lowering hoists them
// out of the filter. What it hoists is the union across the whole tree — a
// superset, deliberately, because the filter is still evaluated precisely
// afterwards and a superset only widens what is CONSIDERED.

// lowerFilter runs one trigger's filter through the github lowering and hands
// back the Action the integration would evaluate.
func lowerFilter(t *testing.T, connYAML, filterYAML string) (repos, excl []string, predicate string) {
	t.Helper()
	cfg := mustDecodeConfig(t, `
connectors:
  gh:
    use: github
    token: x
`+connYAML)
	reg, err := Build(cfg, Deps{Secrets: secrets.New()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	in, ok := reg.Get("gh")
	if !ok || in.DisabledReason != "" {
		t.Fatalf("gh not built cleanly: ok=%v reason=%q", ok, in.DisabledReason)
	}
	impl, ok := in.Impl.(*githubImpl)
	if !ok {
		t.Fatalf("impl is %T", in.Impl)
	}
	act, err := impl.lowerTrigger(CompiledTrigger{
		Spec: mkTriggerSpec(t, "gh.review_requested", "v", filterYAML),
	})
	if err != nil {
		t.Fatalf("lowerTrigger: %v", err)
	}
	return act.Repos, act.ExcludeRepos, act.Filter.String()
}

func TestGithubRepoRoutingHoist(t *testing.T) {
	cases := []struct {
		name       string
		filter     string
		wantRepos  string
		wantExcl   string
		wantNilPre bool // the filter states no predicate
	}{
		{
			name:       "a top-level repo scope",
			filter:     "      repo: [org/web, org/api]",
			wantRepos:  "org/web,org/api",
			wantNilPre: true,
		},
		{
			name:       "the scalar shorthand",
			filter:     "      repo: org/web",
			wantRepos:  "org/web",
			wantNilPre: true,
		},
		{
			name:       "not_repo becomes the exclusion",
			filter:     "      repo: [\"org/*\"]\n      not_repo: [org/legacy]",
			wantRepos:  "org/*",
			wantExcl:   "org/legacy",
			wantNilPre: true,
		},
		{
			// The pre-gate must not drop an OR arm's repos, so the hoist takes
			// the UNION — and because a union is only an approximation of an
			// Or, the filter is KEPT and decides precisely per event.
			name:      "an OR of repo sets unions, and stays a predicate",
			filter:    "      - { repo: [org/web] }\n      - { repo: [org/api] }",
			wantRepos: "org/web,org/api",
		},
		{
			// Hoisting a conditional exclusion would suppress events the
			// filter says should fire, so only a root conjunct is hoisted —
			// and the filter is kept to evaluate the rest.
			name:      "a nested not_repo is NOT hoisted",
			filter:    "      - { not_repo: [org/legacy] }\n      - { repo: [org/web] }",
			wantRepos: "org/web",
		},
		{
			name:      "routing plus a predicate keeps the predicate",
			filter:    "      repo: [org/web]\n      not_draft: true",
			wantRepos: "org/web",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repos, excl, pred := lowerFilter(t, "    repos: [\"fallback/*\"]\n", c.filter)
			if got := strings.Join(repos, ","); got != c.wantRepos {
				t.Errorf("Action.Repos = %q, want %q", got, c.wantRepos)
			}
			if got := strings.Join(excl, ","); got != c.wantExcl {
				t.Errorf("Action.ExcludeRepos = %q, want %q", got, c.wantExcl)
			}
			if c.wantNilPre && pred != "<nil>" {
				t.Errorf("a routing-only filter must state no predicate, got %s", pred)
			}
			if !c.wantNilPre && pred == "<nil>" {
				t.Error("a filter the pre-gate cannot carry in full must be kept")
			}
		})
	}
}

// TestGithubRoutingOnlyFilterKeepsEventDefaults is the behavior-preservation
// half. `filters: {repos: […]}` never touched an event's keep-condition, and
// `filter: {repo: […]}` is its replacement — so it must not either. merge_ready
// is the event where this is observable: its five gates are enforced by
// DEFAULT, and a filter that replaced the default would silently unlock the
// merge of an unreviewed PR.
func TestGithubRoutingOnlyFilterKeepsEventDefaults(t *testing.T) {
	_, _, pred := lowerFilter(t, "", "      repo: [org/web]")
	if pred != "<nil>" {
		t.Fatalf("a routing-only filter must leave the event's default in place, got %s", pred)
	}
	// Adding any predicate key is the operator taking the decision over.
	_, _, pred = lowerFilter(t, "", "      repo: [org/web]\n      merge_state: false")
	if pred == "<nil>" {
		t.Fatal("a filter that names a predicate key must replace the default")
	}
	// An expr counts as a predicate too, even with no match key at all.
	_, _, pred = lowerFilter(t, "", "      repo: [org/web]\n      expr: \"!is_draft\"")
	if pred == "<nil>" {
		t.Fatal("an expr conjunct must count as a predicate")
	}
}

// TestGithubNoRepoFilterFallsBackToTheConnector: a trigger that names no repo
// inherits the connector's `repos:`, exactly as a trigger with no
// `filters.repos` did.
func TestGithubNoRepoFilterFallsBackToTheConnector(t *testing.T) {
	for _, filter := range []string{"", "      not_draft: true", "      not_repo: [org/legacy]"} {
		repos, _, _ := lowerFilter(t, "    repos: [\"fallback/*\"]\n", filter)
		if got := strings.Join(repos, ","); got != "fallback/*" {
			t.Errorf("filter %q: Action.Repos = %q, want the connector's fallback", filter, got)
		}
	}
}

// TestGithubRoutingReachesTheSweepScope: the hoisted repos are what the sweep
// and the stuck poller iterate. The connectors lowering makes every rule a
// "*/*" catch-all and carries the real scope on the action, so reading the
// rule's match.repos instead would send the poller into an installation lookup
// for a wildcard owner.
func TestGithubRoutingReachesTheSweepScope(t *testing.T) {
	cfg := mustDecodeConfig(t, `
connectors:
  gh:
    use: github
    token: x
    sweep: { enabled: true, interval: 10m }
`)
	reg, err := Build(cfg, Deps{Secrets: secrets.New()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	in, _ := reg.Get("gh")
	result, err := in.Impl.Source([]CompiledTrigger{{
		Spec: mkTriggerSpec(t, "gh.stuck_checks", "stuck",
			"      - { repo: [org/web] }\n      - { repo: [org/api] }"),
	}})
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	gi, ok := result.(*ghint.Integration)
	if !ok {
		t.Fatalf("Source returned %T", result)
	}
	refs := gi.Actions()
	if len(refs) != 1 {
		t.Fatalf("Actions() = %d, want 1", len(refs))
	}
	// stuck_checks publishes no predicate facts and evaluates no
	// keep-condition, so the hoisted scope is the ONLY thing routing it — and
	// an OR of repo sets has to survive the hoist as their union.
	if got := strings.Join(refs[0].Action.Repos, ","); got != "org/web,org/api" {
		t.Errorf("the poller's repo scope = %q, want org/web,org/api", got)
	}
}
