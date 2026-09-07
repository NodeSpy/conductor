package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/store"
)

// REGRESSION: the engine never referenced the secrets resolver — the
// dispatch audit wrote ref.Argv and err.Error() raw, and the command-output
// tail logs printed captured output verbatim. A tracked secret riding a
// dispatch argv, a paseo error, or command output landed cleartext in the
// audit log and the journal.
func TestEngineRedactsDispatchAuditAndOutputTails(t *testing.T) {
	const secret = "engine-s3cr3t-XYZZY"
	res := secrets.New()
	res.Track(secret)

	var logs []string
	logSpy := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }

	// A failing dispatch whose error and argv carry the secret.
	d := &fakeDispatcher{
		ref: dispatch.RunRef{Backend: "paseo", Argv: []string{"paseo", "run", "--token", secret}},
		err: fmt.Errorf("paseo run: exit 1: auth rejected for token %s", secret),
	}
	n := &fakeNotifier{}
	dir := t.TempDir()
	auditPath := dir + "/a.jsonl"
	st, err := store.Open(store.Options{StatePath: dir + "/s.json", AuditPath: auditPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := New(Options{
		Config: baseCfg(), Store: st, Dispatch: d, Notifier: n,
		Author:    dispatch.Author{Name: "Me"},
		UserToken: func() (string, error) { return "utok", nil },
		Secrets:   res, Log: logSpy,
	})
	act := config.Action{Type: "command", Command: []string{"true"}}
	e.process(context.Background(), agentTrigger("merge_conflict", "a/w", 1, "h1", "s1", act))

	raw, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("secret reached the dispatch audit: %s", raw)
	}
	var sawDispatch bool
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) == nil && e["event"] == "dispatch" {
			sawDispatch = true
			if !strings.Contains(fmt.Sprint(e["argv"]), secrets.Placeholder) ||
				!strings.Contains(fmt.Sprint(e["error"]), secrets.Placeholder) {
				t.Fatalf("argv/error must carry the placeholder: %v", e)
			}
		}
	}
	if !sawDispatch {
		t.Fatalf("no dispatch audit entry:\n%s", raw)
	}
	for _, l := range logs {
		if strings.Contains(l, secret) {
			t.Fatalf("secret reached the engine log: %s", l)
		}
	}

	// A successful command whose OUTPUT carries the secret: the tail log is
	// redacted too.
	logs = nil
	d2 := &fakeDispatcher{ref: dispatch.RunRef{Backend: "command", Output: "line1\ntoken=" + secret + "\n"}}
	e2 := New(Options{
		Config: baseCfg(), Store: tempStore(t), Dispatch: d2, Notifier: n,
		Author:    dispatch.Author{Name: "Me"},
		UserToken: func() (string, error) { return "utok", nil },
		Secrets:   res, Log: logSpy,
	})
	e2.process(context.Background(), agentTrigger("merge_conflict", "a/w", 2, "h2", "s2", act))
	var sawTail bool
	for _, l := range logs {
		if strings.Contains(l, secret) {
			t.Fatalf("secret reached the output-tail log: %s", l)
		}
		if strings.Contains(l, "command done") && strings.Contains(l, secrets.Placeholder) {
			sawTail = true
		}
	}
	if !sawTail {
		t.Fatalf("expected a redacted command tail log: %v", logs)
	}
}
