package gitpush

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Agent", "GIT_AUTHOR_EMAIL=agent@example.test",
		"GIT_COMMITTER_NAME=Agent", "GIT_COMMITTER_EMAIL=agent@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// fixture is a bare "remote", conductor's base clone of it, and a
// per-dispatch clone borrowing the base's objects (as gitwt makes one), on
// branch "fix/1".
type fixture struct {
	remote, base, ws, key string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	f := fixture{remote: filepath.Join(root, "remote.git"), base: filepath.Join(root, "base"), ws: filepath.Join(root, "ws")}
	run(t, root, "git", "init", "-q", "--bare", "-b", "main", f.remote)
	seed := filepath.Join(root, "seed")
	run(t, root, "git", "init", "-q", "-b", "main", seed)
	os.WriteFile(filepath.Join(seed, "a.txt"), []byte("a\n"), 0o644)
	run(t, seed, "git", "add", ".")
	run(t, seed, "git", "commit", "-q", "-m", "seed")
	run(t, seed, "git", "push", "-q", f.remote, "main", "main:fix/1", "main:other")
	run(t, root, "git", "clone", "-q", f.remote, f.base)
	// The per-dispatch clone: its own refs and config, the base's objects.
	run(t, root, "git", "init", "-q", "--template=", f.ws)
	os.MkdirAll(filepath.Join(f.ws, ".git", "objects", "info"), 0o755)
	os.WriteFile(filepath.Join(f.ws, ".git", "objects", "info", "alternates"), []byte(filepath.Join(f.base, ".git", "objects")+"\n"), 0o644)
	sha := run(t, f.base, "git", "rev-parse", "origin/fix/1")
	run(t, f.ws, "git", "update-ref", "refs/heads/fix/1", sha)
	run(t, f.ws, "git", "symbolic-ref", "HEAD", "refs/heads/fix/1")
	run(t, f.ws, "git", "reset", "-q", "--hard")
	f.key = filepath.Join(root, "signing")
	run(t, root, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "conductor-test", "-f", f.key)
	return f
}

func (f fixture) commit(t *testing.T, name, body string) string {
	t.Helper()
	os.WriteFile(filepath.Join(f.ws, name), []byte(body), 0o644)
	run(t, f.ws, "git", "add", name)
	run(t, f.ws, "git", "commit", "-q", "-m", "agent: "+name)
	return run(t, f.ws, "git", "rev-parse", "HEAD")
}

func (f fixture) req() Request {
	return Request{Workspace: f.ws, GitDir: filepath.Join(f.base, ".git"), Branch: "fix/1", Remote: f.remote,
		Signer: &Signer{Key: f.key, Name: "Op Erator", Email: "op@example.test"}}
}

// verify checks every commit in rev's range over base is SSH-signed by key,
// as `git log --show-signature` reports it: G.
func (f fixture) verifyRange(t *testing.T, base, tip string) {
	t.Helper()
	pub, _ := os.ReadFile(f.key + ".pub")
	signers := filepath.Join(t.TempDir(), "allowed_signers")
	os.WriteFile(signers, []byte("op@example.test "+string(pub)), 0o644)
	out := run(t, f.remote, "git", "-c", "gpg.ssh.allowedSignersFile="+signers, "log", "--format=%G? %ce", base+".."+tip)
	if out == "" {
		t.Fatal("no commits in range")
	}
	for _, line := range strings.Split(out, "\n") {
		if line != "G op@example.test" {
			t.Fatalf("every pushed commit is signed G by the operator; got %q in:\n%s", line, out)
		}
	}
}

