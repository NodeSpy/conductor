package secrets

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseSourceKinds(t *testing.T) {
	for in, want := range map[string]Source{
		"${env.APP_PW}": {Kind: SourceEnv, Env: "APP_PW"},
		"env:APP_PW":    {Kind: SourceEnv, Env: "APP_PW"},
		` {{ vault "op" "Agents/App/password" }}`: {Kind: SourceVault, Vault: "op", Key: "Agents/App/password"},
		"{{ .vaults.house.app_pw }}":              {Kind: SourceVault, Vault: "house", Key: "app_pw"},
		`{{secret "house/app_pw"}}`:               {Kind: SourceSecret, Vault: "house", Key: "app_pw"},
		`{{ secret "pass/web/app" }}`:             {Kind: SourceSecret, Vault: "pass", Key: "web/app"},
		"agent@example.com":                       {Kind: SourceLiteral, Literal: "agent@example.com"},
	} {
		got, err := ParseSource(in)
		if err != nil || got != want {
			t.Errorf("ParseSource(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
}

// A setting is plain config: never a secret's source, wherever it sits.
func TestParseSourceRefusesSettings(t *testing.T) {
	for _, in := range []string{"${settings.app_pw}", "x${settings.a}y", "  ${settings.a.b}  "} {
		if _, err := ParseSource(in); !errors.Is(err, ErrPlainConfig) {
			t.Errorf("ParseSource(%q) = %v, want ErrPlainConfig", in, err)
		}
	}
	if !SettingsRef("a ${settings.b}") || SettingsRef("${env.B}") {
		t.Error("SettingsRef")
	}
}

func TestParseSourceRefusesMalformed(t *testing.T) {
	for _, in := range []string{
		`{{secret "nokey"}}`, `{{secret "/k"}}`, `{{ vault "op" }}`, "prefix ${env.X}", "env:", "env:A B",
		"op://Vault/Item/field", "{{ .vaults.house }}",
	} {
		if src, err := ParseSource(in); err == nil {
			t.Errorf("ParseSource(%q) = %+v, want an error", in, src)
		}
	}
}

func TestSourceString(t *testing.T) {
	for _, in := range []string{"${env.A}", `{{ vault "op" "V/I/f" }}`, `{{secret "house/k"}}`} {
		s, err := ParseSource(in)
		if err != nil || s.String() != in {
			t.Errorf("round trip %q: %q, %v", in, s.String(), err)
		}
	}
	if s, _ := ParseSource("hunter2-typed"); s.String() != "(typed value)" || strings.Contains(s.String(), "hunter2") || s.IsRef() {
		t.Errorf("a literal must never spell itself out: %q", s.String())
	}
}

// The shared resolver reads ${env.NAME} and {{secret "v/k"}} at use, like
// env: and {{ vault }} — generic, not a browser special case.
func TestResolverReadsNewForms(t *testing.T) {
	r := testResolver()
	var asked []string
	r.VaultRead = func(ctx context.Context, name, key string) (string, error) {
		asked = append(asked, name+"|"+key)
		return "from-vault", nil
	}
	if !IsRef("${env.GH_PAT}") || !IsRef(`{{secret "house/k"}}`) || IsRef("${settings.x}") {
		t.Fatal("IsRef must know the new forms (and not settings)")
	}
	v, err := r.Resolve(context.Background(), "${env.GH_PAT}")
	if err != nil || v != "tok-abc123" {
		t.Fatalf("${env.GH_PAT}: %q, %v", v, err)
	}
	if _, err := r.Resolve(context.Background(), "${env.MISSING}"); err == nil || !strings.Contains(err.Error(), "MISSING") {
		t.Fatalf("unset env: %v", err)
	}
	v, err = r.Resolve(context.Background(), `{{secret "house/k"}}`)
	if err != nil || v != "from-vault" || len(asked) != 1 || asked[0] != "house|k" {
		t.Fatalf("named secret: %q, %v, %v", v, err, asked)
	}
	if got := r.Redact("x tok-abc123 from-vault"); strings.Contains(got, "tok-abc123") || strings.Contains(got, "from-vault") {
		t.Fatalf("resolved values are tracked: %q", got)
	}
}

func TestLoaderEnvRef(t *testing.T) {
	if !LoaderEnvRef("${APP_PW}") || LoaderEnvRef("${env.APP_PW}") || LoaderEnvRef("plain") {
		t.Error("LoaderEnvRef")
	}
}
