package main

import (
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// A paseo runtime's headless_mode reaches the dispatcher that serves it — a
// dedicated one here (own bin:, or a host:); the primary's comes from
// cfg.PrimaryHeadlessMode.
func TestHeadlessModeReachesDedicatedDispatchers(t *testing.T) {
	cfg := &config.Config{
		Hosts: map[string]config.HostConfig{"box": {Host: "box.example"}},
		Runtimes: map[string]config.RuntimeConfig{
			"paseo":  {Use: "paseo", Bin: "/usr/bin/paseo", Default: true},
			"remote": {Use: "paseo", Host: "box", HeadlessMode: &config.HeadlessMode{All: "none"}},
			"pinned": {Use: "paseo", Bin: "/opt/paseo", HeadlessMode: &config.HeadlessMode{Providers: map[string]string{"claude": "auto"}}},
		}}
	bin, _ := resolvePaseoBin(cfg)
	over, err := buildPaseoOverrides(cfg, bin, config.Retry{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if d := over["remote"]; d == nil || d.HeadlessMode.All != "none" {
		t.Fatalf("remote dispatcher headless mode = %+v", d)
	}
	if d := over["pinned"]; d == nil || d.HeadlessMode.Providers["claude"] != "auto" {
		t.Fatalf("pinned dispatcher headless mode = %+v", d)
	}
}
