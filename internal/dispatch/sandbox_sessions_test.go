//go:build linux || darwin

package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/skill"
)

// The sandbox's session liveness reads a complete listing of paseo's live
// agents. paseo's default `ls` is filtered (the caller's directory, no
// background agents), so an agent only `ls -g --background` lists must keep
// its home; an archived one (only `ls -a`) must not.
func TestAnAgentOnlyTheGlobalListingShowsKeepsItsHome(t *testing.T) {
	dir := t.TempDir()
	listed := filepath.Join(dir, "listed") // the agent paseo still has, if any
	bin := filepath.Join(dir, "paseo")
	script := `#!/usr/bin/env bash
[ "$1" = ls ] || { echo '{}'; exit 0; }
g=; bg=; all=
for a in "$@"; do case "$a" in -g|--global) g=1;; --background) bg=1;; -a|--all) all=1;; esac; done
ids='"ag-here"'
other=$(cat "` + listed + `" 2>/dev/null)
if [ -n "$all" ]; then ids="$ids,\"ag-archived\""; fi
if [ -n "$other" ] && { [ -n "$all" ] || { [ -n "$g" ] && [ -n "$bg" ]; }; }; then ids="$ids,\"$other\""; fi
printf '[%s]\n' "$(echo "$ids" | sed 's/"\([^"]*\)"/{"id":"\1"}/g')"
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	set := func(id string) {
		if err := os.WriteFile(listed, []byte(id), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	set("ag-elsewhere")

	// The daemon's hold on a prepared home lapses quickly here; the launch
	// that held it has come and gone.
	oldGrace := agentsandbox.LaunchGrace
	agentsandbox.LaunchGrace = 20 * time.Millisecond
	t.Cleanup(func() { agentsandbox.LaunchGrace = oldGrace })

	d := &Dispatcher{PaseoBin: bin}
	d.RegisterSandboxSessions()
	r := &agentsandbox.Resolver{Registry: &agentsandbox.Registry{}, StateRoot: t.TempDir(), GOOS: "linux",
		RunLive: func(string) bool { return false }}
	home := func(ws, dispatch, agent string) string {
		e := agentsandbox.Entry{Workspace: ws, Identity: "agent:fixer@acme/app", Dispatch: dispatch}
		r.Registry.Register(e)
		p, err := r.PolicyForEntry(e, "claude", nil, skill.ProcessAnchor{})
		if err != nil {
			t.Fatal(err)
		}
		r.Registry.BindSession(ws, dispatch, agentsandbox.SessionRef{Backend: d.sandboxSessionBackend(), ID: agent})
		return p.Home
	}
	elsewhere := home(t.TempDir(), "r1:handoff", "ag-elsewhere")
	archived := home(t.TempDir(), "r2:handoff", "ag-archived")
	time.Sleep(10 * agentsandbox.LaunchGrace) // holds lapse; launches are past the grace

	r.SweepDispatchHomes()
	if _, err := os.Lstat(elsewhere); err != nil {
		t.Fatal("the home of an agent paseo still has (listed only globally, with background agents) was reclaimed")
	}
	if _, err := os.Lstat(archived); err == nil {
		t.Fatal("the home of an archived agent was kept")
	}
	set("") // now archived too
	r.SweepDispatchHomes()
	if _, err := os.Lstat(elsewhere); err == nil {
		t.Fatal("the home was kept after paseo stopped listing its agent")
	}
}

// A plugin backend's list_agents is paseo's filtered default listing, so it
// cannot say an agent is gone: liveness answers that it cannot tell (the
// sandbox keeps those homes, boundedly), never a partial set.
func TestAPluginBackendCannotJudgeAgentsGone(t *testing.T) {
	d := &Dispatcher{backendImpl: NewRPCBackend(nil, "paseo", nil, 0, 0)}
	if live, err := d.liveSandboxSessions(context.Background()); err == nil {
		t.Fatalf("a plugin backend answered %v", live)
	}
}
