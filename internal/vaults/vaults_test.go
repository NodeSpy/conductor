package vaults

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/secrets"
)

func resetReg(t *testing.T) {
	t.Helper()
	Reset()
	SetTaint(nil)
	t.Cleanup(func() { Reset(); SetTaint(nil) })
}

// fakeBackend is a writable in-memory backend for registry tests.
type fakeBackend struct {
	m        map[string]string
	readOnly bool
}

func (f *fakeBackend) Read(_ context.Context, key string) (string, error) {
	v, ok := f.m[key]
	if !ok {
		return "", fmt.Errorf("no entry %q", key)
	}
	return v, nil
}

type fakeWritable struct{ fakeBackend }

func (f *fakeWritable) Write(_ context.Context, key, value string) error {
	f.m[key] = value
	return nil
}

func TestRegistryAndDispatch(t *testing.T) {
	resetReg(t)
	ctx := context.Background()

	if _, err := Use(""); err == nil || !strings.Contains(err.Error(), "vault name is required") {
		t.Fatalf("empty name: %v", err)
	}
	if _, err := Use("ghost"); err == nil || !strings.Contains(err.Error(), `no vault named "ghost"`) {
		t.Fatalf("unknown: %v", err)
	}

	ro := &fakeBackend{m: map[string]string{"k": "ro-value"}}
	rw := &fakeWritable{fakeBackend{m: map[string]string{}}}
	if err := Register("op", "onepassword", ro); err != nil {
		t.Fatal(err)
	}
	if err := Register("house", "conductor", rw); err != nil {
		t.Fatal(err)
	}
	if err := Register("op", "onepassword", ro); err == nil {
		t.Fatal("duplicate register must error")
	}
	if got := Names(); len(got) != 2 || got[0] != "house" || got[1] != "op" {
		t.Fatalf("names: %v", got)
	}
	if Type("op") != "onepassword" {
		t.Fatalf("type: %q", Type("op"))
	}

	// Reads taint.
	var tainted []string
	SetTaint(func(v string) { tainted = append(tainted, v) })
	v, err := Read(ctx, "op", "k")
	if err != nil || v != "ro-value" {
		t.Fatalf("read: %q %v", v, err)
	}
	if len(tainted) != 1 || tainted[0] != "ro-value" {
		t.Fatalf("taint after read: %v", tainted)
	}
	if _, err := Read(ctx, "op", ""); err == nil || !strings.Contains(err.Error(), "key is required") {
		t.Fatalf("empty key: %v", err)
	}

	// Writes work on writable backends and taint; a read-only backend gives
	// the clear capability error.
	if err := Write(ctx, "house", "tok", "s3cret-value"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if rw.m["tok"] != "s3cret-value" {
		t.Fatalf("written: %v", rw.m)
	}
	if len(tainted) != 2 || tainted[1] != "s3cret-value" {
		t.Fatalf("taint after write: %v", tainted)
	}
	err = Write(ctx, "op", "k", "x")
	if err == nil || !strings.Contains(err.Error(), `vault "op" (onepassword) is read-only`) {
		t.Fatalf("read-only write: %v", err)
	}
}

func TestBrokenVault(t *testing.T) {
	resetReg(t)
	if err := RegisterBroken("hcv", "hashicorp", "unlock env:VAULT_TOKEN: not set"); err != nil {
		t.Fatal(err)
	}
	if Broken("hcv") == "" {
		t.Fatal("Broken must report the reason")
	}
	_, err := Read(context.Background(), "hcv", "k")
	if err == nil || !strings.Contains(err.Error(), `vault "hcv" (hashicorp) is unavailable: unlock env:VAULT_TOKEN`) {
		t.Fatalf("broken read: %v", err)
	}
	if err := Write(context.Background(), "hcv", "k", "v"); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("broken write: %v", err)
	}
}

func TestParseRef(t *testing.T) {
	cases := []struct {
		in        string
		name, key string
		ok        bool
	}{
		{`{{ vault "op" "Private/GitHub/token" }}`, "op", "Private/GitHub/token", true},
		{`{{vault "house" "gh"}}`, "house", "gh", true},
		{`  {{ vault "a" "b" }}  `, "a", "b", true},
		{`{{ .vaults.house.gh_token }}`, "house", "gh_token", true},
		{`{{ .vaults.house.a.b }}`, "house", "a.b", true},
		{`{{ vault "a" }}`, "", "", false},
		{`{{ vault "a" "b" "c" }}`, "", "", false},
		{`prefix {{ vault "a" "b" }}`, "", "", false},
		{`{{ vault "a" "b" }} suffix`, "", "", false},
		{`{{ .vaults.house }}`, "", "", false},
		{`{{ kv "s" "ns" "k" }}`, "", "", false},
		{`${VAR}`, "", "", false},
		{`plain`, "", "", false},
	}
	for _, c := range cases {
		name, key, ok := ParseRef(c.in)
		if ok != c.ok || name != c.name || key != c.key {
			t.Errorf("ParseRef(%q) = %q %q %v, want %q %q %v", c.in, name, key, ok, c.name, c.key, c.ok)
		}
		if IsRef(c.in) != c.ok {
			t.Errorf("IsRef(%q) = %v", c.in, !c.ok)
		}
	}
}

