package paseover

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Reaching a paseo daemon.
//
// A paseo CLI invocation is a fresh client process that must be told which
// daemon to talk to. It accepts two selectors, on every subcommand conductor
// uses (run/ls/inspect/provider/...):
//
//	--home <path>   read <path>/config.json, connect to the `listen` it names
//	--host <addr>   connect to that endpoint directly, no lookup
//
// So a home is only ever a POINTER to an address. Given neither selector the
// CLI targets ~/.paseo, and if no daemon is there it fails suggesting you
// START one — the worst available answer when a perfectly good daemon is
// running at another home.
//
// paseo does publish enough to find a live daemon, in <home>/paseo.pid:
//
//	{"pid":3001275,"listen":"127.0.0.1:6767","hostname":"devbox",...}
//
// but that record lives INSIDE the home, so it only helps once you already
// know the home. There is no machine-wide registry. Hence this ladder.

// DefaultHomeDir is where paseo looks when told nothing.
const DefaultHomeDir = "~/.paseo"

// DefaultServer is the address a paseo daemon listens on out of the box. It is
// the last rung: if ~/.paseo has no daemon, one at another home is very likely
// still on this port (paseo's own default `listen`), and reaching it beats
// failing while it sits there answering.
const DefaultServer = "127.0.0.1:6767"

// PidFileName is the daemon's liveness + endpoint record inside a home.
const PidFileName = "paseo.pid"

// PidFile is <home>/paseo.pid.
type PidFile struct {
	PID      int    `json:"pid"`
	Listen   string `json:"listen"`
	Hostname string `json:"hostname"`
	UID      int    `json:"uid"`
}

// Target is what a runtime declares about the daemon it wants.
type Target struct {
	// Server is an explicit endpoint (paseo `--host`). Highest priority.
	Server string
	// Home is an explicit daemon home (paseo `--home`).
	Home string
	// Local is false when the paseo CLI runs on ANOTHER box over SSH. This
	// box's PASEO_HOME, its ~/.paseo and its listening ports say nothing
	// about the far side, so a remote target uses only what it was told.
	Local bool
	// Live reports whether a home has a running daemon. nil = LiveAtHome.
	Live func(string) bool
	// Dial reports whether something answers at an address. nil = Answers.
	Dial func(string) bool
	// Env reads an environment variable. nil = os.Getenv.
	Env func(string) string
}

// Endpoint is a resolved way to reach a daemon.
type Endpoint struct {
	// Args is the argv selector prefix — ["--home", p], ["--host", a], or nil
	// when nothing could be determined (paseo falls back to its own default).
	Args []string
	// Source explains which rung produced it, for boot logs and errors.
	Source string
}

// Home returns the home this endpoint selects, or "" for a --host endpoint.
func (e Endpoint) Home() string {
	if len(e.Args) == 2 && e.Args[0] == "--home" {
		return e.Args[1]
	}
	return ""
}

// Server returns the endpoint address, or "" for a --home endpoint.
func (e Endpoint) Server() string {
	if len(e.Args) == 2 && e.Args[0] == "--host" {
		return e.Args[1]
	}
	return ""
}

// Label renders the endpoint for a log line ("home /p/h", "host 1.2.3.4:5").
func (e Endpoint) Label() string {
	if len(e.Args) != 2 {
		return "paseo's own default"
	}
	return strings.TrimPrefix(e.Args[0], "--") + " " + e.Args[1]
}

