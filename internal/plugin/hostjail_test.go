package plugin

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// fakeProbe is a host for probeHostJail: what is installed, what the jail
// run does, which sysctls and container markers exist.
func fakeProbe(goos string, euid int, runErr error, files map[string]string) probeEnv {
	return probeEnv{
		goos: goos, euid: euid,
		lookPath: func(n string) (string, error) {
			if _, ok := files["bin:"+n]; ok {
				return "/usr/bin/" + n, nil
			}
			return "", errors.New("not found")
		},
		run: func([]string) ([]byte, error) {
			if runErr != nil {
				return []byte("unshare: unshare failed: Operation not permitted\n"), runErr
			}
			return nil, nil
		},
		readFile: func(p string) ([]byte, error) {
			if v, ok := files[p]; ok {
				return []byte(v), nil
			}
			return nil, os.ErrNotExist
		},
		stat: func(p string) (os.FileInfo, error) {
			if _, ok := files[p]; ok {
				return nil, nil
			}
			return nil, os.ErrNotExist
		},
		mkdirTemp: func() (string, error) { return "/scratch/jailprobe-1", nil },
	}
}

func TestProbeHostJailNamesWhyNot(t *testing.T) {
	failed := errors.New("exit status 1")
	unshare := map[string]string{"bin:unshare": ""}
	with := func(kv ...string) map[string]string {
		m := map[string]string{"bin:unshare": ""}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	for _, tc := range []struct {
		name   string
		env    probeEnv
		ok     bool
		reason string
		fix    string
	}{
		{"jail builds", fakeProbe("linux", 1000, nil, unshare), true, "", ""},
		{"root", fakeProbe("linux", 0, nil, unshare), false, "runs as root", "non-root user"},
		{"no unshare", fakeProbe("linux", 1000, nil, nil), false, "unshare", "util-linux"},
		{"apparmor", fakeProbe("linux", 1000, failed, with("/proc/sys/kernel/apparmor_restrict_unprivileged_userns", "1\n")), false, "AppArmor", "apparmor_restrict_unprivileged_userns=0"},
		{"debian knob", fakeProbe("linux", 1000, failed, with("/proc/sys/kernel/unprivileged_userns_clone", "0")), false, "unprivileged_userns_clone=0", "unprivileged_userns_clone=1"},
		{"max userns", fakeProbe("linux", 1000, failed, with("/proc/sys/user/max_user_namespaces", "0")), false, "max_user_namespaces=0", "max_user_namespaces"},
		{"docker", fakeProbe("linux", 1000, failed, with("/.dockerenv", "")), false, "container", "seccomp"},
		{"podman", fakeProbe("linux", 1000, failed, with("/run/.containerenv", "")), false, "container", "seccomp"},
		{"other", fakeProbe("linux", 1000, failed, unshare), false, "Operation not permitted", "user namespaces"},
		{"mac", fakeProbe("darwin", 501, nil, map[string]string{"bin:sandbox-exec": ""}), true, "", ""},
		{"mac without sandbox-exec", fakeProbe("darwin", 501, nil, nil), false, "sandbox-exec", "sandbox-exec"},
		{"freebsd", fakeProbe("freebsd", 1000, nil, unshare), false, "no plugin jail on freebsd", "Linux or macOS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := probeHostJail("/opt/conductor/conductor", tc.env)
			if h.OK != tc.ok {
				t.Fatalf("OK = %v, want %v (%+v)", h.OK, tc.ok, h)
			}
			if !strings.Contains(h.Reason, tc.reason) || !strings.Contains(h.Fix, tc.fix) {
				t.Fatalf("got %+v, want reason ~%q fix ~%q", h, tc.reason, tc.fix)
			}
		})
	}
}

