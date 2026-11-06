// Command seed records locally built connector plugins in a state dir's
// install state, exactly as an install from a release would: the binary's
// sha and the permission manifest from its own describe. The harness
// (../run.sh) uses it so a daemon resolves `use: github` / `use: smee` to the
// builds under test, offline.
//
// Usage: seed -state <state-dir> <name>=<binary> [<name>=<binary> …]
//
// NOT part of the shipped product; harness-only.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/plugin"
)

func main() {
	state := flag.String("state", "", "the daemon's state dir")
	flag.Parse()
	if *state == "" || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: seed -state <state-dir> <name>=<binary> …")
		os.Exit(2)
	}
	config.SetStateDir(*state)
	st := plugin.LoadInstallState(plugin.InstallDir())
	for _, arg := range flag.Args() {
		name, bin, ok := strings.Cut(arg, "=")
		if !ok {
			fail("bad argument %q", arg)
		}
		bin, _ = filepath.Abs(bin)
		sum := shaOf(bin)
		spec := plugin.Spec{Name: name, Kind: plugin.KindConnector, Provides: name, BinPath: bin, Sha256: sum}
		cl := plugin.NewClient(spec, plugin.Deps{Log: func(string, ...any) {}})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := cl.Start(ctx); err != nil {
			fail("%s: start: %v", name, err)
		}
		decl, err := cl.Describe(ctx)
		cancel()
		cl.Close()
		if err != nil {
			fail("%s: describe: %v", name, err)
		}
		st.Put(plugin.Installed{
			Key: "connectors/" + name, Kind: config.PluginKindConnector, Name: name, Use: name,
			Resolved: "connectors/" + name + "/v0.0.0-harness", Sha256: sum, Path: bin,
			Manifest: plugin.ManifestFromDecl(decl),
		})
		fmt.Printf("seeded %s -> %s (egress %v)\n", name, bin, decl.Capabilities.Egress)
	}
	if err := st.Save(); err != nil {
		fail("save: %v", err)
	}
}

func shaOf(p string) string {
	f, err := os.Open(p)
	if err != nil {
		fail("%v", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		fail("%v", err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "seed: "+format+"\n", a...)
	os.Exit(1)
}
