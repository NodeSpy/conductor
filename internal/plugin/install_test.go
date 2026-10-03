package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestInstallStateSaveIsAtomic: a concurrent reader of installed.yaml (another
// `conductor` invocation's LoadInstallState, or a crash mid-write) must never
// observe a truncated or half-written file. The old os.WriteFile truncates
// the existing file in place before writing the new bytes, so a reader
// landing in that window sees a zero-length (or partial) file; the fix
// writes to a sibling temp file and renames it over the final path, which
// POSIX guarantees resolves atomically to either the old or the new content.
func TestInstallStateSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	s := LoadInstallState(dir)
	s.Put(Installed{Key: "connectors/a", Kind: "connector", Name: "a", Resolved: "v0"})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, installStateFile)
	stop := make(chan struct{})
	var badRead atomic.Value

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			b, err := os.ReadFile(path)
			if err != nil {
				continue // briefly absent before the first rename is fine
			}
			if len(b) == 0 {
				badRead.Store(fmt.Errorf("observed a zero-length install state file mid-write"))
				return
			}
			var on InstallState
			if err := yaml.Unmarshal(b, &on); err != nil {
				badRead.Store(fmt.Errorf("observed a corrupt/partial install state file: %w", err))
				return
			}
		}
	}()

	for i := 0; i < 300; i++ {
		s.Put(Installed{Key: "connectors/a", Kind: "connector", Name: "a", Resolved: fmt.Sprintf("v%d", i)})
		if err := s.Save(); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("Save: %v", err)
		}
	}
	close(stop)
	wg.Wait()
	if v := badRead.Load(); v != nil {
		t.Fatal(v)
	}

	// Sanity: the final content really did land (this isn't a no-op race).
	final, ok := LoadInstallState(dir).Get("connectors/a")
	if !ok || final.Resolved != "v299" {
		t.Fatalf("final state = %+v, want Resolved v299", final)
	}

	// No stray temp file left behind (rename consumed it, each iteration).
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != installStateFile {
			t.Fatalf("stray file left in install dir: %s", e.Name())
		}
	}
}
