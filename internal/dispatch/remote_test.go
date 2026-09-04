package dispatch

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/hosts"
)

func remoteTarget() *hosts.Target {
	return &hosts.Target{Name: "gpu-box", Cfg: config.HostConfig{
		Host: "gpu01.internal", User: "ml", Key: "/k/id_ed25519",
		Env: map[string]string{"FORGE_BASE": "git://forge/"},
	}}
}

func TestPaseoCommandLocal(t *testing.T) {
	cmd := paseoCommand(context.Background(), "/usr/local/bin/paseo", nil, "ls", "--json")
	if got := strings.Join(cmd.Args, " "); got != "/usr/local/bin/paseo ls --json" {
		t.Fatalf("local argv: %q", got)
	}
}

func TestPaseoCommandRemote(t *testing.T) {
	cmd := paseoCommand(context.Background(), "paseo", remoteTarget(), "run", "fix the thing", "--json")
	argv := cmd.Args
	if argv[0] != "ssh" {
		t.Fatalf("argv[0] = %q, want ssh", argv[0])
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"-o BatchMode=yes", "-i /k/id_ed25519", "ml@gpu01.internal"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ssh argv missing %q: %s", want, joined)
		}
	}
	// The remote command is ONE trailing string: env exports + the quoted argv.
	remote := argv[len(argv)-1]
	if !strings.Contains(remote, "export FORGE_BASE='git://forge/'") {
		t.Errorf("remote command missing env export: %s", remote)
	}
	if !strings.Contains(remote, "'paseo' 'run' 'fix the thing' '--json'") {
		t.Errorf("remote command missing quoted argv: %s", remote)
	}
	// Prove the framing survives a real shell: run the remote string locally.
	out, err := exec.Command("sh", "-c",
		strings.Replace(remote, "'paseo'", "'printf'", 1)).Output()
	_ = out
	if err != nil {
		t.Fatalf("remote command string does not execute under sh: %v", err)
	}
}

// TestRemoteGatesLocalFS: the remote dispatcher must not consult local
// filesystem state for checkout reuse — a memoized dir passes without a local
// git check, and the clone parent is a remote-relative path.
func TestRemoteGatesLocalFS(t *testing.T) {
	d := New("paseo", config.Retry{}, false)
	d.Remote = remoteTarget()
	d.repoDirs["acme/x"] = "/home/ml/.conductor/checkouts/x"
	dir, err := d.resolveCheckoutDir(context.Background(), "acme/x")
	if err != nil || dir != "/home/ml/.conductor/checkouts/x" {
		t.Fatalf("remote memo not trusted: %q, %v", dir, err)
	}
	parent, err := d.cloneParentDir()
	if err != nil || parent != ".conductor/checkouts" {
		t.Fatalf("remote clone parent: %q, %v (want remote-relative)", parent, err)
	}
	local := New("paseo", config.Retry{}, false)
	lp, err := local.cloneParentDir()
	if err != nil || !strings.HasSuffix(lp, "/.conductor/checkouts") || !strings.HasPrefix(lp, "/") {
		t.Fatalf("local clone parent: %q, %v (want absolute under home)", lp, err)
	}
}
