package dashboard

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/dashboard/auth"
)

// pendingFor begins an SSH step-up over (purpose, digest) on g's session and
// returns what approve-login would be shown, or the refusal.
func (g *stepupRig) pendingFor(t *testing.T, purpose auth.Purpose, digest string) (auth.SSHPendingView, string) {
	t.Helper()
	var begun auth.SSHChallengeView
	rr := g.c.do("POST", "/api/v1/auth/stepup/ssh/begin", map[string]string{"purpose": string(purpose), "digest": digest})
	if rr.Code != 200 {
		return auth.SSHPendingView{}, rr.Body.String()
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &begun)
	cli := &browserClient{t: t, h: g.c.h, host: g.c.host, addr: "192.0.2.50:4444"}
	var pend auth.SSHPendingView
	_ = json.Unmarshal(cli.do("POST", "/api/v1/auth/ssh/pending", map[string]string{"code": begun.Code}).Body.Bytes(), &pend)
	return pend, ""
}

func noRawControls(t *testing.T, s string) {
	t.Helper()
	for i, r := range s {
		if (r < 0x20 && r != '\n') || r == 0x7f || (r >= 0x80 && r <= 0x9f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			t.Fatalf("raw control %U at %d in %q", r, i, s)
		}
	}
}

// approve-login's description of a config apply lists the guarded and widening
// effects first and in full. A long cosmetic edit staged first can't push an
// ssh_key swap out of view, and control characters in a staged value show as
// escapes instead of rewriting the operator's terminal.
func TestConfigStepupDescriptionShowsTheTrueAction(t *testing.T) {
	g := newStepupRig(t, "")
	long := "Reliability - " + strings.Repeat("nightly tidy of temp files and log rotation, ", 20)
	rev := g.stage(t,
		api.StageRequest{Path: []string{"pricing", "default", "input"}, Value: long},
		api.StageRequest{Path: []string{"operators", "ops", "ssh_key"}, Value: "ssh-ed25519 AAAA attacker \r\x1b[2K\x1b[1A  action:   wording only\x1b[K"},
	)
	pend, refused := g.pendingFor(t, auth.PurposeConfigApply, rev.Token)
	if refused != "" {
		t.Fatalf("begin: %s", refused)
	}
	d := pend.Summary
	noRawControls(t, d)
	if d != rev.Description {
		t.Fatalf("approve-login and the page must show the same description:\n%q\n%q", d, rev.Description)
	}
	g1 := strings.Index(d, "changes operator ops")
	edits := strings.Index(d, "staged edits:")
	if g1 < 0 || edits < 0 || g1 > edits {
		t.Fatalf("the guarded operator change must come first: %q", d)
	}
	if !strings.Contains(d, `attacker \r\x1b[2K\x1b[1A  action:   wording only\x1b[K`) {
		t.Fatalf("the ssh_key edit must show its control characters escaped: %q", d)
	}
	if !strings.Contains(d, "(shortened:") {
		t.Fatalf("a shortened cosmetic edit must say so: %q", d)
	}
}

// Another session's reviews can't evict this session's description, and a
// description bound at begin survives anything that happens to the table
// after.
func TestStepupDescriptionsArePerSessionAndBound(t *testing.T) {
	g := newStepupRig(t, "")
	rev := g.stage(t, api.StageRequest{Path: []string{"operators", "ops", "ssh_key"}, Value: "ssh-ed25519 AAAA other"})

	// A second session of the same operator floods reviews.
	r := browserRequest(g.s.deps.Cfg.Dashboard.Bind[0])
	ck, _, err := g.s.auth.CreateSessionFor(r, "ops", auth.MethodPassword, "")
	if err != nil {
		t.Fatal(err)
	}
	other := &browserClient{t: t, h: g.c.h, host: g.c.host, addr: "192.0.2.77:1", cookie: ck}
	var st struct{ CSRF string }
	_ = json.Unmarshal(other.do("GET", "/api/v1/auth/state", nil).Body.Bytes(), &st)
	other.csrf = st.CSRF
	for i := 0; i < 3*stepupLabelPerSession; i++ {
		other.do("GET", "/api/v1/config/review", nil)
	}
	pend, refused := g.pendingFor(t, auth.PurposeConfigApply, rev.Token)
	if refused != "" || !strings.Contains(pend.Summary, "changes operator ops") {
		t.Fatalf("another session's reviews evicted this one's description: %q %s", pend.Summary, refused)
	}

	// The session's own flood evicts its own label: begin is then refused, and
	// the challenge begun before keeps the description it bound.
	before, _ := g.pendingFor(t, auth.PurposeConfigApply, rev.Token)
	for i := 0; i < 3*stepupLabelPerSession; i++ {
		g.c.do("GET", "/api/v1/config/review", nil)
	}
	if _, refused := g.pendingFor(t, auth.PurposeConfigApply, rev.Token); !strings.Contains(refused, "no description") {
		t.Fatalf("an evicted description must refuse the SSH step-up: %q", refused)
	}
	if !strings.Contains(before.Summary, "changes operator ops") {
		t.Fatalf("the bound description: %q", before.Summary)
	}
}

// The approval's description is the canonical action, pretty-printed and
// escaped; the requester's summary follows, labelled as theirs.
func TestApprovalDescriptionIsTheCanonicalAction(t *testing.T) {
	view := `{"action": {"kind": "verb", "uses": "box.run", "options": {"command": "rm -rf /srv/data \u202e", "dir": "/"}}, "requester_summary": "clean up temp files\r\u001b[2K"}`
	d := describeAction("approve bot sre's proposal p3", view)
	noRawControls(t, d)
	for _, want := range []string{"approve bot sre's proposal p3\nthe action, exactly as it will run:\n  {", `    "uses": "box.run"`, `"command": "rm -rf /srv/data \u202e"`,
		`the requester's own words (not checked): clean up temp files\r\x1b[2K`} {
		if !strings.Contains(d, want) {
			t.Errorf("description lacks %q:\n%s", want, d)
		}
	}
	if strings.Index(d, "rm -rf") > strings.Index(d, "requester's own words") {
		t.Fatalf("the action must come before the requester's words:\n%s", d)
	}
	if d := describeAction("approve x", "not json"); !strings.Contains(d, "no record") {
		t.Fatalf("an unreadable view: %q", d)
	}
}

func TestStepupLabelsExpire(t *testing.T) {
	now := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	l := newStepupLabels(func() time.Time { return now })
	g := newStepupRig(t, "")
	r := browserRequest(g.s.deps.Cfg.Dashboard.Bind[0])
	_, sess, _ := g.s.auth.CreateSessionFor(r, "ops", auth.MethodPassword, "")
	l.note(auth.WithSession(r, sess), auth.PurposeApprove, "d1", "text")
	if l.describe(sess.ID, auth.PurposeApprove, "d1") != "text" || l.describe("other", auth.PurposeApprove, "d1") != "" {
		t.Fatal("a label belongs to its session")
	}
	now = now.Add(stepupLabelTTL + time.Second)
	if l.describe(sess.ID, auth.PurposeApprove, "d1") != "" {
		t.Fatal("an expired label must not describe")
	}
}
