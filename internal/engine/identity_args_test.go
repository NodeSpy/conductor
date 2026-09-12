package engine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// `agent:` on a step is a REFERENCE — resolving it yields the step's real
// identity, which is the key outcomes, memory and sessions are all filed
// under. The legacy `steps:` path resolved it (actionProfile) and then handed
// the RAW label to the review hand-off and the memory harvest, so a hand-off
// decision was recorded under a key nothing else uses: the step's track record
// silently never accumulated, and every reader looked for it elsewhere.
//
// Asserting the argument at the call site is what catches this precisely —
// the two values are both plain strings, so nothing else would.
func TestTheLegacyStepsPathPassesTheResolvedIdentity(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "steps.go", nil, 0)
	if err != nil {
		t.Fatalf("parse steps.go: %v", err)
	}

	// callee → the 0-based position of its identity parameter.
	identityArg := map[string]int{
		"startReviewHandoff": 3, // (ctx, t, stepID, identity, …)
		"harvestMemory":      1, // (t, identity, runID, …)
	}
	seen := map[string]int{}

	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pos, tracked := identityArg[sel.Sel.Name]
		if !tracked || pos >= len(call.Args) {
			return true
		}
		seen[sel.Sel.Name]++
		line := strconv.Itoa(fset.Position(call.Pos()).Line)

		switch arg := call.Args[pos].(type) {
		case *ast.Ident:
			if arg.Name != "identity" {
				t.Errorf("steps.go:%s: %s receives %q as its identity argument, not the "+
					"resolved `identity` — outcomes and memory would be filed under a key "+
					"nothing else reads", line, sel.Sel.Name, arg.Name)
			}
		case *ast.SelectorExpr:
			// s.Agent — the raw label. This is the bug.
			name := "?"
			if x, ok := arg.X.(*ast.Ident); ok {
				name = x.Name + "." + arg.Sel.Name
			}
			t.Errorf("steps.go:%s: %s receives the RAW label %s as its identity argument. "+
				"`agent:` is a step reference; actionProfile resolves it to the identity that "+
				"outcomes, memory and sessions are keyed by — pass that", line, sel.Sel.Name, name)
		default:
			t.Errorf("steps.go:%s: %s receives an unrecognized identity argument", line, sel.Sel.Name)
		}
		return true
	})

	for callee := range identityArg {
		if seen[callee] == 0 {
			t.Errorf("no call to %s found in steps.go — if it moved, move this check with it "+
				"rather than letting the guard quietly cover nothing", callee)
		}
	}
}

// The premise: a step reference resolves to an identity that is NOT the raw
// label, which is what makes passing the wrong one a real mis-key rather than
// a cosmetic difference.
func TestAStepReferenceResolvesToADifferentIdentity(t *testing.T) {
	cfg := baseCfg()
	cfg.Workflows["w"] = config.WorkflowDef{Steps: []config.Step{{ID: "fixer", Name: "the-fixer"}}}
	e, _ := newEng(t, cfg, &fakeDispatcher{}, &fakeNotifier{}, nil)

	profile, identity := e.actionProfile("w/fixer")
	_ = profile
	if identity == "w/fixer" {
		t.Fatal("test premise broken: the reference did not resolve to a distinct identity, " +
			"so passing the raw label would be harmless and the guard above pointless")
	}
	if identity != "the-fixer" {
		t.Fatalf("expected the step's name as its identity, got %q", identity)
	}
}
