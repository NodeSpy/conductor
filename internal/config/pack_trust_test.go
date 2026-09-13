package config

import "testing"

// A pack_trust/plugin_trust allow entry for one exact repo must not admit a
// different, attacker-registered repo whose name merely CONTINUES the trusted
// one (typosquat / name-continuation supply-chain bypass). Subdir/ref forms of
// the SAME repo must still match.
func TestExactRepoAllowRejectsNameContinuation(t *testing.T) {
	cfg := PackTrustConfig{Allow: []string{"github.com/acme/review-pack"}}
	deny := []string{
		"github.com/acme/review-pack-evil-fork",
		"github.com/acme/review-pack2",
		"github.com/acme/review-packaging",
	}
	for _, s := range deny {
		if cfg.SourceAllowed(s) {
			t.Errorf("BYPASS: %q matched an exact-repo allow entry via bare prefix", s)
		}
	}
	allow := []string{
		"github.com/acme/review-pack",
		"github.com/acme/review-pack//sub",
		"github.com/acme/review-pack@v1.2.0",
		"github.com/acme/review-pack//sub@v1.2.0",
	}
	for _, s := range allow {
		if !cfg.SourceAllowed(s) {
			t.Errorf("the trusted repo (or its subdir/ref) %q must be allowed", s)
		}
	}
}

// BOTH BRANCHES of globMatch, in one table — because the bug this covers was
// fixed in the no-wildcard branch and left standing in the wildcard one, and
// the two are a hundred lines apart.
//
// `pack_trust`/`plugin_trust` decides whether the daemon FETCHES AND EXECUTES
// third-party code. A pattern that admits a name the operator did not write
// is a supply-chain bypass, and the shape is always the same: an attacker
// registers a name that CONTINUES a trusted one.
//
//	no-wildcard branch   github.com/acme/review-pack  vs  …-evil-fork
//	wildcard branch      github.com/trusted-org*      vs  …-evil/malicious-pack
//
// Every case below names which branch it exercises. A future edit to either
// one that reintroduces the continuation bypass fails here.
func TestTrustGlobRejectsNameContinuationOnBothBranches(t *testing.T) {
	for _, tc := range []struct {
		branch  string
		pattern string
		source  string
		allow   bool
		why     string
	}{
		// ---- no-wildcard branch (len(parts) == 1)
		{"no-wildcard", "github.com/acme/review-pack", "github.com/acme/review-pack", true,
			"the trusted repo itself"},
		{"no-wildcard", "github.com/acme/review-pack", "github.com/acme/review-pack//sub", true,
			"a subdir OF the trusted repo"},
		{"no-wildcard", "github.com/acme/review-pack", "github.com/acme/review-pack@v1.2.0", true,
			"a ref OF the trusted repo"},
		{"no-wildcard", "github.com/acme/review-pack", "github.com/acme/review-pack//sub@v1.2.0", true,
			"both"},
		{"no-wildcard", "github.com/acme/review-pack", "github.com/acme/review-pack-evil-fork", false,
			"a DIFFERENT repo whose name continues the trusted one"},
		{"no-wildcard", "github.com/acme/review-pack", "github.com/acme/review-pack2", false,
			"as above"},

		// ---- wildcard branch (len(parts) > 1): the sibling that was missed
		{"wildcard", "github.com/trusted-org*", "github.com/trusted-org-evil/malicious-pack", false,
			"THE BUG: `*` spanned the `/`, so an org-name wildcard admitted a repo under a " +
				"DIFFERENT, attacker-registered org"},
		{"wildcard", "github.com/acme/conductor-packs*", "github.com/acme/conductor-packs-evil/x", false,
			"same shape one segment deeper"},
		{"wildcard", "github.com/trusted-org*", "github.com/trusted-org/real-pack", false,
			"a mid-segment `*` names an ORG, and an org is not a pack source; the repo needs its " +
				"own entry or an org-scoped pattern"},
		{"wildcard", "github.com/acme/*", "github.com/acme/anyrepo", true,
			"the org-scoped form: any repo under acme"},
		{"wildcard", "github.com/acme/*", "github.com/acme/anyrepo//sub@v2", true,
			"…and its subdir/ref"},
		{"wildcard", "github.com/acme/*", "github.com/acme/group/nested", true,
			"a deeper host layout (a gitlab subgroup) is still under acme"},
		{"wildcard", "github.com/acme/*", "github.com/acme-evil/anyrepo", false,
			"the literal prefix ends at the `/`, so a continued ORG name is a different org"},
		{"wildcard", "github.com/acme/*", "github.com/acme", false,
			"the org itself is not a pack source"},
		{"wildcard", "github.com/acme/conductor-packs*", "github.com/acme/conductor-packs2", true,
			"DOCUMENTED RESIDUAL: a mid-segment `*` matches same-segment continuations. " +
				"Registering that name needs write access under acme already; the docs " +
				"recommend the exact or org-scoped forms instead"},
		{"wildcard", "*", "github.com/anyone/anything", true,
			"the operator asked for everything"},
	} {
		t.Run(tc.branch+" "+tc.pattern+" vs "+tc.source, func(t *testing.T) {
			packCfg := PackTrustConfig{Allow: []string{tc.pattern}}
			if got := packCfg.SourceAllowed(tc.source); got != tc.allow {
				t.Errorf("SourceAllowed(%q) with allow %q = %v, want %v — %s",
					tc.source, tc.pattern, got, tc.allow, tc.why)
			}
			// PluginSourceAllowed shares the matcher; a bypass in one is a
			// bypass in the other, so both are asserted.
			if got := packCfg.PluginSourceAllowed(tc.source); got != tc.allow {
				t.Errorf("PluginSourceAllowed(%q) with allow %q = %v, want %v — %s",
					tc.source, tc.pattern, got, tc.allow, tc.why)
			}
			if got := globMatch(tc.pattern, tc.source); got != tc.allow {
				t.Errorf("globMatch(%q, %q) = %v, want %v — %s",
					tc.pattern, tc.source, got, tc.allow, tc.why)
			}
		})
	}
}

