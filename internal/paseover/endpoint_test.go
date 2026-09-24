package paseover

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// never/always are injected probers, so the ladder's default rungs are tested
// without depending on whether THIS box happens to have a daemon at ~/.paseo.
func never(string) bool  { return false }
func always(string) bool { return true }

func noEnv(string) string { return "" }

func TestResolveLadder(t *testing.T) {
	cases := []struct {
		name   string
		target Target
		want   []string
	}{{
		name:   "server wins over everything",
		target: Target{Server: "10.0.0.1:9999", Home: "/srv/h", Local: true, Live: always, Env: func(string) string { return "/env/h" }},
		want:   []string{"--host", "10.0.0.1:9999"},
	}, {
		name:   "home beats the env and the defaults",
		target: Target{Home: "/srv/h", Local: true, Live: always, Env: func(string) string { return "/env/h" }},
		want:   []string{"--home", "/srv/h"},
	}, {
		name:   "PASEO_HOME beats the defaults",
		target: Target{Local: true, Live: always, Env: func(string) string { return "/env/h" }},
		want:   []string{"--home", "/env/h"},
	}, {
		// Rung 4 adds NO flag: ~/.paseo is where paseo looks anyway, and a flag
		// nobody asked for breaks wrappers that do not implement it.
		name:   "live default home adds no flag",
		target: Target{Local: true, Live: always, Dial: never, Env: noEnv},
		want:   nil,
	}, {
		name:   "default endpoint when ~/.paseo is dead but 6767 answers",
		target: Target{Local: true, Live: never, Dial: always, Env: noEnv},
		want:   []string{"--host", DefaultServer},
	}, {
		// Rung 6: nothing found. This must be the OLD behavior exactly — pass
		// nothing and let paseo decide.
		name:   "nothing found falls through to paseo's own default",
		target: Target{Local: true, Live: never, Dial: never, Env: noEnv},
		want:   nil,
	}, {
		name:   "remote with nothing declared resolves to nothing",
		target: Target{Local: false, Live: always, Dial: always, Env: func(string) string { return "/env/h" }},
		want:   nil,
	}, {
		name:   "remote still honors an explicit home",
		target: Target{Home: "/far/h", Local: false},
		want:   []string{"--home", "/far/h"},
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Resolve(c.target)
			if !slices.Equal(got.Args, c.want) {
				t.Fatalf("args = %v, want %v", got.Args, c.want)
			}
			if got.Source == "" {
				t.Error("every rung must name itself for the boot log")
			}
		})
	}
}

// The explicit rungs must NOT be probed. An operator who names a target gets
// that target: a daemon that is briefly down must never cause conductor to
// silently reroute work to a different one.
func TestResolveDoesNotProbeExplicitTargets(t *testing.T) {
	probed := false
	spy := func(string) bool { probed = true; return false }

	Resolve(Target{Server: "10.0.0.1:1", Local: true, Live: spy, Dial: spy, Env: noEnv})
	Resolve(Target{Home: "/srv/h", Local: true, Live: spy, Dial: spy, Env: noEnv})
	Resolve(Target{Local: true, Live: spy, Dial: spy, Env: func(string) string { return "/env/h" }})

	if probed {
		t.Error("an explicitly named daemon was probed and could be silently overridden")
	}
}

