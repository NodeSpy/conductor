package pkg_test

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// pkg/ is the vendor-neutral SDK every plugin builds on
// (docs/design/plugin-contract.md §1.12–§1.13). Vendor code — a forge, a
// chat service, a tunnel service, an alerting service — lives in that
// vendor's plugin, never here: a vendor name in pkg/ is the first step of a
// conductor change every time that vendor changes. This scans every non-test
// Go file (identifiers, strings and comments alike).
var vendorName = regexp.MustCompile(`(?i)\b(github|gitlab|gitea|bitbucket|slack|discord|sentry|smee|jira|linear|pagerduty|cloudflared?|ngrok|tailscale|localxpose|pinggy|serveo)\b`)

// moduleRef is the module's own import path, which names its host.
var moduleRef = regexp.MustCompile(`github\.com/NodeSpy/conductor`)

// transitional are the paths still under pkg/ that the plan moves out. Each
// entry names where it goes; the list only shrinks.
var transitional = map[string]string{
	"githubkit/": "the github plugin's code — moves to conductor-plugins (plugin-contract.md §5, step P)",
}

func TestPkgNamesNoVendor(t *testing.T) {
	var hits []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		for prefix := range transitional {
			if strings.HasPrefix(filepath.ToSlash(path), prefix) {
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
			line := moduleRef.ReplaceAllString(sc.Text(), "")
			if m := vendorName.FindString(line); m != "" {
				hits = append(hits, filepath.ToSlash(path)+":"+itoa(n)+": "+m)
			}
		}
		return sc.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		t.Errorf("vendor name in the vendor-neutral SDK: %s", h)
	}
}

func itoa(n int) string {
	b := []byte{}
	for n > 0 || len(b) == 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
