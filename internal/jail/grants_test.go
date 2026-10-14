//go:build unix

package jail

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/grant"
)

// The files a grant pins are copied into private scratch, hashing what is
// copied, never through a symlink and never from outside the workspace; the
// argv names the copy, in a bare token and in a --flag=path token alike.
func TestStageGrantFiles(t *testing.T) {
	ws := t.TempDir()
	m := &Manager{GrantScratch: filepath.Join(t.TempDir(), "scratch")}
	d := &Dispatch{LaunchSpec: LaunchSpec{Workspace: ws}, TmpDir: t.TempDir()}
	write := func(p, body string) string {
		full := filepath.Join(ws, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(body), 0o644)
		sum, _ := grant.FileSHA256(full)
		return sum
	}
	spec := func(paths map[string]string) grant.GrantSpec {
		g := grant.GrantSpec{ID: "g1", Expiry: time.Now().Add(time.Minute)}
		for p, sum := range paths {
			g.FileDigests = append(g.FileDigests, grant.FileDigest{Path: p, SHA256: sum})
		}
		return g
	}
	planSum := write("plans/plan.tfplan", "plan")
	varSum := write("prod.tfvars", "vars")
	use, err := m.stageGrantFiles(d, spec(map[string]string{"plans/plan.tfplan": planSum, "prod.tfvars": varSum}),
		[]string{"apply", "--var-file=prod.tfvars", "plans/plan.tfplan"}, ws)
	if err != nil {
		t.Fatal(err)
	}
	defer use.cleanup()
	if len(use.files) != 2 || !strings.HasPrefix(use.args[1], "--var-file="+m.GrantScratch) || !strings.HasPrefix(use.args[2], m.GrantScratch) {
		t.Fatalf("argv names the private copies: %q", use.args)
	}
	for _, f := range use.files {
		fi, _ := os.Stat(f)
		if fi.Mode().Perm()&0o222 != 0 {
			t.Fatalf("a private copy is read-only: %s %v", f, fi.Mode())
		}
	}
	fi, _ := os.Stat(m.GrantScratch)
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("the scratch dir is private: %v", fi.Mode())
	}

	if _, err := m.stageGrantFiles(d, spec(map[string]string{"plans/plan.tfplan": varSum}), []string{"apply", "plans/plan.tfplan"}, ws); !errors.Is(err, grant.ErrDigest) {
		t.Fatalf("other content: ErrDigest, got %v", err)
	}
	outside := filepath.Join(t.TempDir(), "plan")
	os.WriteFile(outside, []byte("plan"), 0o644)
	os.Symlink(filepath.Join(ws, "plans"), filepath.Join(ws, "link"))
	os.Symlink(outside, filepath.Join(ws, "plan-link"))
	for _, p := range []string{outside, "../" + filepath.Base(outside), "link/plan.tfplan", "plan-link"} {
		if _, err := m.stageGrantFiles(d, spec(map[string]string{p: planSum}), []string{"apply", p}, ws); !errors.Is(err, grant.ErrDigest) {
			t.Errorf("%s: refused (outside the workspace, or through a symlink), got %v", p, err)
		}
	}
	entries, _ := os.ReadDir(m.GrantScratch)
	if len(entries) != 1 {
		t.Fatalf("a refused staging leaves nothing behind: %d dirs", len(entries))
	}
}