func TestResolveExpandsTildeInHomes(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	got := Resolve(Target{Home: "~/paseo-home", Local: true}).Home()
	if want := filepath.Join(home, "paseo-home"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEndpointAccessors(t *testing.T) {
	h := Endpoint{Args: []string{"--home", "/srv/h"}}
	if h.Home() != "/srv/h" || h.Server() != "" {
		t.Errorf("home endpoint: home=%q server=%q", h.Home(), h.Server())
	}
	if !strings.Contains(h.Label(), "home /srv/h") {
		t.Errorf("label = %q", h.Label())
	}
	s := Endpoint{Args: []string{"--host", "1.2.3.4:5"}}
	if s.Server() != "1.2.3.4:5" || s.Home() != "" {
		t.Errorf("server endpoint: home=%q server=%q", s.Home(), s.Server())
	}
	if (Endpoint{}).Label() == "" {
		t.Error("an unresolved endpoint still needs a label")
	}
}

// A pid file is a CLAIM, not proof. A daemon killed with SIGKILL leaves one
// behind, and trusting it would route every dispatch at a dead home — the
// exact silent misrouting this ladder exists to prevent.
func TestLiveAtHomeRejectsStaleAndMalformedPidFiles(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, PidFileName), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	// Our own pid is certainly alive.
	if dir := write(t, `{"pid":`+strconv.Itoa(os.Getpid())+`,"listen":"127.0.0.1:6767"}`); !LiveAtHome(dir) {
		t.Error("a pid file naming a live process should count as live")
	}
	// pid 0 / negative / garbage / absent → not live.
	for _, body := range []string{`{"pid":0}`, `{"pid":-1}`, `not json`, `{}`} {
		if dir := write(t, body); LiveAtHome(dir) {
			t.Errorf("malformed pid file %q was treated as live", body)
		}
	}
	if LiveAtHome(t.TempDir()) {
		t.Error("a home with no pid file was treated as live")
	}
}

func TestReadPidFileCarriesTheListenAddress(t *testing.T) {
	dir := t.TempDir()
	body := `{"pid":1,"startedAt":"2026-09-23T05:31:15.941Z","hostname":"devbox","uid":1000,"listen":"127.0.0.1:6767","heartbeat":true}`
	if err := os.WriteFile(filepath.Join(dir, PidFileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	p, ok := ReadPidFile(dir)
	if !ok {
		t.Fatal("well-formed pid file did not parse")
	}
	if p.Listen != "127.0.0.1:6767" || p.PID != 1 || p.Hostname != "devbox" {
		t.Fatalf("parsed %+v", p)
	}
}

// The regression CI caught: rung 5 used to emit `--host 127.0.0.1:6767`
// unconditionally when nothing was configured, which prepended a flag to EVERY
// paseo invocation on a box that had never asked for one. The hermetic e2e's
// fakepaseo dispatches on os.Args[1], so `--host` became the subcommand and 48
// checks failed — but the same breakage applies to any paseo-compatible
// wrapper that does not implement the flag.
//
// The guessing rungs must be ADDITIVE: they may only introduce a selector that
// is demonstrably better than passing nothing.
func TestGuessingRungsNeverAddAnUnprovenFlag(t *testing.T) {
	// Nothing anywhere: byte-for-byte the pre-ladder behavior.
	if got := Resolve(Target{Local: true, Live: never, Dial: never, Env: noEnv}); len(got.Args) != 0 {
		t.Fatalf("no daemon found, yet a selector was added: %v", got.Args)
	}
	// A live ~/.paseo is already paseo's default — evidence, but no flag.
	if got := Resolve(Target{Local: true, Live: always, Dial: never, Env: noEnv}); len(got.Args) != 0 {
		t.Fatalf("live default home added a redundant selector: %v", got.Args)
	}
	// Only a reachable non-default endpoint earns one.
	got := Resolve(Target{Local: true, Live: never, Dial: always, Env: noEnv})
	if !slices.Equal(got.Args, []string{"--host", DefaultServer}) {
		t.Fatalf("a reachable fallback endpoint should be used: %v", got.Args)
	}
}

// Rung 5 must not be taken on faith — it is the only rung that invents an
// address, so it has to prove something answers there first.
func TestRung5ProbesBeforeCommitting(t *testing.T) {
	var dialed []string
	spy := func(a string) bool { dialed = append(dialed, a); return false }
	Resolve(Target{Local: true, Live: never, Dial: spy, Env: noEnv})
	if !slices.Contains(dialed, DefaultServer) {
		t.Fatalf("the fallback endpoint was never probed, dialed=%v", dialed)
	}
}

// Answers must not hang a boot on a dead address.
func TestAnswersIsBoundedAndHonest(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback listener")
	}
	defer ln.Close()
	if !Answers(ln.Addr().String()) {
		t.Error("a real listener was reported as unreachable")
	}

	// A port nothing is on: must answer false, fast.
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback listener")
	}
	addr := closed.Addr().String()
	closed.Close()
	start := time.Now()
	if Answers(addr) {
		t.Errorf("a closed port %s was reported as reachable", addr)
	}
	if elapsed := time.Since(start); elapsed > 5*DialTimeout {
		t.Errorf("probe took %v, want <= %v — a dead address must not wedge boot", elapsed, 5*DialTimeout)
	}
}
