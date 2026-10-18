package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/store"
)

// TEMPLATE UPDATES (docs/wiki/Bot-Templates.md#updates). A template bot's
// memory starts as its template's charter and seed documents; from then on
// it is the bot's, evolving with every write. A newer template version never
// overwrites it: what changed arrives as ONE proposal — each document as a
// diff against the bot's memory as it is now — that an operator approves
// (signed, like every decision) or denies. Approval lands it as one commit
// authored by the operator; a denial is remembered, so the same changes are
// not proposed again. Pins and holds on the pack instance decide which
// version is vendored at all: nothing here fetches.

// KindTemplate is the template-update proposal kind.
const KindTemplate = "template"

const templateVerb = "template.update"

// templateProposalTTL bounds a template proposal; an expired one is proposed
// again at the next check.
const templateProposalTTL = 14 * 24 * time.Hour

func textSHA(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// templateFiles is what a template puts in memory: its charter document and
// every valid document of its seed (the same rule as a seed: under a kind's
// home, valid for that kind, within its cap). Path → text.
func templateFiles(repo *Repo, o *config.BotOrigin, seedDir string) (map[string]string, error) {
	files := map[string]string{}
	ks := repo.Kinds()
	if o.Charter != "" {
		files[config.CharterDocPath] = CharterDoc(ks, o, o.Charter)
	}
	if seedDir == "" {
		return files, nil
	}
	err := filepath.WalkDir(seedDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(seedDir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.Contains(rel, "/") {
			return nil
		}
		k, ok := ks.Of(rel)
		if !ok || k.Name == KindCharter || k.Name == KindLog || k.Name == KindHandoff {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		text := string(b)
		if k.CheckPath(rel) != nil || k.Validate(text) != nil || k.CheckSize(text) != nil {
			return nil // not a document the seed would take
		}
		files[rel] = text
		return nil
	})
	return files, err
}

// CheckTemplate compares a template bot's memory with its template and, when
// the template brings something new, proposes it. It returns the proposal
// it opened ("" when there was nothing to propose).
func (s *Service) CheckTemplate(ctx context.Context, name string) (string, error) {
	cfg := s.Cfg()
	m, ok := cfg.Bots[name]
	if !ok || m.Origin == nil {
		return "", nil
	}
	o := m.Origin
	repo := s.Repo(name)
	if err := repo.Init(ctx); err != nil {
		return "", err
	}
	// The template's own seed — not an operator's seed_dir.
	files, err := templateFiles(repo, o, o.SeedDir)
	if err != nil {
		return "", err
	}
	rec := s.Ledger.Bot(name)
	st := rec.Template
	if st == nil || st.Ref != o.Ref() {
		st = &store.TemplateState{Ref: o.Ref(), Pack: o.Pack, Since: s.now(), Applied: map[string]string{}}
	} else {
		cp := *st
		cp.Applied = map[string]string{}
		for k, v := range st.Applied {
			cp.Applied[k] = v
		}
		st = &cp
	}
	from := st.Version
	var changes []store.TemplateChange
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		text := files[p]
		tsha := textSHA(text)
		cur, rerr := repo.Read(p)
		exists := rerr == nil
		if exists && cur == text {
			st.Applied[p] = tsha // the memory holds exactly the template's text
			continue
		}
		if st.Applied[p] == tsha {
			continue // the template did not change it; what is there is the bot's
		}
		ch := store.TemplateChange{Path: p, Action: "new", Text: text, SHA256: tsha}
		if exists {
			ch.Action, ch.Current, ch.CurrentSHA256 = "update", cur, textSHA(cur)
			ch.Evolved = ch.CurrentSHA256 != st.Applied[p]
		}
		changes = append(changes, ch)
	}
	save := func(version string) error {
		return s.Ledger.UpdateBot(name, func(r *store.BotRecord) error {
			cp := *st
			if version != "" {
				cp.Version = version
			}
			r.Template = &cp
			return nil
		})
	}
	if len(changes) == 0 {
		return "", save(o.Version)
	}
	h := sha256.New()
	for _, c := range changes {
		fmt.Fprintf(h, "%s %s %s\n", c.Path, c.SHA256, c.CurrentSHA256)
	}
	digest := hex.EncodeToString(h.Sum(nil))[:16]
	if st.Declined == digest {
		return "", save("")
	}
	for _, p := range rec.Proposals {
		if p.Kind == KindTemplate && p.Status == "pending" && p.Template != nil && p.Template.Digest == digest {
			return "", save("")
		}
	}
	tu := store.TemplateUpdate{Ref: o.Ref(), Pack: o.Pack, FromVersion: from, ToVersion: o.Version, Changes: changes, Digest: digest}
	opts := map[string]any{"ref": tu.Ref, "to": tu.ToVersion, "changes": digest}
	pd, err := ActionDigest(name, KindTemplate, templateVerb, opts)
	if err != nil {
		return "", err
	}
	now := s.now()
	var id string
	err = s.Ledger.UpdateBot(name, func(r *store.BotRecord) error {
		for _, p := range r.Proposals {
			if p.Kind == KindTemplate && p.Status == "pending" {
				p.Status, p.Reason = "superseded", "a newer template version was proposed"
			}
		}
		p := &store.Proposal{ID: r.NextID("p"), Kind: KindTemplate, Tier: config.TierPropose, Uses: templateVerb,
			Options: opts, Digest: pd, Template: &tu, Requester: "conductor", RequesterClass: ReqEvent,
			Summary: fmt.Sprintf("update memory from template %s %s → %s: %d document(s)", tu.Ref, orDash(from), tu.ToVersion, len(changes)),
			Status:  "pending", Approvers: cfg.BotOperators(name), Created: now, Expires: now.Add(templateProposalTTL)}
		if r.Proposals == nil {
			r.Proposals = map[string]*store.Proposal{}
		}
		r.Proposals[p.ID] = p
		id = p.ID
		cp := *st
		r.Template = &cp
		return nil
	})
	if err != nil {
		return "", err
	}
	s.audit(map[string]any{"event": "bot_template_update", "bot": name, "proposal": id, "template": tu.Ref, "from": from, "to": tu.ToVersion, "changes": len(changes), "action": "proposed"})
	s.logf("%s: template %s %s brings %d memory change(s): proposal %s", name, tu.Ref, tu.ToVersion, len(changes), id)
	s.enqueue(name, "", fmt.Sprintf("[conductor] Your template %s has a new version (%s). Its changes to your memory (%d document(s)) wait for your operator as proposal %s; until they decide, your memory stays as it is.", tu.Ref, tu.ToVersion, len(changes), id))
	return id, nil
}

// maybeCheckTemplate checks a bot's template once per loaded config: at
// start, and after a reload (which is when a newer vendored pack arrives).
func (s *Service) maybeCheckTemplate(ctx context.Context, name string) {
	cfg := s.Cfg()
	s.mu.Lock()
	if s.tplCfg == nil {
		s.tplCfg = map[string]*config.Config{}
	}
	seen := s.tplCfg[name] == cfg
	s.tplCfg[name] = cfg
	s.mu.Unlock()
	if seen {
		return
	}
	if _, err := s.CheckTemplate(ctx, name); err != nil {
		s.logf("%s: template check: %v", name, err)
	}
}

// LandTemplate writes an approved template update into the bot's memory —
// exactly the approved texts, and only where the memory still holds what the
// proposal was made against. One commit, authored by the approving operator.
func (s *Service) LandTemplate(ctx context.Context, bot string, p store.Proposal, operator string) (string, error) {
	tu := p.Template
	if tu == nil {
		return "", errors.New("template proposal without its changes")
	}
	repo := s.Repo(bot)
	files := map[string]string{}
	for _, c := range tu.Changes {
		if textSHA(c.Text) != c.SHA256 {
			return "", fmt.Errorf("%s: the proposed text does not match its hash", c.Path)
		}
		cur, err := repo.Read(c.Path)
		switch {
		case err != nil && c.Action == "update":
			return "", fmt.Errorf("%s was removed since the proposal — nothing landed; the next check proposes again", c.Path)
		case err == nil && textSHA(cur) != c.CurrentSHA256:
			return "", fmt.Errorf("%s changed since the proposal — nothing landed; the next check proposes it again against what is there now", c.Path)
		}
		files[c.Path] = c.Text
	}
	commit, err := repo.ApplyTemplate(ctx, OperatorIdent(operator), files, map[string]string{
		"Template-Update": tu.Ref + "@" + tu.ToVersion, "Bot-Proposal": p.ID, "Approved-By": operator})
	if err != nil {
		return "", err
	}
	_ = s.Ledger.UpdateBot(bot, func(r *store.BotRecord) error {
		if r.Template == nil {
			r.Template = &store.TemplateState{Ref: tu.Ref, Pack: tu.Pack, Since: s.now()}
		}
		if r.Template.Applied == nil {
			r.Template.Applied = map[string]string{}
		}
		for _, c := range tu.Changes {
			r.Template.Applied[c.Path] = c.SHA256
		}
		r.Template.Version = tu.ToVersion
		if x := r.Proposals[p.ID]; x != nil && x.Template != nil {
			x.Template.Landed = commit
		}
		return nil
	})
	s.audit(map[string]any{"event": "bot_template_update", "bot": bot, "proposal": p.ID, "template": tu.Ref, "to": tu.ToVersion, "commit": commit, "operator": operator, "action": "landed"})
	return commit, nil
}

// templateDenied remembers a denied change set.
func (s *Service) templateDenied(bot string, p store.Proposal) {
	if p.Template == nil {
		return
	}
	_ = s.Ledger.UpdateBot(bot, func(r *store.BotRecord) error {
		if r.Template != nil {
			r.Template.Declined = p.Template.Digest
		}
		return nil
	})
	s.audit(map[string]any{"event": "bot_template_update", "bot": bot, "proposal": p.ID, "template": p.Template.Ref, "to": p.Template.ToVersion, "operator": p.Approver, "action": "denied"})
}

// ApplyTemplate writes template texts as one commit authored by id — each
// through the kinds' chokepoint (the charter as conductor's, the rest as
// the operator's who approved them).
func (r *Repo) ApplyTemplate(ctx context.Context, id Ident, files map[string]string, trailers map[string]string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ks := r.Kinds()
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		c, err := r.cleanPath(p)
		if err != nil {
			return "", err
		}
		w := WriterOperator
		if k, ok := ks.Of(c); ok && k.Name == KindCharter {
			w = WriterSystem
		}
		if _, err := ks.CheckWrite(c, files[p], w, ""); err != nil {
			return "", err
		}
	}
	for _, p := range paths {
		abs := filepath.Join(r.Dir, p)
		if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(abs, []byte(files[p]), 0o600); err != nil {
			return "", err
		}
	}
	if _, err := r.git(ctx, r.Dir, append([]string{"add", "--"}, paths...)...); err != nil {
		return "", err
	}
	msg := commitMessage(fmt.Sprintf("memory: template update (%d document(s))", len(paths)), trailers)
	if _, err := r.gitIdent(ctx, id, r.Dir, append([]string{"commit", "-q", "-m", msg, "--"}, paths...)...); err != nil {
		return "", err
	}
	head := r.headLocked(ctx)
	r.refreshLocked(ctx)
	r.indexCommitLocked(ctx)
	return head, nil
}

// TemplateAction is the operator's view of a template proposal.
func TemplateAction(p store.Proposal) string {
	tu := p.Template
	var parts []string
	for _, c := range tu.Changes {
		s := c.Action + " " + c.Path
		if c.Evolved {
			s += " (your memory changed it since)"
		}
		parts = append(parts, s)
	}
	return oneLine(fmt.Sprintf("update memory from template %s %s → %s: %s", tu.Ref, orDash(tu.FromVersion), tu.ToVersion, strings.Join(parts, ", ")), 600)
}

// TemplateDiff renders a template proposal as unified diffs, per document.
func TemplateDiff(tu *store.TemplateUpdate) string {
	var b strings.Builder
	fmt.Fprintf(&b, "template %s %s → %s (%s)\n", tu.Ref, orDash(tu.FromVersion), tu.ToVersion, tu.Pack)
	for _, c := range tu.Changes {
		note := ""
		if c.Evolved {
			note = "  (your memory changed it since the template last did — approving replaces that)"
		}
		fmt.Fprintf(&b, "\n--- a/%s\n+++ b/%s%s\n", c.Path, c.Path, note)
		b.WriteString(UnifiedDiff(c.Current, c.Text))
	}
	return b.String()
}
