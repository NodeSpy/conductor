package memory

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// META-TEST (round-10 #1, the class). "The trusted dispatch" is represented
// three times — core.Trigger, the provenance a live tool is handed
// (memory.Source), and the identity a skill session stands for
// (flow.SkillIdentity) — and each one carries a repo next to the bit saying
// whether the event's SENDER chose it. For three rounds the combining lived
// at the call sites, so each round fixed one face and the next face kept its
// raw repo. The memory IPC face granted implicit own-scope from a
// webhook-forged repo the whole time, and the round-9 audit test could not see
// it: that test skips files with no "TargetTrusted" in them, and this face
// never mentioned it.
//
// So the rule moved INTO the type. Caller.ownRepo is unexported and set only
// by NewAgentCaller, which applies core.OwnRepo — there is no raw repo for a
// face to reach for, and a new face cannot write one without editing this
// package.
//
// This test is the guard on that: no construction of a Caller outside
// NewAgentCaller (except the zero value, which owns nothing).
func TestOnlyNewAgentCallerGrantsOwnScope(t *testing.T) {
	root := repoRoot(t)
	var offenders []string
	err := filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if rel == "internal/memory/scopeguard.go" {
			return nil // where NewAgentCaller lives
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			name := ""
			switch tp := lit.Type.(type) {
			case *ast.Ident:
				name = tp.Name
			case *ast.SelectorExpr:
				name = tp.Sel.Name
			}
			if name != "Caller" || len(lit.Elts) == 0 {
				return true // the ZERO value owns nothing; it is always safe
			}
			for _, e := range lit.Elts {
				kv, ok := e.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if id, ok := kv.Key.(*ast.Ident); ok && id.Name != "AgentFacing" {
					offenders = append(offenders, rel)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range offenders {
		t.Errorf("%s builds a memory.Caller field-by-field. Own-scope must come from "+
			"NewAgentCaller, which applies core.OwnRepo — a repo written in directly is a "+
			"repo whose provenance nobody checked, which is how the IPC face granted "+
			"cross-tenant memory access for three rounds", rel)
	}
}

// …and the rule itself: a forged target owns nothing, a real one owns its
// repo. Asserted on the constructor, which is now the only way in.
func TestNewAgentCallerAppliesTheOwnRepoRule(t *testing.T) {
	if got := NewAgentCaller("acme/app", true).OwnRepo(); got != "acme/app" {
		t.Errorf("a trusted target must own its repo, got %q", got)
	}
	if got := NewAgentCaller("acme/app", false).OwnRepo(); got != "" {
		t.Errorf("a target the sender chose must own nothing, got %q", got)
	}
	if !NewAgentCaller("acme/app", true).AgentFacing {
		t.Error("a caller built this way is agent-facing by construction")
	}
	// The zero Caller is the config-authored one: no own scope, not gated.
	var zero Caller
	if zero.OwnRepo() != "" || zero.AgentFacing {
		t.Errorf("the zero Caller must own nothing and claim nothing: %+v", zero)
	}
}