func TestPushSignsTheRangeAndFastForwards(t *testing.T) {
	f := newFixture(t)
	before := run(t, f.remote, "git", "rev-parse", "fix/1")
	otherBefore := run(t, f.remote, "git", "rev-parse", "other")
	f.commit(t, "b.txt", "b\n")
	f.commit(t, "c.txt", "c\n")
	tree := run(t, f.ws, "git", "rev-parse", "HEAD^{tree}")
	res, err := Push(context.Background(), f.req())
	if err != nil {
		t.Fatal(err)
	}
	if res.Commits != 2 || !res.Signed || res.Old != before || res.UpToDate {
		t.Fatalf("result: %+v", res)
	}
	after := run(t, f.remote, "git", "rev-parse", "fix/1")
	if after != res.New {
		t.Fatalf("remote fix/1 = %s, want %s", after, res.New)
	}
	f.verifyRange(t, before, after)
	if run(t, f.remote, "git", "rev-parse", "other") != otherBefore || run(t, f.remote, "git", "rev-parse", "main") != otherBefore {
		t.Fatal("no other ref moves")
	}
	// The same trees as the agent's commits.
	if run(t, f.remote, "git", "rev-parse", after+"^{tree}") != tree {
		t.Fatal("signing changed the tree")
	}
	// The workspace moved onto the signed commits and reads as pushed.
	if !res.WorkspaceUpdated || run(t, f.ws, "git", "rev-parse", "HEAD") != after || run(t, f.ws, "git", "rev-parse", "origin/fix/1") != after {
		t.Fatalf("workspace not moved onto the push: %+v", res)
	}
	// A second push has nothing to do; a further commit builds on the signed one.
	if res2, err := Push(context.Background(), f.req()); err != nil || !res2.UpToDate {
		t.Fatalf("second push: %+v %v", res2, err)
	}
	f.commit(t, "d.txt", "d\n")
	res3, err := Push(context.Background(), f.req())
	if err != nil || res3.Commits != 1 || res3.Old != after {
		t.Fatalf("follow-up push: %+v %v", res3, err)
	}
	f.verifyRange(t, before, res3.New)
}

func TestPushRefusesADivergedRemote(t *testing.T) {
	f := newFixture(t)
	// Someone else moves fix/1 on the remote.
	other := filepath.Join(t.TempDir(), "o")
	run(t, "", "git", "clone", "-q", "-b", "fix/1", f.remote, other)
	os.WriteFile(filepath.Join(other, "x.txt"), []byte("x\n"), 0o644)
	run(t, other, "git", "add", ".")
	run(t, other, "git", "commit", "-q", "-m", "elsewhere")
	run(t, other, "git", "push", "-q", "origin", "fix/1")
	theirs := run(t, f.remote, "git", "rev-parse", "fix/1")
	f.commit(t, "b.txt", "b\n")
	_, err := Push(context.Background(), f.req())
	if !errors.Is(err, ErrDiverged) {
		t.Fatalf("a diverged remote is refused, not forced: %v", err)
	}
	if run(t, f.remote, "git", "rev-parse", "fix/1") != theirs {
		t.Fatal("the remote branch is unchanged")
	}
}

func TestPushCreatesTheBranchWhenAbsent(t *testing.T) {
	f := newFixture(t)
	r := f.req()
	r.Branch = "conductor/new-7"
	base := f.commit(t, "b.txt", "b\n")
	res, err := Push(context.Background(), r)
	if err != nil || res.Old != "" || res.Commits != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if got := run(t, f.remote, "git", "rev-parse", "conductor/new-7"); got != res.New || got == base {
		t.Fatalf("new branch at %s (signed %s, unsigned %s)", got, res.New, base)
	}
}

func TestPushUnsignedWhenNoSigner(t *testing.T) {
	f := newFixture(t)
	head := f.commit(t, "b.txt", "b\n")
	r := f.req()
	r.Signer = nil
	res, err := Push(context.Background(), r)
	if err != nil || res.Signed || res.New != head {
		t.Fatalf("%+v %v", res, err)
	}
}

