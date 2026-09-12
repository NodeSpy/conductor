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