// The official sources stay trusted with no entry at all, and an empty trust
// block still denies a third party — the fix must not have made the matcher
// so strict that the defaults stopped working.
func TestTrustDefaultsSurviveTheStricterGlob(t *testing.T) {
	// No pack_trust block at all = no restriction (the documented default:
	// the allowlist is opt-in). The stricter glob must not change that.
	var none *PackTrustConfig
	for _, s := range []string{OfficialSource, OfficialPacksSource, "github.com/someone/else"} {
		if !none.SourceAllowed(s) {
			t.Errorf("with no pack_trust block, %q must still resolve", s)
		}
	}
	// An allowlist the operator DID write denies a third party…
	set := PackTrustConfig{Allow: []string{"github.com/acme/*"}}
	if set.SourceAllowed("github.com/someone/else") {
		t.Error("a source outside the operator's allowlist must be denied")
	}
	// …while the official repos stay trusted without an entry. Each surface
	// default-trusts its own: packs the official PACKS repo, plugins that one
	// and the official PLUGIN repo both.
	for _, s := range []string{OfficialPacksSource, OfficialPacksSource + "/sub"} {
		if !set.SourceAllowed(s) {
			t.Errorf("the official packs repo must need no entry: %q", s)
		}
	}
	for _, s := range []string{OfficialSource, OfficialSource + "/sub", OfficialPacksSource} {
		if !set.PluginSourceAllowed(s) {
			t.Errorf("an official plugin source must need no entry: %q", s)
		}
	}
	// Plugins are stricter by default: no block means no third-party plugin.
	if none.PluginSourceAllowed("github.com/someone/else") {
		t.Error("a third-party plugin source must not be allowed without an allowlist")
	}
}

