//go:build unix

package jail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/grant"
)

// The files a grant pins are copied into private scratch, hashing what is
// copied, never through a symlink and never from outside the workspace; the
// argv names the copy, in a bare token and in a --flag=path token alike.
func TestStageGrantFiles(t *testing.T) {
	ws := t.TempDir()
	m := &Manager{GrantScratch: filepath.Join(t.TempDir(), "scratch")}
	d := &Dispatch{LaunchSpec: LaunchSpec{Workspace: ws}, TmpDir: t.TempDir()}
	write := func(p, body string) string {
		full := filepath.Join(ws, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(body), 0o644)
		sum, _ := grant.FileSHA256(full)
		return sum
	}
	spec := func(paths map[string]string) grant.GrantSpec {
		g := grant.GrantSpec{ID: "g1", Expiry: time.Now().Add(time.Minute)}
		for p, sum := range paths {
			g.FileDigests = append(g.FileDigests, grant.FileDigest{Path: p, SHA256: sum})
		}
		return g
	}
	planSum := write("plans/plan.tfplan", "plan")
	varSum := write("prod.tfvars", "vars")
	use, err := m.stageGrantFiles(d, spec(map[string]string{"plans/plan.tfplan": planSum, "prod.tfvars": varSum}),
		[]string{"apply", "--var-file=prod.tfvars", "plans/plan.tfplan"}, ws)
	if err != nil {
		t.Fatal(err)
	}
	defer use.cleanup()
	if len(use.files) != 2 || !strings.HasPrefix(use.args[1], "--var-file="+m.GrantScratch) || !strings.HasPrefix(use.args[2], m.GrantScratch) {
		t.Fatalf("argv names the private copies: %q", use.args)
	}
	for _, f := range use.files {
		fi, _ := os.Stat(f)
		if fi.Mode().Perm()&0o222 != 0 {
			t.Fatalf("a private copy is read-only: %s %v", f, fi.Mode())
		}
	}
	fi, _ := os.Stat(m.GrantScratch)
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("the scratch dir is private: %v", fi.Mode())
	}

	if _, err := m.stageGrantFiles(d, spec(map[string]string{"plans/plan.tfplan": varSum}), []string{"apply", "plans/plan.tfplan"}, ws); !errors.Is(err, grant.ErrDigest) {
		t.Fatalf("other content: ErrDigest, got %v", err)
	}
	outside := filepath.Join(t.TempDir(), "plan")
	os.WriteFile(outside, []byte("plan"), 0o644)
	os.Symlink(filepath.Join(ws, "plans"), filepath.Join(ws, "link"))
	os.Symlink(outside, filepath.Join(ws, "plan-link"))
	for _, p := range []string{outside, "../" + filepath.Base(outside), "link/plan.tfplan", "plan-link"} {
		if _, err := m.stageGrantFiles(d, spec(map[string]string{p: planSum}), []string{"apply", p}, ws); !errors.Is(err, grant.ErrDigest) {
			t.Errorf("%s: refused (outside the workspace, or through a symlink), got %v", p, err)
		}
	}
	// A command run from a subdirectory names the file from there, as the
	// tool will open it.
	subSum := write("plans/sub.tfplan", "sub plan")
	if use, err := m.stageGrantFiles(d, spec(map[string]string{"sub.tfplan": subSum}), []string{"apply", "sub.tfplan"}, filepath.Join(ws, "plans")); err != nil {
		t.Fatalf("from the command's cwd: %v", err)
	} else {
		use.cleanup()
	}
	entries, _ := os.ReadDir(m.GrantScratch)
	if len(entries) != 1 {
		t.Fatalf("a refused staging leaves nothing behind: %d dirs", len(entries))
	}
}