// The probe builds the jail a plugin launch builds: conductor's own binary
// as the jail's entry and payload, the same namespaces, the minimal base.
func TestProbeHostJailRunsTheRealJail(t *testing.T) {
	var argv []string
	e := fakeProbe("linux", 1000, nil, map[string]string{"bin:unshare": ""})
	e.run = func(a []string) ([]byte, error) { argv = a; return nil, nil }
	probeHostJail("/opt/conductor/conductor", e)
	got := strings.Join(argv, " ")
	if strings.Contains(got, "--bind-ro /opt/conductor") {
		t.Fatalf("the probe binds conductor's own dir, which a plugin's jail never does: %q", got)
	}
	for _, want := range []string{"unshare --user --map-root-user", "--net", "/opt/conductor/conductor sandbox-net", `"path":"/scratch/jailprobe-1"`, `"noexec":true`, "--base minimal", "-- /proc/self/exe sandbox-net --noop"} {
		if !strings.Contains(got, want) {
			t.Fatalf("probe argv %q lacks %q", got, want)
		}
	}
}

func TestHostJailStatusDefaultsToCanJail(t *testing.T) {
	SetHostJailProbe(nil)
	if !HostJailStatus().OK {
		t.Fatal("no probe installed must mean the host can jail (every launch assumed so)")
	}
	calls := 0
	SetHostJailProbe(func() HostJail { calls++; return HostJail{Reason: "r", Fix: "f"} })
	defer SetHostJailProbe(nil)
	HostJailStatus()
	if HostJailStatus().OK || calls != 1 {
		t.Fatalf("probe must run once and stick: calls=%d", calls)
	}
}

var noJail = HostJail{Reason: "conductor runs as root", Fix: "run conductor as a non-root user"}

func asBeforeDeps(allow *[]string) SandboxDeps {
	return SandboxDeps{EgressAddr: func(a []string) (string, string, func(), error) {
		*allow = append([]string{}, a...)
		return "127.0.0.1:9", "cred", func() {}, nil
	}}
}

// On a host that cannot jail: a plugin installed before the jail runs as it
// did then (no jail, its declared egress at the proxy, config: destinations
// resolved to their configured hosts); one installed since is refused,
// naming why and the fix; trust: full runs unjailed either way.
func TestCannotJailHostDecision(t *testing.T) {
	defer SetHostJailForTests(noJail)()
	bin := filepath.Join(t.TempDir(), "conductor-widget")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := Manifest{Egress: []string{"config:base_url", "api.example.com"}, Env: []string{"WIDGET_TOKEN"}}
	s := Spec{Name: "widget", Kind: KindConnector, BinPath: bin, Manifest: m, PreJail: true,
		Conns: []map[string]any{{"base_url": "https://sonarr.lan:8989"}}, AllowEnv: []string{"WIDGET_TOKEN"}}
	t.Setenv("WIDGET_TOKEN", "t0k")

	var allow []string
	cmd, _, sandboxed, err := buildCommandIn(s, asBeforeDeps(&allow))
	if err != nil {
		t.Fatalf("a plugin from before the jail must run on a host that cannot jail: %v", err)
	}
	if sandboxed || cmd.Path != bin {
		t.Fatalf("want the plugin itself, unjailed; got %v (sandboxed=%v)", cmd.Args, sandboxed)
	}
	slices.Sort(allow)
	if strings.Join(allow, ",") != "api.example.com:443,sonarr.lan:8989" {
		t.Fatalf("proxy allowlist = %v: a config: destination must resolve to its configured host", allow)
	}
	if !slices.Contains(cmd.Env, "WIDGET_TOKEN=t0k") {
		t.Fatalf("allow_env'd declared variable not passed: %v", cmd.Env)
	}

	// A declaration made only of config: destinations whose fields are
	// unset resolves to none: as before, the connector gets no proxy (it
	// declared nothing it can reach), not a proxy that refuses everything.
	unset := s
	unset.Manifest = Manifest{Egress: []string{"config:base_url", "config:webhook.smee"}}
	unset.Conns = []map[string]any{{}}
	allow = nil
	cmd, _, _, err = buildCommandIn(unset, asBeforeDeps(&allow))
	if err != nil || allow != nil {
		t.Fatalf("unset config: destinations: err=%v allow=%v", err, allow)
	}
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "HTTPS_PROXY=") {
			t.Fatalf("no destination resolved, yet the plugin was pointed at the proxy: %v", cmd.Env)
		}
	}

	fresh := s
	fresh.PreJail = false
	_, _, _, err = buildCommandIn(fresh, asBeforeDeps(&allow))
	if err == nil {
		t.Fatal("a plugin installed since the jail ran unjailed on a host that cannot jail")
	}
	for _, want := range []string{"widget", noJail.Reason, noJail.Fix, "trust: full"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q lacks %q", err, want)
		}
	}

	local := fresh
	local.Local = true
	if _, _, sb, err := buildCommandIn(local, asBeforeDeps(&allow)); err != nil || sb {
		t.Fatalf("the operator's own local build must run as before: err=%v sandboxed=%v", err, sb)
	}

	trusted := fresh
	trusted.TrustFull = true
	if c, _, sb, err := buildCommandIn(trusted, SandboxDeps{}); err != nil || sb || c.Path != bin {
		t.Fatalf("trust: full: err=%v sandboxed=%v", err, sb)
	}
}

