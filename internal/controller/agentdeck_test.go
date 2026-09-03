package controller

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
)

// deckCall records one invocation of the agent-deck CLI.
type deckCall struct {
	dir  string
	env  []string
	args []string
}

// fakeDeck is a scriptable agent-deck CLI stub.
type fakeDeck struct {
	mu     sync.Mutex
	calls  []deckCall
	byArgs func(args []string) ([]byte, error)
}

func (f *fakeDeck) run(_ context.Context, dir string, env []string, _ string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, deckCall{dir: dir, env: env, args: args})
	f.mu.Unlock()
	if f.byArgs != nil {
		return f.byArgs(args)
	}
	return nil, nil
}

func (f *fakeDeck) call(i int) deckCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[i]
}

func newDeckFor(prov Provisioner, deck *fakeDeck) *agentDeckController {
	c := newAgentDeckController("deck", config.ControllerConfig{Type: "agent-deck"}, prov)
	c.run = deck.run
	c.pollInterval = time.Millisecond // don't slow the wait poll in tests
	return c
}

func TestAgentDeckLaunchTagsAndIdentity(t *testing.T) {
	deck := &fakeDeck{byArgs: func(args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "launch" {
			return []byte(`{"id":"deck-1"}`), nil
		}
		if len(args) > 1 && args[0] == "session" && args[1] == "show" {
			return []byte(`{"status":"idle"}`), nil
		}
		return nil, nil
	}}
	c := newDeckFor(&fakeProv{}, deck)

	sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "fix"), Cwd: "/wt/o-r-7"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID() != "deck-1" {
		t.Fatalf("session id = %q, want deck-1", sess.ID())
	}

	launch := deck.call(0)
	if launch.dir != "/wt/o-r-7" {
		t.Fatalf("launch cwd = %q, want the conductor worktree", launch.dir)
	}
	// PR identity is encoded in the title and group.
	if ti := argIndex(launch.args, "--title"); ti < 0 || !strings.Contains(launch.args[ti+1], "o/r") {
		t.Fatalf("launch --title should carry the PR identity: %v", launch.args)
	}
	if gi := argIndex(launch.args, "--group"); gi < 0 || launch.args[gi+1] != "o/r" {
		t.Fatalf("launch --group should be the repo: %v", launch.args)
	}
	// acts-as-user identity reaches the exec environment.
	if !envHas(launch.env, "GH_TOKEN", "utok") {
		t.Fatalf("launch env missing the user token: %v", launch.env)
	}
	if !envHas(launch.env, "GIT_AUTHOR_NAME", "Me") {
		t.Fatalf("launch env missing the git author: %v", launch.env)
	}
}

func TestAgentDeckFindByTitleFallback(t *testing.T) {
	deck := &fakeDeck{byArgs: func(args []string) ([]byte, error) {
		switch {
		case args[0] == "launch":
			return []byte("launched, see board\n"), nil // no id in output
		case args[0] == "list":
			return []byte(`[{"id":"found-9","title":"` + deckTitle(makeReq("merge_conflict", "")) + `"}]`), nil
		}
		return nil, nil
	}}
	c := newDeckFor(&fakeProv{}, deck)

	sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "fix"), Cwd: "/wt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID() != "found-9" {
		t.Fatalf("id should be resolved via list --json by title, got %q", sess.ID())
	}
}

func TestAgentDeckSendWaitAndClose(t *testing.T) {
	var showCalls int
	deck := &fakeDeck{byArgs: func(args []string) ([]byte, error) {
		switch {
		case args[0] == "launch":
			return []byte(`{"id":"deck-2"}`), nil
		case args[0] == "session" && args[1] == "show":
			showCalls++
			if showCalls < 2 {
				return []byte(`{"status":"running"}`), nil
			}
			return []byte(`{"status":"done"}`), nil
		}
		return nil, nil
	}}
	c := newDeckFor(&fakeProv{}, deck)

	sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "fix"), Cwd: "/wt"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Follow-up send.
	ch, err := sess.Prompt(context.Background(), Message{Text: "more"})
	if err != nil {
		t.Fatal(err)
	}
	<-ch

	// Wait polls session show until status is terminal.
	waitSession(t, sess)
	if showCalls < 2 {
		t.Fatalf("wait should poll show until done, polled %d times", showCalls)
	}

	if err := sess.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The last call is the remove.
	last := deck.call(len(deck.calls) - 1)
	if last.args[0] != "remove" || last.args[1] != "deck-2" {
		t.Fatalf("close should remove the session, got %v", last.args)
	}
}
