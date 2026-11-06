package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// TestBoxHarness boots a real plugin the way the daemon does — the boot
// reconcile pass, then loadConnectorPlugins and connector.Build, jailed —
// against a box-shaped config and state dir, and invokes verbs on one
// instance. It is a manual harness for upgrade proofs, skipped unless
// BOX_CONFIG is set:
//
//	BOX_CONFIG   config file to load
//	BOX_STATE    XDG_STATE_HOME (the install state lives under it)
//	BOX_HOME     HOME for the run
//	BOX_INSTANCE connectors: entry to invoke
//	BOX_CALLS    JSON list of {"verb": …, "opts": {…}}
//	BOX_BIN_<n>  optional: install this binary for plugin <n> with a fresh
//	             record first; without it the state dir is booted as found
func TestBoxHarness(t *testing.T) {
	cfgPath := os.Getenv("BOX_CONFIG")
	if cfgPath == "" {
		t.Skip("manual harness: set BOX_CONFIG")
	}
	t.Setenv("XDG_STATE_HOME", os.Getenv("BOX_STATE"))
	t.Setenv("HOME", os.Getenv("BOX_HOME"))
	cfg, _, err := loadConfig([]string{"--config", cfgPath})
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	dir := plugin.InstallDir()
	state := plugin.LoadInstallState(dir)
	for _, ref := range cfg.PluginRefs() {
		bin := os.Getenv("BOX_BIN_" + ref.Name)
		if bin == "" {
			continue
		}
		key := ref.Use.InstallKey()
		dst := filepath.Join(plugin.BinDirForVersion(dir, key, "v9.9.9", ref.Source()), filepath.Base(bin))
		b, err := os.ReadFile(bin)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, b, 0o755); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		rec := plugin.Installed{Key: key, Kind: ref.Kind(), Name: ref.Name, Use: ref.Use.String(), Source: ref.Source(),
			Resolved: "v9.9.9", Sha256: hex.EncodeToString(sum[:]), Path: dst}
		decl, err := describeForInstall(cfg)(context.Background(), plugin.SpecFromRef(ref, "", rec, true))
		if err != nil {
			t.Fatalf("describe %s: %v", ref.Name, err)
		}
		rec.Manifest = plugin.ManifestFromDecl(decl)
		state.Put(rec)
		fmt.Printf("RECORDED %s: %s\n", ref.Name, rec.Manifest.Summary())
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	fmt.Printf("---- boot reconcile ----\n%s\n", captureLogf(t, func() { bootGapFill(cfg) }))
	sec := secrets.New()
	var mgr *plugin.Manager
	logged := captureLogf(t, func() { mgr, err = loadConnectorPlugins(cfg, sec, func(map[string]any) {}, nil) })
	fmt.Printf("---- load ----\n%s\n", logged)
	if err != nil {
		t.Fatalf("loadConnectorPlugins: %v", err)
	}
	defer mgr.Close()
	deps := connector.Deps{Secrets: sec, Log: func(f string, a ...any) { fmt.Printf("connlog: "+f+"\n", a...) }, Config: cfg, Auth: connector.NewAuthRegistry()}
	reg, err := connector.Build(cfg, deps)
	if err != nil {
		t.Fatalf("connector.Build: %v", err)
	}
	in, ok := reg.Get(os.Getenv("BOX_INSTANCE"))
	if !ok || in.DisabledReason != "" {
		t.Fatalf("instance: ok=%v disabled=%q", ok, in.DisabledReason)
	}
	var calls []struct {
		Verb string         `json:"verb"`
		Opts map[string]any `json:"opts"`
	}
	if err := json.Unmarshal([]byte(os.Getenv("BOX_CALLS")), &calls); err != nil {
		t.Fatal(err)
	}
	for _, c := range calls {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		var out map[string]any
		calllog := captureLogf(t, func() { out, err = in.Invoke(ctx, c.Verb, c.Opts) })
		cancel()
		js, _ := json.Marshal(out)
		if len(js) > 300 {
			js = append(js[:300], "..."...)
		}
		fmt.Printf("CALL %-18s err=%v\n     out=%s\n", c.Verb, err, js)
		if calllog != "" {
			fmt.Printf("     log: %s\n", calllog)
		}
		if err != nil {
			t.Errorf("%s: %v", c.Verb, err)
		}
	}
	fmt.Printf("---- install record after boot ----\n")
	for _, rec := range plugin.LoadInstallState(dir).AllVersions("connectors/" + os.Getenv("BOX_INSTANCE")) {
		fmt.Printf("%s schema=%d: %s\n", rec.Resolved, rec.ManifestSchema, rec.Manifest.Summary())
	}
}
