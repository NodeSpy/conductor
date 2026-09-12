package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// META-TEST (round-8, the #2/#3 class). Three findings in a row were the same
// shape: a core.Trigger whose Target came from data the event's SENDER chose
// — a webhook `repo:` templated from the POST body, a third-party plugin
// source's wire event, a run_step rebuilding its trigger — and which nobody
// remembered to mark. The scope layer then extended "your own target needs no
// grant" to a value an attacker typed.
//
// The field is inverted for exactly that reason: TargetTrusted's zero value
// is UNTRUSTED, so a source that says nothing gets the safe answer and
// claiming trust is a deliberate line of code. This test makes the claim
// auditable — it finds every place that claims it and requires the claim to
// be one of the reviewed ones, so a NEW `TargetTrusted: true` cannot be added
// without someone updating this list and answering its question:
//
//	who chose this Target — the platform, or whoever sent the event?
func TestOnlyAuditedSourcesClaimTargetTrust(t *testing.T) {
	// Each entry is a file that constructs a trusted Target, with WHY. The
	// list is the audit.
	audited := map[string]string{
		"internal/integrations/github/events.go": "a signature-verified GitHub payload; the repo and number are GitHub's",
		"internal/integrations/slack/handle.go":  "a synthetic target built from the channel id Slack assigned",
		"internal/integrations/rss/rss.go":       "the feed's CONFIGURED repo, or a synthetic target named after the feed",
		"internal/integrations/cron/cron.go":     "a synthetic target named after the operator's own schedule",
		"internal/integrations/webhook/webhook.go": "a STATIC `repo:` (the operator's word) or a synthetic target; " +
			"a body-templated repo: is left untrusted",
		"internal/connector/conductor.go": "conductor's own lifecycle event",
		"internal/connector/httpapi.go":   "a synthetic target named after the source and event, from config",
		"cmd/conductor/main.go":           "an operator-invoked manual trigger",
		"cmd/conductor/mcp.go": "the memory MCP subprocess parses --target-trusted, which the daemon " +
			"emits from the dispatch it launched; absent means untrusted",
		// Carriers, not claimants: they propagate a bit decided upstream.
		"internal/flow/plan.go":       "run_step carries the launching dispatch's provenance",
		"internal/flow/skillverbs.go": "the skill surface carries the dispatch's provenance",
		"internal/flow/flow.go":       "the runner carries the trigger's provenance into memory.Source",
		"internal/engine/engine.go":   "the engine carries the trigger's provenance into memory.Source",
		"internal/dispatch/toolserver.go": "the tool server carries the dispatch's provenance into the skill " +
			"identity",
		"internal/memory/memory.go":     "memory.Source declares the field it carries alongside the Repo it describes",
		"internal/memory/ipc.go":        "the MCP/CLI memory face applies the rule through NewAgentCaller",
		"internal/memory/scopeguard.go": "memory.Caller applies the rule at construction so no face holds a raw repo",
		"internal/memory/harvest.go":    "the output-contract face applies the rule through NewAgentCaller",
		"internal/skill/broker.go":      "skill.Identity declares the field it carries for a minted session",
	}
	root := repoRootFor(t)
	claims := map[string]bool{}
	err := filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "test/") || rel == "internal/core/event.go" {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		src := string(b)
		if !strings.Contains(src, "TargetTrusted") {
			return nil
		}
		// A claim is `TargetTrusted: true` or `TargetTrusted = true`; anything
		// else is reading or carrying the bit.
		if strings.Contains(src, "TargetTrusted: true") || strings.Contains(src, "TargetTrusted = true") {
			claims[rel] = true
			return nil
		}
		if strings.Contains(src, "TargetTrusted:") || strings.Contains(src, "TargetTrusted)") {
			claims[rel] = true // a carrier: still on the audit list
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) == 0 {
		t.Fatal("found no TargetTrusted sites at all — this test has stopped looking where the code is")
	}
	for rel := range claims {
		if _, ok := audited[rel]; !ok {
			t.Errorf("%s decides target trust but is not on the audit list. Add it with the "+
				"reason, and answer the question the reason has to answer: who chose this "+
				"Target — the platform, or whoever sent the event? If the sender can influence "+
				"it, leave TargetTrusted false.", rel)
		}
	}
}

// The zero value must stay the SAFE one. A field flipped back to
// `TargetUntrusted` — or any spelling whose zero value means trusted — brings
// back the class: a new source that says nothing would be believed.
func TestTargetTrustZeroValueIsUntrusted(t *testing.T) {
	var zero Trigger
	if zero.TargetTrusted {
		t.Fatal("the zero Trigger claims a trusted target")
	}
	// And the field is spelled positively, so "not set" reads as "not trusted"
	// at every call site rather than as a double negative.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(repoRootFor(t), "internal/core/event.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		fld, ok := n.(*ast.Field)
		if !ok {
			return true
		}
		for _, name := range fld.Names {
			switch name.Name {
			case "TargetTrusted":
				found = true
			case "TargetUntrusted":
				t.Error("core.Trigger carries TargetUntrusted again — its zero value means " +
					"TRUSTED, which is how three sources shipped believing a sender-chosen target")
			}
		}
		return true
	})
	if !found {
		t.Error("core.Trigger has no TargetTrusted field")
	}
}

// repoRootFor walks up to the module root.
func repoRootFor(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("module root not found")
	return ""
}
