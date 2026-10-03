package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/plugin"
)

// describeLogEntry mirrors test/plugins/acme-instance's own type: one line the
// fixture appends to the file its connection's "describe_log" field names,
// recording which OS process (pid) answered plugin.describe {instance} for
// which configured instance, with what connection.
type describeLogEntry struct {
	Pid      int            `json:"pid"`
	Instance string         `json:"instance"`
	Config   map[string]any `json:"config"`
}

func readDescribeLog(t *testing.T, path string) []describeLogEntry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open describe log: %v", err)
	}
	defer f.Close()
	var out []describeLogEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e describeLogEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("decode describe log line %q: %v", sc.Text(), err)
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestDescribeInstancesForInstallUsesOnePerInstanceProcess is the finding-1
// regression: describeInstancesForInstall must probe each live instance in
// its OWN process, never one shared process serving every instance's
// (secret-bearing) connection in turn.
//
// Each instance's connection names the SAME describe_log file via its own
// "token" (a stand-in secret) — the acme-instance fixture appends one line
// per DescribeInstance call, tagged with its own os.Getpid(). A shared
// process would log BOTH instances' tokens under the identical pid; one
// process per instance logs each token under a DIFFERENT pid.
func TestDescribeInstancesForInstallUsesOnePerInstanceProcess(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	bin, sum := buildAcmeInstance(t, "v1")

	logPath := filepath.Join(t.TempDir(), "describe.log")
	spec := plugin.Spec{
		Name: "acme-instance", Kind: plugin.KindConnector, Provides: "acme-instance",
		BinPath: bin, Sha256: sum,
		Instances: map[string]config.ConnectorGrant{
			"instA": {}, "instB": {},
		},
	}
	instances := []connector.PluginInstanceDecl{
		{Instance: "instA", Connection: map[string]any{"token": "secretA", "describe_log": logPath}},
		{Instance: "instB", Connection: map[string]any{"token": "secretB", "describe_log": logPath}},
	}

	out, err := describeInstancesForInstall(context.Background(), spec, instances)
	if err != nil {
		t.Fatalf("describeInstancesForInstall: %v", err)
	}
	if out["instA"] == nil || out["instB"] == nil {
		t.Fatalf("expected both instances to answer plugin.describe {instance}: %+v", out)
	}

	entries := readDescribeLog(t, logPath)
	if len(entries) != 2 {
		t.Fatalf("want 2 describe calls logged (one per instance), got %d: %+v", len(entries), entries)
	}

	byInstance := map[string]describeLogEntry{}
	pids := map[int]bool{}
	for _, e := range entries {
		byInstance[e.Instance] = e
		pids[e.Pid] = true
	}
	// The cross-instance-exposure check: each instance's probe ran in its OWN
	// process (distinct pid), and that process never saw the OTHER
	// instance's token — a single shared process would both share one pid
	// across the two log lines AND (irrelevantly, since the call always gets
	// the right config) still violate the "own process" guarantee the pid
	// count alone already disproves.
	if len(pids) != 2 {
		t.Fatalf("want 2 distinct probe processes (one per instance), got %d distinct pid(s) across %+v — a shared process serves every sibling instance's connection, including its secrets",
			len(pids), entries)
	}
	a, ok := byInstance["instA"]
	if !ok || a.Config["token"] != "secretA" {
		t.Fatalf("instA's own process did not see instA's own token: %+v", a)
	}
	b, ok := byInstance["instB"]
	if !ok || b.Config["token"] != "secretB" {
		t.Fatalf("instB's own process did not see instB's own token: %+v", b)
	}
	if a.Pid == b.Pid {
		t.Fatalf("instA and instB were probed in the SAME process (pid %d) — one process saw both instances' connections, secrets included", a.Pid)
	}
}
