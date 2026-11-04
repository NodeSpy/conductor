package plugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// A declared fs: may not be /, the home directory or a parent of it, or
// reach into (or contain) conductor's config and state dirs — unless the
// operator's own fs: names the same path. A path inside the home
// directory is allowed.
func TestCheckDeclaredFS(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := filepath.Join(home, ".local", "state", "conductor")
	conf := filepath.Join(home, ".config", "conductor")
	other := t.TempDir()
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(other, "rootlink")
	if err := os.Symlink("/", link); err != nil {
		t.Fatal(err)
	}
	stateLink := filepath.Join(other, "statelink")
	if err := os.Symlink(state, stateLink); err != nil {
		t.Fatal(err)
	}
	protected := []string{state, conf}
	for _, tc := range []struct {
		name     string
		declared string
		operator []string
		refused  bool
	}{
		{"root", "/", nil, true},
		{"root rw", "/:rw", nil, true},
		{"home", home, nil, true},
		{"home via ~", "~", nil, true},
		{"parent of home", filepath.Dir(home), nil, true},
		{"state dir", state, nil, true},
		{"inside the state dir", filepath.Join(state, "plugins"), nil, true},
		{"config dir parent", filepath.Join(home, ".config"), nil, true},
		{"inside the config dir", filepath.Join(conf, "secrets"), nil, true},
		{"a link to /", link, nil, true},
		{"a link to the state dir", stateLink, nil, true},
		{"inside home", "~/.config/gh", nil, false},
		{"elsewhere", other, nil, false},
		{"operator names it", home, []string{"~"}, false},
		{"operator names it rw", "/", []string{"/:rw"}, false},
		{"operator names another path", home, []string{filepath.Join(home, "x")}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckDeclaredFS("p", []string{tc.declared}, tc.operator, protected)
			if (err != nil) != tc.refused {
				t.Fatalf("CheckDeclaredFS(%q, operator %v) = %v, want refused=%v", tc.declared, tc.operator, err, tc.refused)
			}
			if err != nil && !strings.Contains(err.Error(), "own fs:") {
				t.Fatalf("the refusal does not say how to grant it: %v", err)
			}
		})
	}
}

// Red-team regression (phase 4, H2): a plugin declaring a sensitive host
// dir under fs: had it bound into its jail. Now the launch is refused —
// under on as under required, never falling back to no jail — and --caps
// shows it as refused.
func TestDeclaredFSBeyondCapRefusedAtLaunch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	s := Spec{Name: "evil", Kind: KindConnector, BinPath: bin, Manifest: Manifest{FS: []string{home}}}
	for _, policy := range []JailPolicy{JailOn, JailRequired} {
		for _, probe := range []string{"", "no user namespaces here"} {
			sd := SandboxDeps{Jail: policy, StagingRoot: t.TempDir(), JailProbe: func() string { return probe }}
			_, cleanup, conf, err := buildCommand(s, sd)
			if err == nil {
				cleanup()
				t.Fatalf("policy %q probe %q: launched (%s)", policy, probe, conf.Mode)
			}
			if plan := PlannedConfinement(s, sd); plan.Mode != "refused" || !strings.Contains(plan.String(), "declares fs: "+home) {
				t.Fatalf("policy %q probe %q: --caps shows %q", policy, probe, plan.String())
			}
		}
	}
	// The operator naming the path in the entry's own fs: is the grant.
	granted := s
	granted.FS = []string{home}
	if plan := PlannedConfinement(granted, SandboxDeps{Jail: JailOn, JailProbe: func() string { return "" }}); plan.Mode != "jail" {
		t.Fatalf("with the operator's fs: it is %q", plan.String())
	}
}

// At install, a declared fs: beyond the cap fails the install: no record
// is kept for it.
func TestDeclaredFSBeyondCapRefusedAtInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	st := stateAt(t)
	ref := refFor(t, config.UseKindConnector, "acme/conductor-plugins/jira@^1.0")
	api := stubFor("jira", "jira/v1.0.0")
	opts := Options{Describe: func(_ context.Context, _ Spec) (*Decl, error) {
		d := &Decl{Type: "jira"}
		d.Capabilities.FS = []string{"/"}
		return d, nil
	}}
	res, err := Reconcile(map[string]config.PluginRef{ref.Key(): ref}, st,
		&config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}, api, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Action != ActionFailed || res[0].Err == nil || !strings.Contains(res[0].Err.Error(), "declares fs: /") {
		t.Fatalf("resolution = %+v, want failed naming the fs: entry", res)
	}
	if _, ok := st.Get("connectors/jira"); ok {
		t.Fatal("the refused install was recorded")
	}
	// The same plugin with the operator's grant installs.
	ref.FS = []string{"/"}
	res, err = Reconcile(map[string]config.PluginRef{ref.Key(): ref}, stateAt(t),
		&config.PackTrustConfig{Allow: []string{"github.com/acme/*"}}, api, opts)
	if err != nil || len(res) != 1 || res[0].Action != ActionInstalled {
		t.Fatalf("with the operator's fs: the install = %+v %v", res, err)
	}
}

// The per-instance install path (instances resolving to their own
// versions) applies the same cap.
func TestDeclaredFSBeyondCapRefusedAtInstancedInstall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := stateAt(t)
	ref := twoInstanceRef(t)
	opts := Options{AllowUnlisted: true, Describe: func(_ context.Context, _ Spec) (*Decl, error) {
		d := &Decl{Type: "jira"}
		d.Capabilities.FS = []string{"/"}
		return d, nil
	}}
	res, err := Reconcile(map[string]config.PluginRef{ref.Key(): ref}, st, nil, stubFor("jira", "jira/v1.0.0"), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 {
		t.Fatal("no resolutions")
	}
	for _, r := range res {
		if r.Action != ActionFailed || r.Err == nil || !strings.Contains(r.Err.Error(), "declares fs: /") {
			t.Fatalf("resolution = %+v, want failed naming the fs: entry", r)
		}
	}
}
