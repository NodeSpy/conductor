package plugin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// readingEchoAt installs a build of acme-echo whose declaration lacks a
// file it reads (its "read" verb; it declares no read paths) as the binary
// an install record names, and writes a key file it will be asked to read.
func readingEchoAt(t *testing.T) (dir, bin, sum, key string) {
	t.Helper()
	built, sum := buildExamplePluginVariant(t, "acme-echo", "-X main.readVerb=true")
	dir = t.TempDir()
	bin = filepath.Join(dir, "connectors", "acme-echo", "v1.0.0", "acme-echo")
	b, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(bin), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, b, 0o755); err != nil {
		t.Fatal(err)
	}
	key = filepath.Join(t.TempDir(), "app-key.pem")
	if err := os.WriteFile(key, []byte("the key"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, bin, sum, key
}

// bootManager is what a daemon's boot does with install state alone (no
// update pass: dependency updates off): load it and build the plugin
// manager from it as the daemon does (ExplodeRefs, NewManager). It returns
// the manager, the plugin's group key, and what it logged so far.
func bootManager(t *testing.T, dir string) (*Manager, string, func() string) {
	t.Helper()
	ref := refFor(t, config.UseKindConnector, "acme/plugins/acme-echo")
	var (
		mu  sync.Mutex
		buf []string
	)
	state := LoadInstallState(dir)
	refs, failures := ExplodeRefs(map[string]config.PluginRef{ref.Key(): ref}, "", state)
	if len(failures) != 0 || len(refs) != 1 {
		t.Fatalf("%+v %+v", refs, failures)
	}
	m := NewManager(refs, "", state, Deps{Sandbox: testJailDeps(t), Log: func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		buf = append(buf, fmt.Sprintf(f, a...))
	}})
	t.Cleanup(func() { _ = m.Close() })
	var key string
	for k := range refs {
		key = k
	}
	return m, key, func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(buf, "\n")
	}
}

