package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/repocache"
)

func TestSandboxCachesListAndClear(t *testing.T) {
	root := t.TempDir()
	store := filepath.Join(root, "caches")
	agentState := filepath.Join(root, "agent-state")
	args := writeCfg(t, "connectors:\n  gh: { use: github }\nsandbox:\n  state_dir: "+agentState+"\n  caches:\n    dir: "+store+"\n")
	st := &repocache.Store{Dir: store}
	pub := func(repo, name, key string) repocache.Entry {
		e, err := st.Publish(repocache.Meta{Repo: repo, Name: name, Key: key, Origin: repocache.OriginAutodetect, Paths: []string{"~/c"}, Created: time.Now()},
			func(data string) error {
				if err := os.MkdirAll(filepath.Join(data, "home", "c"), 0o700); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(data, "home", "c", "f"), []byte(strings.Repeat("x", 8192)), 0o600)
			})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	pub("acme/web", "go", "go-linux-aaa")
	inUse := pub("acme/web", "npm", "npm-linux-bbb")
	pub("acme/api", "go", "go-linux-ccc")
	// A live launch overlays the npm cache: conductor's ledger beside the
	// store names it and what its overlays sit on.
	home := filepath.Join(agentState, ".dispatch-homes", "agent_x@d1")
	// Named for when it was restored, so it is live within the grace.
	run := filepath.Join(store, ".layers", fmt.Sprintf("%d-r1", time.Now().UnixNano()))
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "home"), []byte(home), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "uses"), []byte(inUse.Dir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, _ := captureStdout(t, func() error {
		if c := cmdSandboxCaches(args); c != 0 {
			t.Errorf("list exit %d", c)
		}
		return nil
	})
	for _, want := range []string{"REPO", "acme/web", "go-linux-aaa", "npm-linux-bbb", "acme/api", "autodetect", "yes", "3 cache(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	// Clearing a repository keeps what a running launch uses.
	out, _ = captureStdout(t, func() error {
		if c := cmdSandboxCaches(append([]string{"clear"}, append(args, "acme/web")...)); c != 1 {
			t.Errorf("clear with a kept cache exit %d", c)
		}
		return nil
	})
	if !strings.Contains(out, "removed acme/web go go-linux-aaa") || !strings.Contains(out, "kept acme/web npm") {
		t.Fatalf("clear:\n%s", out)
	}
	if len(st.List()) != 2 {
		t.Fatalf("left %d", len(st.List()))
	}
	// --force removes it; another repository is untouched.
	captureStdout(t, func() error {
		if c := cmdSandboxCaches(append([]string{"clear", "--force"}, append(args, "acme/web", "npm")...)); c != 0 {
			t.Errorf("forced clear exit %d", c)
		}
		return nil
	})
	if l := st.List(); len(l) != 1 || l[0].Repo != "acme/api" {
		t.Fatalf("left %+v", l)
	}
	// Clear's in-use check and removal wait for the store's lock, held here
	// as a daemon's restore would hold it (another process's flock).
	held, release := make(chan struct{}), make(chan struct{})
	go (&repocache.Store{Dir: store}).Locked(func() { close(held); <-release })
	<-held
	start := time.Now()
	time.AfterFunc(300*time.Millisecond, func() { close(release) })
	captureStdout(t, func() error {
		if c := cmdSandboxCaches(append([]string{"clear"}, append(args, "acme/api")...)); c != 0 {
			t.Errorf("clear exit %d", c)
		}
		return nil
	})
	if d := time.Since(start); d < 250*time.Millisecond {
		t.Fatalf("clear did not wait for the store's lock (%v)", d)
	}
	if len(st.List()) != 0 {
		t.Fatalf("left %+v", st.List())
	}
	if c := cmdSandboxCaches(append([]string{"clear"}, append(args, "../etc")...)); c != 2 {
		t.Fatalf("bad repo exit %d", c)
	}
}
