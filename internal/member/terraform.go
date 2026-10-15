package member

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/store"
)

// CONDUCTOR-EXECUTED TERRAFORM. Agents never hold a write role and never run
// an apply. A member proposes a CHANGE — a repo, a pushed commit, a root
// module, a workspace — and conductor does the rest itself:
//
//  1. checks that exact commit out in its OWN private directory
//     (<state>/members/<m>/tf/<proposal>, owner-only), plans it with the
//     READ role (the plan connector's env), and records the plan's SHA-256,
//     its summary and every address it replaces or destroys;
//  2. refuses outright a plan that destroys or replaces a STATEFUL resource
//     (terraform.stateful, default DefaultStatefulTypes) unless
//     terraform.allow_destroy names that address;
//  3. offers the rest for a signed approval whose digest covers the plan's
//     SHA-256 — the operator approves those plan bytes, nothing else;
//  4. on approval re-hashes the plan file and checks the checkout is still
//     that commit and clean, then applies EXACTLY that saved plan with the
//     WRITE role (the apply connector's env, which only conductor holds);
//  5. a plan made stale by a state change fails, offering a re-plan; the
//     plan file and the checkout are deleted when the proposal is applied,
//     denied, refused, failed or expired.

// KindTerraform is a terraform proposal's kind.
const KindTerraform = "terraform"

// TerraformRequest is what a member pins.
type TerraformRequest struct {
	Repo, Commit, Dir, Workspace string
	VarFiles, Targets            []string
	Summary                      string
	ReplanOf                     string
}

// Terraform plans and applies member terraform proposals.
type Terraform struct {
	Cfg    func() *config.Config
	Ledger Ledger
	Gate   *Gate
	// Root is a member's private terraform directory (default
	// <state dir>/members/<m>/tf).
	Root func(member string) string
	Now  func() time.Time
	// Run executes one command (nil = os/exec). Tests substitute it.
	Run func(ctx context.Context, dir string, env []string, name string, args ...string) (stdout, stderr string, err error)
	// Background runs a plan off the proposing call (nil = `go fn()`).
	Background func(fn func())
	// OnPlanned is told when a plan is ready (or refused / failed).
	OnPlanned func(member string, p store.Proposal)
	Log       func(string, ...any)
}

func (t *Terraform) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Terraform) logf(f string, a ...any) {
	if t.Log != nil {
		t.Log("member terraform: "+f, a...)
	}
}

func (t *Terraform) root(member string) string {
	if t.Root != nil {
		return t.Root(member)
	}
	return filepath.Join(config.StateDir(), "members", member, "tf")
}

// workDir is one proposal's private directory: the checkout (src/) and the
// saved plan (plan.bin).
func (t *Terraform) workDir(member, pid string) string { return filepath.Join(t.root(member), pid) }

