package engine

import (
	"context"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/dispatch"
)

// archive_when_done has to close the session on the transport that OPENED it.
// Both archive sites called e.disp.Archive — the paseo dispatcher — no matter
// which controller actually ran the step, so on ACP/opencode/CLI the flag
// closed nothing and the agent's process stayed resident. Before the fix the
// archive lands on paseo and this test fails on both assertions.
func TestArchiveGoesToTheRunnerThatOpenedTheAgent(t *testing.T) {
	paseoArchives := make(chan string, 4)
	altArchives := make(chan string, 4)

	paseo := &fakeDispatcher{archived: paseoArchives}
	// Stands in for a non-paseo controller's runner (ACP, opencode, a CLI recipe).
	alt := &fakeDispatcher{archived: altArchives, onDispatch: func(dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{AgentID: "acp-session-1"}, nil
	}}

	e, _ := newEng(t, baseCfg(), paseo, &fakeNotifier{}, nil)

	ref, err := e.dispatchAgent(context.Background(), alt, dispatch.Request{})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := e.archiveAgent(context.Background(), ref.AgentID); err != nil {
		t.Fatalf("archive: %v", err)
	}

	select {
	case id := <-altArchives:
		if id != "acp-session-1" {
			t.Fatalf("archived wrong agent: %q", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the controller that opened the session never got the archive: " +
			"a non-paseo archive_when_done session leaks")
	}
	select {
	case id := <-paseoArchives:
		t.Fatalf("archive went to paseo (%q) instead of the owning controller", id)
	default:
	}
}

// An id with no recorded owner — a session adopted across a daemon restart —
// still archives through paseo, which is where the overwhelming majority of
// them live. The fallback is the pre-existing behaviour, deliberately kept.
func TestArchiveFallsBackToPaseoForAnUnknownAgent(t *testing.T) {
	paseoArchives := make(chan string, 4)
	e, _ := newEng(t, baseCfg(), &fakeDispatcher{archived: paseoArchives}, &fakeNotifier{}, nil)

	if err := e.archiveAgent(context.Background(), "adopted-42"); err != nil {
		t.Fatalf("archive: %v", err)
	}
	select {
	case id := <-paseoArchives:
		if id != "adopted-42" {
			t.Fatalf("archived wrong agent: %q", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an unowned agent id was not archived at all")
	}
}
