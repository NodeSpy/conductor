package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/store"
)

// fakeTF is a scripted terraform: real git runs for real; `terraform` is
// answered from the scenario, recording every call and the env it ran with.
type fakeTF struct {
	mu        sync.Mutex
	planBytes string
	planJSON  string
	applyErr  string // non-empty: apply fails with this output
	calls     []tfCall
}

type tfCall struct {
	dir  string
	env  []string
	args []string
}

func (f *fakeTF) run(ctx context.Context, dir string, env []string, name string, args ...string) (string, string, error) {
	if name == "git" {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = env
		var o, e strings.Builder
		cmd.Stdout, cmd.Stderr = &o, &e
		err := cmd.Run()
		return o.String(), e.String(), err
	}
	f.mu.Lock()
	f.calls = append(f.calls, tfCall{dir, env, args})
	f.mu.Unlock()
	switch args[0] {
	case "init", "workspace":
		return "ok", "", nil
	case "plan":
		for _, a := range args {
			if out, ok := strings.CutPrefix(a, "-out="); ok {
				if err := os.WriteFile(out, []byte(f.planBytes), 0o644); err != nil {
					return "", "", err
				}
			}
		}
		return "Plan: ...", "", nil
	case "show":
		if args[1] == "-json" {
			return f.planJSON, "", nil
		}
		return "  # null_resource.x will be created\nPlan: 1 to add", "", nil
	case "apply":
		if f.applyErr != "" {
			return "", f.applyErr, errors.New("exit status 1")
		}
		return "Apply complete! Resources: 1 added, 0 changed, 0 destroyed.", "", nil
	}
	return "", "unknown", errors.New("unknown terraform subcommand")
}

func (f *fakeTF) count(sub string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.args[0] == sub {
			n++
		}
	}
	return n
}

func (f *fakeTF) last(sub string) tfCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].args[0] == sub {
			return f.calls[i]
		}
	}
	return tfCall{}
}

func planJSON(changes ...[3]string) string {
	type rc struct {
		Address string `json:"address"`
		Type    string `json:"type"`
		Change  struct {
			Actions []string `json:"actions"`
		} `json:"change"`
	}
	var out []rc
	for _, c := range changes {
		r := rc{Address: c[0], Type: c[1]}
		r.Change.Actions = strings.Split(c[2], ",")
		out = append(out, r)
	}
	b, _ := json.Marshal(map[string]any{"format_version": "1.0", "resource_changes": out})
	return string(b)
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// infraRepo is a repository with envs/prod/main.tf at one commit.
func infraRepo(t *testing.T) (url, commit string) {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	_ = os.MkdirAll(filepath.Join(dir, "envs", "prod"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "envs", "prod", "main.tf"), []byte(`resource "null_resource" "x" {}`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "envs", "prod", "prod.tfvars"), []byte(`a = 1`), 0o644)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "infra")
	return dir, git(t, dir, "rev-parse", "HEAD")
}

type tfFixture struct {
	*execFixture
	tf     *Terraform
	fake   *fakeTF
	root   string
	commit string
	url    string
}

func tfBot(url string) config.BotConfig {
	m := testBot()
	m.Terraform = &config.BotTerraform{PlanConnector: "tf-read", ApplyConnector: "tf-write", Repos: map[string]string{"infra": url}}
	return m
}

func newTFFixture(t *testing.T) *tfFixture {
	t.Helper()
	url, commit := infraRepo(t)
	f := &tfFixture{execFixture: newExecFixture(t, tfBot(url)), fake: &fakeTF{planBytes: "PLAN-1", planJSON: planJSON([3]string{"null_resource.x", "null_resource", "create"})},
		root: t.TempDir(), commit: commit, url: url}
	cfgYAML := "connectors:\n  tf-read: { use: terraform, env: { ROLE: read } }\n  tf-write: { use: terraform, env: { ROLE: write } }\n"
	p := filepath.Join(t.TempDir(), "c.yaml")
	_ = os.WriteFile(p, []byte(cfgYAML), 0o600)
	loaded, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	f.cfg.ConnectorsMap = loaded.ConnectorsMap
	f.tf = &Terraform{Cfg: func() *config.Config { return f.cfg }, Ledger: f.st, Gate: f.gate,
		Root: func(m string) string { return filepath.Join(f.root, m) }, Now: func() time.Time { return f.now },
		Run: f.fake.run, Background: func(fn func()) { fn() }}
	f.x.Terraform = f.tf
	f.x.Background = func(fn func()) { fn() }
	return f
}