// Resolve picks the daemon to talk to:
//
//  1. an explicit `server:` — the operator named an endpoint, use it;
//  2. an explicit `home:` — the operator named a home, use it;
//  3. the ambient PASEO_HOME, for a local runtime;
//  4. the default ~/.paseo, IF a daemon is live there;
//  5. the default endpoint 127.0.0.1:6767, IF something answers there;
//  6. no selector at all — paseo's own default, and whatever it decides.
//
// Rungs 1–3 are DECLARED and never probed: an operator who names a target gets
// that target, because a daemon that is briefly down must not silently reroute
// work to a different one.
//
// Rungs 4 and 5 are GUESSES, so they are only taken on positive evidence and
// they only ever ADD a selector that demonstrably helps:
//
//   - rung 4 emits NO flag. ~/.paseo is already where paseo looks when told
//     nothing, so naming it changes nothing except the argv — and an argv that
//     grows a flag no one asked for breaks every paseo-compatible wrapper that
//     does not implement it. Evidence here buys a clear boot log, not a flag.
//   - rung 5 is the only rung that invents an address, so it must prove that
//     address answers before conductor commits every subsequent command to it.
//
// Rung 6 is the old behavior, unchanged: pass nothing, let paseo decide. That
// is what a box gets when no daemon can be found — the fallback never makes a
// working setup worse, it only rescues one that would otherwise fail.
//
// A remote target stops after rung 2 — this box cannot answer for that one.
func Resolve(t Target) Endpoint {
	if s := strings.TrimSpace(t.Server); s != "" {
		return Endpoint{Args: []string{"--host", s}, Source: "runtime server:"}
	}
	if h := strings.TrimSpace(t.Home); h != "" {
		return Endpoint{Args: []string{"--home", ExpandTilde(h)}, Source: "runtime home:"}
	}
	if !t.Local {
		return Endpoint{Source: "remote runtime with no server:/home: — paseo's own default on that box"}
	}
	env := t.Env
	if env == nil {
		env = os.Getenv
	}
	if h := strings.TrimSpace(env("PASEO_HOME")); h != "" {
		return Endpoint{Args: []string{"--home", ExpandTilde(h)}, Source: "PASEO_HOME"}
	}
	live := t.Live
	if live == nil {
		live = LiveAtHome
	}
	if live(ExpandTilde(DefaultHomeDir)) {
		return Endpoint{Source: "default home " + DefaultHomeDir + " (daemon running; paseo's own default already points there)"}
	}
	dial := t.Dial
	if dial == nil {
		dial = Answers
	}
	if dial(DefaultServer) {
		return Endpoint{Args: []string{"--host", DefaultServer},
			Source: "default endpoint " + DefaultServer + " (no daemon at " + DefaultHomeDir + ", but one answers here)"}
	}
	return Endpoint{Source: "no daemon found at " + DefaultHomeDir + " or " + DefaultServer + " — falling through to paseo's own default"}
}

// Answers reports whether something accepts a TCP connection at addr. It is
// the evidence rung 5 needs and nothing more: conductor is asking "is there a
// daemon here at all", not "is it healthy" (that is ProbeDaemon's job, which
// runs once at boot against whatever this resolves to).
//
// The timeout is deliberately short. This runs on a loopback address during
// startup and during cold model discovery; a box with nothing listening must
// pay milliseconds for the answer, not seconds.
func Answers(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, DialTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// DialTimeout bounds the rung-5 probe.
const DialTimeout = 300 * time.Millisecond

// ReadPidFile parses <home>/paseo.pid. Missing or malformed → ok false.
func ReadPidFile(home string) (PidFile, bool) {
	b, err := os.ReadFile(filepath.Join(ExpandTilde(home), PidFileName))
	if err != nil {
		return PidFile{}, false
	}
	var p PidFile
	if json.Unmarshal(b, &p) != nil || p.PID <= 0 {
		return PidFile{}, false
	}
	return p, true
}

// LiveAtHome reports whether a daemon is actually running for this home.
//
// The pid file alone is not the answer: a daemon killed with SIGKILL, or a box
// that lost power, leaves the file behind. Trusting a STALE pid file would
// route every dispatch at a home with nothing listening — the same silent
// misrouting this ladder exists to prevent — so the process is checked too.
func LiveAtHome(home string) bool {
	p, ok := ReadPidFile(home)
	if !ok {
		return false
	}
	return processAlive(p.PID)
}

// processAlive reports whether a pid names a live process. Signal 0 performs
// the existence and permission checks without delivering anything.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// EPERM means the process EXISTS and belongs to someone else — alive.
	err = proc.Signal(syscall.Signal(0))
	return err == nil || err == syscall.EPERM
}

// ExpandTilde expands a leading ~ or ~/ to the user's home directory.
func ExpandTilde(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}
