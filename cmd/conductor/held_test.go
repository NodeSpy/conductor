package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/plugin"
)

// heldSetup configures a third-party widget and records it in install state:
// installed at v1, with v2 held because it changes what the widget may do
// (update), or with nothing held (none). Both builds are on disk.
func heldSetup(t *testing.T, shape string) []string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	args := writeCfg(t, `
plugin_trust: { allow: ["github.com/acme/*"] }
connectors:
  widget: { use: acme/plugins/widget }
`)
	cfg, _, err := loadConfig(args)
	if err != nil {
		t.Fatal(err)
	}
	var ref config.PluginRef
	for _, r := range cfg.PluginRefs() {
		ref = r
	}
	bins := t.TempDir()
	bin := func(name string) string {
		p := filepath.Join(bins, name)
		if err := os.WriteFile(p, []byte(name), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	v1 := plugin.Installed{Key: ref.Use.InstallKey(), Kind: config.PluginKindConnector, Name: "widget", Source: ref.Source(),
		Resolved: "widget/v1.0.0", Sha256: "aaaa", Path: bin("v1"),
		Manifest: plugin.Manifest{Egress: []string{"api.widget.example:443"}}}
	v2 := v1
	v2.Resolved, v2.Sha256, v2.Path = "widget/v2.0.0", "bbbb", bin("v2")
	v2.Manifest = plugin.Manifest{Egress: []string{"uploads.widget.example:443"}}
	state := plugin.LoadInstallState(plugin.InstallDir())
	state.Put(v1)
	if shape == "update" {
		state.Pending = append(state.Pending, plugin.Pending{Installed: v2, Replaces: v1.Resolved,
			Widens: []string{"connects to uploads.widget.example:443"}, Removes: []string{"connects to api.widget.example:443"}})
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	return args
}

func countingHeldNotifier(t *testing.T) *[]string {
	var sent []string
	setHeldNotifier(func(msg string) { sent = append(sent, msg) })
	t.Cleanup(func() { setHeldNotifier(nil) })
	return &sent
}

// installedWidget is the widget build install state runs: the highest
// installed (the next reconcile prunes the one it replaced).
func installedWidget(t *testing.T) string {
	t.Helper()
	r, ok := plugin.LoadInstallState(plugin.InstallDir()).Get("connectors/widget")
	if !ok {
		t.Fatal("widget is not installed")
	}
	return r.Resolved
}

// A held update is announced once through the daemon's notify path, with
// the running and held versions and what it adds and removes: the next pass,
// or a restart (a fresh pass over the saved state), does not announce it
// again. `plugin list` and `status` show it until `plugin approve` applies
// it: the held build then replaces the running one.
func TestHeldUpdateIsAnnouncedOnceAndListedUntilApproved(t *testing.T) {
	args := heldSetup(t, "update")
	sent := countingHeldNotifier(t)

	announceHeld()
	if len(*sent) != 1 {
		t.Fatalf("one announcement, got %q", *sent)
	}
	for _, w := range []string{"plugin widget: update widget/v1.0.0 -> widget/v2.0.0 is waiting for your approval",
		"adds: connects to uploads.widget.example:443", "removes: connects to api.widget.example:443",
		"widget/v1.0.0 keeps running as it is", "dashboard", "conductor plugin approve widget@", "conductor plugin decline widget"} {
		if !strings.Contains((*sent)[0], w) {
			t.Fatalf("announcement lacks %q: %s", w, (*sent)[0])
		}
	}
	announceHeld()
	setHeldNotifier(nil)
	sent = countingHeldNotifier(t) // a restart: a new daemon over the saved state
	announceHeld()
	if len(*sent) != 0 {
		t.Fatalf("announced again: %q", *sent)
	}

	out, err := captureStdout(t, func() error { return cmdPluginList(args) })
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"update widget/v2.0.0 waiting for approval", "widget/v1.0.0 keeps running as it is",
		"+ connects to uploads.widget.example:443", "- connects to api.widget.example:443",
		"approve with: conductor plugin approve widget@", "keep widget/v1.0.0 with: conductor plugin decline widget"} {
		if !strings.Contains(out, w) {
			t.Fatalf("plugin list lacks %q:\n%s", w, out)
		}
	}
	out, err = captureStdout(t, func() error { return cmdStatus(args) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "plugin updates waiting for your approval (1)") {
		t.Fatalf("status lacks the held update:\n%s", out)
	}
	if got := installedWidget(t); got != "widget/v1.0.0" {
		t.Fatalf("held, %s must keep running; install state runs %s", "widget/v1.0.0", got)
	}

	if _, err := captureStdout(t, func() error { return cmdPluginApprove(append(args, "widget")) }); err != nil {
		t.Fatal(err)
	}
	if got := installedWidget(t); got != "widget/v2.0.0" {
		t.Fatalf("approved, the held build replaces the running one; install state runs %s", got)
	}
	out, err = captureStdout(t, func() error { return cmdPluginList(args) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "approval") {
		t.Fatalf("approved, it is no longer shown as waiting:\n%s", out)
	}
}

// `plugin decline` keeps the running build: nothing waits any more, and
// install state still runs it.
func TestDeclineKeepsTheRunningWidget(t *testing.T) {
	args := heldSetup(t, "update")
	out, err := captureStdout(t, func() error { return cmdPluginDecline(append(args, "widget")) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "widget/v1.0.0 keeps running") {
		t.Fatalf("%s", out)
	}
	if got := installedWidget(t); got != "widget/v1.0.0" {
		t.Fatalf("declined, install state runs %s", got)
	}
	if h := dashboardPluginHeld(); len(h) != 0 {
		t.Fatalf("declined, nothing waits: %+v", h)
	}
	sent := countingHeldNotifier(t)
	announceHeld()
	if len(*sent) != 0 {
		t.Fatalf("a declined update is not announced: %q", *sent)
	}
}

// With nothing held, nothing is announced or marked.
func TestNothingHeldAnnouncesNothing(t *testing.T) {
	args := heldSetup(t, "none")
	sent := countingHeldNotifier(t)
	announceHeld()
	if len(*sent) != 0 {
		t.Fatalf("%q", *sent)
	}
	out, err := captureStdout(t, func() error { return cmdPluginList(args) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "approval") {
		t.Fatalf("%s", out)
	}
}

// The dashboard approves or declines the update it showed (by its
// fingerprint), and refuses one that is not what waits.
func TestDashboardDecidesTheShownUpdate(t *testing.T) {
	for _, decision := range []string{"approve", "decline"} {
		t.Run(decision, func(t *testing.T) {
			heldSetup(t, "update")
			decide := map[string]func(string, string) (string, error){"approve": dashboardApprovePlugin, "decline": dashboardDeclinePlugin}[decision]
			held := dashboardPluginHeld()
			if len(held) != 1 || held[0].Running != "widget/v1.0.0" || held[0].Version != "widget/v2.0.0" || held[0].Fingerprint == "" ||
				len(held[0].Adds) != 1 || len(held[0].Removes) != 1 {
				t.Fatalf("held: %+v", held)
			}
			if _, err := decide("widget", "000000000000"); err == nil {
				t.Fatal("a fingerprint that is not what waits must be refused")
			}
			if got := installedWidget(t); got != "widget/v1.0.0" {
				t.Fatalf("refused, install state runs %s", got)
			}
			if _, err := decide("widget", held[0].Fingerprint); err != nil {
				t.Fatal(err)
			}
			if h := dashboardPluginHeld(); len(h) != 0 {
				t.Fatalf("decided, nothing waits: %+v", h)
			}
			want := map[string]string{"approve": "widget/v2.0.0", "decline": "widget/v1.0.0"}[decision]
			if got := installedWidget(t); got != want {
				t.Fatalf("%s: install state runs %s, want %s", decision, got, want)
			}
		})
	}
}

// localReleases is a release source on local disk: each tag publishes its
// own binary, with a checksums.txt listing it.
type localReleases map[string][]byte

func (l localReleases) ListTags(plugin.RemoteSource) ([]string, error) {
	var tags []string
	for t := range l {
		tags = append(tags, t)
	}
	return tags, nil
}

func (l localReleases) Download(rs plugin.RemoteSource, tag, file, destDir string) (string, error) {
	bin, ok := l[tag]
	if !ok {
		return "", fmt.Errorf("no release %s", tag)
	}
	p := filepath.Join(destDir, file)
	if file == "checksums.txt" {
		sum := sha256.Sum256(bin)
		return p, os.WriteFile(p, []byte(hex.EncodeToString(sum[:])+"  "+rs.AssetName()+"\n"), 0o644)
	}
	return p, os.WriteFile(p, bin, 0o755)
}

// `conductor plugin add` of a plugin already installed, whose newest release
// changes what it may do, holds that release like any update and prints it:
// the running and held versions, what it adds and removes, and how to
// approve or decline it. The installed build stays.
func TestPluginAddPrintsAHeldUpdate(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	args := writeCfg(t, "plugin_trust: { allow: [\"github.com/acme/*\"] }\nconnectors:\n  hooks: { use: webhook }\n")
	u, err := config.ParseUse(config.UseKindConnector, "acme/plugins/widget")
	if err != nil {
		t.Fatal(err)
	}
	v1, v2 := u.TagPrefix()+"v1.0.0", u.TagPrefix()+"v2.0.0"
	declares := map[string][]string{}
	oldAPI, oldDescribe := addReleaseAPI, addDescribe
	t.Cleanup(func() { addReleaseAPI, addDescribe = oldAPI, oldDescribe })
	addDescribe = func(*config.Config) plugin.DescribeFunc {
		return func(_ context.Context, s plugin.Spec) (*plugin.Decl, error) {
			b, err := os.ReadFile(s.BinPath)
			if err != nil {
				return nil, err
			}
			return &plugin.Decl{Kind: plugin.KindConnector, Type: "widget", Capabilities: plugin.Capabilities{Egress: declares[string(b)]}}, nil
		}
	}
	declares["v1"] = []string{"api.widget.example:443"}
	declares["v2"] = []string{"uploads.widget.example:443"}

	addReleaseAPI = localReleases{v1: []byte("v1")}
	if _, err := captureStdout(t, func() error { return cmdPluginAdd(append(args, "acme/plugins/widget")) }); err != nil {
		t.Fatal(err)
	}
	addReleaseAPI = localReleases{v1: []byte("v1"), v2: []byte("v2")}
	out, err := captureStdout(t, func() error { return cmdPluginAdd(append(args, "acme/plugins/widget")) })
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		"widget: update " + v1 + " -> " + v2 + " changes what it may do; " + v1 + " keeps running as it is until you approve it:",
		"+ connects to uploads.widget.example:443", "- connects to api.widget.example:443",
		"approve with: conductor plugin approve widget@", "keep " + v1 + " with: conductor plugin decline widget",
	} {
		if !strings.Contains(out, w) {
			t.Fatalf("plugin add lacks %q:\n%s", w, out)
		}
	}
	if got := installedWidget(t); got != v1 {
		t.Fatalf("held, %s keeps running; install state runs %s", v1, got)
	}
}
