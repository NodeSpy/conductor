package sandbox

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// seatbeltProfile renders deny-by-default + the base essentials + one rule per
// bind (read-only vs read-write) + the network verdict.
func TestSeatbeltProfile(t *testing.T) {
	p := seatbeltProfile([]BindMount{
		{Path: "/work/wt"},            // rw (workdir)
		{Path: "/tmp/code", RO: true}, // ro (code temp dir)
		{Path: "/srv/media"},          // rw (declared fs:)
	}, true)

	must := func(sub string) {
		t.Helper()
		if !strings.Contains(p, sub) {
			t.Fatalf("profile missing %q:\n%s", sub, p)
		}
	}
	must("(version 1)")
	must("(deny default)")
	must(`(allow file-read* (literal "/"))`) // root-inode read — without it every launch aborts on macOS
	must(`(allow file-read* file-write* (subpath "/work/wt"))`)
	must(`(allow file-read* (subpath "/tmp/code"))`) // RO → no file-write*
	must(`(allow file-read* file-write* (subpath "/srv/media"))`)
	must("(deny network*)")
	if strings.Contains(p, `file-write* (subpath "/tmp/code")`) {
		t.Fatalf("RO bind must not get write:\n%s", p)
	}

	// Open network → allow, not deny.
	if op := seatbeltProfile(nil, false); !strings.Contains(op, "(allow network*)") || strings.Contains(op, "(deny network*)") {
		t.Fatalf("open network profile wrong:\n%s", op)
	}
}

// A path with a quote can't break out of the SBPL string literal.
func TestSeatbeltProfileQuoting(t *testing.T) {
	p := seatbeltProfile([]BindMount{{Path: `/tmp/a"b`}}, true)
	if !strings.Contains(p, `(subpath "/tmp/a\"b")`) {
		t.Fatalf("quote not escaped:\n%s", p)
	}
}

// On darwin, WrapLocal's namespace case renders a sandbox-exec invocation whose
// profile carries the workdir + nf.Binds allow-list — no unshare, no
// sandbox-net. (CheckGOOS is the injectable backend selector.)
func TestWrapLocalNamespaceSeatbeltOnDarwin(t *testing.T) {
	old := CheckGOOS
	CheckGOOS = "darwin"
	defer func() { CheckGOOS = old }()

	spec := FromConfig(&config.IsolationConfig{Mode: "namespace", Network: &config.IsolationNetwork{Deny: true}})
	nf := &NetForward{Binds: []BindMount{{Path: "/tmp/conductor-code-x", RO: true}}}
	wrapped, err := spec.WrapLocal([]string{"python3", "sync.py"}, "/work/wt", nil, nf)
	if err != nil {
		t.Fatalf("darwin namespace wrap: %v", err)
	}
	if len(wrapped) < 4 || wrapped[0] != "sandbox-exec" || wrapped[1] != "-p" {
		t.Fatalf("expected sandbox-exec -p <profile> …, got %v", wrapped[:min(4, len(wrapped))])
	}
	profile := wrapped[2]
	if !strings.Contains(profile, `(subpath "/work/wt")`) || !strings.Contains(profile, `(subpath "/tmp/conductor-code-x")`) {
		t.Fatalf("profile must bind workdir + nf.Binds:\n%s", profile)
	}
	if !strings.Contains(profile, "(deny network*)") {
		t.Fatalf("deny must cut the network:\n%s", profile)
	}
	if got := strings.Join(wrapped[3:], " "); got != "python3 sync.py" {
		t.Fatalf("payload argv wrong: %q", got)
	}
	// No Linux plumbing leaked in.
	if strings.Contains(strings.Join(wrapped, " "), "unshare") || strings.Contains(strings.Join(wrapped, " "), "sandbox-net") {
		t.Fatalf("darwin wrap must not use unshare/sandbox-net: %v", wrapped)
	}
}
