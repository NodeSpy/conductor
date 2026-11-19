package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/keycache"
)

func TestSandboxCachesListAndClear(t *testing.T) {
	root := t.TempDir()
	store := filepath.Join(root, "caches")
	args := writeCfg(t, "connectors:\n  gh: { use: github }\nsandbox:\n  state_dir: "+filepath.Join(root, "agent-state")+"\n  caches:\n    dir: "+store+"\n")
	st := &keycache.Store{Dir: store}
	pub := func(k keycache.Key) keycache.Entry {
		e, err := st.Publish(k, []string{".cache/fake"}, func(data string) error {
			d := filepath.Join(data, ".cache", "fake")
			if err := os.MkdirAll(d, 0o700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(d, "f"), []byte(strings.Repeat("x", 8192)), 0o600)
		})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	shared := keycache.Key{Namespace: "autopilot", Template: "{{.repo}}", Value: "acme/web"}
	pr := keycache.Key{Namespace: "autopilot", Template: "pr-{{.repo}}-{{.pr}}", Value: "pr-acme/web-12"}
	other := keycache.Key{Template: "{{.repo}}", Value: "acme/api"}
	pub(shared)
	inUse := pub(pr)
	pub(other)
	// A live launch overlays the PR's cache: conductor's ledger beside the
	// store names what its overlays sit on. Named for when it was restored,
	// so it is live within the grace.
	run := filepath.Join(store, ".layers", fmt.Sprintf("%d-r1", time.Now().UnixNano()))
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	for f, v := range map[string]string{"home": filepath.Join(root, "home"), "key": pr.ID(), "dispatch": "d1", "uses": inUse.Dir + "\n"} {
		if err := os.WriteFile(filepath.Join(run, f), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	out, _ := captureStdout(t, func() error {
		if c := cmdSandboxCaches(args); c != 0 {
			t.Errorf("list exit %d", c)
		}
		return nil
	})
	for _, want := range []string{"KEY", "TEMPLATE", "autopilot:acme/web", "autopilot:pr-acme/web-12", "pr-{{.repo}}-{{.pr}}", "acme/api", "(learning)", "yes", "3 key(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	// Clearing keeps what a running launch uses.
	out, _ = captureStdout(t, func() error {
		if c := cmdSandboxCaches(append([]string{"clear"}, append(args, "autopilot:acme/web", "pr-acme/web-12")...)); c != 1 {
			t.Errorf("clear with a kept cache exit %d", c)
		}
		return nil
	})
	if !strings.Contains(out, "cleared autopilot:acme/web") || !strings.Contains(out, "kept autopilot:pr-acme/web-12") {
		t.Fatalf("clear:\n%s", out)
	}
	if len(st.List()) != 2 {
		t.Fatalf("left %d", len(st.List()))
	}
	// --force removes it; another key is untouched.
	captureStdout(t, func() error {
		if c := cmdSandboxCaches(append([]string{"clear", "--force"}, append(args, pr.ID())...)); c != 0 {
			t.Errorf("forced clear exit %d", c)
		}
		return nil
	})
	if l := st.List(); len(l) != 1 || l[0].ID != other.ID() {
		t.Fatalf("left %+v", l)
	}
	// Clear's in-use check and removal wait for the store's lock, held here
	// as a daemon's restore would hold it (another process's flock).
	held, release := make(chan struct{}), make(chan struct{})
	go (&keycache.Store{Dir: store}).Locked(func() { close(held); <-release })
	<-held
	start := time.Now()
	time.AfterFunc(300*time.Millisecond, func() { close(release) })
	captureStdout(t, func() error {
		if c := cmdSandboxCaches(append([]string{"clear", "--all"}, args...)); c != 0 {
			t.Errorf("clear --all exit %d", c)
		}
		return nil
	})
	if d := time.Since(start); d < 250*time.Millisecond {
		t.Fatalf("clear did not wait for the store's lock (%v)", d)
	}
	if len(st.List()) != 0 || len(st.Keys()) != 0 {
		t.Fatalf("left %+v %+v", st.List(), st.Keys())
	}
	out, _ = captureStdout(t, func() error {
		if c := cmdSandboxCaches(append([]string{"clear"}, append(args, "../etc")...)); c != 0 {
			t.Errorf("no-match exit %d", c)
		}
		return nil
	})
	if !strings.Contains(out, "no matching cache") {
		t.Fatalf("no-match:\n%s", out)
	}
	if c := cmdSandboxCaches(append([]string{"clear"}, args...)); c != 2 {
		t.Fatalf("clear without a key exit %d", c)
	}
}
