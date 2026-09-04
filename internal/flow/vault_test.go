package flow

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/vaults"
)

// vaultRig builds a config with one conductor vault (entry gh=hunter2-tok)
// plus the fake connector, one registry, and a runner whose Secrets resolver
// is the SAME one the vault taints into — the wiring the daemon has.
func vaultRig(t *testing.T, extraYAML string) (*testRig, *fakeState, *config.Config) {
	t.Helper()
	t.Cleanup(vaults.Reset)
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	vpath := filepath.Join(t.TempDir(), "vault.json")
	v, err := secrets.InitVault(vpath, func() ([]byte, error) { return []byte(key), nil }, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = v.Set("gh", "hunter2-tok")
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}

	cfg := loadConfig(t, `
connectors:
  svc: { type: fake }
vaults:
  house: { type: conductor, path: `+vpath+`, unlock: { key: "`+key+`" } }
`+extraYAML)
	sec := testSecrets(nil)
	reg, err := connector.Build(cfg, connector.Deps{Secrets: sec, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.Secrets = sec // the taint hook points here (SetTaint in Build)
	return rig, fake, cfg
}

// TestVaultReadTaintsThroughLaterSteps is the tainting property end to end:
// a <vault>.read output flows into a later verb's options; the DOWNSTREAM
// connector receives the real value, but the audit trail redacts it — in
// the reading step's own entry AND the later step's.
func TestVaultReadTaintsThroughLaterSteps(t *testing.T) {
	rig, fake, _ := vaultRig(t, "")
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: rt
    uses: house.read
    options: { key: gh }
  - id: post
    uses: svc.post
    options: { text: "token is {{.rt.value}}" }
`)
	runTrigger(rig, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	// The wire got the real value…
	calls := fake.snapshot()
	if len(calls) != 1 || calls[0].Opts["text"] != "token is hunter2-tok" {
		t.Fatalf("calls: %+v", calls)
	}
	// …the audit got the placeholder, in every entry that touched it.
	rig.Store.mu.Lock()
	audits := append([]map[string]any(nil), rig.Store.audits...)
	rig.Store.mu.Unlock()
	if len(audits) == 0 {
		t.Fatal("no audit entries")
	}
	for i, e := range audits {
		flat := fmt.Sprintf("%v", e)
		if strings.Contains(flat, "hunter2-tok") {
			t.Errorf("audit[%d] leaks the vault value: %v", i, e)
		}
	}
	// The verb audit for the later step still shows the surrounding text.
	found := false
	for _, e := range audits {
		if e["verb"] == "post" {
			found = true
			if opts, _ := e["options"].(map[string]any); opts != nil {
				if opts["text"] != "token is "+secrets.Placeholder {
					t.Errorf("post options in audit: %v", opts["text"])
				}
			}
		}
	}
	if !found {
		t.Fatal("no post verb audit entry")
	}
}

// TestVaultTemplateFuncAndPreload: {{ vault "house" "gh" }} resolves inline,
// and the preloaded {{.vaults.house.gh}} field form works too.
func TestVaultTemplateFuncAndPreload(t *testing.T) {
	rig, fake, _ := vaultRig(t, "")
	rig.Runner.VaultVals = vaults.PreloadListable(t.Context())
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: post
    uses: svc.post
    options:
      text: 'fn={{ vault "house" "gh" }} field={{ .vaults.house.gh }}'
`)
	runTrigger(rig, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	calls := fake.snapshot()
	if len(calls) != 1 || calls[0].Opts["text"] != "fn=hunter2-tok field=hunter2-tok" {
		t.Fatalf("calls: %+v", calls)
	}
}

// TestVaultWriteVerb: a workflow rotates a secret back into a writable vault.
func TestVaultWriteVerb(t *testing.T) {
	rig, _, _ := vaultRig(t, "")
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { uses: house.write, options: { key: rotated, value: "{{.msg}}" } }
  - { id: back, uses: house.read, options: { key: rotated } }
  - { uses: svc.post, options: { text: "{{.back.value}}" } }
`)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "new-secret-77"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	v, err := vaults.Read(t.Context(), "house", "rotated")
	if err != nil || v != "new-secret-77" {
		t.Fatalf("vault after run: %q %v", v, err)
	}
}

// TestVaultRefValidation: load-time checks — unknown vault names in the
// function and field forms, and write on a read-only vault type.
func TestVaultRefValidation(t *testing.T) {
	fdir := t.TempDir()
	vpath := filepath.Join(t.TempDir(), "vault.json")
	base := `
connectors:
  svc: { type: fake }
vaults:
  house: { type: conductor, path: ` + vpath + `, unlock: { key: "some-passphrase" } }
  files: { type: file, dir: ` + fdir + ` }
`
	valid := func(y string) error {
		vaults.Reset()
		cfg := loadConfig(t, base+y)
		reg, err := connector.Build(cfg, connector.Deps{Secrets: testSecrets(nil), Config: cfg})
		if err != nil {
			t.Fatal(err)
		}
		return Validate(cfg, reg)
	}
	t.Cleanup(vaults.Reset)
	cases := []struct{ name, yaml, wantErr string }{
		{"unknown vault in fn", `
triggers:
  - on: svc.ping
    steps: [ { uses: svc.post, options: { text: '{{ vault "ghost" "k" }}' } } ]`,
			`no vault named "ghost"`},
		{"unknown vault in field", `
triggers:
  - on: svc.ping
    steps: [ { uses: svc.post, options: { text: "{{ .vaults.ghost.k }}" } } ]`,
			`no vault named "ghost"`},
		{"write on read-only vault", `
triggers:
  - on: svc.ping
    steps: [ { uses: files.write, options: { key: k, value: v } } ]`,
			`no verb "write"`},
		{"unknown verb target", `
triggers:
  - on: svc.ping
    steps: [ { uses: ghostvault.read, options: { key: k } } ]`,
			`unknown connector "ghostvault"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := valid(tc.yaml); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
		})
	}
	// The valid shapes pass.
	if err := valid(`
triggers:
  - on: svc.ping
    steps:
      - { id: rt, uses: house.read, options: { key: gh } }
      - { uses: svc.post, options: { text: '{{ vault "house" "gh" }} {{ .vaults.house.gh }} {{.rt.value}}' } }
`); err != nil {
		t.Fatalf("valid vault refs rejected: %v", err)
	}
}