func TestBootstrapResolve(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "boot"), []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	credsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(credsDir, "conductor-vault-key"), []byte("from-creds\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := &Bootstrap{
		LookupEnv: func(k string) (string, bool) {
			switch k {
			case "BOOT":
				return "from-env", true
			case "CREDENTIALS_DIRECTORY":
				return credsDir, true
			}
			return "", false
		},
		ReadFile: os.ReadFile,
		Keyring: func(service string) string {
			if service == "custom-svc" {
				return "from-keyring"
			}
			return ""
		},
	}
	cases := []struct{ ref, want string }{
		{"", ""},
		{"env:BOOT", "from-env"},
		{"file:" + filepath.Join(dir, "boot"), "from-file"},
		{"keyring:custom-svc", "from-keyring"},
		{"creds:", "from-creds"},
		{"literal-passphrase", "literal-passphrase"},
	}
	for _, c := range cases {
		got, err := b.Resolve(c.ref)
		if err != nil || got != c.want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", c.ref, got, err, c.want)
		}
	}
	for _, bad := range []string{"env:NOPE", "env:", "file:", "file:/dev/null/nope", "keyring:absent", "creds:nope"} {
		if _, err := b.Resolve(bad); err == nil {
			t.Errorf("Resolve(%q) should error", bad)
		}
	}
	// creds: without $CREDENTIALS_DIRECTORY.
	noCreds := &Bootstrap{LookupEnv: func(string) (string, bool) { return "", false }, ReadFile: os.ReadFile, Keyring: func(string) string { return "" }}
	if _, err := noCreds.Resolve("creds:"); err == nil || !strings.Contains(err.Error(), "CREDENTIALS_DIRECTORY") {
		t.Errorf("creds without dir: %v", err)
	}
}

func TestConductorBackend(t *testing.T) {
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "vault.json")
	// Seed a vault file the backend will open (unlock ref = literal key).
	v, err := secrets.InitVault(path, func() ([]byte, error) { return []byte(key), nil }, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = v.Set("gh", "tok-1")
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}

	c := NewConductorVault(path, key, nil)
	ctx := context.Background()
	if err := c.Unlock(); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if got, err := c.Read(ctx, "gh"); err != nil || got != "tok-1" {
		t.Fatalf("read: %q %v", got, err)
	}
	if _, err := c.Read(ctx, "nope"); err == nil {
		t.Fatal("absent entry must error")
	}
	if err := c.Write(ctx, "new", "val-2"); err != nil {
		t.Fatalf("write: %v", err)
	}
	names, err := c.List(ctx)
	if err != nil || strings.Join(names, ",") != "gh,new" {
		t.Fatalf("list: %v %v", names, err)
	}
	if err := c.Delete(ctx, "gh"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := c.Delete(ctx, "gh"); err == nil {
		t.Fatal("double delete must error")
	}

	// Persistence: a fresh backend over the same file sees the write.
	c2 := NewConductorVault(path, key, nil)
	if got, err := c2.Read(ctx, "new"); err != nil || got != "val-2" {
		t.Fatalf("reopen read: %q %v", got, err)
	}

	// Wrong key fails at unlock with the vault error, not a panic.
	wrong, _ := secrets.GenerateKey()
	c3 := NewConductorVault(path, wrong, nil)
	if err := c3.Unlock(); err == nil || !strings.Contains(err.Error(), "wrong key") {
		t.Fatalf("wrong key: %v", err)
	}

	// A failing unlock ref surfaces its bootstrap error.
	c4 := NewConductorVault(path, "env:NOPE_VAULT_KEY", &Bootstrap{
		LookupEnv: func(string) (string, bool) { return "", false },
		ReadFile:  os.ReadFile,
		Keyring:   func(string) string { return "" },
	})
	if err := c4.Unlock(); err == nil || !strings.Contains(err.Error(), "NOPE_VAULT_KEY") {
		t.Fatalf("bad unlock ref: %v", err)
	}
}

func TestOnePasswordBackend(t *testing.T) {
	var gotArgs []string
	var gotEnv []string
	calls := 0
	o := &OnePasswordVault{
		Account:        "acme",
		ServiceAccount: "sa-token",
		Exec: func(_ context.Context, stdin string, env []string, name string, args ...string) (string, error) {
			calls++
			if name != "op" || stdin != "" {
				t.Fatalf("exec: %s stdin=%q", name, stdin)
			}
			gotArgs, gotEnv = args, env
			return "op-secret", nil
		},
	}
	v, err := o.Read(context.Background(), "Private/GitHub/token")
	if err != nil || v != "op-secret" {
		t.Fatalf("read: %q %v", v, err)
	}
	want := []string{"read", "-n", "op://Private/GitHub/token", "--account", "acme"}
	if strings.Join(gotArgs, " ") != strings.Join(want, " ") {
		t.Fatalf("args: %v", gotArgs)
	}
	if len(gotEnv) != 1 || gotEnv[0] != "OP_SERVICE_ACCOUNT_TOKEN=sa-token" {
		t.Fatalf("env: %v", gotEnv)
	}
	// Cached: the helper runs once.
	if _, err := o.Read(context.Background(), "Private/GitHub/token"); err != nil || calls != 1 {
		t.Fatalf("cache: calls=%d %v", calls, err)
	}
	// Read-only: no Writer face.
	if _, ok := any(o).(Writer); ok {
		t.Fatal("onepassword must not implement Writer")
	}
}

