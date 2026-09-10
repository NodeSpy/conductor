package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/flow"
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

// TestSkillCLISubmitReviewRoundTrip drives `conductor call gh.submit_review`
// the whole way — agent CLI → IPC socket → broker → the REAL github connector →
// a stub GitHub server — with NO skill-level identity anywhere. It proves:
//   - the round trip works end to end and reaches the reviews endpoint;
//   - the inline `--comments` JSON array survives the CLI as a real list;
//   - with no `--as`, the review posts AS ME (the connector's write_token),
//     never a forced bot — i.e. identity now defaults to the connector default.
func TestSkillCLISubmitReviewRoundTrip(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 42})
	}))
	defer srv.Close()
	t.Setenv("PC_GITHUB_API_BASE", srv.URL)

	// A github connector that writes as "me" via a literal write_token, and a
	// profile granted ONLY gh.submit_review with no identity field at all.
	cfg := loadConfigDoc(t, `
connectors:
  gh:
    use: github
    identity: { write_token: me-sentinel }
steps:
  fixer:
    type: agent
    name: fixer
    model: x
    skill: { verbs: [gh.submit_review] }
`)
	stack, err := buildFlowStack(cfg, nil, nil, false) // dryRun=false → really invoke
	if err != nil || stack == nil {
		t.Fatalf("buildFlowStack: stack=%v err=%v", stack, err)
	}

	b := skill.NewBroker(func(string) (string, bool) { return "", false }, nil)
	tok, err := b.MintSession(skill.Identity{
		Agent:  "fixer",
		Policy: config.SkillPolicy{Verbs: []string{"gh.submit_review"}},
	}, uint32(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	asPeer := func(p memory.Peer) skill.Peer {
		return skill.Peer{PID: p.PID, StartTime: p.StartTime, UID: p.UID, Valid: p.Valid}
	}
	memory.SetLiveOps(memory.LiveOps{
		RunVerb: func(ctx context.Context, token, uses string, opts map[string]any, peer memory.Peer) (map[string]any, error) {
			id, aerr := b.Authorize(token, asPeer(peer))
			if aerr != nil {
				return nil, aerr
			}
			return stack.Runner.RunSkillVerb(ctx, flow.SkillIdentity{
				Agent: id.Agent, Verbs: id.Policy.Verbs,
			}, uses, opts)
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

	out := capture(t, func() error {
		return cmdCall([]string{"gh.submit_review",
			"--repo", "o/r", "--pr", "1", "--event", "APPROVE",
			"--body", "lgtm",
			"--comments", `[{"path":"a.go","line":1,"body":"nit"}]`,
		})
	})
	if !strings.Contains(out, "comments") {
		t.Fatalf("call result should report posted comments, got:\n%s", out)
	}

	if gotPath != "/repos/o/r/pulls/1/reviews" {
		t.Fatalf("reviews endpoint not hit; path = %q", gotPath)
	}
	// Posted as me (write_token), never a forced bot — the crux of the redesign.
	if gotAuth != "Bearer me-sentinel" {
		t.Fatalf("must post as me (write_token) by default; Authorization = %q", gotAuth)
	}
	if gotBody["event"] != "APPROVE" || gotBody["body"] != "lgtm" {
		t.Fatalf("review body not carried: %v", gotBody)
	}
	cs, ok := gotBody["comments"].([]any)
	if !ok || len(cs) != 1 {
		t.Fatalf("inline comments not carried through the CLI: %v", gotBody["comments"])
	}
	if c0, _ := cs[0].(map[string]any); c0["path"] != "a.go" || c0["body"] != "nit" {
		t.Fatalf("comment payload wrong: %v", cs[0])
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
