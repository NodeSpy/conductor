package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// META-TEST E. A list-form `on:` decodes into OnSources and leaves On EMPTY
// until NormalizeTriggers expands it. Load runs instantiatePacks BEFORE
// NormalizeTriggers, so every pack consumer sees On=="" for a list-form
// trigger — and reading On there doesn't fail loudly, it silently concludes
// the trigger has no source. That is how an armed list-form github trigger
// slipped past the repo-consent check.
//
// preNormalizeFiles is the window: the files that run before normalization.
// Anything in it that asks "what does this trigger fire on" must use
// Sources(), which answers correctly for both forms.
var preNormalizeFiles = []string{
	"pack_instantiate.go",
	"pack_sources.go",
	"pack_overlay.go",
	"pack_refs.go",
}

// Raw `.On` reads that are provably correct, each with the reason it is
// exempt. Anything not listed fails the test — the point is that adding a
// new raw read requires justifying it here.
var allowedRawOnReads = map[string]string{
	// Assignments write the normalized scalar; they are not form-blind reads.
	"pack_sources.go:trs[i].On = bound":       "assignment: the scalar path writes its rebound source",
	"pack_refs.go:t.On = rw.rebindSource":     "assignment, and the OnSources loop directly below rebinds the list form",
	"pack_sources.go:trs[i].On == \"\"":       "the form DISCRIMINATOR itself — it selects the list-form branch",
	"pack_instantiate.go:tr.On == \"\"":       "form discriminator",
	"pack_overlay.go:t.On == \"\"":            "form discriminator",
	"pack_refs.go:t.On == \"\"":               "form discriminator",
	"pack_refs.go:t.On != \"\"":               "form discriminator",
	"pack_sources.go:trs[i].On != \"\"":       "form discriminator",
	"pack_instantiate.go:tr.On != \"\"":       "form discriminator",
	"pack_overlay.go:t.On != \"\"":            "form discriminator",
	"pack_instantiate.go:tr.On = ":            "assignment",
	"pack_overlay.go:t.On = ":                 "assignment",
	"pack_instantiate.go:.On = ":              "assignment",
	"pack_sources.go:.On = ":                  "assignment",
	"pack_instantiate.go:sourceIsRepoScoped(": "takes one already-extracted source string, not a TriggerSpec",
	// Reached only via the scalar branch: the list-form branch immediately
	// above it handles OnSources per source and `continue`s, so control
	// arrives here only when On is non-empty.
	"pack_sources.go:st.bindOneSource(ns, &trs[i], i, trs[i].Connector()": "scalar branch only — the list-form branch above continues",
}

// TestPreNormalizeReadersHandleBothTriggerForms fails if a file in the
// pre-normalize window reads a trigger's `.On` (or calls Connector()/Event(),
// which read it) outside the allowlist above.
//
// It catches: the next reader added to pack instantiation that asks On what it
// should ask Sources() — the exact class that produced the consent bypass.
func TestPreNormalizeReadersHandleBothTriggerForms(t *testing.T) {
	fset := token.NewFileSet()
	var offenders []string

	for _, name := range preNormalizeFiles {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		lines := strings.Split(string(src), "\n")

		ast.Inspect(f, func(n ast.Node) bool {
			var label string
			switch e := n.(type) {
			case *ast.SelectorExpr:
				if e.Sel.Name != "On" {
					return true
				}
				label = "On"
			case *ast.CallExpr:
				sel, ok := e.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "Connector" && sel.Sel.Name != "Event") {
					return true
				}
				label = sel.Sel.Name + "()"
			default:
				return true
			}

			pos := fset.Position(n.Pos())
			if pos.Line-1 >= len(lines) {
				return true
			}
			line := strings.TrimSpace(lines[pos.Line-1])
			// OnSources / OnError etc. are different fields.
			if label == "On" && !isBareOnRead(line) {
				return true
			}
			for frag, why := range allowedRawOnReads {
				file, want, _ := strings.Cut(frag, ":")
				if file == name && strings.Contains(line, want) {
					_ = why
					return true
				}
			}
			offenders = append(offenders, filepath.Base(name)+":"+
				strconv.Itoa(pos.Line)+": "+line)
			return true
		})
	}

	if len(offenders) > 0 {
		t.Fatalf("pre-normalize reader(s) consult a trigger's scalar `On` (or Connector()/Event(), "+
			"which read it) without handling the list form. A list-form `on:` leaves On empty until "+
			"NormalizeTriggers, which runs AFTER pack instantiation — so these silently see no source "+
			"at all. Use TriggerSpec.Sources(), or add an entry to allowedRawOnReads saying why the "+
			"raw read is correct:\n  %s", strings.Join(offenders, "\n  "))
	}
}