// A confined run writes the workspace through its tool's copy-on-write
// layer: a planfile an earlier confined `terraform plan -out` wrote is there,
// not in the workspace, and that copy is the one checked and run — whatever
// the command using it, since the gate hashing it at mint does not know.
func TestStageGrantFilesReadsTheConfinedLayer(t *testing.T) {
	ws := t.TempDir()
	m := &Manager{GrantScratch: filepath.Join(t.TempDir(), "scratch")}
	d := &Dispatch{LaunchSpec: LaunchSpec{Workspace: ws}, TmpDir: t.TempDir(), Dir: t.TempDir()}
	layer := func(tool string) string {
		up := filepath.Join(d.Dir, "cow-ws", tool, "up")
		os.MkdirAll(up, 0o700)
		os.MkdirAll(filepath.Join(d.Dir, "cow-ws", tool, "wk"), 0o700)
		return up
	}
	os.WriteFile(filepath.Join(ws, "plan.tfplan"), []byte("a stale workspace copy"), 0o644)
	layer("tflint")
	upper := layer("terraform")
	os.WriteFile(filepath.Join(upper, "plan.tfplan"), []byte("the plan terraform wrote"), 0o644)
	sum, _ := grant.FileSHA256(filepath.Join(upper, "plan.tfplan"))
	g := grant.GrantSpec{ID: "g1", FileDigests: []grant.FileDigest{{Path: "plan.tfplan", SHA256: sum}}}
	use, err := m.stageGrantFiles(d, g, []string{"apply", "plan.tfplan"}, ws)
	if err != nil {
		t.Fatalf("the layer's planfile is the one checked: %v", err)
	}
	use.cleanup()
	// A file only the workspace has is the workspace's.
	os.WriteFile(filepath.Join(ws, "prod.tfvars"), []byte("vars"), 0o644)
	vsum, _ := grant.FileSHA256(filepath.Join(ws, "prod.tfvars"))
	g2 := grant.GrantSpec{ID: "g2", FileDigests: []grant.FileDigest{{Path: "prod.tfvars", SHA256: vsum}}}
	if use, err := m.stageGrantFiles(d, g2, []string{"apply", "-var-file=prod.tfvars"}, ws); err != nil {
		t.Fatalf("a workspace file: %v", err)
	} else {
		use.cleanup()
	}
	// Two tools' layers holding the same path: which one is meant is unknown.
	os.WriteFile(filepath.Join(layer("tflint"), "plan.tfplan"), []byte("another"), 0o644)
	if _, err := m.stageGrantFiles(d, g, []string{"apply", "plan.tfplan"}, ws); !errors.Is(err, grant.ErrDigest) || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("two layers: refused as ambiguous, got %v", err)
	}
}

// DigestFile is the gate's hash at mint: the same read the broker makes at
// use, for the jail of the identity the grant binds to — so a grant minted
// with it stages, through the copy-on-write layer, and one for a file the
// broker will not read the same way does not.
func TestDigestFileIsWhatTheBrokerReads(t *testing.T) {
	m := &Manager{GrantScratch: filepath.Join(t.TempDir(), "scratch")}
	ws := t.TempDir()
	paseo := &Dispatch{LaunchSpec: LaunchSpec{Workspace: ws, DispatchID: "paseo-cj2.1", AgentID: "member-ops-h1"}, TmpDir: t.TempDir(), Dir: t.TempDir(), Token: "t1"}
	wws := t.TempDir()
	worker := &Dispatch{LaunchSpec: LaunchSpec{Workspace: wws, DispatchID: "run-9:fix"}, TmpDir: t.TempDir(), Dir: t.TempDir(), Token: "t2"}
	m.register(paseo)
	m.register(worker)

	upper := filepath.Join(paseo.Dir, "cow-ws", "terraform", "up")
	os.MkdirAll(upper, 0o700)
	os.WriteFile(filepath.Join(upper, "plan.tfplan"), []byte("the plan terraform wrote"), 0o644)
	os.WriteFile(filepath.Join(ws, "plan.tfplan"), []byte("a stale workspace copy"), 0o644)
	want, _ := grant.FileSHA256(filepath.Join(upper, "plan.tfplan"))
	sum, err := m.DigestFile("member-ops-h1", "plan.tfplan")
	if err != nil || sum != want {
		t.Fatalf("by agent identity, through the layer: %q %v (want %s)", sum, err, want)
	}
	g := grant.GrantSpec{ID: "g1", AgentID: "member-ops-h1", FileDigests: []grant.FileDigest{{Path: "plan.tfplan", SHA256: sum}}}
	use, err := m.stageGrantFiles(paseo, g, []string{"apply", "plan.tfplan"}, ws)
	if err != nil {
		t.Fatalf("a grant minted with DigestFile stages: %v", err)
	}
	use.cleanup()

	// A cli worker binds by its dispatch; its /tmp is its own.
	os.WriteFile(filepath.Join(worker.TmpDir, "p.bin"), []byte("tmp plan"), 0o644)
	tsum, _ := grant.FileSHA256(filepath.Join(worker.TmpDir, "p.bin"))
	if got, err := m.DigestFile("run-9:fix", filepath.Join(worker.jailTmp(), "p.bin")); err != nil || got != tsum {
		t.Fatalf("by dispatch, in the jail's /tmp: %q %v", got, err)
	}
	if _, err := m.DigestFile("run-9:fix", "plan.tfplan"); err == nil {
		t.Fatal("another agent's file is not this one's")
	}
	// "" is no identity, though a cli worker's jail has no AgentID.
	for _, bad := range []string{"", "member-ops-h2"} {
		if _, err := m.DigestFile(bad, filepath.Join(worker.jailTmp(), "p.bin")); err == nil {
			t.Fatalf("%q: no live jail, refused", bad)
		}
	}
	for _, p := range []string{"../x", "/etc/hostname"} {
		if _, err := m.DigestFile("member-ops-h1", p); err == nil {
			t.Fatalf("%s: outside the workspace, refused", p)
		}
	}
	os.Symlink(filepath.Join(ws, "plan.tfplan"), filepath.Join(ws, "link.tfplan"))
	if _, err := m.DigestFile("member-ops-h1", "link.tfplan"); err == nil {
		t.Fatal("never through a symlink")
	}

	// Two live jails of one identity: the same bytes, or refused.
	twin := &Dispatch{LaunchSpec: LaunchSpec{Workspace: ws, DispatchID: "paseo-cj2.2", AgentID: "member-ops-h1"}, TmpDir: t.TempDir(), Dir: t.TempDir(), Token: "t3"}
	m.register(twin)
	if _, err := m.DigestFile("member-ops-h1", "plan.tfplan"); err == nil {
		t.Fatal("two jails reading different bytes: refused")
	}
	tup := filepath.Join(twin.Dir, "cow-ws", "terraform", "up")
	os.MkdirAll(tup, 0o700)
	os.WriteFile(filepath.Join(tup, "plan.tfplan"), []byte("the plan terraform wrote"), 0o644)
	if got, err := m.DigestFile("member-ops-h1", "plan.tfplan"); err != nil || got != want {
		t.Fatalf("two jails reading the same bytes: %q %v", got, err)
	}
	m.unregister(twin)
	m.unregister(paseo)
	if _, err := m.DigestFile("member-ops-h1", "plan.tfplan"); err == nil {
		t.Fatal("an ended jail is not live")
	}
}

