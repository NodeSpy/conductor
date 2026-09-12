package connector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ROUND-8 #3. A source plugin's events arrive on the wire carrying their own
// `target`, which the plugin built from whatever payload it was handed — the
// same provenance as a webhook body, and the plugin is a third party's code,
// not conductor's. The adapter emitted that Target with no provenance mark,
// so the scope layer extended own-repo trust to a repo an attacker (or a
// careless plugin) named.
//
// The bit is inverted now (core.Trigger.TargetTrusted, zero = untrusted), so
// this face is safe by DEFAULT — which is the point of the inversion, and is
// exactly what a test should pin: the adapter must never start claiming trust
// for a target it did not assign.
func TestPluginSourceNeverClaimsTargetTrust(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("pluginsource.go"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if strings.Contains(src, "TargetTrusted: true") || strings.Contains(src, "TargetTrusted = true") {
		t.Error("the plugin source adapter claims target trust. Its Target arrives on the wire " +
			"from third-party code that built it from event payload — the same provenance as a " +
			"webhook body. Leave TargetTrusted false; an operator scoping plugin-sourced " +
			"dispatches lists the repos.")
	}
	// …and the reason is written down where the next reader will be.
	if !strings.Contains(src, "TargetTrusted") {
		t.Error("pluginsource.go no longer explains why it does not claim target trust — the " +
			"silence is the same silence that shipped the bug")
	}
}