// A host that can jail jails a plugin from before the jail like any other:
// nothing about the jail is weakened there.
func TestCanJailHostJailsPreJailPlugins(t *testing.T) {
	defer SetHostJailForTests(HostJail{OK: true})()
	s := Spec{Name: "widget", Kind: KindConnector, BinPath: "/nonexistent/conductor-widget", PreJail: true, Local: true}
	_, _, _, err := buildCommandIn(s, SandboxDeps{})
	if err == nil || !strings.Contains(err.Error(), "jail") {
		t.Fatalf("want the jailed launch (which needs its wiring), got %v", err)
	}
	if _, before := runsAsBefore(s); before {
		t.Fatal("runsAsBefore on a host that can jail")
	}
}

func writeState(t *testing.T, dir, doc string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, installStateFile), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeAgedBin(t *testing.T, p string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// What marks a plugin as installed before the jail: a version 1 state file;
// a version 2 file a jail-era conductor wrote with the record still Legacy
// (it could not describe the build on a host that cannot jail — the broken
// boxes); a binary older than the jail. A plugin installed since is not
// marked. The mark is saved, and a new build of the plugin inherits it.
func TestPreJailMarker(t *testing.T) {
	dir := t.TempDir()
	old, now := jailEpoch.Add(-48*time.Hour), time.Now()
	binA, binB, binC := filepath.Join(dir, "a", "bin"), filepath.Join(dir, "b", "bin"), filepath.Join(dir, "c", "bin")
	writeAgedBin(t, binA, now)
	writeAgedBin(t, binB, old)
	writeAgedBin(t, binC, now)

	writeState(t, dir, "version: 1\nplugins:\n- {key: connectors/a, name: a, kind: connector, source: s, resolved: v1, sha256: aa, path: "+binA+"}\n")
	if st := LoadInstallState(dir); !st.Plugins[0].PreJail {
		t.Fatal("a record from a version 1 file is not marked")
	}

	writeState(t, dir, `version: 2
plugins:
- {key: connectors/a, name: a, kind: connector, source: s, resolved: v1, sha256: aa, path: `+binA+`, legacy: true}
- {key: connectors/b, name: b, kind: connector, source: s, resolved: v1, sha256: bb, path: `+binB+`, manifest_schema: 1}
- {key: connectors/c, name: c, kind: connector, source: s, resolved: v1, sha256: cc, path: `+binC+`, manifest_schema: 1}
`)
	st := LoadInstallState(dir)
	marked := map[string]bool{}
	for _, r := range st.Plugins {
		marked[r.Key] = r.PreJail
	}
	if !marked["connectors/a"] || !marked["connectors/b"] || marked["connectors/c"] {
		t.Fatalf("marks = %v; want a (legacy record in a rewritten file) and b (binary from before the jail), not c", marked)
	}

	// A new build of b, installed now, inherits the mark; the old build
	// and its binary go, and the mark stays through a save and reload.
	st.Put(Installed{Key: "connectors/b", Name: "b", Kind: "connector", Source: "s", Resolved: "v2", Sha256: "b2", Path: binC, ManifestSchema: manifestSchema})
	st.DeleteVersion("connectors/b", "v1")
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	os.Remove(binB)
	again := LoadInstallState(dir)
	for _, r := range again.Plugins {
		if r.Key == "connectors/b" && (!r.PreJail || r.Resolved != "v2") {
			t.Fatalf("the mark did not outlive the old build: %+v", r)
		}
	}
	// A different source under the same key is a different plugin.
	if again.preJail("connectors/b", "elsewhere") {
		t.Fatal("the mark crossed sources")
	}
}

// The describe a reconcile runs for a pre-jail plugin's new build carries the
// mark, so on a host that cannot jail it describes (and updates) as before
// instead of failing forever.
func TestReconcileDescribeCarriesPreJail(t *testing.T) {
	st := &InstallState{Plugins: []Installed{{Key: "connectors/b", Source: "s", PreJail: true}}}
	rec := Installed{Key: "connectors/b", Source: "s", PreJail: st.preJail("connectors/b", "s")}
	if !SpecFromRef(config.PluginRef{Name: "b"}, "", rec, true).PreJail {
		t.Fatal("SpecFromRef dropped PreJail")
	}
}

// On a host that can jail plugins, the real probe (conductor's own binary
// building a jail around `sandbox-net --noop`) says so.
func TestProbeHostJailOnThisHost(t *testing.T) {
	sd := testJailDeps(t)
	if h := ProbeHostJail(sd.Self); !h.OK {
		t.Fatalf("a host that jails plugins (testJailDeps) probed as unable: %+v", h)
	}
}

// A network: narrowing to the host an instance configures for a config:
// destination is within what the plugin declares; an unset field declares
// nothing, which narrows nothing (as a plugin declaring nothing always did).
func TestNetworkWithinConfigDestination(t *testing.T) {
	m := Manifest{Egress: []string{"config:base_url"}}
	conn := map[string]any{"base_url": "http://sonarr.lan:8989"}
	if err := CheckNetworkWithinManifest("tv", EgressFor("sonarr", m, conn), []string{"sonarr.lan:8989"}); err != nil {
		t.Fatalf("the configured host refused: %v", err)
	}
	if err := CheckNetworkWithinManifest("tv", EgressFor("sonarr", m, conn), []string{"evil.example:443"}); err == nil {
		t.Fatal("a host the plugin does not declare was accepted")
	}
	if got := EgressFor("sonarr", m, map[string]any{}); len(got) != 0 {
		t.Fatalf("unset field resolved to %v", got)
	}
}

// The pre-jail launch's PATH directory is conductor's own (the staging
// dir), never beside a binary the daemon's user may not write, holds the
// declared commands, and goes when the plugin exits.
func TestAsBeforePathDirIsStagingNotBinaryDir(t *testing.T) {
	defer SetHostJailForTests(noJail)()
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "conductor-widget")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(binDir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(binDir, 0o755)
	stage := t.TempDir()
	s := Spec{Name: "widget", Kind: KindConnector, BinPath: bin, PreJail: true, Manifest: Manifest{Commands: []string{"sh"}}}
	cmd, cleanup, _, err := buildCommandIn(s, SandboxDeps{StagingRoot: stage})
	if err != nil {
		t.Fatalf("launch with a read-only binary dir: %v", err)
	}
	var path string
	for _, kv := range cmd.Env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	if !strings.HasPrefix(path, filepath.Join(stage, "widget")+string(filepath.Separator)) {
		t.Fatalf("PATH %q is not in the staging dir", path)
	}
	if _, err := os.Lstat(filepath.Join(path, "sh")); err != nil {
		t.Fatalf("declared command not linked: %v", err)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("PATH dir left behind: %v", err)
	}
}
