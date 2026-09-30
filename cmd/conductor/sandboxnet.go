package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// runSandboxNet is the hidden `conductor sandbox-net` entry point: the
// in-sandbox side of enforced egress (#36 §15). It runs INSIDE the
// namespace/container conductor created, brings loopback up, forwards
// 127.0.0.1:PORT → the egress proxy's unix socket, and execs the wrapped
// launch. Never invoked by operators directly.
func runSandboxNet(args []string) int {
	opt := sandbox.EnterOpts{}
	i := 0
	for ; i < len(args); i++ {
		switch {
		case args[i] == "--listen" && i+1 < len(args):
			opt.Listen = args[i+1]
			i++
		case args[i] == "--unix" && i+1 < len(args):
			opt.Unix = args[i+1]
			i++
		case args[i] == "--mask" && i+1 < len(args):
			opt.Masks = append(opt.Masks, args[i+1])
			i++
		case args[i] == "--bind" && i+1 < len(args):
			opt.Binds = append(opt.Binds, sandbox.BindMount{Path: args[i+1]})
			i++
		case args[i] == "--bind-ro" && i+1 < len(args):
			opt.Binds = append(opt.Binds, sandbox.BindMount{Path: args[i+1], RO: true})
			i++
		case args[i] == "--jail" && i+1 < len(args):
			var bs []sandbox.BindMount
			if err := json.Unmarshal([]byte(args[i+1]), &bs); err != nil {
				fmt.Fprintf(os.Stderr, "sandbox-net: --jail: %v\n", err)
				return 2
			}
			opt.Binds = append(opt.Binds, bs...)
			i++
		case args[i] == "--relay" && i+1 < len(args):
			l, u, ok := strings.Cut(args[i+1], "=")
			if !ok {
				fmt.Fprintf(os.Stderr, "sandbox-net: --relay wants LISTEN=SOCKET\n")
				return 2
			}
			opt.Relays = append(opt.Relays, sandbox.Relay{Listen: l, Unix: u})
			i++
		case args[i] == "--chdir" && i+1 < len(args):
			opt.Chdir = args[i+1]
			i++
		case args[i] == "--":
			opt.Argv = args[i+1:]
			i = len(args)
		default:
			fmt.Fprintf(os.Stderr, "sandbox-net: unknown flag %q\n", args[i])
			return 2
		}
	}
	return sandbox.RunEnter(opt)
}