// isBareOnRead filters out OnSources/OnError-style selectors sharing the prefix.
func isBareOnRead(line string) bool {
	for _, bad := range []string{".OnSources", ".OnError", ".OnFail", "inst.On", "Notify.On", "n.On", "r.On"} {
		if strings.Contains(line, bad) {
			return false
		}
	}
	return strings.Contains(line, ".On")
}

// Sources() is the accessor the window depends on; it must answer identically
// for the two spellings of the same trigger.
func TestSourcesIsFormBlind(t *testing.T) {
	scalar := TriggerSpec{On: "gh.review_requested"}
	list := TriggerSpec{OnSources: []OnSource{{Source: "gh.review_requested"}}}

	if got := scalar.Sources(); len(got) != 1 || got[0] != "gh.review_requested" {
		t.Fatalf("scalar Sources() = %v", got)
	}
	if got := list.Sources(); len(got) != 1 || got[0] != "gh.review_requested" {
		t.Fatalf("list Sources() = %v — the pre-normalize window cannot see a list-form source", got)
	}

	multi := TriggerSpec{OnSources: []OnSource{
		{Source: "manual.rerun"}, {Source: "gh.pull_request"},
	}}
	got := multi.Sources()
	if len(got) != 2 || got[0] != "manual.rerun" || got[1] != "gh.pull_request" {
		t.Fatalf("multi-source Sources() = %v", got)
	}
}

// The behavioural half of META-TEST E, and the proof for the CRITICAL: the
// repo-consent boundary must reject an armed no-repos github trigger written
// in EITHER `on:` form. It read tr.On, which a list-form trigger leaves empty
// until NormalizeTriggers — and pack instantiation runs before that — so the
// check silently found no repo-scoped source and admitted the trigger. It then
// matched every repo the connector could see.
//
// The two forms are asserted side by side so a fix to one that misses the
// other cannot pass.
func TestArmedTriggerNeedsReposInEitherOnForm(t *testing.T) {
	const scalarOn = `    on: github.review_requested`
	const listOn = `    on:
      - github.review_requested`

	manifest := func(onBlock string) string {
		return `
pack:
  name: gh-pack
  version: 1.0.0
  requires:
    conductor: ">=0.1"
    connectors: [github]
triggers:
  - name: on_review
` + onBlock + `
    steps: [ { id: s, run: js, code: "return {}" } ]
`
	}

	write := func(t *testing.T, onBlock, arm string) string {
		dir := t.TempDir()
		writePackSource(t, dir, "src", manifest(onBlock))
		body := `
connectors: { gh: { use: github } }
packs:
  p:
    source: ./src
    connectors: { github: gh }
    triggers:
      on_review:
` + arm
		path := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	for _, form := range []struct{ name, on string }{
		{"scalar on:", scalarOn},
		{"list on:", listOn},
	} {
		t.Run(form.name, func(t *testing.T) {
			// Armed with no repos: → hard error naming the fix.
			_, err := resolveAndLoad(t, write(t, form.on, "        enabled: true\n"))
			if err == nil {
				t.Fatal("an ARMED github pack trigger with no repos: was accepted — " +
					"an empty repo set matches every repo the connector observes, so the " +
					"pack runs on repos the consumer never granted it")
			}
			if !strings.Contains(err.Error(), "repos") {
				t.Fatalf("rejection does not name repos as the fix: %v", err)
			}

			// Armed WITH repos: → accepted, so the check can't pass by
			// rejecting everything.
			if _, err := resolveAndLoad(t, write(t, form.on,
				"        enabled: true\n        repos: [acme/app]\n")); err != nil {
				t.Fatalf("armed WITH repos must load: %v", err)
			}
		})
	}
}
