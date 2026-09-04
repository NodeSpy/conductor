package hosts

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

func TestShellJoinAndRemoteCommand(t *testing.T) {
	if got := ShellJoin([]string{"echo", "it's a test"}); got != `'echo' 'it'\''s a test'` {
		t.Fatalf("ShellJoin: %s", got)
	}
	if got := RemoteCommand([]string{"make", "deploy"}, ""); got != `'make' 'deploy'` {
		t.Fatalf("no cwd: %s", got)
	}
	got := RemoteCommand([]string{"make"}, "/srv/app")
	if !strings.HasPrefix(got, `cd '/srv/app' && `) {
		t.Fatalf("cwd prefix: %s", got)
	}
}

func TestRemoteCommandEnv(t *testing.T) {
	got := RemoteCommandEnv([]string{"tool"}, "/wt", []string{"GH_TOKEN=se'cret", "malformed", "A=b"})
	if !strings.Contains(got, `export GH_TOKEN='se'\''cret'; `) {
		t.Fatalf("env quoting: %s", got)
	}
	if !strings.Contains(got, "export A='b'; ") || strings.Contains(got, "malformed") {
		t.Fatalf("env entries: %s", got)
	}
	if !strings.HasSuffix(got, `cd '/wt' && 'tool'`) {
		t.Fatalf("tail: %s", got)
	}
}

func TestArgvPrefix(t *testing.T) {
	c := &Client{}
	target := Target{Name: "box", Cfg: config.HostConfig{Host: "build01", User: "ci", Port: 2222}}
	prefix := c.ArgvPrefix(target)
	joined := strings.Join(prefix, " ")
	if !strings.Contains(joined, "ci@build01") || !strings.Contains(joined, "-p 2222") {
		t.Fatalf("prefix: %s", joined)
	}
	if prefix[len(prefix)-1] != "--" {
		t.Fatalf("prefix must end at the -- separator: %v", prefix)
	}
}
