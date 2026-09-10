package controller

import "sync"

// runnerMemo caches a controller's engine-facing Runner.
//
// Every non-paseo Runner() built a FRESH controllerRunner per call, and a
// controllerRunner is where the live-agent tracking lives (its byPR map).
// A fresh one knows about nothing, so "is an agent already working this
// PR?" was always answered no on those transports — and a second event for
// an in-flight PR spawned a duplicate agent against the same worktree.
//
// One runner per controller, for the life of the controller, is what makes
// that question answerable at all.
type runnerMemo struct {
	once sync.Once
	val  Runner
}

// get builds the runner on first use and returns the same one thereafter.
func (m *runnerMemo) get(build func() Runner) Runner {
	m.once.Do(func() { m.val = build() })
	return m.val
}