// HOST-OMITTED TRUST GLOBS (docs/design/config-surface-refinements.md §4).
// `pack_trust`/`plugin_trust` entries take the same host default the
// `use:`/source resolver applies, through the SAME canonicalizer — so
// `your-org/*` means what it obviously means, and the two cannot drift.
//
// This is normalization only. The segment-anchored `*` is preserved, so a
// host-omitted pattern rejects exactly the typosquats its written-out form
// does — the cases marked BYPASS below are the round-14 class, re-asserted
// through the shorter spelling.
func TestTrustGlobsMayOmitTheHost(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		source  string
		allow   bool
		why     string
	}{
		{"acme/review-kit", "github.com/acme/review-kit", true,
			"a host-omitted exact repo defaults to github.com"},
		{"acme/review-kit", "acme/review-kit", true,
			"…and both sides normalize, so an unqualified source matches too"},
		{"acme/review-kit", "github.com/acme/review-kit//sub@v1", true,
			"subdir/ref continuation still resolves"},
		{"your-org/*", "github.com/your-org/anything", true,
			"the org-scoped form, host omitted"},
		{"your-org/*", "github.com/your-org-evil/anything", false,
			"BYPASS: the literal prefix ends at the `/`, so a continued org is a different org"},
		{"acme/review-kit", "github.com/acme/review-kit-evil-fork", false,
			"BYPASS: name continuation, through the short spelling"},
		{"trusted-org*", "github.com/trusted-org-evil/malicious-pack", false,
			"BYPASS: `*` still does not cross a `/` after normalization"},
		{"gitlab.com/team/*", "gitlab.com/team/repo", true,
			"a written host is used as-is"},
		{"gitlab.com/team/*", "github.com/team/repo", false,
			"…and is not confused with the default"},
		{"acme/review-kit", "gitlab.com/acme/review-kit", false,
			"a host-omitted pattern means github.com, not any host"},
		{"*", "gitlab.com/anyone/anything", true,
			"a bare `*` stays host-AGNOSTIC — defaulting it would silently narrow " +
				"a pattern the operator wrote to mean everything"},
	} {
		t.Run(tc.pattern+" vs "+tc.source, func(t *testing.T) {
			cfg := PackTrustConfig{Allow: []string{tc.pattern}}
			if got := cfg.SourceAllowed(tc.source); got != tc.allow {
				t.Errorf("SourceAllowed(%q) with allow %q = %v, want %v — %s",
					tc.source, tc.pattern, got, tc.allow, tc.why)
			}
			if got := cfg.PluginSourceAllowed(tc.source); got != tc.allow {
				t.Errorf("PluginSourceAllowed(%q) with allow %q = %v, want %v — %s",
					tc.source, tc.pattern, got, tc.allow, tc.why)
			}
		})
	}
}

// The trust allowlist and the `use:` resolver must agree about which host a
// host-omitted reference means, or an operator writes a pattern that reads as
// covering the source they are installing and silently does not. One
// canonicalizer, asserted against the resolver's own output.
func TestTrustCanonicalizerAgreesWithTheUseResolver(t *testing.T) {
	for _, ref := range []string{"acme/review-kit", "your-org/packs", "gitlab.com/team/repo"} {
		u, err := ParseUse(UseKindPack, ref)
		if err != nil {
			t.Fatalf("ParseUse(%q): %v", ref, err)
		}
		src := u.Source()
		if got := CanonicalRemoteRef(ref); got != src {
			t.Errorf("the trust canonicalizer and the use: resolver disagree about %q: "+
				"trust says %q, use: resolves to %q — a trust pattern that does not mean "+
				"what the source means is a check that silently misses", ref, got, src)
		}
	}
}

// A source on a forge OTHER than github used to skip the allowlist entirely:
// the remote test was a fixed prefix list, so anything it did not recognize
// fell through to "local" and was exempt. Being unlisted made a source MORE
// permitted, not less.
func TestANonGithubForgeIsGovernedByTheAllowlist(t *testing.T) {
	cfg := PackTrustConfig{Allow: []string{"github.com/acme/*"}}
	if cfg.SourceAllowed("gitlab.com/evil/pack") {
		t.Error("a gitlab source must be governed by pack_trust, not exempt from it")
	}
	if cfg.SourceAllowed("git.corp.example/team/pack") {
		t.Error("a self-hosted forge must be governed too")
	}
	// …while the operator's own disk stays exempt, which is the point of the
	// remote test in the first place.
	for _, local := range []string{"./src/kit", "../shared/kit", "/opt/packs/kit", "~/packs/kit"} {
		if !cfg.SourceAllowed(local) {
			t.Errorf("a local path must stay exempt: %q", local)
		}
	}
	// An explicitly-listed other-forge source resolves.
	ok := PackTrustConfig{Allow: []string{"gitlab.com/team/*"}}
	if !ok.SourceAllowed("gitlab.com/team/pack") {
		t.Error("a listed gitlab source must resolve")
	}
}
