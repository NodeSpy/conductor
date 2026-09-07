package main

import (
	"fmt"
	"os"

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