func (t *Terraform) run(ctx context.Context, dir string, env []string, name string, args ...string) (string, string, error) {
	if t.Run != nil {
		return t.Run(ctx, dir, env, name, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

var (
	commitRe    = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	workspaceRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	targetRe    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.\[\]"\-:/]*$`)
)

// cleanRel canonicalizes a repo-relative path ("" and "." are the root) and
// refuses one that is absolute or climbs out.
func cleanRel(p string) (string, error) {
	if strings.ContainsAny(p, "\\\x00") || strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("path %q must be repo-relative", p)
	}
	if p == "" {
		return ".", nil
	}
	c := path.Clean(p)
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("path %q climbs out of the repository", p)
	}
	return c, nil
}

func (t *Terraform) member(name string) (config.MemberConfig, *config.MemberTerraform, error) {
	m, ok := t.Cfg().Members[name]
	if !ok || m.Disabled {
		return m, nil, fmt.Errorf("unknown member %q", name)
	}
	if m.Terraform == nil {
		return m, nil, fmt.Errorf("member %s has no terraform: configured", name)
	}
	return m, m.Terraform, nil
}

// Propose records a terraform proposal and starts conductor's plan of it.
// The proposal is `planning` until the plan is ready; then `pending` (or
// `shadow`), `refused` (a stateful destroy/replace, or no changes), or
// `failed`.
func (t *Terraform) Propose(ctx context.Context, c Actor, req TerraformRequest) (store.Proposal, error) {
	m, tc, err := t.member(c.Member)
	if err != nil {
		return store.Proposal{}, err
	}
	if Classify(m, "terraform.apply") == config.TierNever {
		return store.Proposal{}, fmt.Errorf("terraform.apply is classified never for member %s", c.Member)
	}
	if _, ok := tc.Repos[req.Repo]; !ok {
		return store.Proposal{}, fmt.Errorf("repo %q is not one of this member's terraform.repos", req.Repo)
	}
	req.Commit = strings.ToLower(strings.TrimSpace(req.Commit))
	if !commitRe.MatchString(req.Commit) {
		return store.Proposal{}, fmt.Errorf("commit must be the FULL commit id (40 or 64 hex) of the pushed change, got %q", req.Commit)
	}
	if req.Dir, err = cleanRel(req.Dir); err != nil {
		return store.Proposal{}, fmt.Errorf("dir: %w", err)
	}
	if req.Workspace == "" {
		req.Workspace = "default"
	}
	if !workspaceRe.MatchString(req.Workspace) {
		return store.Proposal{}, fmt.Errorf("workspace %q: letters, digits, - and _ only", req.Workspace)
	}
	for i, vf := range req.VarFiles {
		joined, err := cleanRel(path.Join(req.Dir, vf))
		if err != nil || strings.HasPrefix(vf, "/") || joined == "." {
			return store.Proposal{}, fmt.Errorf("var file %q: must stay inside the repository", vf)
		}
		req.VarFiles[i] = path.Clean(vf)
	}
	for _, tg := range req.Targets {
		if !targetRe.MatchString(tg) {
			return store.Proposal{}, fmt.Errorf("target %q is not a resource address", tg)
		}
	}
	class, who, caseID := ReqMember, "", c.CaseID
	if t.Gate != nil {
		class, who, caseID = t.Gate.requester(flow.SkillIdentity{Member: c.Member, MemberRole: c.Role, CaseID: c.CaseID})
	}
	now := t.now()
	var out store.Proposal
	err = t.Ledger.UpdateMember(c.Member, func(r *store.MemberRecord) error {
		p := &store.Proposal{
			ID: r.NextID("p"), CaseID: caseID, Kind: KindTerraform, Tier: config.TierPropose, Uses: "terraform.apply",
			Terraform: &store.TerraformPlan{Repo: req.Repo, Commit: req.Commit, Dir: req.Dir, Workspace: req.Workspace,
				VarFiles: append([]string(nil), req.VarFiles...), Targets: append([]string(nil), req.Targets...), ReplanOf: req.ReplanOf},
			Summary: req.Summary, Role: c.Role, Agent: c.Agent, Dispatch: c.Dispatch,
			Requester: who, RequesterClass: class, Status: "planning", Created: now, Expires: now.Add(m.Autonomy.TTL()),
		}
		if cc := r.Cases[caseID]; cc != nil {
			cc.Proposals = append(cc.Proposals, p.ID)
			cc.Updated = now
		}
		if r.Proposals == nil {
			r.Proposals = map[string]*store.Proposal{}
		}
		r.Proposals[p.ID] = p
		out = *p
		return nil
	})
	if err != nil {
		return store.Proposal{}, err
	}
	run := func() { t.Plan(context.WithoutCancel(ctx), c.Member, out.ID) }
	if t.Background != nil {
		t.Background(run)
	} else {
		go run()
	}
	return out, nil
}

// Replan proposes the same pins again — after a plan went stale, say — as a
// new proposal needing a new approval.
func (t *Terraform) Replan(ctx context.Context, c Actor, pid string) (store.Proposal, error) {
	p := t.Ledger.Member(c.Member).Proposals[pid]
	if p == nil || p.Kind != KindTerraform || p.Terraform == nil {
		return store.Proposal{}, fmt.Errorf("no terraform proposal %s", pid)
	}
	switch p.Status {
	case "failed", "expired", "denied", "refused", "executed":
	default:
		return store.Proposal{}, fmt.Errorf("proposal %s is %s — re-plan a finished one", pid, p.Status)
	}
	tp := p.Terraform
	return t.Propose(ctx, c, TerraformRequest{Repo: tp.Repo, Commit: tp.Commit, Dir: tp.Dir, Workspace: tp.Workspace,
		VarFiles: append([]string(nil), tp.VarFiles...), Targets: append([]string(nil), tp.Targets...), Summary: p.Summary, ReplanOf: pid})
}

// env is the process environment for a role: the daemon's own, minus any
// inherited Terraform/cloud settings that would blur which role runs, plus
// the role's env.
func roleEnv(role config.TerraformRole) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "TF_") || strings.HasPrefix(k, "AWS_") || strings.HasPrefix(k, "GOOGLE_") || strings.HasPrefix(k, "ARM_") {
			continue
		}
		env = append(env, kv)
	}
	keys := make([]string, 0, len(role.Env))
	for k := range role.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+role.Env[k])
	}
	return append(env, "TF_IN_AUTOMATION=1", "TF_INPUT=0", "GIT_TERMINAL_PROMPT=0")
}

