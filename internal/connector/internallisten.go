package connector

import (
	"fmt"
	"net"
	"sort"
	"sync"

	"github.com/NodeSpy/conductor/internal/config"
)

// A listener that is only ever reached through its exposure needs no address
// of the operator's: `expose: <conn>` with no `listen` makes the hop between
// the exposure and the listener conductor's own business. AssignListenAddrs
// picks that address before anything reads the connector's config, and
// writes it where the listener's declared `listen` field is — so the plugin
// listens there, its jail serves that port, the exposure is granted that
// port (ExposeTargets) and handed it (openListeners), all from the one
// value, exactly as if the operator had written it.
//
// The address is a loopback TCP port, not a unix socket: every listener and
// exposure the contract has spoken to so far binds and dials host:port, the
// jail's listen and expose_local plumbing is per loopback port (a jailed
// listener's port is served by conductor on the host and piped in; a jailed
// exposure is granted exactly the wired listener's 127.0.0.1 port, never one
// of conductor's own), and the macOS profile allows network by port. A
// socket would need a new contract on both ends and new releases of every
// plugin involved. What a loopback port does not do is hide the listener
// from other processes on this machine: it is as reachable as the
// 127.0.0.1:<port> an operator would have written, and the listener's own
// delivery check (a webhook secret) is what guards it, as it always has.

// internalListen remembers the address picked for each (instance, listen
// field) for the life of the process, so the next config load hands the
// listener the address it already has instead of moving it.
var (
	internalMu     sync.Mutex
	internalListen = map[string]string{}
)

// pickLoopbackAddr picks a free 127.0.0.1 port. A var so a test can make it
// fail.
var pickLoopbackAddr = func() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr, nil
}

// AssignListenAddrs gives every enabled connector listener whose expose field
// is set and whose listen field is not a loopback address of conductor's
// choosing, written into that connector's entry in cfg. When the listener's
// path field is unset too, the path its exposure delivers to (the declared
// default path, else "/") is written beside it, so a listener whose own
// default differs still receives what is relayed to it. An explicit listen
// or path is never touched. It returns the new connection config of every
// instance it changed.
//
// Call it once every connector type is registered (declFor) and before
// anything reads the connector configs: ExposeTargets, Build and the plugin
// manager's grants (plugin.Manager.SetConns).
func AssignListenAddrs(cfg *config.Config, logf func(string, ...any)) map[string]map[string]any {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	names := make([]string, 0, len(cfg.ConnectorsMap))
	for n := range cfg.ConnectorsMap {
		names = append(names, n)
	}
	sort.Strings(names)
	taken := explicitListenPorts(cfg, names)
	out := map[string]map[string]any{}
	for _, name := range names {
		ref := cfg.ConnectorsMap[name]
		if !ref.IsEnabled() {
			continue
		}
		d, _, ok := declFor(ref.TypeName(), name)
		if !ok {
			continue
		}
		changed := false
		for _, l := range listenersOf(d) {
			raw := ref.Conn()
			if stringAtPath(raw, l.Expose) == "" || stringAtPath(raw, l.Listen) != "" {
				continue
			}
			addr, err := internalAddr(name, l.Listen, taken)
			if err != nil {
				// Left unset: checkListenerExposures disables the connector,
				// naming the missing listen field.
				logf("connector %q: no local address for %s: %v", name, l.Listen, err)
				continue
			}
			next, err := ref.WithString(l.Listen, addr)
			if err == nil && l.Path != "" && stringAtPath(raw, l.Path) == "" {
				next, err = next.WithString(l.Path, resolveListenerPath(raw, l))
			}
			if err != nil {
				logf("connector %q: %s: %v", name, l.Listen, err)
				continue
			}
			ref, changed = next, true
		}
		if changed {
			cfg.ConnectorsMap[name] = ref
			out[name] = ref.Conn()
		}
	}
	return out
}

// internalAddr is the address for instance's listener at field: the one it
// was given before in this process, else a free loopback port no configured
// listener names.
func internalAddr(instance, field string, taken map[string]bool) (string, error) {
	internalMu.Lock()
	defer internalMu.Unlock()
	key := instance + "\x00" + field
	if a, ok := internalListen[key]; ok {
		return a, nil
	}
	for range 8 {
		a, err := pickLoopbackAddr()
		if err != nil {
			return "", err
		}
		_, port, _ := net.SplitHostPort(a)
		if taken[port] || pickedLocked(a) {
			continue
		}
		internalListen[key] = a
		return a, nil
	}
	return "", fmt.Errorf("no free loopback port")
}

func pickedLocked(addr string) bool {
	for _, a := range internalListen {
		if a == addr {
			return true
		}
	}
	return false
}

// explicitListenPorts are the ports the configured listeners name.
func explicitListenPorts(cfg *config.Config, names []string) map[string]bool {
	out := map[string]bool{}
	for _, name := range names {
		ref := cfg.ConnectorsMap[name]
		d, _, ok := declFor(ref.TypeName(), name)
		if !ok {
			continue
		}
		raw := ref.Conn()
		for _, l := range listenersOf(d) {
			if _, port, err := net.SplitHostPort(stringAtPath(raw, l.Listen)); err == nil {
				out[port] = true
			}
		}
	}
	return out
}
