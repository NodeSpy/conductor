package testhome

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) { Main(m) }

// Under Main, every home-derived dir resolves into the throwaway home.
func TestMainIsolatesHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(filepath.Dir(home)), "conductor-testhome-") {
		t.Fatalf("HOME should be a throwaway dir, got %s", home)
	}
	for _, k := range ambient {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("%s should be unset under testhome, got %q", k, v)
		}
	}
	for name, f := range map[string]func() (string, error){
		"config": os.UserConfigDir, "cache": os.UserCacheDir,
	} {
		if d, err := f(); err != nil || !strings.HasPrefix(d, home) {
			t.Errorf("user %s dir should fall under the throwaway home: %q %v", name, d, err)
		}
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); !strings.HasPrefix(d, filepath.Dir(home)) {
		t.Errorf("XDG_RUNTIME_DIR should move with HOME, got %q", d)
	}
}

// TestEveryTestPackageIsolatesHome is the guard that keeps the isolation from
// eroding: a test package without a TestMain that calls testhome.Main runs
// against the developer's real home, which is how dispatch tests came to leave
// run dirs in the live ~/.conductor/runs. Add
//
//	func TestMain(m *testing.M) { testhome.Main(m) }
//
// in a testmain_test.go.
//
// pkg/ is exempt: it is public SDK and may not import internal/ even from its
// tests (pkg/boundary_test.go), and it owns no home-dir state. The sentinel
// HOME scripts/test-home-guard.sh runs the suite under still covers it.
func TestEveryTestPackageIsolatesHome(t *testing.T) {
	root := moduleRoot(t)
	dirs := map[string]bool{} // dir with tests → has an isolating TestMain
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" || d.Name() == "vendor") {
				return filepath.SkipDir
			}
			if path == filepath.Join(root, "pkg") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		dir := filepath.Dir(path)
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		dirs[dir] = dirs[dir] || callsMain(f)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) < 2 {
		t.Fatalf("found only %d test packages under %s — is the walk broken?", len(dirs), root)
	}
	for dir, ok := range dirs {
		if !ok {
			rel, _ := filepath.Rel(root, dir)
			t.Errorf("%s: test package has no `func TestMain(m *testing.M) { testhome.Main(m) }` — its tests run against the real $HOME", rel)
		}
	}
}

// callsMain reports whether f declares a TestMain that calls testhome.Main
// (or plain Main, inside this package).
func callsMain(f *ast.File) bool {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name != "TestMain" || fn.Body == nil {
			continue
		}
		found := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				if x, ok := fun.X.(*ast.Ident); ok && x.Name == "testhome" && fun.Sel.Name == "Main" {
					found = true
				}
			case *ast.Ident:
				if fun.Name == "Main" && f.Name.Name == "testhome" {
					found = true
				}
			}
			return !found
		})
		return found
	}
	return false
}

func moduleRoot(t *testing.T) string {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working dir")
		}
		dir = parent
	}
}
