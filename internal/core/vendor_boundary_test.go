package core

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The engine side is vendor-neutral (docs/design/plugin-contract.md §0,
// §1.13): it acts only on what a connector DECLARES, so no forge, chat,
// alerting or tunnel vendor is named in the packages that route, dispatch,
// check out, scope or remember work. A vendor name here is the first step of
// a conductor change every time that vendor changes. This scans every
// non-test Go file of those packages (identifiers, strings and comments
// alike); vendor code lives in its plugin.
var engineSide = []string{
	"internal/core", "internal/engine", "internal/flow", "internal/dispatch",
	"internal/gitwt", "internal/controller", "internal/skill", "internal/memory",
	"internal/plugin",
}

var engineVendorName = regexp.MustCompile(`(?i)\b(github|gitlab|gitea|bitbucket|slack|discord|sentry|smee|jira|linear|pagerduty|cloudflared?|ngrok|tailscale|localxpose|pinggy|serveo|ntfy|pushover|notifiarr)\b`)

// moduleImport is the module's own import path, which names its host.
var moduleImport = regexp.MustCompile(`github\.com/NodeSpy/conductor[A-Za-z0-9_./-]*`)

// engineSideAllowed are the paths that may name a vendor, each with why. The
// list only shrinks.
var engineSideAllowed = map[string]string{
	"internal/core/coretest/": "test support only: the fixture IS a snapshot of the github plugin's declaration",
}

func TestEngineSideNamesNoVendor(t *testing.T) {
	root := repoRootFor(t)
	var hits []string
	for _, dir := range engineSide {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if d.IsDir() || !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
				return nil
			}
			for prefix := range engineSideAllowed {
				if strings.HasPrefix(rel, prefix) {
					return nil
				}
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			for n := 1; sc.Scan(); n++ {
				if m := engineVendorName.FindString(moduleImport.ReplaceAllString(sc.Text(), "")); m != "" {
					hits = append(hits, fmt.Sprintf("%s:%d: %s", rel, n, m))
				}
			}
			return sc.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, h := range hits {
		t.Errorf("vendor name on the engine side: %s", h)
	}
	for prefix := range engineSideAllowed {
		if _, err := os.Stat(filepath.Join(root, prefix)); err != nil {
			t.Errorf("%s is allowed to name a vendor but no longer exists — drop its entry", prefix)
		}
	}
}
