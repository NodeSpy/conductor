// Command conductor-github is the REFERENCE build of the github connector
// plugin: the same handler (pkg/githubkit/ghplugin) the official
// conductor-plugins build serves, compiled from this tree so conductor's own
// tests — the parity suite and the e2e harness's plugin mode — drive the plugin
// through the real daemon path without reaching outside the repository.
package main

import (
	"fmt"
	"os"

	"github.com/NodeSpy/conductor/pkg/githubkit/ghplugin"
	"github.com/NodeSpy/conductor/pkg/plugin"
)

func main() {
	if err := plugin.Serve(ghplugin.New()); err != nil {
		fmt.Fprintln(os.Stderr, "conductor-github:", err)
		os.Exit(1)
	}
}