// A HEAD (or ref, or object dir) the agent turned into a symlink is refused,
// never followed out of the workspace.
func TestPushNeverFollowsAgentSymlinks(t *testing.T) {
	f := newFixture(t)
	f.commit(t, "b.txt", "b\n")
	secret := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secret, []byte("0123456789012345678901234567890123456789\n"), 0o600)
	head := filepath.Join(f.ws, ".git", "HEAD")
	os.Remove(head)
	os.Symlink(secret, head)
	if _, err := Push(context.Background(), f.req()); err == nil {
		t.Fatal("a symlinked HEAD must be refused")
	}
	os.Remove(head)
	os.WriteFile(head, []byte("ref: refs/heads/fix/1\n"), 0o644)
	ref := filepath.Join(f.ws, ".git", "refs", "heads", "fix", "1")
	os.Remove(ref)
	os.Symlink(secret, ref)
	if _, err := Push(context.Background(), f.req()); err == nil {
		t.Fatal("a symlinked branch ref must be refused")
	}
}

// Objects the agent points elsewhere (alternates, a symlinked pack dir) are
// not what conductor pushes: the snapshot copies only the clone's own
// regular files.
func TestSnapshotIgnoresAlternatesAndSymlinks(t *testing.T) {
	f := newFixture(t)
	f.commit(t, "b.txt", "b\n")
	elsewhere := t.TempDir()
	os.WriteFile(filepath.Join(elsewhere, "loot"), []byte("x"), 0o644)
	os.Symlink(elsewhere, filepath.Join(f.ws, ".git", "objects", "ee"))
	dst := t.TempDir()
	if err := snapshotObjects(f.ws, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "info")); err == nil {
		t.Fatal("objects/info is never copied")
	}
	if _, err := os.Stat(filepath.Join(dst, "ee")); err == nil {
		t.Fatal("a symlinked object dir is never followed")
	}
}

func TestCheckBranch(t *testing.T) {
	ok := []string{"fix/1", "conductor/fix-42", "feature.x", "a_b", "deps/bump-go-1.26"}
	bad := []string{"", "HEAD", "@", "refs/heads/main", "-f", "--force", "a b", "fix/42 --force", "a~1", "a^b", "x:y", "x*", "x?", "x[1]",
		`a\b`, "../evil", "a/../b", "a//b", "/a", "a/", "a.", "a.lock", "a@{1}", ".hidden", "a/.b", "a\x01b", strings.Repeat("x", 201)}
	for _, b := range ok {
		if err := CheckBranch(b); err != nil {
			t.Errorf("%q should be accepted: %v", b, err)
		}
	}
	for _, b := range bad {
		if err := CheckBranch(b); err == nil {
			t.Errorf("%q should be refused", b)
		}
	}
}

func TestPushRefusesAnIllegalBranch(t *testing.T) {
	f := newFixture(t)
	for _, b := range []string{"refs/heads/main", "main:other", "HEAD", "", "-d"} {
		r := f.req()
		r.Branch = b
		if _, err := Push(context.Background(), r); err == nil {
			t.Errorf("branch %q must be refused", b)
		}
	}
}

func TestRewriteCommitMapsParentsAndDropsOldSignature(t *testing.T) {
	raw := "tree aaaa\nparent p1\nauthor A <a@x> 1 +0000\ncommitter A <a@x> 2 +0000\ngpgsig -----BEGIN SSH SIGNATURE-----\n old\n -----END SSH SIGNATURE-----\n\nmsg\n"
	out, err := rewriteCommit(context.Background(), []byte(raw), map[string]string{"p1": "P1"}, &Signer{Name: "Op", Email: "op@x"})
	if err == nil {
		t.Fatalf("signing with no key must fail: %s", out)
	}
	out, err = rewriteCommit(context.Background(), []byte(raw), map[string]string{"p1": "P1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "tree aaaa\nparent P1\nauthor A <a@x> 1 +0000\ncommitter A <a@x> 2 +0000\n\nmsg\n"
	if string(out) != want {
		t.Fatalf("got\n%q\nwant\n%q", out, want)
	}
	if got := replaceIdent("A <a@x> 2 +0000", "Op Erator", "op@x"); got != "Op Erator <op@x> 2 +0000" {
		t.Fatal(got)
	}
}