func TestPassBackend(t *testing.T) {
	store := map[string]string{"conductor/gh": "pass-secret\nmeta: x"}
	p := &PassVault{
		Prefix: "conductor",
		Exec: func(_ context.Context, stdin string, env []string, name string, args ...string) (string, error) {
			if name != "pass" {
				t.Fatalf("exec: %s", name)
			}
			switch args[0] {
			case "show":
				v, ok := store[args[1]]
				if !ok {
					return "", fmt.Errorf("pass: %s is not in the password store", args[1])
				}
				return v, nil
			case "insert":
				store[args[3]] = strings.TrimSuffix(stdin, "\n")
				return "", nil
			case "rm":
				delete(store, args[2])
				return "", nil
			}
			return "", fmt.Errorf("unexpected args %v", args)
		},
	}
	ctx := context.Background()
	// Read takes line one only.
	if v, err := p.Read(ctx, "gh"); err != nil || v != "pass-secret" {
		t.Fatalf("read: %q %v", v, err)
	}
	if err := p.Write(ctx, "new", "written"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if store["conductor/new"] != "written" {
		t.Fatalf("store: %v", store)
	}
	if err := p.Delete(ctx, "new"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := store["conductor/new"]; ok {
		t.Fatal("delete did not remove")
	}
	if _, err := p.Read(ctx, "absent"); err == nil {
		t.Fatal("absent must error")
	}
}

func TestFileBackend(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh-token"), []byte("file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "..data"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &FileVault{Dir: dir}
	ctx := context.Background()
	if v, err := f.Read(ctx, "gh-token"); err != nil || v != "file-secret" {
		t.Fatalf("read: %q %v", v, err)
	}
	if _, err := f.Read(ctx, "../etc/passwd"); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("escape: %v", err)
	}
	if _, err := f.Read(ctx, "/etc/passwd"); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("absolute: %v", err)
	}
	names, err := f.List(ctx)
	if err != nil || strings.Join(names, ",") != "gh-token" {
		t.Fatalf("list: %v %v", names, err)
	}
	if _, ok := any(f).(Writer); ok {
		t.Fatal("file must not implement Writer")
	}
}

func TestHashicorpBackend(t *testing.T) {
	// A KV v2 fake: /v1/secret/data/<path>.
	data := map[string]map[string]any{
		"ci/github": {"value": "hcv-secret", "other": "keep-me"},
	}
	var lastToken, lastNS string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastToken = r.Header.Get("X-Vault-Token")
		lastNS = r.Header.Get("X-Vault-Namespace")
		path := strings.TrimPrefix(r.URL.Path, "/v1/secret/data/")
		switch r.Method {
		case http.MethodGet:
			d, ok := data[path]
			if !ok {
				w.WriteHeader(404)
				fmt.Fprint(w, `{"errors":[]}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": d}})
		case http.MethodPost:
			var body struct {
				Data map[string]any `json:"data"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			data[path] = body.Data
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()

	h := &HashicorpVault{Addr: srv.URL, Token: "root-token", Namespace: "team-a", Client: srv.Client()}
	ctx := context.Background()

	if v, err := h.Read(ctx, "ci/github"); err != nil || v != "hcv-secret" {
		t.Fatalf("read default field: %q %v", v, err)
	}
	if v, err := h.Read(ctx, "ci/github#other"); err != nil || v != "keep-me" {
		t.Fatalf("read named field: %q %v", v, err)
	}
	if lastToken != "root-token" || lastNS != "team-a" {
		t.Fatalf("headers: token=%q ns=%q", lastToken, lastNS)
	}
	if _, err := h.Read(ctx, "ci/github#missing"); err == nil || !strings.Contains(err.Error(), `no field "missing"`) {
		t.Fatalf("missing field: %v", err)
	}
	if _, err := h.Read(ctx, "absent/path"); err == nil || !strings.Contains(err.Error(), `no field`) {
		t.Fatalf("absent path: %v", err)
	}

	// Write merges over existing fields.
	if err := h.Write(ctx, "ci/github#value", "rotated"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if data["ci/github"]["value"] != "rotated" || data["ci/github"]["other"] != "keep-me" {
		t.Fatalf("merged write: %v", data["ci/github"])
	}
	// Write to a fresh path creates it.
	if err := h.Write(ctx, "new/secret", "created"); err != nil {
		t.Fatalf("create write: %v", err)
	}
	if data["new/secret"]["value"] != "created" {
		t.Fatalf("created: %v", data["new/secret"])
	}
	if _, err := (&HashicorpVault{}).Read(ctx, "x"); err == nil || !strings.Contains(err.Error(), "addr:") {
		t.Fatalf("no addr: %v", err)
	}
}
