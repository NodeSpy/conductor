package controller

import (
	"context"
	"testing"
)

// §23: ResumeSession is handed only a session id — the Controller
// interface carries no Spec there — and it rooted the resumed server at ""
// (the daemon's own cwd) instead of the session's checkout, so a follow-up
// turn read and edited a different tree than the session had worked in.
func TestOpencodeResumeRootsAtTheSessionsWorktree(t *testing.T) {
	var dialedCwd []string
	c := &opencodeController{
		name: "oc",
		dial: func(_ context.Context, cwd string, _ []string) (string, func() error, error) {
			dialedCwd = append(dialedCwd, cwd)
			return "http://127.0.0.1:1", func() error { return nil }, nil
		},
	}
	c.rememberCwd("sess-1", "/wt/o-r-7")

	if _, err := c.ResumeSession(context.Background(), "sess-1", false, nil); err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	if len(dialedCwd) != 1 || dialedCwd[0] != "/wt/o-r-7" {
		t.Fatalf("resume must root at the session's worktree, dialed %v", dialedCwd)
	}
}

// An id we have never seen (a resume after a daemon restart) falls back to
// the previous behavior rather than inventing a path.
func TestOpencodeResumeUnknownSessionKeepsTheOldBehavior(t *testing.T) {
	var dialedCwd []string
	c := &opencodeController{
		name: "oc",
		dial: func(_ context.Context, cwd string, _ []string) (string, func() error, error) {
			dialedCwd = append(dialedCwd, cwd)
			return "http://127.0.0.1:1", func() error { return nil }, nil
		},
	}
	if _, err := c.ResumeSession(context.Background(), "never-seen", false, nil); err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	if len(dialedCwd) != 1 || dialedCwd[0] != "" {
		t.Fatalf("an unknown session should not invent a path, dialed %v", dialedCwd)
	}
}
