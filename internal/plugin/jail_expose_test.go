package plugin

import (
	"bufio"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// TestExposeTargetsReachableFromTheJail runs a jailed probe as an exposure
// plugin: the listener the engine wired to it is reachable from its jail,
// and nothing else on this machine is — not another local service, not a
// wired listener that is conductor's own port, and not the wired listener
// either when the build does not declare expose_local.
func TestExposeTargetsReachableFromTheJail(t *testing.T) {
	sd := testJailDeps(t)
	probe := filepath.Join(t.TempDir(), "jailprobe")
	if out, err := exec.Command("go", "build", "-o", probe, "./testdata/jailprobe").CombinedOutput(); err != nil {
		t.Fatalf("build probe: %v\n%s", err, out)
	}
	wired := httpOn(t, "wired-listener")
	other := httpOn(t, "other-service")
	own := httpOn(t, "conductors-own")
	_, ownPort, _ := net.SplitHostPort(own)
	ownWas := ownServicePort
	ownServicePort = func(port int) (bool, error) { return strconv.Itoa(port) == ownPort, nil }
	t.Cleanup(func() { ownServicePort = ownWas })

	launch := func(m Manifest, exposeTo []string) (map[string]string, []string) {
		t.Helper()
		var logged []string
		s := Spec{
			Name: "relay", Kind: KindConnector, BinPath: probe, Manifest: m,
			ExposeTo: exposeTo,
			Args:     []string{"get=http://" + wired, "get=http://" + other, "get=http://" + own},
		}
		s.ExposeTo = grantExposeTargets(s, func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) })
		cmd, cleanup, sandboxed, err := buildCommand(s, sd)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if !sandboxed {
			t.Fatal("the launch is not jailed")
		}
		stdin, _ := cmd.StdinPipe()
		stdout, _ := cmd.StdoutPipe()
		var stderr strings.Builder
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { stdin.Close(); _ = cmd.Wait() }()
		got := map[string]string{}
		done := make(chan struct{})
		go func() {
			defer close(done)
			sc := bufio.NewScanner(stdout)
			for sc.Scan() {
				if sc.Text() == "READY" {
					return
				}
				if k, v, ok := strings.Cut(sc.Text(), "\t"); ok {
					got[k] = v
				}
			}
		}()
		select {
		case <-done:
		case <-time.After(60 * time.Second):
			t.Fatalf("probe did not finish; stderr:\n%s", stderr.String())
		}
		return got, logged
	}
	check := func(got map[string]string, want map[string]string) {
		t.Helper()
		for k, w := range want {
			if !strings.HasPrefix(got[k], w) {
				t.Errorf("%s: got %q, want %s", k, got[k], w)
			}
		}
	}

	got, logged := launch(Manifest{ExposeLocal: true}, []string{wired, own})
	check(got, map[string]string{
		"get=http://" + wired: "ok:wired-listener",
		"get=http://" + other: "denied",
		"get=http://" + own:   "denied",
	})
	if !slices.ContainsFunc(logged, func(l string) bool {
		return strings.Contains(l, own) && strings.Contains(l, "conductor itself listens there")
	}) {
		t.Errorf("conductor's own port was not refused by name: %q", logged)
	}

	got, logged = launch(Manifest{}, []string{wired})
	check(got, map[string]string{"get=http://" + wired: "denied"})
	if len(logged) != 1 || !strings.Contains(logged[0], "does not declare expose_local") {
		t.Errorf("an undeclared build's refusal is not logged: %q", logged)
	}
}

func TestGrantExposeTargets(t *testing.T) {
	ownWas := ownServicePort
	ownServicePort = func(port int) (bool, error) { return port == 9000, nil }
	t.Cleanup(func() { ownServicePort = ownWas })
	s := Spec{Name: "relay", Manifest: Manifest{ExposeLocal: true},
		ExposeTo: []string{"127.0.0.1:8787", "localhost:8788", "[::1]:8789", "192.0.2.7:8790", "127.0.0.1:9000"}}
	var logged []string
	got := grantExposeTargets(s, func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) })
	if want := []string{"127.0.0.1:8787", "localhost:8788"}; !slices.Equal(got, want) {
		t.Fatalf("granted %v, want %v", got, want)
	}
	if len(logged) != 3 {
		t.Fatalf("every address not granted is logged once: %q", logged)
	}
	s.ExposeTo = got
	if p := exposePorts(s); !slices.Equal(p, []int{8787, 8788}) {
		t.Fatalf("exposePorts = %v", p)
	}
	for _, sp := range []Spec{
		{Manifest: Manifest{ExposeLocal: true}, ExposeTo: got, TrustFull: true},
		{Manifest: Manifest{ExposeLocal: true}, ExposeTo: got, Probe: true},
	} {
		if g := grantExposeTargets(sp, nil); g != nil {
			t.Errorf("granted %v to an unjailed or probe launch", g)
		}
	}
	if p := exposePorts(Spec{ExposeTo: got}); p != nil {
		t.Errorf("a build without expose_local got ports %v", p)
	}
}

