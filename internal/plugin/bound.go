package plugin

import (
	"fmt"
	"path/filepath"
	"slices"
	"sync"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// What running jails have bound to run: each launch's declared programs
// and the directories they load from. A launch that would be let write
// over one of them — another plugin, a code step, an agent — could change
// what that jail runs, so it is refused (sandbox.WriteGuard). The other
// direction (a program found inside what a launch may already write) is
// grantChecks.programWritable's.
var bound struct {
	sync.Mutex
	next int
	m    map[any]boundLaunch
}

type boundLaunch struct {
	plugin string
	paths  []string
}

func init() { sandbox.WriteGuard = boundOverlap }

// bindLaunch checks that no path a launch was let write since its commands
// were checked overlaps what it binds to run, and registers what it binds
// (registerBound), as one step against sandbox.AdmitWrites: a launch
// admitted between the check and the registration could otherwise write
// what this one runs. writable is every path a launch may write now.
func bindLaunch(owner *Client, plugin string, cmds []jailCommand, writable func() []string) (func(), error) {
	var unbind func()
	err := sandbox.Exclusive(func() error {
		ps := boundPaths(cmds)
		for _, w := range writable() {
			if !filepath.IsAbs(w) {
				continue
			}
			for _, wf := range pathForms(w) {
				for _, p := range ps {
					if within(wf, p) || within(p, wf) {
						return fmt.Errorf("plugin %s: it runs %s, and a launch was let write %s since it was checked%s", plugin, p, w, ledgerNote(w))
					}
				}
			}
		}
		unbind = registerBound(owner, plugin, ps)
		return nil
	})
	return unbind, err
}

// boundPaths are every form of the paths cmds bind to run.
func boundPaths(cmds []jailCommand) []string {
	var ps []string
	for _, c := range cmds {
		for _, p := range append([]string{c.path, c.real}, c.reads...) {
			for _, f := range pathForms(p) {
				if !slices.Contains(ps, f) {
					ps = append(ps, f)
				}
			}
		}
	}
	return ps
}

// registerBound records paths as what a jailed launch of plugin binds to
// run. With no owner, until the returned func is called (the launch's
// cleanup). With an owner (the Client that launches it), until the owner's
// next launch replaces it or the owner is closed (releaseBound): the
// returned func does nothing, so a plugin that exits, restarts or is parked
// crash-looping keeps what it runs from being written meanwhile, and its
// relaunch is not then refused.
func registerBound(owner *Client, plugin string, paths []string) func() {
	bound.Lock()
	defer bound.Unlock()
	if bound.m == nil {
		bound.m = map[any]boundLaunch{}
	}
	var key any = owner
	if owner == nil {
		bound.next++
		key = bound.next
	}
	if len(paths) == 0 {
		delete(bound.m, key)
		return func() {}
	}
	bound.m[key] = boundLaunch{plugin: plugin, paths: paths}
	if owner != nil {
		return func() {}
	}
	return func() {
		bound.Lock()
		defer bound.Unlock()
		delete(bound.m, key)
	}
}

// releaseBound forgets what owner's launches bind (the Client is closed).
func releaseBound(owner *Client) {
	bound.Lock()
	defer bound.Unlock()
	delete(bound.m, owner)
}

// boundOverlap refuses writes that overlap (either inside the other) what
// a running jail has bound to run.
func boundOverlap(writes []string) error {
	bound.Lock()
	defer bound.Unlock()
	for _, w := range writes {
		if !filepath.IsAbs(w) {
			continue
		}
		for _, wf := range pathForms(w) {
			for _, b := range bound.m {
				for _, p := range b.paths {
					if within(wf, p) || within(p, wf) {
						return fmt.Errorf("it would be let write %s, and plugin %s runs %s from there: it could change what that plugin runs — narrow the write, or name the program elsewhere under commands:", w, b.plugin, p)
					}
				}
			}
		}
	}
	return nil
}