// readWith launches the plugin (if it is not running) and asks it to read key.
func readWith(t *testing.T, m *Manager, groupKey, key string) map[string]any {
	t.Helper()
	c, ok := m.Client(groupKey)
	if !ok {
		t.Fatal("no client for the plugin")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := c.Invoke(ctx, InvokeRequest{Instance: "acme-echo", Verb: "read", Options: map[string]any{"path": key}})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// bootLaunch boots from install state and calls the plugin once.
func bootLaunch(t *testing.T, dir, key string) (map[string]any, string) {
	t.Helper()
	m, g, logs := bootManager(t, dir)
	out := readWith(t, m, g, key)
	return out, logs()
}

// A build that ran before the plugin jail runs as it did then from its
// first launch after the upgrade, decided from install state alone, with
// no update pass (dependency updates off) and nothing held: its record may
// lack a file it reads, and its connector keeps working. The evidence: a
// version 1 record, a mark saved in its record (adoption clears Legacy), or
// a binary older than the jail. A build installed under the jail, a later
// build of the same pre-jail plugin included, has a complete record and is
// jailed: the same read is refused.
func TestABuildThatRanBeforeTheJailKeepsWorkingFromBoot(t *testing.T) {
	defer SetHostJailForTests(HostJail{OK: true})()
	for _, c := range []struct {
		name   string
		state  func(bin, sum string) string
		aged   bool
		before bool
	}{
		{"version 1 record", func(bin, sum string) string {
			return "version: 1\nplugins:\n- {key: connectors/acme-echo, name: acme-echo, kind: connector, source: SRC, resolved: connectors/acme-echo/v1.0.0, sha256: " + sum + ", path: " + bin + "}\n"
		}, false, true},
		{"adopted record marked as having run before the jail", func(bin, sum string) string {
			return "version: 2\nplugins:\n- {key: connectors/acme-echo, name: acme-echo, kind: connector, source: SRC, resolved: connectors/acme-echo/v1.0.0, sha256: " + sum + ", path: " + bin + ", manifest_schema: 2, pre_jail: true, ran_before_jail: true}\n"
		}, false, true},
		{"adopted record whose binary predates the jail", func(bin, sum string) string {
			return "version: 2\nplugins:\n- {key: connectors/acme-echo, name: acme-echo, kind: connector, source: SRC, resolved: connectors/acme-echo/v1.0.0, sha256: " + sum + ", path: " + bin + ", manifest_schema: 2, pre_jail: true}\n"
		}, true, true},
		{"build installed under the jail, of a pre-jail plugin", func(bin, sum string) string {
			return "version: 2\nplugins:\n- {key: connectors/acme-echo, name: acme-echo, kind: connector, source: SRC, resolved: connectors/acme-echo/v1.0.0, sha256: " + sum + ", path: " + bin + ", manifest_schema: 2, pre_jail: true}\n"
		}, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, bin, sum, key := readingEchoAt(t)
			if c.aged {
				old := jailEpoch.Add(-48 * time.Hour)
				if err := os.Chtimes(bin, old, old); err != nil {
					t.Fatal(err)
				}
			}
			src := refFor(t, config.UseKindConnector, "acme/plugins/acme-echo").Source()
			writeState(t, dir, strings.ReplaceAll(c.state(bin, sum), "SRC", src))
			out, logs := bootLaunch(t, dir, key)
			if !c.before {
				if out["content"] == "the key" || out["error"] == "" {
					t.Fatalf("a build installed under the jail must be jailed to its record (no such read): %+v\n%s", out, logs)
				}
				if !strings.Contains(logs, "jailed to what it declares") {
					t.Fatalf("log: %s", logs)
				}
				return
			}
			if out["content"] != "the key" {
				t.Fatalf("a build that ran before the jail must keep working from its first launch: %+v\n%s", out, logs)
			}
			if !strings.Contains(logs, "this build ran before the jail") || strings.Contains(logs, "cannot jail plugins") {
				t.Fatalf("log: %s", logs)
			}
			// The mark is saved: it outlives adoption clearing Legacy.
			st := LoadInstallState(dir)
			st.Plugins[0].Legacy = false
			if err := st.Save(); err != nil {
				t.Fatal(err)
			}
			if again, _ := LoadInstallState(dir).Get("connectors/acme-echo"); !again.RanBeforeJail {
				t.Fatalf("the mark did not survive a save: %+v", again)
			}
		})
	}
}

// The mark is the build's: when a build installed under the jail replaces a
// build that ran before it live (a hot reload of an approved update), the
// new build runs jailed, and the read its record does not declare is
// refused.
func TestTheBuildThatReplacesItLiveRunsJailed(t *testing.T) {
	defer SetHostJailForTests(HostJail{OK: true})()
	dir, bin, sum, key := readingEchoAt(t)
	src := refFor(t, config.UseKindConnector, "acme/plugins/acme-echo").Source()
	writeState(t, dir, "version: 2\nplugins:\n- {key: connectors/acme-echo, name: acme-echo, kind: connector, source: "+src+", resolved: connectors/acme-echo/v1.0.0, sha256: "+sum+", path: "+bin+", manifest_schema: 2, pre_jail: true, ran_before_jail: true}\n")
	m, g, logs := bootManager(t, dir)
	if out := readWith(t, m, g, key); out["content"] != "the key" {
		t.Fatalf("before the swap: %+v\n%s", out, logs())
	}

	next := filepath.Join(dir, "connectors", "acme-echo", "v2.0.0", "acme-echo")
	b, _ := os.ReadFile(bin)
	if err := os.MkdirAll(filepath.Dir(next), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(next, b, 0o755); err != nil {
		t.Fatal(err)
	}
	cur, _ := m.Spec(g)
	if !cur.RanBeforeJail {
		t.Fatalf("booted spec: %+v", cur)
	}
	ref := refFor(t, config.UseKindConnector, "acme/plugins/acme-echo")
	newSpec := SpecFromRef(ref, "", Installed{Key: "connectors/acme-echo", Name: "acme-echo", Kind: "connector", Source: src,
		Resolved: "connectors/acme-echo/v2.0.0", Sha256: sum, Path: next, ManifestSchema: manifestSchema, PreJail: true}, true)
	if err := m.Reload(g, newSpec); err != nil {
		t.Fatal(err)
	}
	if s, _ := m.Spec(g); s.RanBeforeJail {
		t.Fatalf("the swapped-in build kept the old build's mark: %+v", s)
	}
	if out := readWith(t, m, g, key); out["content"] == "the key" || out["error"] == "" {
		t.Fatalf("the build installed under the jail must run jailed after the swap: %+v\n%s", out, logs())
	}
}
