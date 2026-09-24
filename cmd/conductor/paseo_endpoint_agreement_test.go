package main

import (
	"slices"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/models"
	"github.com/NodeSpy/conductor/internal/paseover"
)

// The #145 regression guard, stated structurally rather than as a reminder.
//
// Dispatch and model discovery each decide which paseo daemon to talk to.
// When those decisions were independent they drifted: config carried `home:`,
// dispatch passed `--home`, and the model lister quietly used paseo's default.
// The roster came back empty, every fleet "matched nothing available", every
// step degraded to a bare launch, and paseo rejected it with MISSING_PROVIDER.
//
// Both sides now project the same config onto paseover.Target and call the
// same paseover.Resolve. This asserts they agree across every shape of runtime
// — so a future field that reaches only one of them fails here.
func TestDispatchAndDiscoveryResolveTheSameDaemon(t *testing.T) {
	t.Setenv("PASEO_HOME", "/env/home")

	runtimes := map[string]config.RuntimeConfig{
		"explicit-home":   {Use: "paseo", Home: "/srv/paseo"},
		"explicit-server": {Use: "paseo", Server: "127.0.0.1:6767"},
		"server-and-home": {Use: "paseo", Server: "10.0.0.9:1234", Home: "/srv/paseo"},
		"tilde-home":      {Use: "paseo", Home: "~/paseo-home"},
		"nothing":         {Use: "paseo"},
		"remote":          {Use: "paseo", Host: "build-box"},
		"remote-home":     {Use: "paseo", Host: "build-box", Home: "/far/home"},
	}

	for name, rc := range runtimes {
		t.Run(name, func(t *testing.T) {
			// The dispatch side, as cmd/conductor builds it.
			dispatchEnd := paseoEndpointFor(paseoRuntimeDef{
				Name: name, Bin: rc.Bin, Home: rc.Home, Server: rc.Server, Host: rc.Host,
			})
			// The discovery side, as internal/models builds it.
			discoveryEnd := paseover.Resolve(models.Runtime{
				Name: name, Impl: "paseo", Bin: rc.Bin,
				Home: rc.Home, Server: rc.Server, Remote: rc.Host != "",
			}.PaseoTarget())

			if !slices.Equal(dispatchEnd.Args, discoveryEnd.Args) {
				t.Fatalf("dispatch and discovery disagree:\n dispatch:  %v\n discovery: %v",
					dispatchEnd.Args, discoveryEnd.Args)
			}
		})
	}
}