// The capability is recorded, shown at consent, and an update that adds it
// asks for approval.
func TestExposeLocalIsRecordedShownAndWidens(t *testing.T) {
	m := ManifestFromDecl(&Decl{Capabilities: Capabilities{ExposeLocal: true}})
	if !m.ExposeLocal || !m.Caps().ExposeLocal || m.IsZero() {
		t.Fatalf("not recorded: %+v", m)
	}
	if !slices.Contains(m.Lines(), exposeLocalLabel) {
		t.Errorf("not shown: %q", m.Lines())
	}
	if w := (Manifest{}).Widens(m); !slices.Contains(w, exposeLocalLabel) {
		t.Errorf("adding it does not widen: %q", w)
	}
	if w := m.Widens(Manifest{}); len(w) != 0 {
		t.Errorf("dropping it widens: %q", w)
	}
}

// SetExposeTargets gives each process its own instances' targets: the
// shared process the union over its non-isolated ones, an isolated instance
// exactly its own, and nobody another plugin's.
func TestSetExposeTargetsPerProcess(t *testing.T) {
	config.SetStateDir(t.TempDir())
	t.Cleanup(func() { config.SetStateDir("") })
	ref := refFor(t, config.UseKindConnector, "relay")
	ref.Instances = map[string]config.ConnectorGrant{"a": {}, "b": {}, "c": {Isolate: true}}
	key := ref.Key()
	mgr := NewManager(map[string]config.PluginRef{key: ref}, "", LoadInstallState(InstallDir()), Deps{})
	defer mgr.Close()
	mgr.SetExposeTargets(map[string][]string{
		"a":     {"127.0.0.1:8788"},
		"b":     {"127.0.0.1:8787", "127.0.0.1:8788"},
		"c":     {"127.0.0.1:8789"},
		"other": {"127.0.0.1:9999"},
	})
	spec, _ := mgr.Spec(key)
	if want := []string{"127.0.0.1:8787", "127.0.0.1:8788"}; !slices.Equal(spec.ExposeTo, want) {
		t.Errorf("shared spec: %v, want %v", spec.ExposeTo, want)
	}
	if shared, ok := mgr.Client(key); !ok || !slices.Equal(shared.spec.ExposeTo, spec.ExposeTo) {
		t.Errorf("shared client: %v", shared.spec.ExposeTo)
	}
	c, err := mgr.InstanceClient(key, "c")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"127.0.0.1:8789"}; !slices.Equal(c.spec.ExposeTo, want) {
		t.Errorf("isolated instance: %v, want %v", c.spec.ExposeTo, want)
	}
}

// ExposeRefusals judges each instance's launch from its recorded manifest,
// as its launch would: an undeclared build is refused its targets, a
// declared one only a target off loopback, and trust: full nothing.
func TestExposeRefusals(t *testing.T) {
	for _, c := range []struct {
		name string
		m    Manifest
		want map[string]string
	}{
		{"undeclared", Manifest{}, map[string]string{"a": "does not declare expose_local", "b": "does not declare expose_local"}},
		{"declared", Manifest{ExposeLocal: true}, map[string]string{"b": "127.0.0.1 only"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			config.SetStateDir(t.TempDir())
			t.Cleanup(func() { config.SetStateDir("") })
			ref := refFor(t, config.UseKindConnector, "relay")
			ref.Instances = map[string]config.ConnectorGrant{"a": {}, "b": {Isolate: true}, "c": {Isolate: true, TrustFull: true}, "idle": {}}
			st := LoadInstallState(InstallDir())
			st.Put(Installed{Key: ref.Use.InstallKey(), Name: "relay", Resolved: "connectors/relay/v1.1.0", Path: "/nonexistent", Manifest: c.m})
			mgr := NewManager(map[string]config.PluginRef{ref.Key(): ref}, "", st, Deps{})
			defer mgr.Close()
			mgr.SetExposeTargets(map[string][]string{
				"a": {"127.0.0.1:8787"},
				"b": {"192.0.2.7:8788"},
				"c": {"127.0.0.1:8789"},
			})
			got := mgr.ExposeRefusals()
			if len(got) != len(c.want) {
				t.Fatalf("refusals = %q, want %d", got, len(c.want))
			}
			for name, want := range c.want {
				if !strings.Contains(got[name], want) {
					t.Errorf("%s: %q, want it to say %q", name, got[name], want)
				}
			}
		})
	}
}
