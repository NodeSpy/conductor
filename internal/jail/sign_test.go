package jail

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// The signing broker signs only commit objects of the dispatch's own
// repository: a tree (and parents) that exist in its object store.
func TestHandleSignBindsToTheDispatchRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git missing")
	}
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", repo).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	// The empty tree exists in every repository only once written; write it.
	cmd := exec.Command("git", "--git-dir="+repo, "hash-object", "-t", "tree", "-w", "--stdin")
	cmd.Stdin = strings.NewReader("")
	tree, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	signed := 0
	m := &Manager{Signer: func(context.Context, *Dispatch, string, []byte) ([]byte, []byte, error) {
		signed++
		return []byte("SIG"), nil, nil
	}}
	d := &Dispatch{LaunchSpec: LaunchSpec{Git: &GitLayout{CommonDir: repo}}}
	call := func(payload string) Reply {
		var buf strings.Builder
		fw := newFrameWriter(&buf)
		m.handleSign(context.Background(), d, Request{Payload: []byte(payload)}, fw)
		return lastReply(t, buf.String())
	}
	own := "tree " + strings.TrimSpace(string(tree)) + "\nauthor Op <op@x> 1 +0000\ncommitter Op <op@x> 1 +0000\n\nmsg\n"
	if r := call(own); r.Refused != "" || string(r.Sig) != "SIG" {
		t.Fatalf("a commit of the dispatch's repository is signed: %+v", r)
	}
	foreign := strings.Replace(own, strings.TrimSpace(string(tree)), "0123456789abcdef0123456789abcdef01234567", 1)
	if r := call(foreign); r.Refused == "" || !strings.Contains(r.Refused, "not in the dispatch's repository") {
		t.Fatalf("a tree from elsewhere is refused: %+v", r)
	}
	withParent := strings.Replace(own, "\nauthor", "\nparent 0123456789abcdef0123456789abcdef01234567\nauthor", 1)
	if r := call(withParent); r.Refused == "" {
		t.Fatal("an unknown parent is refused")
	}
	if signed != 1 {
		t.Fatalf("the signer ran %d times, want exactly once", signed)
	}
}

func lastReply(t *testing.T, frames string) Reply {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(frames), "\n")
	var r Reply
	if err := jsonUnmarshal([]byte(lines[len(lines)-1]), &r); err != nil {
		t.Fatalf("reply: %v %q", err, frames)
	}
	return r
}
