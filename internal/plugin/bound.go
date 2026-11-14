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
	m    map[int]boundLaunch
}

type boundLaunch struct {
	plugin string
	paths  []string
}

func init() { sandbox.WriteGuard = boundOverlap }

// registerBound records what a jailed launch of plugin binds to run, until
// the returned func is called (the launch's cleanup).
func registerBound(plugin string, cmds []jailCommand) func() {
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
	if len(ps) == 0 {
		return func() {}
	}
	bound.Lock()
	defer bound.Unlock()
	if bound.m == nil {
		bound.m = map[int]boundLaunch{}
	}
	bound.next++
	id := bound.next
	bound.m[id] = boundLaunch{plugin: plugin, paths: ps}
	return func() {
		bound.Lock()
		defer bound.Unlock()
		delete(bound.m, id)
	}
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