// The scratch dir is this user's and nobody else's: one someone else made
// first in a shared /tmp, or left open, is refused rather than used.
func TestGrantScratchMustBePrivate(t *testing.T) {
	base := filepath.Join(t.TempDir(), "scratch")
	os.Mkdir(base, 0o755)
	m := &Manager{GrantScratch: base}
	if _, err := m.grantScratch(); err == nil {
		t.Fatal("an open scratch dir is refused")
	}
	os.Chmod(base, 0o700)
	if _, err := m.grantScratch(); err != nil {
		t.Fatalf("a private one is used: %v", err)
	}
}

// When no grant lifts a refusal, the refusal says why from the grants near
// it (grant.Store.Near): this command's grant is used, expired or revoked —
// the newest approval's state — or a live grant approves another command.
// Another agent's grants, or none, say nothing.
func TestWhyNoGrant(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	store := grant.NewMemStoreClock(func() time.Time { return now })
	m := &Manager{Grants: store}
	apply := []string{"apply", "-input=false", "plan.tfplan"}
	p := grant.Presented{AgentID: "member-ops-h1", Tool: "terraform", Argv: grant.ParseArgv(apply)}
	mint := func(agent string, argv []string, ttl time.Duration) grant.GrantSpec {
		now = now.Add(time.Second) // each approval newer than the last
		g, err := store.Mint(ctx, grant.GrantSpec{AgentID: agent, Tool: "terraform", Argv: grant.ParseArgv(argv),
			Expiry: now.Add(ttl), SingleUse: true, ApprovedBy: "op@example.test"})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	want := func(step string, err error, id string) {
		t.Helper()
		got := m.whyNoGrant(ctx, p)
		if err == nil {
			if got != "" {
				t.Fatalf("%s: nothing to say, got %q", step, got)
			}
			return
		}
		if !strings.Contains(got, err.Error()) || !strings.Contains(got, id) {
			t.Fatalf("%s: want %v naming %s, got %q", step, err, id, got)
		}
	}

	want("no grants", nil, "")
	mint("member-ops-h2", apply, time.Hour)
	want("another agent's grant", nil, "")

	other := mint("member-ops-h1", []string{"apply", "plan.tfplan"}, time.Hour)
	want("a live grant for another command", grant.ErrMismatch, other.ID)
	if got := m.whyNoGrant(ctx, p); !strings.Contains(got, "`terraform apply plan.tfplan`") {
		t.Fatalf("the mismatch shows what was approved: %q", got)
	}
	store.Revoke(ctx, other.ID)
	want("another command's grant, not live", nil, "")

	used := mint("member-ops-h1", apply, time.Hour)
	if _, err := store.Consume(ctx, used.ID, p); err != nil {
		t.Fatal(err)
	}
	live := mint("member-ops-h1", []string{"apply", "other.tfplan"}, time.Hour)
	want("this command's grant, used — over another command's live one", grant.ErrConsumed, used.ID)
	_ = live

	expired := mint("member-ops-h1", apply, time.Minute)
	now = now.Add(2 * time.Minute)
	want("the newest approval of this command expired", grant.ErrExpired, expired.ID)

	revoked := mint("member-ops-h1", apply, time.Hour)
	store.Revoke(ctx, revoked.ID)
	want("the newest approval of this command revoked", grant.ErrRevoked, revoked.ID)
}
