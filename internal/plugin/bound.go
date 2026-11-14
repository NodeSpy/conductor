package plugin

import (
	"errors"
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

func init() {
	sandbox.WriteGuard = boundOverlap
	sandbox.ResolveWrite = pathForms
}

// bindHook runs inside bindLaunch's exclusive step (a test's hold).
var bindHook = func() {}

// bindLaunch checks that no path a launch was let write since its commands
// were checked overlaps what it binds to run, and registers what it binds
// (registerBound), as one step against sandbox.AdmitWrites: a launch
// admitted between the check and the registration could otherwise write
// what this one runs. peers is what the other plugins may write. Every
// path is resolved before the step, which only compares strings: a path
// that cannot be resolved (a hung network mount) holds up this launch, not
// every launch.
func bindLaunch(owner *Client, plugin string, cmds []jailCommand, peers func() []string) (func(), error) {
	ps := boundPaths(cmds)
	var ws []sandbox.Write
	if peers != nil {
		for _, w := range peers() {
			if filepath.IsAbs(w) {
				ws = append(ws, sandbox.Write{Path: w, Forms: pathForms(w)})
			}
		}
	}
	var unbind func()
	var hitW, hitP string
	err := sandbox.Exclusive(func() error {
		bindHook()
		for _, w := range append(ws, sandbox.GrantedForms()...) {
			if p := overlapping(w.Forms, ps); p != "" {
				hitW, hitP = w.Path, p
				return errBound
			}
		}
		unbind = registerBound(owner, plugin, ps)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("plugin %s: it runs %s, and a launch was let write %s since it was checked%s", plugin, hitP, hitW, ledgerNote(pathForms(hitW)))
	}
	return unbind, nil
}

var errBound = errors.New("bound")

// overlapping is the first of ps that one of forms lies inside, or holds,
// or "".
func overlapping(forms, ps []string) string {
	for _, wf := range forms {
		for _, p := range ps {
			if within(wf, p) || within(p, wf) {
				return p
			}
		}
	}
	return ""
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
func boundOverlap(writes []sandbox.Write) error {
	bound.Lock()
	defer bound.Unlock()
	for _, w := range writes {
		for _, b := range bound.m {
			if p := overlapping(w.Forms, b.paths); p != "" {
				return fmt.Errorf("it would be let write %s, and plugin %s runs %s from there: it could change what that plugin runs — narrow the write, or name the program elsewhere under commands:", w.Path, b.plugin, p)
			}
		}
	}
	return nil
}
