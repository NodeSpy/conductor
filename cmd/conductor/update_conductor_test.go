package main

import (
	"context"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/notify"
)

// capturePublisher wires a notifier whose lifecycle publishes are recorded.
func capturePublisher() (*notify.Notifier, func() []string) {
	var mu sync.Mutex
	var events []string
	n := notify.New(config.Notify{}, func(string, ...any) {}, nil)
	n.SetPublisher(func(_ context.Context, event string, _ core.Trigger, line string, extra map[string]any) {
		mu.Lock()
		v, _ := extra["version"].(string)
		events = append(events, event+" "+v+" | "+line)
		mu.Unlock()
	})
	return n, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), events...)
	}
}

// TestApplyModeParsing: update.apply accepts true/false/workflow; the
// DEFAULT stays self-apply (a hard rule — unattended boxes must keep
// updating themselves).
func TestApplyModeParsing(t *testing.T) {
	parse := func(y string) config.Update {
		var u config.Update
		if err := yaml.Unmarshal([]byte(y), &u); err != nil {
			t.Fatalf("%s: %v", y, err)
		}
		return u
	}
	if u := parse("auto: true"); !u.ShouldApply() || u.ApplyWorkflow() {
		t.Fatal("default must self-apply")
	}
	if u := parse("auto: true\napply: true"); !u.ShouldApply() || u.ApplyWorkflow() {
		t.Fatal("apply: true must self-apply")
	}
	if u := parse("auto: true\napply: false"); u.ShouldApply() || u.ApplyWorkflow() {
		t.Fatal("apply: false must stage only")
	}
	if u := parse("auto: true\napply: workflow"); u.ShouldApply() || !u.ApplyWorkflow() {
		t.Fatal("apply: workflow must emit, not self-apply")
	}
	var u config.Update
	if err := yaml.Unmarshal([]byte("apply: sideways"), &u); err == nil ||
		!strings.Contains(err.Error(), "true, false, or workflow") {
		t.Fatalf("bad mode: %v", err)
	}
}

// TestHandleNewerReleaseModes: apply: true installs and applies (the
// unchanged default); apply: workflow emits conductor.update_available once
// per tag and never installs.
func TestHandleNewerReleaseModes(t *testing.T) {
	installs, applies := 0, 0
	origInstall, origApply := installRelease, applyRelease
	installRelease = func(force bool, tag string) (bool, string, error) {
		installs++
		return true, tag, nil
	}
	applyRelease = func(func()) { applies++ }
	t.Cleanup(func() { installRelease, applyRelease = origInstall, origApply })

	// Default (apply unset = true): install + apply, no emission.
	n, got := capturePublisher()
	_, applied := handleNewerRelease(context.Background(), config.Update{Auto: true}, "v9.9.9", "", n, nil)
	if !applied || installs != 1 || applies != 1 {
		t.Fatalf("default mode: applied=%v installs=%d applies=%d", applied, installs, applies)
	}
	if evs := got(); len(evs) != 0 {
		t.Fatalf("default mode must not emit: %v", evs)
	}

	// workflow: emit once per tag, install nothing.
	var wu config.Update
	if err := yaml.Unmarshal([]byte("auto: true\napply: workflow"), &wu); err != nil {
		t.Fatal(err)
	}
	announced, applied := handleNewerRelease(context.Background(), wu, "v9.9.9", "", n, nil)
	if applied || announced != "v9.9.9" {
		t.Fatalf("workflow mode: applied=%v announced=%q", applied, announced)
	}
	if installs != 1 || applies != 1 {
		t.Fatalf("workflow mode must not install/apply: installs=%d applies=%d", installs, applies)
	}
	evs := got()
	if len(evs) != 1 || !strings.HasPrefix(evs[0], "update_available v9.9.9") ||
		!strings.Contains(evs[0], "release v9.9.9 available") {
		t.Fatalf("emission: %v", evs)
	}
	// The same tag announces once.
	if a2, _ := handleNewerRelease(context.Background(), wu, "v9.9.9", announced, n, nil); a2 != "v9.9.9" {
		t.Fatalf("re-announce: %q", a2)
	}
	if evs := got(); len(evs) != 1 {
		t.Fatalf("same tag re-emitted: %v", evs)
	}
	// A NEWER tag announces again.
	if _, _ = handleNewerRelease(context.Background(), wu, "v9.9.10", "v9.9.9", n, nil); len(got()) != 2 {
		t.Fatalf("new tag not announced: %v", got())
	}
}

// TestEmitUpdatedOnBoot: the first boot of a new release publishes
// conductor.updated with the previous version; a same-version boot stays
// quiet.
func TestEmitUpdatedOnBoot(t *testing.T) {
	old := bootAnnounceDelay
	bootAnnounceDelay = 0
	t.Cleanup(func() { bootAnnounceDelay = old })
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Store.StateFile = dir + "/state.json"

	// First boot: no record → write it, no event.
	n, got := capturePublisher()
	emitUpdatedOnBoot(cfg, n)
	if evs := got(); len(evs) != 0 {
		t.Fatalf("first boot emitted: %v", evs)
	}
	// Same version: still quiet.
	emitUpdatedOnBoot(cfg, n)
	if evs := got(); len(evs) != 0 {
		t.Fatalf("same version emitted: %v", evs)
	}
	// Simulate a prior run on an older release.
	if err := writeLastVersion(cfg, "v0.0.1-old"); err != nil {
		t.Fatal(err)
	}
	emitUpdatedOnBoot(cfg, n)
	evs := got()
	if len(evs) != 1 || !strings.HasPrefix(evs[0], "updated "+version) ||
		!strings.Contains(evs[0], "v0.0.1-old → "+version) {
		t.Fatalf("updated event: %v", evs)
	}
}