func (t *Terraform) fail(member, pid, reason string) store.Proposal {
	var out store.Proposal
	_ = t.Ledger.UpdateMember(member, func(r *store.MemberRecord) error {
		if p := r.Proposals[pid]; p != nil {
			p.Status, p.Reason = "failed", reason
			out = *p
		}
		return nil
	})
	t.Discard(member, pid)
	t.logf("%s %s: %s", member, pid, reason)
	return out
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

// Plan makes conductor's own plan of a planning proposal.
func (t *Terraform) Plan(ctx context.Context, member, pid string) store.Proposal {
	m, tc, err := t.member(member)
	if err != nil {
		return t.fail(member, pid, err.Error())
	}
	p := t.Ledger.Member(member).Proposals[pid]
	if p == nil || p.Terraform == nil || p.Status != "planning" {
		return store.Proposal{}
	}
	tp := *p.Terraform
	ctx, cancel := context.WithTimeout(ctx, tc.TimeoutD())
	defer cancel()
	cfg := t.Cfg()
	role, err := cfg.MemberTerraformRole(tc.PlanConnector)
	if err != nil {
		return t.fail(member, pid, "plan role: "+err.Error())
	}
	wd := t.workDir(member, pid)
	if err := os.MkdirAll(wd, 0o700); err != nil {
		return t.fail(member, pid, err.Error())
	}
	_ = os.Chmod(wd, 0o700)
	src := filepath.Join(wd, "src")
	env := roleEnv(role)
	git := func(args ...string) (string, error) {
		o, e, err := t.run(ctx, wd, env, "git", args...)
		if err != nil {
			return o, fmt.Errorf("git %s: %v: %s", args[0], err, tail(e, 400))
		}
		return o, nil
	}
	if _, err := git("clone", "--quiet", "--no-checkout", "--", tc.Repos[tp.Repo], src); err != nil {
		return t.fail(member, pid, err.Error())
	}
	if _, err := git("-C", src, "cat-file", "-e", tp.Commit+"^{commit}"); err != nil {
		if _, ferr := git("-C", src, "fetch", "--quiet", "origin", tp.Commit); ferr != nil {
			return t.fail(member, pid, fmt.Sprintf("commit %s is not in %s — push it first (git.push), then propose it: %v", tp.Commit, tp.Repo, ferr))
		}
	}
	if _, err := git("-C", src, "-c", "advice.detachedHead=false", "checkout", "--quiet", "--detach", tp.Commit); err != nil {
		return t.fail(member, pid, err.Error())
	}
	if head, _ := git("-C", src, "rev-parse", "HEAD"); strings.TrimSpace(head) != tp.Commit {
		return t.fail(member, pid, "checkout is not at the pinned commit")
	}
	mod := filepath.Join(src, filepath.FromSlash(tp.Dir))
	if fi, err := os.Stat(mod); err != nil || !fi.IsDir() {
		return t.fail(member, pid, fmt.Sprintf("dir %q is not a directory at %s", tp.Dir, tp.Commit))
	}
	bin := role.Bin()
	tf := func(args ...string) (string, error) {
		o, e, err := t.run(ctx, mod, env, bin, args...)
		if err != nil {
			return o, fmt.Errorf("%s %s: %v: %s", bin, args[0], err, tail(e+"\n"+o, 1500))
		}
		return o, nil
	}
	if _, err := tf("init", "-input=false", "-no-color"); err != nil {
		return t.fail(member, pid, err.Error())
	}
	if tp.Workspace != "default" {
		if _, err := tf("workspace", "select", "-no-color", tp.Workspace); err != nil {
			if _, err := tf("workspace", "new", "-no-color", tp.Workspace); err != nil {
				return t.fail(member, pid, err.Error())
			}
		}
	}
	planFile := filepath.Join(wd, "plan.bin")
	args := []string{"plan", "-input=false", "-no-color", "-lock=true", "-out=" + planFile}
	for _, vf := range tp.VarFiles {
		args = append(args, "-var-file="+vf)
	}
	for _, tg := range tp.Targets {
		args = append(args, "-target="+tg)
	}
	if _, err := tf(args...); err != nil {
		return t.fail(member, pid, err.Error())
	}
	_ = os.Chmod(planFile, 0o600)
	js, err := tf("show", "-json", planFile)
	if err != nil {
		return t.fail(member, pid, err.Error())
	}
	text, err := tf("show", "-no-color", planFile)
	if err != nil {
		return t.fail(member, pid, err.Error())
	}
	sum, err := fileSHA256(planFile)
	if err != nil {
		return t.fail(member, pid, err.Error())
	}
	an, err := analyzePlan(js)
	if err != nil {
		return t.fail(member, pid, err.Error())
	}
	tp.PlanSHA256, tp.Add, tp.Change, tp.Destroy = sum, an.add, an.change, an.destroy
	tp.Replaced, tp.Destroyed, tp.PlanText, tp.PlannedAt = an.replaced, an.destroyed, tail(text, 64<<10), t.now()

	status, reason := "pending", ""
	switch {
	case an.add+an.change+an.destroy == 0:
		status, reason = "refused", "the plan has no changes — nothing to apply"
	default:
		if bad := statefulLosses(tc, an); len(bad) > 0 {
			status, reason = "refused", fmt.Sprintf("the plan destroys or replaces stateful resources: %s — refused by default; to allow one, name its address in members.%s.terraform.allow_destroy", strings.Join(bad, ", "), member)
		}
	}
	if status == "pending" && m.Autonomy.ShadowMode() {
		status = "shadow"
	}
	var digest string
	if status == "pending" || status == "shadow" {
		digest, err = ActionDigest(member, KindTerraform, "terraform.apply", terraformAction(tp))
		if err != nil {
			return t.fail(member, pid, err.Error())
		}
	}
	var out store.Proposal
	_ = t.Ledger.UpdateMember(member, func(r *store.MemberRecord) error {
		q := r.Proposals[pid]
		if q == nil {
			return nil
		}
		cp := tp
		q.Terraform, q.Status, q.Reason, q.Digest = &cp, status, reason, digest
		q.Expires = t.now().Add(m.Autonomy.TTL())
		out = *q
		return nil
	})
	if status != "pending" {
		// Refused or shadow: nothing will ever be applied from this plan.
		t.Discard(member, pid)
	}
	if t.OnPlanned != nil {
		t.OnPlanned(member, out)
	}
	return out
}

// terraformAction is what an operator's signature covers: the pins AND the
// plan bytes' hash.
func terraformAction(tp store.TerraformPlan) map[string]any {
	return map[string]any{"repo": tp.Repo, "commit": tp.Commit, "dir": tp.Dir, "workspace": tp.Workspace,
		"var_files": tp.VarFiles, "targets": tp.Targets, "plan_sha256": tp.PlanSHA256}
}

type planAnalysis struct {
	add, change, destroy int
	replaced, destroyed  []string
	types                map[string]string // address → resource type
}

// analyzePlan reads `terraform show -json` of a saved plan.
func analyzePlan(js string) (planAnalysis, error) {
	var doc struct {
		ResourceChanges []struct {
			Address string `json:"address"`
			Type    string `json:"type"`
			Change  struct {
				Actions []string `json:"actions"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(js)), &doc); err != nil {
		return planAnalysis{}, fmt.Errorf("read the plan's JSON: %w", err)
	}
	a := planAnalysis{types: map[string]string{}}
	for _, rc := range doc.ResourceChanges {
		a.types[rc.Address] = rc.Type
		acts := strings.Join(rc.Change.Actions, ",")
		switch acts {
		case "create":
			a.add++
		case "update":
			a.change++
		case "delete":
			a.destroy++
			a.destroyed = append(a.destroyed, rc.Address)
		case "delete,create", "create,delete":
			a.add++
			a.destroy++
			a.replaced = append(a.replaced, rc.Address)
		}
	}
	sort.Strings(a.replaced)
	sort.Strings(a.destroyed)
	return a, nil
}

// statefulLosses lists the destroyed or replaced addresses whose type is
// stateful and whose address allow_destroy does not name.
func statefulLosses(tc *config.MemberTerraform, a planAnalysis) []string {
	var bad []string
	for _, addr := range append(append([]string(nil), a.replaced...), a.destroyed...) {
		if !globAny(tc.StatefulTypes(), a.types[addr]) {
			continue
		}
		if globAny(tc.AllowDestroy, addr) {
			continue
		}
		bad = append(bad, addr)
	}
	sort.Strings(bad)
	return bad
}

func globAny(pats []string, s string) bool {
	for _, p := range pats {
		if ok, _ := path.Match(p, s); ok || p == s {
			return true
		}
	}
	return false
}

// initArtifact reports a path terraform's own init/apply writes inside the
// module directory — the only untracked change a clean checkout may have.
func initArtifact(p, dir string) bool {
	rel := p
	if dir != "." {
		if !strings.HasPrefix(p, dir+"/") {
			return false
		}
		rel = strings.TrimPrefix(p, dir+"/")
	}
	return rel == ".terraform.lock.hcl" || strings.HasPrefix(rel, ".terraform/") || rel == ".terraform" ||
		strings.HasPrefix(rel, "terraform.tfstate") || strings.HasPrefix(rel, "terraform.tfstate.d/")
}

// Apply applies an approved terraform proposal's saved plan, exactly.
func (t *Terraform) Apply(ctx context.Context, member string, p store.Proposal) store.Proposal {
	_, tc, err := t.member(member)
	if err != nil {
		return t.fail(member, p.ID, err.Error())
	}
	if p.Terraform == nil {
		return t.fail(member, p.ID, "not a terraform proposal")
	}
	tp := *p.Terraform
	ctx, cancel := context.WithTimeout(ctx, tc.TimeoutD())
	defer cancel()
	wd := t.workDir(member, p.ID)
	planFile := filepath.Join(wd, "plan.bin")
	// 1. The plan file is the bytes the operator approved.
	sum, err := fileSHA256(planFile)
	if err != nil {
		return t.fail(member, p.ID, "the saved plan is gone: "+err.Error())
	}
	if sum != tp.PlanSHA256 {
		return t.fail(member, p.ID, fmt.Sprintf("the saved plan changed since it was approved (approved %s…, now %s…) — not applied", short(tp.PlanSHA256), short(sum)))
	}
	role, err := t.Cfg().MemberTerraformRole(tc.ApplyConnector)
	if err != nil {
		return t.fail(member, p.ID, "apply role: "+err.Error())
	}
	env := roleEnv(role)
	src := filepath.Join(wd, "src")
	// 2. The checkout is still that commit, and clean.
	head, _, err := t.run(ctx, wd, env, "git", "-C", src, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(head) != tp.Commit {
		return t.fail(member, p.ID, fmt.Sprintf("the checkout is no longer at %s — not applied", short(tp.Commit)))
	}
	st, _, err := t.run(ctx, wd, env, "git", "-C", src, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return t.fail(member, p.ID, "checkout status: "+err.Error())
	}
	for _, line := range strings.Split(st, "\n") {
		if len(line) < 4 {
			continue
		}
		f := strings.Trim(strings.TrimSpace(line[3:]), `"`)
		if !initArtifact(f, tp.Dir) {
			return t.fail(member, p.ID, fmt.Sprintf("the checkout is not clean (%s) — not applied", strings.TrimSpace(line)))
		}
	}
	// 3. Apply exactly that saved plan, with the write role.
	mod := filepath.Join(src, filepath.FromSlash(tp.Dir))
	o, e, err := t.run(ctx, mod, env, role.Bin(), "apply", "-input=false", "-no-color", "-lock=true", planFile)
	all := o + "\n" + e
	var out store.Proposal
	status, reason := "executed", ""
	switch {
	case err != nil && strings.Contains(all, "Saved plan is stale"):
		status, reason = "failed", fmt.Sprintf("the plan is stale — the state changed since it was made; nothing was applied. `conductor member replan %s %s` plans the same commit again for a new approval", member, p.ID)
	case err != nil:
		status, reason = "failed", "apply failed: "+tail(all, 2000)
	}
	_ = t.Ledger.UpdateMember(member, func(r *store.MemberRecord) error {
		q := r.Proposals[p.ID]
		if q == nil {
			return nil
		}
		q.Status, q.Reason = status, reason
		q.Result = map[string]any{"output": tail(all, 4000)}
		out = *q
		return nil
	})
	t.Discard(member, p.ID)
	return out
}

// Discard deletes a proposal's plan file and checkout.
func (t *Terraform) Discard(member, pid string) {
	if pid == "" || strings.ContainsAny(pid, "/\\.") {
		return
	}
	_ = os.RemoveAll(t.workDir(member, pid))
}

// Recover runs at start: a plan or apply a restart interrupted is failed and
// its files deleted; a directory no pending proposal owns is deleted.
func (t *Terraform) Recover(member string) {
	keep := map[string]bool{}
	var interrupted []string
	_ = t.Ledger.UpdateMember(member, func(r *store.MemberRecord) error {
		for _, p := range r.Proposals {
			if p.Kind != KindTerraform {
				continue
			}
			switch p.Status {
			case "planning", "approved":
				p.Status, p.Reason = "failed", "interrupted by a daemon restart — nothing further was done; re-plan to try again"
				interrupted = append(interrupted, p.ID)
			case "pending":
				keep[p.ID] = true
			}
		}
		return nil
	})
	entries, _ := os.ReadDir(t.root(member))
	for _, e := range entries {
		if !keep[e.Name()] {
			_ = os.RemoveAll(filepath.Join(t.root(member), e.Name()))
		}
	}
	for _, id := range interrupted {
		t.logf("%s %s: interrupted by restart", member, id)
	}
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

var errNoTerraform = errors.New("no terraform runner on this daemon")
