package memory

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// §1: the round-1 H8 fix guarded two of FOUR agent-facing memory writers.
// The other two — the `memory.remember` verb and the `run: code` binding —
// stayed open, which is a live cross-tenant leak on a shared daemon.
//
// The per-path tests live with their packages (they need those packages'
// harnesses). This one guards the SHAPE: every call that hands a caller's
// scope to Remember must have CheckAgentScope in view, so a fifth writer
// cannot be added without either guarding it or deleting this list.
func TestEveryAgentFacingRememberIsGuarded(t *testing.T) {
	// Each entry: a file that writes memory, and whether its scope comes
	// from an AGENT (guarded) or from conductor itself (not).
	paths := map[string]bool{
		"internal/memory/harvest.go":   true,  // the ```remember output contract
		"internal/memory/ipc.go":       true,  // the skill / MCP memory_remember tool
		"internal/connector/memory.go": true,  // the memory.remember verb
		"internal/code/membind.go":     true,  // run: code + the go-embed MemHandle
		"internal/engine/outcome.go":   false, // conductor's own outcome notes
	}
	root := repoRoot(t)
	callRe := regexp.MustCompile(`\bm\.Remember\(|\bmemInvoke\(`)

	// Every file that calls Remember must be accounted for above — a new
	// one shows up here rather than shipping unguarded.
	var found []string
	for _, dir := range []string{"internal"} {
		_ = filepath.Walk(filepath.Join(root, dir), func(p string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr != nil || !regexp.MustCompile(`\.Remember\(`).Match(b) {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			found = append(found, rel)
			return nil
		})
	}
	for _, rel := range found {
		if _, known := paths[rel]; !known {
			t.Errorf("%s writes memory but is not classified here — if its scope is agent-supplied it needs memory.CheckAgentScope", rel)
		}
	}

	for rel, agentFacing := range paths {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		src := string(b)
		if !callRe.MatchString(src) {
			t.Errorf("%s no longer writes memory — drop it from this list", rel)
			continue
		}
		// Either the direct reserved-bucket call, or CheckOp — the round-3
		// chokepoint, which runs CheckAgentScope itself AND the operator's
		// scope allowlist, for every op rather than just this one.
		guarded := strings.Contains(src, "CheckAgentScope") || strings.Contains(src, ".CheckOp(")
		if agentFacing && !guarded {
			t.Errorf("%s takes an agent-supplied scope but never calls CheckAgentScope — the shared bucket is reachable from it", rel)
		}
		if !agentFacing && guarded {
			t.Errorf("%s is conductor-authored; guarding it would block legitimate global context", rel)
		}
	}
}

// repoRoot walks up to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		d = filepath.Dir(d)
	}
	t.Fatal("module root not found")
	return ""
}