func (f *tfFixture) propose(t *testing.T, req TerraformRequest) store.Proposal {
	t.Helper()
	if req.Repo == "" {
		req.Repo, req.Commit, req.Dir = "infra", f.commit, "envs/prod"
	}
	p, err := f.tf.Propose(context.Background(), Actor{Bot: "sre", Role: config.BotRoleWorker, Dispatch: "d-w"}, req)
	if err != nil {
		t.Fatal(err)
	}
	return f.st.Bot("sre").Proposals[p.ID].Clone()
}

func (f *tfFixture) approve(t *testing.T, pid string) store.Proposal {
	t.Helper()
	out, _ := f.x.Decide(context.Background(), Decision{Bot: "sre", Proposal: pid, Operator: "op", Decision: "approve", Signature: f.sign(t, f.op, pid, "approve")})
	_ = out
	return *f.st.Bot("sre").Proposals[pid]
}

func (f *tfFixture) wd(pid string) string { return filepath.Join(f.root, "sre", pid) }

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func roleOf(env []string) string {
	r := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "ROLE="); ok {
			r = v
		}
	}
	return r
}

// The whole path: pinned → conductor's own plan (read role, private dir,
// owner-only plan) → signed approval of the plan hash → apply of exactly that
// plan (write role) → files deleted.
func TestTerraformProposePlanApproveApply(t *testing.T) {
	f := newTFFixture(t)
	p := f.propose(t, TerraformRequest{Repo: "infra", Commit: f.commit, Dir: "envs/prod", Workspace: "prod", VarFiles: []string{"prod.tfvars"}, Targets: []string{"null_resource.x"}})
	if p.Status != "pending" || p.Terraform.PlanSHA256 != sha("PLAN-1") || p.Terraform.Add != 1 || p.Digest == "" {
		t.Fatalf("planned proposal: %+v %+v", p, p.Terraform)
	}
	plan := f.fake.last("plan")
	if roleOf(plan.env) != "read" || !strings.HasSuffix(plan.dir, filepath.Join("src", "envs", "prod")) {
		t.Fatalf("plan ran with role %q in %s", roleOf(plan.env), plan.dir)
	}
	if !strings.Contains(strings.Join(plan.args, " "), "-var-file=prod.tfvars") || !strings.Contains(strings.Join(plan.args, " "), "-target=null_resource.x") {
		t.Fatalf("plan args: %v", plan.args)
	}
	if fi, _ := os.Stat(f.wd(p.ID)); fi.Mode().Perm() != 0o700 {
		t.Fatalf("work dir mode %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Join(f.wd(p.ID), "plan.bin")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("plan file mode %v", fi.Mode().Perm())
	}
	if a := ProposalAction(p); !strings.Contains(a, "envs/prod") || !strings.Contains(a, p.Terraform.PlanSHA256) || !strings.Contains(a, "+1 ~0 -0") {
		t.Fatalf("approval text: %s", a)
	}
	got := f.approve(t, p.ID)
	if got.Status != "executed" || f.fake.count("apply") != 1 {
		t.Fatalf("apply: status %s reason %q applies %d", got.Status, got.Reason, f.fake.count("apply"))
	}
	ap := f.fake.last("apply")
	if roleOf(ap.env) != "write" || ap.args[len(ap.args)-1] != filepath.Join(f.wd(p.ID), "plan.bin") {
		t.Fatalf("apply ran with role %q args %v", roleOf(ap.env), ap.args)
	}
	if _, err := os.Stat(f.wd(p.ID)); !os.IsNotExist(err) {
		t.Fatal("plan and checkout not deleted after apply")
	}
	// The plan's hash is in what the operator signed.
	tp := *p.Terraform
	d1, _ := ActionDigest("sre", KindTerraform, "terraform.apply", terraformAction(tp))
	tp.PlanSHA256 = sha("other")
	d2, _ := ActionDigest("sre", KindTerraform, "terraform.apply", terraformAction(tp))
	if d1 != p.Digest || d1 == d2 {
		t.Fatal("the signed digest does not cover the plan hash")
	}
}

// A plan file that changed after approval is not applied.
func TestTerraformApplyChecksThePlanHash(t *testing.T) {
	f := newTFFixture(t)
	p := f.propose(t, TerraformRequest{})
	_ = os.WriteFile(filepath.Join(f.wd(p.ID), "plan.bin"), []byte("TAMPERED"), 0o600)
	got := f.approve(t, p.ID)
	if got.Status != "failed" || !strings.Contains(got.Reason, "changed since it was approved") || f.fake.count("apply") != 0 {
		t.Fatalf("tampered plan: %s %q applies %d", got.Status, got.Reason, f.fake.count("apply"))
	}
	if _, err := os.Stat(f.wd(p.ID)); !os.IsNotExist(err) {
		t.Fatal("files kept after a failed apply")
	}
}

// The checkout must still be the commit, and clean — except for terraform's
// own init/state artifacts in the module directory.
func TestTerraformApplyChecksTheCheckout(t *testing.T) {
	cases := map[string]func(src string){
		"untracked file": func(src string) { _ = os.WriteFile(filepath.Join(src, "envs", "prod", "extra.tf"), []byte("x"), 0o644) },
		"modified tracked": func(src string) {
			_ = os.WriteFile(filepath.Join(src, "envs", "prod", "main.tf"), []byte("changed"), 0o644)
		},
		"other dir artifact": func(src string) {
			_ = os.MkdirAll(filepath.Join(src, ".terraform"), 0o755)
			_ = os.WriteFile(filepath.Join(src, ".terraform", "x"), nil, 0o644)
		},
		"moved HEAD": func(src string) {
			_ = os.WriteFile(filepath.Join(src, "envs", "prod", "main.tf"), []byte("changed"), 0o644)
			git(t, src, "commit", "-qam", "drift")
		},
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			f := newTFFixture(t)
			p := f.propose(t, TerraformRequest{})
			mut(filepath.Join(f.wd(p.ID), "src"))
			got := f.approve(t, p.ID)
			if got.Status != "failed" || f.fake.count("apply") != 0 {
				t.Fatalf("%s: applied (status %s, %q)", name, got.Status, got.Reason)
			}
		})
	}
	// Terraform's own artifacts in the module dir do not make it dirty.
	f := newTFFixture(t)
	p := f.propose(t, TerraformRequest{})
	mod := filepath.Join(f.wd(p.ID), "src", "envs", "prod")
	_ = os.MkdirAll(filepath.Join(mod, ".terraform", "providers"), 0o755)
	_ = os.WriteFile(filepath.Join(mod, ".terraform", "providers", "p"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(mod, ".terraform.lock.hcl"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(mod, "terraform.tfstate"), nil, 0o644)
	if got := f.approve(t, p.ID); got.Status != "executed" {
		t.Fatalf("init artifacts made the checkout dirty: %s %q", got.Status, got.Reason)
	}
}

// A plan that destroys or replaces a stateful resource is refused by default
// (and its files deleted); allow_destroy names an address to let it through.
func TestTerraformRefusesStatefulDestroy(t *testing.T) {
	for name, js := range map[string]string{
		"destroy": planJSON([3]string{"aws_db_instance.main", "aws_db_instance", "delete"}),
		"replace": planJSON([3]string{"aws_s3_bucket.logs", "aws_s3_bucket", "delete,create"}, [3]string{"null_resource.x", "null_resource", "create"}),
	} {
		t.Run(name, func(t *testing.T) {
			f := newTFFixture(t)
			f.fake.planJSON = js
			p := f.propose(t, TerraformRequest{})
			if p.Status != "refused" || !strings.Contains(p.Reason, "stateful") || p.Digest != "" {
				t.Fatalf("stateful %s: %s %q", name, p.Status, p.Reason)
			}
			if _, err := os.Stat(f.wd(p.ID)); !os.IsNotExist(err) {
				t.Fatal("a refused plan's files were kept")
			}
			if _, err := f.x.Decide(context.Background(), Decision{Bot: "sre", Proposal: p.ID, Operator: "op", Decision: "approve", Signature: f.sign(t, f.op, p.ID, "approve")}); err == nil {
				t.Fatal("a refused plan was approvable")
			}
		})
	}
	// A non-stateful destroy is offered (and shown).
	f := newTFFixture(t)
	f.fake.planJSON = planJSON([3]string{"null_resource.old", "null_resource", "delete"})
	if p := f.propose(t, TerraformRequest{}); p.Status != "pending" || len(p.Terraform.Destroyed) != 1 || !strings.Contains(ProposalAction(p), "DESTROYS null_resource.old") {
		t.Fatalf("non-stateful destroy: %s %+v", p.Status, p.Terraform)
	}
	// The explicit knob: allow_destroy by address; the configurable list.
	f = newTFFixture(t)
	f.fake.planJSON = planJSON([3]string{"aws_db_instance.scratch", "aws_db_instance", "delete"})
	m := f.cfg.Bots["sre"]
	tc := *m.Terraform
	tc.AllowDestroy = []string{"aws_db_instance.scratch"}
	m.Terraform = &tc
	f.cfg.Bots["sre"] = m
	if p := f.propose(t, TerraformRequest{}); p.Status != "pending" {
		t.Fatalf("allow_destroy ignored: %s %q", p.Status, p.Reason)
	}
	tc.AllowDestroy, tc.Stateful = nil, []string{"null_*"}
	m.Terraform = &tc
	f.cfg.Bots["sre"] = m
	f.fake.planJSON = planJSON([3]string{"null_resource.x", "null_resource", "delete"})
	if p := f.propose(t, TerraformRequest{}); p.Status != "refused" {
		t.Fatalf("configured stateful list ignored: %s", p.Status)
	}
}

// A stale plan fails without applying, its files are deleted, and a re-plan
// is a new proposal needing a new approval.
func TestTerraformStalePlanFailsAndReplans(t *testing.T) {
	f := newTFFixture(t)
	p := f.propose(t, TerraformRequest{})
	f.fake.applyErr = "Error: Saved plan is stale\n\nThe given plan file can no longer be applied because the state was changed"
	got := f.approve(t, p.ID)
	if got.Status != "failed" || !strings.Contains(got.Reason, "stale") || !strings.Contains(got.Reason, "replan") {
		t.Fatalf("stale: %s %q", got.Status, got.Reason)
	}
	if _, err := os.Stat(f.wd(p.ID)); !os.IsNotExist(err) {
		t.Fatal("stale plan's files kept")
	}
	f.fake.applyErr = ""
	f.fake.planBytes = "PLAN-2"
	np, err := f.tf.Replan(context.Background(), Actor{Bot: "sre", Role: config.BotRoleHome}, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	np = *f.st.Bot("sre").Proposals[np.ID]
	if np.ID == p.ID || np.Terraform.ReplanOf != p.ID || np.Status != "pending" || np.Terraform.PlanSHA256 != sha("PLAN-2") || np.Digest == p.Digest {
		t.Fatalf("replan: %+v %+v", np, np.Terraform)
	}
	if _, err := f.tf.Replan(context.Background(), Actor{Bot: "sre"}, np.ID); err == nil {
		t.Fatal("re-planned a proposal still pending")
	}
}

// Denied and expired proposals delete their plan and checkout.
func TestTerraformFilesDeletedOnDenyAndExpiry(t *testing.T) {
	f := newTFFixture(t)
	p := f.propose(t, TerraformRequest{})
	if _, err := f.x.Decide(context.Background(), Decision{Bot: "sre", Proposal: p.ID, Operator: "op", Decision: "deny", Signature: f.sign(t, f.op, p.ID, "deny")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.wd(p.ID)); !os.IsNotExist(err) {
		t.Fatal("denied plan kept")
	}
	p2 := f.propose(t, TerraformRequest{})
	f.now = f.now.Add(48 * time.Hour)
	f.x.ExpireProposals("sre")
	if st := f.st.Bot("sre").Proposals[p2.ID].Status; st != "expired" {
		t.Fatalf("status %s", st)
	}
	if _, err := os.Stat(f.wd(p2.ID)); !os.IsNotExist(err) {
		t.Fatal("expired plan kept")
	}
}

func TestTerraformProposalValidation(t *testing.T) {
	f := newTFFixture(t)
	a := Actor{Bot: "sre", Role: config.BotRoleWorker, Dispatch: "d-w"}
	short := f.commit[:12]
	for name, req := range map[string]TerraformRequest{
		"unknown repo":     {Repo: "other", Commit: f.commit},
		"short commit":     {Repo: "infra", Commit: short},
		"branch name":      {Repo: "infra", Commit: "main"},
		"escaping dir":     {Repo: "infra", Commit: f.commit, Dir: "../x"},
		"absolute dir":     {Repo: "infra", Commit: f.commit, Dir: "/etc"},
		"escaping varfile": {Repo: "infra", Commit: f.commit, Dir: "envs/prod", VarFiles: []string{"../../../etc/passwd"}},
		"bad workspace":    {Repo: "infra", Commit: f.commit, Workspace: "a b"},
		"flag target":      {Repo: "infra", Commit: f.commit, Targets: []string{"-destroy"}},
	} {
		if _, err := f.tf.Propose(context.Background(), a, req); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// A commit the repo does not have fails at plan time, files deleted.
	p, err := f.tf.Propose(context.Background(), a, TerraformRequest{Repo: "infra", Commit: strings.Repeat("a", 40), Dir: "envs/prod"})
	if err != nil {
		t.Fatal(err)
	}
	q := f.st.Bot("sre").Proposals[p.ID]
	if q.Status != "failed" || !strings.Contains(q.Reason, "push it first") {
		t.Fatalf("unknown commit: %s %q", q.Status, q.Reason)
	}
	// never-tier.
	m := f.cfg.Bots["sre"]
	m.Autonomy.Tiers = map[string][]string{config.TierNever: {"terraform.*"}}
	f.cfg.Bots["sre"] = m
	if _, err := f.tf.Propose(context.Background(), a, TerraformRequest{Repo: "infra", Commit: f.commit}); err == nil {
		t.Fatal("never-tier terraform proposed")
	}
}

func TestTerraformShadowAndNoChanges(t *testing.T) {
	m := testBot()
	m.Autonomy.Shadow = nil
	f := newTFFixture(t)
	mm := f.cfg.Bots["sre"]
	mm.Autonomy.Shadow = nil
	f.cfg.Bots["sre"] = mm
	p := f.propose(t, TerraformRequest{})
	if p.Status != "shadow" {
		t.Fatalf("shadow bot: %s", p.Status)
	}
	if _, err := os.Stat(f.wd(p.ID)); !os.IsNotExist(err) {
		t.Fatal("a shadow plan's files were kept (it can never be applied)")
	}
	f = newTFFixture(t)
	f.fake.planJSON = planJSON()
	if p := f.propose(t, TerraformRequest{}); p.Status != "refused" || !strings.Contains(p.Reason, "no changes") {
		t.Fatalf("no-op plan: %s %q", p.Status, p.Reason)
	}
}

// A restart fails plans/applies it interrupted and deletes every directory
// no pending proposal owns.
func TestTerraformRecover(t *testing.T) {
	f := newTFFixture(t)
	keep := f.propose(t, TerraformRequest{})
	_ = f.st.UpdateBot("sre", func(r *store.BotRecord) error {
		r.Proposals["pX"] = &store.Proposal{ID: "pX", Kind: KindTerraform, Status: "planning", Terraform: &store.TerraformPlan{}}
		return nil
	})
	_ = os.MkdirAll(f.wd("pX"), 0o700)
	_ = os.MkdirAll(f.wd("orphan"), 0o700)
	f.tf.Recover("sre")
	if st := f.st.Bot("sre").Proposals["pX"].Status; st != "failed" {
		t.Fatalf("interrupted plan: %s", st)
	}
	for _, d := range []string{"pX", "orphan"} {
		if _, err := os.Stat(f.wd(d)); !os.IsNotExist(err) {
			t.Errorf("%s kept", d)
		}
	}
	if _, err := os.Stat(f.wd(keep.ID)); err != nil {
		t.Fatal("a pending proposal's plan was deleted")
	}
}
