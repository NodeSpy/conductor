package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/dashboard/auth"
	"github.com/NodeSpy/conductor/internal/vaults"
)

// memVault is a writable, deletable, listable in-memory vault backend.
type memVault struct {
	mu sync.Mutex
	m  map[string]string
}

func (v *memVault) Read(_ context.Context, k string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.m[k], nil
}
func (v *memVault) Write(_ context.Context, k, val string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.m[k] = val
	return nil
}
func (v *memVault) Delete(_ context.Context, k string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.m, k)
	return nil
}
func (v *memVault) List(_ context.Context) ([]string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []string
	for k := range v.m {
		out = append(out, k)
	}
	return out, nil
}

// roVault can only be read.
type roVault struct{}

func (roVault) Read(context.Context, string) (string, error) { return "", nil }

func registerTestVaults(t *testing.T, g *botRig) *memVault {
	t.Helper()
	// The config names both vaults, as a real one would.
	f, err := os.OpenFile(g.s.deps.ConfigPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("vaults:\n  house: { type: conductor }\n  mounted: { type: file, dir: /nonexistent }\n")
	f.Close()
	vaults.Reset()
	t.Cleanup(vaults.Reset)
	mv := &memVault{m: map[string]string{}}
	if err := vaults.Register("house", "conductor", mv); err != nil {
		t.Fatal(err)
	}
	if err := vaults.Register("mounted", "file", roVault{}); err != nil {
		t.Fatal(err)
	}
	return mv
}

// Storing a value goes to the vault only with a step-up bound to the exact
// entry and length, never comes back in any answer, and is audited without
// the value.
func TestSecretWriteNeedsAStepupAndNeverEchoes(t *testing.T) {
	g := newBotRig(t, false)
	mv := registerTestVaults(t, g)
	const value = "s3cr3t-value-never-echoed"
	path := "/api/v1/secrets/write"
	body := map[string]any{"vault": "house", "key": "pagerduty-ui", "value": value}

	rr := g.dev.do("POST", path, body)
	var refused struct {
		Stepup struct{ Purpose, Digest, Description string } `json:"stepup"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &refused)
	if rr.Code != http.StatusForbidden || refused.Stepup.Purpose != string(auth.PurposeSecretWrite) {
		t.Fatalf("write without a step-up: %d %s", rr.Code, rr.Body)
	}
	if strings.Contains(rr.Body.String(), value) {
		t.Fatalf("the refusal echoed the value: %s", rr.Body)
	}
	if len(mv.m) != 0 {
		t.Fatalf("the refused write reached the vault: %v", mv.m)
	}

	// A token confirmed for a different key doesn't cover this one.
	other := g.dev.do("POST", path, map[string]any{"vault": "house", "key": "other", "value": value})
	var otherRef struct {
		Stepup struct{ Digest string } `json:"stepup"`
	}
	_ = json.Unmarshal(other.Body.Bytes(), &otherRef)
	otherTok := sshStepupAs(t, g.dev, g.devKey, auth.PurposeSecretWrite, otherRef.Stepup.Digest)
	if rr := g.dev.doWith("POST", path, body, otherTok); rr.Code != http.StatusForbidden {
		t.Fatalf("a step-up for another entry: %d %s", rr.Code, rr.Body)
	}
	// Nor a different value length for the same key.
	if rr := g.dev.doWith("POST", path, map[string]any{"vault": "house", "key": "other", "value": value + "x"}, otherTok); rr.Code != http.StatusForbidden {
		t.Fatalf("a step-up for another length: %d %s", rr.Code, rr.Body)
	}

	tok := sshStepupAs(t, g.dev, g.devKey, auth.PurposeSecretWrite, refused.Stepup.Digest)
	rr = g.dev.doWith("POST", path, body, tok)
	if rr.Code != http.StatusOK {
		t.Fatalf("write with its own step-up: %d %s", rr.Code, rr.Body)
	}
	if strings.Contains(rr.Body.String(), value) {
		t.Fatalf("the answer echoed the value: %s", rr.Body)
	}
	if mv.m["pagerduty-ui"] != value {
		t.Fatalf("the vault didn't get the value: %v", mv.m)
	}

	entries := mustRead(t, g.auditLog)
	if !strings.Contains(entries, `"event":"dashboard.secret.write"`) || !strings.Contains(entries, `"key":"pagerduty-ui"`) {
		t.Fatalf("no audit entry for the write:\n%s", entries)
	}
	if strings.Contains(entries, value) {
		t.Fatalf("the audit trail recorded the value:\n%s", entries)
	}

	// The listing shows the new entry by name; no value anywhere.
	list := g.dev.do("GET", "/api/v1/secrets", nil)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"ref":"house/pagerduty-ui"`) || strings.Contains(list.Body.String(), value) {
		t.Fatalf("secrets listing: %d %s", list.Code, list.Body)
	}
}

// A read-only vault, or one that doesn't exist, is refused before anyone is
// asked to confirm anything.
func TestSecretWriteRefusesReadOnlyAndUnknownVaults(t *testing.T) {
	g := newBotRig(t, false)
	registerTestVaults(t, g)
	for _, tc := range []struct {
		vault string
		code  int
	}{{"mounted", http.StatusConflict}, {"nowhere", http.StatusNotFound}} {
		rr := g.dev.do("POST", "/api/v1/secrets/write", map[string]any{"vault": tc.vault, "key": "k", "value": "v"})
		if rr.Code != tc.code || strings.Contains(rr.Body.String(), "stepup") {
			t.Fatalf("%s: %d %s", tc.vault, rr.Code, rr.Body)
		}
	}
	if rr := g.dev.do("POST", "/api/v1/secrets/write", map[string]any{"vault": "house", "key": "../x y", "value": "v"}); rr.Code != http.StatusBadRequest {
		t.Fatalf("a bad key: %d %s", rr.Code, rr.Body)
	}
}

// Removing an entry also needs its own step-up, and is audited.
func TestSecretDeleteNeedsAStepup(t *testing.T) {
	g := newBotRig(t, false)
	mv := registerTestVaults(t, g)
	mv.m["old-key"] = "x"
	path := "/api/v1/secrets/delete"
	body := map[string]any{"vault": "house", "key": "old-key"}
	rr := g.dev.do("POST", path, body)
	var refused struct {
		Stepup struct{ Digest string } `json:"stepup"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &refused)
	if rr.Code != http.StatusForbidden || refused.Stepup.Digest == "" {
		t.Fatalf("delete without a step-up: %d %s", rr.Code, rr.Body)
	}
	if _, ok := mv.m["old-key"]; !ok {
		t.Fatal("the refused delete removed the entry")
	}
	tok := sshStepupAs(t, g.dev, g.devKey, auth.PurposeSecretWrite, refused.Stepup.Digest)
	if rr := g.dev.doWith("POST", path, body, tok); rr.Code != http.StatusOK {
		t.Fatalf("delete with its step-up: %d %s", rr.Code, rr.Body)
	}
	if _, ok := mv.m["old-key"]; ok {
		t.Fatal("the entry is still there")
	}
	if e := mustRead(t, g.auditLog); !strings.Contains(e, `"event":"dashboard.secret.delete"`) {
		t.Fatalf("no audit entry for the delete:\n%s", e)
	}
	if rr := g.dev.do("POST", path, map[string]any{"vault": "mounted", "key": "k"}); rr.Code != http.StatusConflict {
		t.Fatalf("delete on a read-only vault: %d %s", rr.Code, rr.Body)
	}
}
