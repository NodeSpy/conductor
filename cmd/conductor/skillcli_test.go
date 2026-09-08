package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/skill"
)

// TestSkillCLIEndToEnd drives the agent-facing CLI (discover + call) through the
// real IPC socket and a real broker: a uid-bound session token minted by
// MintSession, authorized server-side by the calling process's uid. No paseo,
// no daemon boot — just the CLI ↔ socket ↔ broker path the dispatched agent uses.
func TestSkillCLIEndToEnd(t *testing.T) {
	b := skill.NewBroker(func(string) (string, bool) { return "s3kr1t-value", true }, nil)
	tok, err := b.MintSession(skill.Identity{
		Agent:  "fixer",
		Policy: config.SkillPolicy{Verbs: []string{"gh.*"}, SecretsVia: "broker", AllowSecrets: []string{"deploy_key"}},
	}, uint32(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	asPeer := func(p memory.Peer) skill.Peer {
		return skill.Peer{PID: p.PID, StartTime: p.StartTime, UID: p.UID, Valid: p.Valid}
	}
	memory.SetLiveOps(memory.LiveOps{
		SkillVerbs: func(token string, peer memory.Peer) ([]map[string]any, error) {
			if _, err := b.Authorize(token, asPeer(peer)); err != nil {
				return nil, err
			}
			return []map[string]any{{
				"uses": "gh.comment", "description": "Post a comment on a PR or issue.",
				"inputSchema": map[string]any{"type": "object",
					"properties": map[string]any{"body": map[string]any{"type": "string", "description": "the comment text"}},
					"required":   []any{"body"}},
			}}, nil
		},
		RunVerb: func(_ context.Context, token, uses string, opts map[string]any, peer memory.Peer) (map[string]any, error) {
			if _, err := b.Authorize(token, asPeer(peer)); err != nil {
				return nil, err
			}
			return map[string]any{"uses": uses, "posted": true, "body": opts["body"]}, nil
		},
	})
	t.Cleanup(func() { memory.SetLiveOps(memory.LiveOps{}) })

	sock := filepath.Join(t.TempDir(), "m.sock")
	l, err := memory.ListenSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go memory.ServeIPC(ctx, l, nil, func(map[string]any) {}, func(string, ...any) {})

	t.Setenv("CONDUCTOR_ENDPOINT", "unix://"+sock)
	t.Setenv("CONDUCTOR_SKILL_TOKEN", tok)

	// discover (no args) → the connectors this token can act through.
	if out := capture(t, func() error { return cmdDiscover(nil) }); !strings.Contains(out, "gh") {
		t.Fatalf("discover should list the gh connector, got:\n%s", out)
	}
	// discover a specific verb → its options.
	if out := capture(t, func() error { return cmdDiscover([]string{"gh.comment"}) }); !strings.Contains(out, "--body") || !strings.Contains(out, "required") {
		t.Fatalf("discover gh.comment should show the body option as required, got:\n%s", out)
	}
	// discover search.
	if out := capture(t, func() error { return cmdDiscover([]string{"-s", "comment"}) }); !strings.Contains(out, "gh.comment") {
		t.Fatalf("discover -s comment should find gh.comment, got:\n%s", out)
	}
	// call the verb → conductor runs it server-side and returns the result.
	if out := capture(t, func() error { return cmdCall([]string{"gh.comment", "--body", "looks good"}) }); !strings.Contains(out, `"posted": true`) || !strings.Contains(out, "looks good") {
		t.Fatalf("call gh.comment should return the server-side result, got:\n%s", out)
	}

	// A token presented from a DIFFERENT uid is refused end-to-end. We can't
	// change our real uid in-process, so exercise the broker directly with a
	// foreign-uid peer (the socket path's uid is covered by the calls above).
	if _, err := b.Authorize(tok, skill.Peer{PID: 9, UID: uint32(os.Getuid()) + 1, Valid: true}); err == nil {
		t.Fatal("a different uid must be refused")
	}
}

// capture swaps os.Stdout for a pipe, runs f, and returns what it printed.
func capture(t *testing.T, f func() error) string {
	t.Helper()
	orig := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := f()
	_ = w.Close()
	os.Stdout = orig
	out, _ := io.ReadAll(r)
	if err != nil {
		t.Fatalf("command error: %v\noutput:\n%s", err, out)
	}
	return string(out)
}
