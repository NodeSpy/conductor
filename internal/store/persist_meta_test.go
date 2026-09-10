package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// META-TEST D. Every save in this package used to marshal under s.mu, RELEASE
// it, then write+rename — so two concurrent saves of different keys could
// interleave and land a stale snapshot over a newer one. Round 2 routed the
// affinity/plans/runs/sessions savers through persist() and missed the three
// in outcomes.go entirely.
//
// So the rule is enforced structurally rather than per-file: a write or rename
// in this package must live inside persist(), or be one of the exceptions
// named below with the reason it is safe.
//
// It catches: a new save* function that hand-rolls WriteFile+Rename — the
// exact regression that produced this finding twice.
var persistExceptions = map[string]string{
	// persist() IS the chokepoint; the write it performs is the point.
	"persist": "the chokepoint itself",
	// A history record's path is derived from its OWN run id
	// (historyFile(dir, rec.ID)), so there is no concurrent save of the same
	// file to lose an update to — the condition persist() exists to prevent
	// cannot arise.
	"WriteHistory": "per-run-id file: one writer per path, ever",
	// The HMAC key is written once on first use and read thereafter; the
	// rename is the create, not a snapshot update.
	"historyKey": "create-once key file, not a mutable snapshot",
	// The audit log rotates by renaming itself aside; it is an append-only
	// log, not a marshalled snapshot, so persist's marshal→write→rename
	// shape does not apply.
	"rotate": "append-only audit log rotation, not a snapshot write",
}

func TestEveryStoreWriteGoesThroughPersist(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var offenders []string
	for _, pkg := range pkgs {
		for path, f := range pkg.Files {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				if _, exempt := persistExceptions[fn.Name.Name]; exempt {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					pkgIdent, ok := sel.X.(*ast.Ident)
					if !ok || pkgIdent.Name != "os" {
						return true
					}
					if sel.Sel.Name != "WriteFile" && sel.Sel.Name != "Rename" {
						return true
					}
					offenders = append(offenders, fn.Name.Name+" ("+
						shortPath(path)+":"+posLine(fset, call.Pos())+") os."+sel.Sel.Name)
					return true
				})
			}
		}
	}

	if len(offenders) > 0 {
		t.Fatalf("state-file write(s) outside persist():\n  %s\n\n"+
			"persist() holds writeMu across marshal→write→rename, so a concurrent save of a "+
			"DIFFERENT key cannot interleave and land a stale snapshot over a newer one — now "+
			"reachable through parallel: branches. Route the save through s.persist(...), or add "+
			"the function to persistExceptions with the reason its path has only one writer.",
			strings.Join(offenders, "\n  "))
	}
}

// The exception list has to stay honest: an entry naming a function that no
// longer exists hides a real offender behind a stale name.
func TestPersistExceptionsAreAllReal(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	live := map[string]bool{}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, decl := range f.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok {
					live[fn.Name.Name] = true
				}
			}
		}
	}
	for name, why := range persistExceptions {
		if !live[name] {
			t.Errorf("persistExceptions names %q (%s) but no such function exists — "+
				"a stale exemption can mask a real offender that happens to share the name", name, why)
		}
	}
}

func shortPath(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func posLine(fset *token.FileSet, p token.Pos) string {
	return strconv.Itoa(fset.Position(p).Line)
}
