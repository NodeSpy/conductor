//go:build linux

package jail

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/grant"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// An approved `terraform apply plan.tfplan` (a grant pinned to the planfile's
// content) runs once — confined, reading a private copy of the planfile, not
// the agent's workspace file — and only that once: a second use, a swapped
// planfile, a different argv and an expired grant are all refused, and the
// command never runs.
func TestJailGrantRunsTheApprovedCommandOnce(t *testing.T) {
	needJailKernel(t)
	f := newFixture(t)
	m := f.manager(t)
	tf := "#!/bin/sh\necho \"tf-args=$*\"\nfor a in \"$@\"; do case \"$a\" in *plan*) echo \"tf-plan-path=$a\"; echo \"tf-plan-sha=$(sha256sum \"$a\" | cut -c1-64)\";; esac; done\n" +
		"echo tamper >> \"$(pwd)/plan.tfplan\" 2>/dev/null && echo ws-write-landed\n"
	os.WriteFile(f.bin+"/terraform", []byte(tf), 0o755)
	look := m.LookPath
	m.LookPath = func(n string) (string, error) {
		if n == "terraform" {
			return f.bin + "/terraform", nil
		}
		return look(n)
	}
	pm := sandbox.NewProxyManager(func(string, string) {})
	defer pm.Close()
	m.HostEgress = pm.UnixEndpointLabeled
	now := time.Now()
	clock := func() time.Time { return now }
	store := grant.NewMemStoreClock(func() time.Time { return clock() })
	m.Grants = store
	m.GrantScratch = filepath.Join(f.root, "grant-scratch")

	plan := filepath.Join(f.ws, "plan.tfplan")
	approved := "the approved plan\n"
	os.WriteFile(plan, []byte(approved), 0o644)
	sum, _ := grant.FileSHA256(plan)
	mint := func(argv []string, ttl time.Duration) grant.GrantSpec {
		now = now.Add(time.Second) // each approval newer than the last
		g, err := store.Mint(context.Background(), grant.GrantSpec{
			Dispatch: "d1", Tool: "terraform", Argv: grant.ParseArgv(argv),
			FileDigests: []grant.FileDigest{{Path: "plan.tfplan", SHA256: sum}},
			Expiry:      now.Add(ttl), SingleUse: true, ApprovedBy: "op@example.test", CaseID: "case-7",
		})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	run := func(script string) string {
		t.Helper()
		l, err := m.Prepare(context.Background(), LaunchSpec{
			DispatchID: "d1", Tool: "claude-code", Workspace: f.ws, Repo: "acme/app", Number: 42, IsPR: true,
			HeadBranch: "fix/42", Label: "fix acme/app#42",
		})
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		defer l.Close()
		deps := sandbox.LocalWrapDeps{SelfExe: m.SelfExe, Confine: true, ExtraBinds: l.Binds}
		argv, env, cleanup, err := sandbox.WrapLocalCommand(sandbox.FromConfig(&config.IsolationConfig{Mode: "namespace"}),
			[]string{"/bin/sh", "-c", script}, f.ws, append(sandbox.MinimalEnv(), l.Env...), deps)
		if err != nil {
			t.Fatalf("wrap: %v", err)
		}
		defer cleanup()
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir, cmd.Env = f.ws, env
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	apply := `terraform apply -input=false -var=a=1 -var=b=2 -var-file prod.tfvars plan.tfplan; echo "exit=$?"`
	approvedArgv := []string{"apply", "-input=false", "-var=a=1", "-var=b=2", "-var-file", "prod.tfvars", "plan.tfplan"}

	if out := run(apply); !strings.Contains(out, "exit=126") || strings.Contains(out, "tf-args=") {
		t.Fatalf("no grant: refused, never run:\n%s", out)
	}
	g := mint(approvedArgv, 10*time.Minute)

	out := run(apply)
	t.Logf("granted:\n%s", out)
	if !strings.Contains(out, "exit=0") || !strings.Contains(out, "tf-plan-sha="+sum) {
		t.Fatalf("the approved command runs on the approved planfile:\n%s", out)
	}
	if strings.Contains(out, "tf-plan-path=plan.tfplan") || strings.Contains(out, "tf-plan-path="+plan) {
		t.Fatalf("it reads the private copy, not the workspace file:\n%s", out)
	}
	if strings.Contains(out, "ws-write-landed") && func() bool { b, _ := os.ReadFile(plan); return string(b) != approved }() {
		t.Fatal("the confined run's write must not reach the workspace planfile")
	}
	if got, _ := store.Get(context.Background(), g.ID); got.ID != g.ID {
		t.Fatal("the grant is the store's")
	}
	if !f.audited("host_command", g.ID) {
		t.Fatal("the run is audited with its grant")
	}

	if out := run(apply); !strings.Contains(out, "exit=126") || !strings.Contains(out, grant.ErrConsumed.Error()) || strings.Contains(out, "tf-args=") {
		t.Fatalf("a second use is refused (ErrConsumed):\n%s", out)
	}

	mint(approvedArgv, 10*time.Minute)
	os.WriteFile(plan, []byte("a swapped plan\n"), 0o644)
	if out := run(apply); !strings.Contains(out, "exit=126") || !strings.Contains(out, grant.ErrDigest.Error()) || strings.Contains(out, "tf-args=") {
		t.Fatalf("a swapped planfile is refused (ErrDigest), and the grant is not burnt:\n%s", out)
	}
	os.WriteFile(plan, []byte(approved), 0o644)
	// grant.ParseArgv: words (and a bare flag's value) keep their order; a
	// self-contained -k=v flag is order-free across names, ordered within
	// one name.
	for _, other := range []string{
		`terraform apply -input=false -var=a=1 -var=b=2 plan.tfplan -var-file prod.tfvars; echo "exit=$?"`,             // words reordered
		`terraform apply -input=false -var=b=2 -var=a=1 -var-file prod.tfvars plan.tfplan; echo "exit=$?"`,             // one flag's values reordered
		`terraform apply -input=false -var=a=1 -var=b=2 -lock=false -var-file prod.tfvars plan.tfplan; echo "exit=$?"`, // an extra flag
		`terraform apply -input=false -var=a=1 -var=b=2 -var-file prod.tfvars plan.tfplan extra; echo "exit=$?"`,       // an extra word
		`terraform apply -input=true -var=a=1 -var=b=2 -var-file prod.tfvars plan.tfplan; echo "exit=$?"`,              // another value
		`terraform apply -input false -var=a=1 -var=b=2 -var-file prod.tfvars plan.tfplan; echo "exit=$?"`,             // a bare flag and a word
	} {
		if out := run(other); !strings.Contains(out, "exit=126") || !strings.Contains(out, grant.ErrMismatch.Error()) || strings.Contains(out, "tf-args=") {
			t.Fatalf("a different argv is not the approved command (ErrMismatch):\n%s\n%s", other, out)
		}
	}
	// Distinct -k=v flags in another order are the same command (to the
	// tool, and to grant.ParseArgv): it runs, once.
	if out := run(`terraform apply -var=a=1 -var=b=2 -input=false -var-file prod.tfvars plan.tfplan; echo "exit=$?"`); !strings.Contains(out, "exit=0") {
		t.Fatalf("distinct flags reordered are the approved command:\n%s", out)
	}
	mint(approvedArgv, 10*time.Minute)
	if out := run(apply); !strings.Contains(out, "exit=0") {
		t.Fatalf("the unburnt grant still runs the approved command:\n%s", out)
	}

	mint(approvedArgv, time.Minute)
	now = now.Add(2 * time.Minute)
	if out := run(apply); !strings.Contains(out, "exit=126") || !strings.Contains(out, grant.ErrExpired.Error()) || strings.Contains(out, "tf-args=") {
		t.Fatalf("an expired grant is refused (ErrExpired):\n%s", out)
	}
}

// audited reports an event of type typ that names the grant id.
func (f *fixture) audited(typ, grantID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.events {
		if e.Type == typ && e.Fields["grant"] == grantID && e.Fields["approved_by"] != nil {
			return true
		}
	}
	return false
}

// Live (real terraform; CONDUCTOR_LIVE_TERRAFORM=1): the agent plans
// (confined; the planfile lands in the dispatch's copy-on-write layer), the
// operator approves the apply of exactly that planfile, and the apply runs
// once — the real `terraform apply` of the approved plan — and never again,
// nor for a re-planned (swapped) planfile, another argv, or after expiry.
func TestJailLiveTerraformGrant(t *testing.T) {
	if os.Getenv("CONDUCTOR_LIVE_TERRAFORM") == "" {
		t.Skip("live: CONDUCTOR_LIVE_TERRAFORM=1")
	}
	needJailKernel(t)
	tfBin, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform missing")
	}
	f := newFixture(t)
	m := f.manager(t)
	os.WriteFile(filepath.Join(f.ws, "main.tf"), []byte(`terraform {
  required_providers {
    local = { source = "hashicorp/local" }
  }
}
variable "msg" {
  default = "approved"
}
resource "local_file" "out" {
  filename = "${path.module}/applied.txt"
  content  = var.msg
}
`), 0o644)
	look := m.LookPath
	m.LookPath = func(n string) (string, error) {
		if n == "terraform" {
			return tfBin, nil
		}
		return look(n)
	}
	pm := sandbox.NewProxyManager(nil)
	defer pm.Close()
	m.HostEgress = pm.UnixEndpointLabeled
	now := time.Now()
	store := grant.NewMemStoreClock(func() time.Time { return now })
	m.Grants = store
	allow := []*config.IsolationConfig{{Host: map[string]*config.HostCommand{"terraform": {Allow: []string{"init *", "plan *"}}}}}
	l, err := m.Prepare(context.Background(), LaunchSpec{
		DispatchID: "d1", Tool: "claude-code", Workspace: f.ws, Repo: "acme/app", Number: 42, IsPR: true,
		HeadBranch: "fix/42", Label: "fix acme/app#42", Layers: allow,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	run := func(script string) string {
		t.Helper()
		deps := sandbox.LocalWrapDeps{SelfExe: m.SelfExe, Confine: true, ExtraBinds: l.Binds}
		argv, env, cleanup, err := sandbox.WrapLocalCommand(sandbox.FromConfig(&config.IsolationConfig{Mode: "namespace"}),
			[]string{"/bin/sh", "-c", script}, f.ws, append(sandbox.MinimalEnv(), l.Env...), deps)
		if err != nil {
			t.Fatalf("wrap: %v", err)
		}
		defer cleanup()
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir, cmd.Env = f.ws, env
		out, _ := cmd.CombinedOutput()
		t.Logf("$ %s\n%s", script, out)
		return string(out)
	}
	layerPlan := filepath.Join(l.Dispatch.Dir, "cow-ws", "terraform", "up", "plan.tfplan")
	mint := func(sum string, ttl time.Duration) grant.GrantSpec {
		now = now.Add(time.Second) // each approval newer than the last
		g, err := store.Mint(context.Background(), grant.GrantSpec{
			Dispatch: "d1", Tool: "terraform", Argv: grant.ParseArgv([]string{"apply", "-input=false", "-no-color", "plan.tfplan"}),
			FileDigests: []grant.FileDigest{{Path: "plan.tfplan", SHA256: sum}},
			Expiry:      now.Add(ttl), SingleUse: true, ApprovedBy: "op@example.test", CaseID: "case-live",
		})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	apply := `terraform apply -input=false -no-color plan.tfplan; echo "exit=$?"`

	if out := run(`terraform init -input=false -no-color >/dev/null; echo "init=$?"; terraform plan -input=false -no-color -out=plan.tfplan | tail -2; echo "plan=$?"`); !strings.Contains(out, "init=0") || !strings.Contains(out, "plan=0") {
		t.Fatalf("init + plan:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(f.ws, "plan.tfplan")); err == nil {
		t.Fatal("the planfile stays in the copy-on-write layer, not the workspace")
	}
	// The gate's hash at mint (DigestFile, through the live jail): the plan
	// in the layer, the bytes the broker will read.
	approvedSum, err := m.DigestFile("d1", "plan.tfplan")
	if want, _ := grant.FileSHA256(layerPlan); err != nil || approvedSum != want {
		t.Fatalf("the plan in the layer: %q %v (want %s)", approvedSum, err, want)
	}
	if out := run(apply); !strings.Contains(out, "exit=126") {
		t.Fatalf("no grant: refused:\n%s", out)
	}
	g := mint(approvedSum, 10*time.Minute)
	out := run(apply + `; cat applied.txt; echo`)
	if !strings.Contains(out, "Apply complete! Resources: 1 added") || !strings.Contains(out, "exit=0") {
		t.Fatalf("the approved apply runs:\n%s", out)
	}
	if !f.audited("host_command", g.ID) {
		t.Fatal("audited with its grant")
	}
	if _, err := os.Stat(filepath.Join(f.ws, "applied.txt")); err == nil {
		t.Fatal("the confined apply's file lands in the layer, not the workspace")
	}
	if out := run(apply); !strings.Contains(out, "exit=126") || !strings.Contains(out, grant.ErrConsumed.Error()) {
		t.Fatalf("second use: ErrConsumed:\n%s", out)
	}
	// Re-planned (a different plan, same name): the approval does not cover it.
	run(`terraform plan -input=false -no-color -var msg=swapped -out=plan.tfplan >/dev/null; echo "replan=$?"`)
	mint(approvedSum, 10*time.Minute)
	if out := run(apply); !strings.Contains(out, "exit=126") || !strings.Contains(out, grant.ErrDigest.Error()) {
		t.Fatalf("a swapped planfile: ErrDigest:\n%s", out)
	}
	swappedSum, _ := m.DigestFile("d1", "plan.tfplan")
	mint(swappedSum, 10*time.Minute)
	for _, other := range []string{
		`terraform apply -input=false plan.tfplan -no-color; echo "exit=$?"`,             // words reordered
		`terraform apply -input=false -no-color -lock=false plan.tfplan; echo "exit=$?"`, // an extra flag
	} {
		if out := run(other); !strings.Contains(out, "exit=126") || !strings.Contains(out, grant.ErrMismatch.Error()) || strings.Contains(out, "Apply complete") {
			t.Fatalf("another argv is not the approved command (ErrMismatch):\n%s", out)
		}
	}
	now = now.Add(11 * time.Minute)
	if out := run(apply); !strings.Contains(out, "exit=126") || !strings.Contains(out, grant.ErrExpired.Error()) || strings.Contains(out, "Apply complete") {
		t.Fatalf("an expired grant is refused (ErrExpired):\n%s", out)
	}
}
