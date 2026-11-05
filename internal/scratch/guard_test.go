package scratch

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// tempDirUse is a call that puts a file in the system temp dir, often a
// RAM-backed tmpfs.
var tempDirUse = regexp.MustCompile(`os\.(MkdirTemp|CreateTemp)\(""|os\.TempDir\(\)`)

// tinyTempFiles are the files allowed to use the system temp dir, each for
// something small and bounded. Anything that can be large or of unpredictable
// size (a clone, an archive, an upload, staging, a build, a browser profile,
// what a launched tool writes) goes in the scratch dir: Dir, MkdirTemp.
var tinyTempFiles = map[string]string{
	"internal/scratch/scratch.go":       "the fallback when no scratch dir is set",
	"internal/scratch/held.go":          "records the temp dir the process started with",
	"internal/agentsandbox/binaries.go": "lists the temp dirs as roots a binary must not come from",
	"internal/sandbox/jail_linux.go":    "a mountpoint for the jail root when no scratch dir is given (holds nothing on the host)",
	"internal/bot/approve.go":           "an ssh-keygen signature and its payload",
	"internal/code/cli.go":              "a code step's source file",
	"internal/code/hostinterp.go":       "a code step's source file",
	"internal/controller/opencode.go":   "an opencode config file",
	"internal/dashboard/auth/sign.go":   "an ssh signature",
	"internal/dashboard/auth/ssh.go":    "an ssh signature check",
	"internal/gitpush/sign.go":          "a public key file",
	"internal/testhome/testhome.go":     "a test's fake home",
	"cmd/conductor/botcli.go":           "a public key file",
	"cmd/conductor/browserwallet.go":    "a public key file",
	"cmd/conductor/dashboard.go":        "a public key file",
	"cmd/conductor/dashboardlogin.go":   "a public key file",
	"cmd/conductor/main.go":             "sweeps the empty jail dirs older releases left there",
}

// TestLargeDataStaysOutOfTheSystemTempDir fails when non-test code outside
// the allowlist uses the system temp dir.
func TestLargeDataStaysOutOfTheSystemTempDir(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, top := range []string{"internal", "cmd", "pkg"} {
		err := filepath.WalkDir(filepath.Join(repo, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(repo, p)
			rel = filepath.ToSlash(rel)
			for i, line := range strings.Split(string(b), "\n") {
				code, _, _ := strings.Cut(line, "//")
				if !tempDirUse.MatchString(code) {
					continue
				}
				if _, ok := tinyTempFiles[rel]; ok {
					seen[rel] = true
					continue
				}
				t.Errorf("%s:%d uses the system temp dir (often a RAM-backed tmpfs): put large or unpredictable data in the scratch dir (scratch.Dir / scratch.MkdirTemp), or add the file to tinyTempFiles with why it stays small", rel, i+1)
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	for f := range tinyTempFiles {
		if !seen[f] {
			t.Errorf("tinyTempFiles lists %s, which no longer uses the system temp dir: drop it", f)
		}
	}
}
