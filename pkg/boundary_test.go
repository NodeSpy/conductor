// Package pkg_test enforces the one rule every package under pkg/ shares: it is
// PUBLIC surface a third party builds on, so it may not import anything under
// internal/. A plugin module cannot import conductor's internal packages (Go
// refuses it), so a pkg/ package that leans on one is a package an external
// plugin cannot use — and "the builtin and the plugin run the same code" stops
// being true the moment it happens.
//
// Checking direct imports is enough: if no pkg/ file imports internal/, the
// transitive closure of any pkg/ package stays out of internal/ too (pkg/ only
// reaches the rest of the module through pkg/).
package pkg_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const internalPrefix = "github.com/NodeSpy/conductor/internal"

func TestPkgImportsNoInternal(t *testing.T) {
	fset := token.NewFileSet()
	checked := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		checked++
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p == internalPrefix || strings.HasPrefix(p, internalPrefix+"/") {
				t.Errorf("%s imports %s — pkg/ is public SDK surface and must not depend on internal/", path, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("walked no .go files under pkg/ — the boundary check is not checking anything")
	}
}
