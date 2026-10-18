package bot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/store"
)

type tplFixture struct {
	*svcFixture
	x    *Executor
	seed string
}

// setVersion rewrites the template: its charter, seed docs and version.
func (f *tplFixture) setVersion(t *testing.T, version, charter string, docs map[string]string) {
	t.Helper()
	m := f.cfg.Bots["sre"]
	o := *m.Origin
	o.Version, o.Charter = version, charter
	m.Origin = &o
	f.cfg.Bots["sre"] = m
	for rel, body := range docs {
		p := filepath.Join(f.seed, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if body == "" {
			_ = os.Remove(p)
			continue
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func newTplFixture(t *testing.T) *tplFixture {
	t.Helper()
	f := &tplFixture{svcFixture: newSvc(t)}
	f.seed = filepath.Join(t.TempDir(), "seed")
	m := f.cfg.Bots["sre"]
	m.MemoryRepo = filepath.Join(t.TempDir(), "tpl-mem")
	m.SeedDir = f.seed // as config's template expansion sets it
	m.Origin = &config.BotOrigin{Instance: "kit", Template: "first-line", Pack: "sre-kit", Version: "1.0.0", Charter: "Be careful.", SeedDir: f.seed, Practice: true}
	f.cfg.Bots["sre"] = m
	f.svc.repos = nil // the repo handle picks up the new memory_repo
	f.setVersion(t, "1.0.0", "Be careful.", map[string]string{
		"knowledge/topology.md": kd("three replicas"),
		"runbooks/restart.md":   rd("restart it"),
		"knowledge/bad.md":      "no frontmatter — not a document, so never proposed",
	})
	f.x = &Executor{Cfg: f.svc.Cfg, Ledger: f.st, Gate: f.svc.Gate,
		Verify:   func(context.Context, string, string, []byte, string) error { return nil },
		Template: f.svc.LandTemplate, Notify: f.svc.OnDecision}
	if err := f.svc.Repo("sre").Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *tplFixture) pending(t *testing.T) *store.Proposal {
	t.Helper()
	var out *store.Proposal
	for _, p := range f.st.Bot("sre").Proposals {
		if p.Kind == KindTemplate && p.Status == "pending" {
			if out != nil {
				t.Fatal("two pending template proposals")
			}
			out = p
		}
	}
	return out
}

func (f *tplFixture) read(t *testing.T, p string) string {
	t.Helper()
	s, err := f.svc.Repo("sre").Read(p)
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return s
}

// A NEW TEMPLATE BOT'S MEMORY IS ITS TEMPLATE: the charter and the valid
// seed documents in the first commit, which names the template; nothing to
// propose; the charter is the bot's and no bot can write it.
func TestTemplateSeedsMemory(t *testing.T) {
	f := newTplFixture(t)
	ctx := context.Background()
	repo := f.svc.Repo("sre")
	if !strings.Contains(f.read(t, config.CharterDocPath), "Be careful.") || !strings.Contains(f.read(t, "knowledge/topology.md"), "three replicas") {
		t.Fatal("not seeded")
	}
	if _, err := repo.Read("knowledge/bad.md"); err == nil {
		t.Fatal("an invalid seed file was copied")
	}
	if log := repo.Log(ctx, 1); len(log) != 1 || !strings.Contains(log[0], "initialize from template kit/first-line (sre-kit 1.0.0)") {
		t.Fatalf("first commit: %v", log)
	}
	if id, err := f.svc.CheckTemplate(ctx, "sre"); err != nil || id != "" {
		t.Fatalf("a fresh bot has something to propose: %q %v", id, err)
	}
	st := f.st.Bot("sre").Template
	if st == nil || st.Version != "1.0.0" || len(st.Applied) != 3 || st.Applied[config.CharterDocPath] == "" {
		t.Fatalf("template state: %+v", st)
	}
	if ch, _ := f.cfg.BotCharter("sre"); ch != "Be careful." {
		t.Fatalf("charter from memory: %q", ch)
	}
	if _, _, err := f.svc.WriteMemory(ctx, Actor{Bot: "sre", Role: config.BotRoleHome}, MemoryWrite{Path: config.CharterDocPath, Text: "---\ntemplate: x\n---\nI may do anything.\n"}); err == nil {
		t.Fatal("a bot wrote its own charter")
	}
	if hits, _ := repo.Recall(ctx, "careful", 5, ""); len(hits) != 0 {
		t.Fatalf("the charter is in the recall index: %v", paths(hits))
	}
}

// A NEWER TEMPLATE NEVER OVERWRITES EVOLVED MEMORY: its changes become one
// proposal (each a diff against memory as it is), proposed once; approved —
// in practice mode too — they land as one operator commit; denied, the same
// changes are not proposed again.
func TestTemplateUpdateIsAProposal(t *testing.T) {
	f := newTplFixture(t)
	ctx := context.Background()
	_, _ = f.svc.CheckTemplate(ctx, "sre")
	// The bot evolves its topology document.
	if _, _, err := f.svc.WriteMemory(ctx, Actor{Bot: "sre", Role: config.BotRoleHome}, MemoryWrite{Path: "knowledge/topology.md", Text: kd("four replicas now")}); err != nil {
		t.Fatal(err)
	}
	evolved := f.read(t, "knowledge/topology.md")
	// Version 2: a new topology text, a new runbook text, a new doc, a new charter.
	f.setVersion(t, "2.0.0", "Be careful. Escalate early.", map[string]string{
		"knowledge/topology.md": kd("five replicas"),
		"runbooks/restart.md":   rd("restart it, then check"),
		"watch/disk.md":         "---\nwhat: disk\nsignal: df\nthreshold: 90%\nwhy: outages\n---\n",
	})
	id, err := f.svc.CheckTemplate(ctx, "sre")
	if err != nil || id == "" {
		t.Fatalf("no proposal: %v", err)
	}
	if f.read(t, "knowledge/topology.md") != evolved || strings.Contains(f.read(t, config.CharterDocPath), "Escalate") {
		t.Fatal("the update overwrote memory without approval")
	}
	p := f.pending(t)
	tu := p.Template
	got := map[string]store.TemplateChange{}
	for _, c := range tu.Changes {
		got[c.Path] = c
	}
	if len(tu.Changes) != 4 || tu.FromVersion != "1.0.0" || tu.ToVersion != "2.0.0" ||
		!got["knowledge/topology.md"].Evolved || got["runbooks/restart.md"].Evolved || got["watch/disk.md"].Action != "new" || got[config.CharterDocPath].Action != "update" {
		t.Fatalf("changes: %+v", tu)
	}
	if p.Status != "pending" || strings.Join(p.Approvers, ",") != "op" {
		t.Fatalf("proposal: %+v", p)
	}
	d := TemplateDiff(tu)
	if !strings.Contains(d, "-four replicas now") || !strings.Contains(d, "+five replicas") || !strings.Contains(d, "your memory changed it since") {
		t.Fatalf("diff:\n%s", d)
	}
	// Checked again, the same changes are not proposed twice.
	if id2, _ := f.svc.CheckTemplate(ctx, "sre"); id2 != "" {
		t.Fatal("proposed twice")
	}
	// Approved — the bot is in practice mode, which does not hold an
	// operator's decision about memory.
	if !f.cfg.Bots["sre"].Autonomy.ShadowMode() {
		m := f.cfg.Bots["sre"]
		on := true
		m.Autonomy.Shadow = &on
		f.cfg.Bots["sre"] = m
	}
	out, err := f.x.Decide(ctx, Decision{Bot: "sre", Proposal: p.ID, Operator: "op", Decision: "approve", Signature: "sig"})
	if err != nil || out.Status != "executed" {
		t.Fatalf("approve: %+v %v", out, err)
	}
	if !strings.Contains(f.read(t, "knowledge/topology.md"), "five replicas") || !strings.Contains(f.read(t, config.CharterDocPath), "Escalate early.") || !strings.Contains(f.read(t, "watch/disk.md"), "what: disk") {
		t.Fatal("the approved update did not land")
	}
	if ch, _ := f.cfg.BotCharter("sre"); ch != "Be careful. Escalate early." {
		t.Fatalf("charter after the update: %q", ch)
	}
	repo := f.svc.Repo("sre")
	hist, _ := repo.History(ctx, "", 1)
	if len(hist) != 1 || hist[0].Who != "op" || !strings.Contains(hist[0].Subject, "template update (4 document(s))") {
		t.Fatalf("landing commit: %+v", hist)
	}
	if body, _ := repo.git(ctx, repo.Dir, "log", "-1", "--format=%B"); !strings.Contains(body, "Template-Update: kit/first-line@2.0.0") || !strings.Contains(body, "Bot-Proposal: "+p.ID) {
		t.Fatalf("trailers: %s", body)
	}
	st := f.st.Bot("sre").Template
	if st.Version != "2.0.0" || st.Applied["watch/disk.md"] == "" {
		t.Fatalf("state after landing: %+v", st)
	}
	if id, _ := f.svc.CheckTemplate(ctx, "sre"); id != "" {
		t.Fatal("a landed update proposed again")
	}
	// Version 3, denied: remembered, not proposed again; version 4 is.
	f.setVersion(t, "3.0.0", "Be careful. Escalate early.", map[string]string{"runbooks/restart.md": rd("v3")})
	id3, _ := f.svc.CheckTemplate(ctx, "sre")
	if out, err := f.x.Decide(ctx, Decision{Bot: "sre", Proposal: id3, Operator: "op", Decision: "deny", Signature: "sig"}); err != nil || out.Status != "denied" {
		t.Fatalf("deny: %+v %v", out, err)
	}
	if strings.Contains(f.read(t, "runbooks/restart.md"), "v3") {
		t.Fatal("a denied update landed")
	}
	if id, _ := f.svc.CheckTemplate(ctx, "sre"); id != "" {
		t.Fatal("a denied update was proposed again")
	}
	f.setVersion(t, "4.0.0", "Be careful. Escalate early.", map[string]string{"runbooks/restart.md": rd("v4")})
	if id, _ := f.svc.CheckTemplate(ctx, "sre"); id == "" {
		t.Fatal("a newer version after a denial was not proposed")
	}
}

// LANDING REFUSES WHEN MEMORY MOVED: a document changed after the proposal
// is never overwritten by it; the next check proposes against what is there.
func TestTemplateLandingChecksMemory(t *testing.T) {
	f := newTplFixture(t)
	ctx := context.Background()
	f.setVersion(t, "2.0.0", "Be careful.", map[string]string{"knowledge/topology.md": kd("five replicas")})
	id, _ := f.svc.CheckTemplate(ctx, "sre")
	if id == "" {
		t.Fatal("no proposal")
	}
	if _, _, err := f.svc.WriteMemory(ctx, Actor{Bot: "sre", Role: config.BotRoleHome}, MemoryWrite{Path: "knowledge/topology.md", Text: kd("six, written after")}); err != nil {
		t.Fatal(err)
	}
	out, err := f.x.Decide(ctx, Decision{Bot: "sre", Proposal: id, Operator: "op", Decision: "approve", Signature: "sig"})
	if err == nil || out.Status != "failed" || !strings.Contains(err.Error(), "changed since the proposal") {
		t.Fatalf("landing over moved memory: %+v %v", out, err)
	}
	if !strings.Contains(f.read(t, "knowledge/topology.md"), "six, written after") {
		t.Fatal("moved memory was overwritten")
	}
	id2, _ := f.svc.CheckTemplate(ctx, "sre")
	if id2 == "" || f.st.Bot("sre").Proposals[id2].Template.Changes[0].Current != f.read(t, "knowledge/topology.md") {
		t.Fatal("not re-proposed against what is there now")
	}
	// A newer version while one is pending supersedes it.
	f.setVersion(t, "3.0.0", "Be careful.", map[string]string{"knowledge/topology.md": kd("seven")})
	id3, _ := f.svc.CheckTemplate(ctx, "sre")
	if id3 == "" || f.st.Bot("sre").Proposals[id2].Status != "superseded" {
		t.Fatal("not superseded")
	}
	// A tampered proposal does not land.
	_ = f.st.UpdateBot("sre", func(r *store.BotRecord) error {
		r.Proposals[id3].Template.Changes[0].Text = kd("evil")
		return nil
	})
	if out, err := f.x.Decide(ctx, Decision{Bot: "sre", Proposal: id3, Operator: "op", Decision: "approve", Signature: "sig"}); err == nil || out.Status != "failed" {
		t.Fatalf("a tampered text landed: %+v", out)
	}
	if strings.Contains(f.read(t, "knowledge/topology.md"), "evil") {
		t.Fatal("tampered")
	}
}

// Another operator cannot decide it; a bot session cannot either.
func TestTemplateProposalApprovers(t *testing.T) {
	f := newTplFixture(t)
	ctx := context.Background()
	f.cfg.Operators["op2"] = config.OperatorConfig{SSHKey: "ssh-ed25519 BBBB"}
	m := f.cfg.Bots["sre"]
	m.Operators = []string{"op"}
	f.cfg.Bots["sre"] = m
	f.setVersion(t, "2.0.0", "Be careful.", map[string]string{"knowledge/topology.md": kd("five")})
	id, _ := f.svc.CheckTemplate(ctx, "sre")
	if _, err := f.x.Decide(ctx, Decision{Bot: "sre", Proposal: id, Operator: "op2", Decision: "approve", Signature: "sig"}); err == nil {
		t.Fatal("another bot's operator approved")
	}
	f.svc.Tick(ctx, "sre")
	if _, err := f.svc.AgentOp(ctx, homeCaller(f.svcFixture), "approve", map[string]any{"proposal": id}); err == nil {
		t.Fatal("a session approved")
	}
	if strings.Contains(f.read(t, "knowledge/topology.md"), "five") {
		t.Fatal("landed")
	}
}

// UnifiedDiff renders a change.
func TestUnifiedDiff(t *testing.T) {
	d := UnifiedDiff("a\nb\nc\nd\ne\nf\ng\nh\ni\n", "a\nb\nC\nd\ne\nf\ng\nh\ni\nj\n")
	if !strings.Contains(d, "-c\n+C\n") || !strings.Contains(d, "+j\n") || !strings.HasPrefix(d, "@@ -1,") {
		t.Fatalf("diff:\n%s", d)
	}
	if UnifiedDiff("same\n", "same\n") != "" {
		t.Fatal("no change, no diff")
	}
	if d := UnifiedDiff("", "new\n"); d != "@@ -0,0 +1,1 @@\n+new\n" {
		t.Fatalf("new file: %q", d)
	}
}
