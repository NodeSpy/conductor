package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// clockBroker is a broker on an injectable clock, persisting to path.
func clockBroker(path string, clk *time.Time) *Broker {
	b := NewBroker(nil, nil)
	b.now = func() time.Time { return *clk }
	b.SetPersistPath(path)
	return b
}

var handoffPeer = Peer{UID: 1000, Valid: true}

func handoffID() Identity {
	return Identity{
		Agent: "review/handoff", Repo: "o/r", Trigger: "review_requested", Number: 7,
		Policy:   config.SkillPolicy{Verbs: []string{"gh.submit_review"}, MaxCalls: 5},
		Dispatch: "d-1", TargetTrusted: true,
		Context: map[string]any{"repo": "o/r"},
	}
}

// TestHandoffSessionSurvivesRestart: the token a waiting hand-off holds in its
// env must still authorize after the daemon restarts (a fresh broker loading
// the same file) — the failure was every auto-update killing it.
func TestHandoffSessionSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "handoff-sessions.json")
	clk := time.Unix(1_800_000_000, 0)
	b1 := clockBroker(path, &clk)
	tok, err := b1.MintHandoffSession(handoffID(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := b1.MintSession(handoffID(), 1000, SessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	if err := b1.ChargeVerbCall(tok, handoffPeer); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("hand-off session not persisted: %v", err)
	}
	if strings.Contains(string(raw), tok) || strings.Contains(string(raw), plain) {
		t.Fatal("a raw session token was written to disk")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("session file mode %v, want 0600", fi.Mode().Perm())
	}

	clk = clk.Add(time.Hour)
	b2 := clockBroker(path, &clk) // the restarted daemon
	id, err := b2.Authorize(tok, handoffPeer)
	if err != nil {
		t.Fatalf("hand-off token rejected after restart: %v", err)
	}
	want := handoffID()
	if id.Agent != want.Agent || id.Repo != want.Repo || id.Number != want.Number || id.Dispatch != want.Dispatch ||
		!id.TargetTrusted || len(id.Policy.Verbs) != 1 || id.Policy.Verbs[0] != "gh.submit_review" || id.Context["repo"] != "o/r" {
		t.Fatalf("restored identity differs: %+v", id)
	}
	// The verb-call cap carries over, so a restart doesn't reset it.
	for i := 0; i < 4; i++ {
		if err := b2.ChargeVerbCall(tok, handoffPeer); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if err := b2.ChargeVerbCall(tok, handoffPeer); err == nil {
		t.Fatal("verb-call cap was reset by the restart")
	}
	// The uid binding carries over too.
	if _, err := b2.Authorize(tok, Peer{UID: 1001, Valid: true}); err == nil {
		t.Fatal("restored session accepted a different uid")
	}
	// A non-hand-off dispatch token still dies with the daemon.
	if _, err := b2.Authorize(plain, handoffPeer); err == nil {
		t.Fatal("an ordinary dispatch token survived the restart")
	}
}

// TestHandoffSessionSlides: use keeps a hand-off session alive past its
// original TTL; idleness still expires it, and an expired session is not
// reloaded.
func TestHandoffSessionSlides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	clk := time.Unix(1_800_000_000, 0)
	b := clockBroker(path, &clk)
	tok, _ := b.MintHandoffSession(handoffID(), 1000)

	clk = clk.Add(HandoffSessionTTL - time.Hour)
	if _, err := b.Authorize(tok, handoffPeer); err != nil {
		t.Fatal(err)
	}
	clk = clk.Add(HandoffSessionTTL - time.Hour) // past the original expiry
	if _, err := b.Authorize(tok, handoffPeer); err != nil {
		t.Fatalf("a used hand-off session expired at its original TTL: %v", err)
	}
	// The slid expiry is what was persisted.
	clk = clk.Add(HandoffSessionTTL - time.Hour)
	if _, err := clockBroker(path, &clk).Authorize(tok, handoffPeer); err != nil {
		t.Fatalf("restart lost the slid expiry: %v", err)
	}
	clk = clk.Add(HandoffSessionTTL + time.Minute) // idle past HandoffSessionTTL since its last use
	if _, err := b.Authorize(tok, handoffPeer); err == nil {
		t.Fatal("an idle hand-off session never expired")
	}
	if _, err := clockBroker(path, &clk).Authorize(tok, handoffPeer); err == nil {
		t.Fatal("an expired session was reloaded")
	}
}

// TestRevokeDispatch: releasing a hand-off revokes its token at once, in
// memory and on disk (a restored-but-unused session included).
func TestRevokeDispatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	clk := time.Unix(1_800_000_000, 0)
	b1 := clockBroker(path, &clk)
	tok, _ := b1.MintHandoffSession(handoffID(), 1000)
	other := handoffID()
	other.Dispatch = "d-2"
	keep, _ := b1.MintHandoffSession(other, 1000)

	b2 := clockBroker(path, &clk) // restored, not yet presented
	b2.RevokeDispatch("d-1")
	if _, err := b2.Authorize(tok, handoffPeer); err == nil {
		t.Fatal("revoked token still authorizes")
	}
	if _, err := clockBroker(path, &clk).Authorize(tok, handoffPeer); err == nil {
		t.Fatal("revoked token came back after a restart")
	}
	if _, err := b2.Authorize(keep, handoffPeer); err != nil {
		t.Fatalf("revoking one dispatch killed another: %v", err)
	}
}

// TestRejectedTokenIsAudited: a refused token is visible to the operator — it
// used to fail silently on the daemon side.
func TestRejectedTokenIsAudited(t *testing.T) {
	var entries []map[string]any
	var logged []string
	b := NewBroker(nil, func(e map[string]any) { entries = append(entries, e) })
	b.SetLog(func(f string, a ...any) { logged = append(logged, f) })
	if _, err := b.Authorize("nope", handoffPeer); err == nil {
		t.Fatal("unknown token authorized")
	}
	if len(entries) != 1 || entries[0]["action"] != "deny" || entries[0]["reason"] != "unknown session token" {
		t.Fatalf("rejection not audited: %v", entries)
	}
	if len(logged) != 1 {
		t.Fatalf("rejection not logged: %v", logged)
	}
}
