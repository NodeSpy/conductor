package vaults

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The fixture's values: none may ever leave a listing.
var opFixtureSecrets = []string{"fixture-user@example.test", "Fixture-Pa55word!", "JBSWY3DPEHPK3PXP", "583201", "fixture-note-body", "fixture-api-key"}

const opListJSON = `[
 {"id":"aaaaaaaaaaaaaaaaaaaaaaaaaa","title":"Example App","category":"LOGIN","vault":{"id":"v1","name":"Agents"},
  "urls":[{"primary":true,"href":"https://app.example.test/login"}],"additional_information":"fixture-user@example.test"},
 {"id":"bbbbbbbbbbbbbbbbbbbbbbbbbb","title":"Other","category":"API_CREDENTIAL","vault":{"id":"v2","name":"Private"},"urls":[]}
]`

const opGetJSON = `{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaa","title":"Example App","category":"LOGIN","vault":{"id":"v1","name":"Agents"},
 "urls":[{"primary":true,"href":"https://app.example.test/login"}],
 "sections":[{"id":"s1","label":"Security"}],
 "fields":[
  {"id":"username","type":"STRING","purpose":"USERNAME","label":"username","value":"fixture-user@example.test","reference":"op://Agents/Example App/username"},
  {"id":"password","type":"CONCEALED","purpose":"PASSWORD","label":"password","value":"Fixture-Pa55word!","entropy":80,"password_details":{"strength":"FANTASTIC","history":["Fixture-Pa55word!"]},"reference":"op://Agents/Example App/password"},
  {"id":"totp1","type":"OTP","label":"one-time password","value":"otpauth://totp/x?secret=JBSWY3DPEHPK3PXP","totp":"583201","section":{"id":"s1","label":"Security"},"reference":"op://Agents/Example App/Security/one-time password"},
  {"id":"notes","type":"STRING","purpose":"NOTES","label":"notesPlain","value":"fixture-note-body","reference":"op://Agents/Example App/notesPlain"},
  {"id":"k","type":"CONCEALED","label":"api key","value":"fixture-api-key","reference":"op://Agents/Example App/api key"}
 ]}`

func fakeOp(t *testing.T, calls *[]string) ExecFn {
	return func(ctx context.Context, stdin string, env []string, name string, args ...string) (string, error) {
		*calls = append(*calls, name+" "+strings.Join(args, " "))
		switch {
		case len(args) >= 2 && args[0] == "item" && args[1] == "list":
			return opListJSON, nil
		case len(args) >= 3 && args[0] == "item" && args[1] == "get":
			return opGetJSON, nil
		}
		t.Fatalf("a listing ran %s %v — only item list/get", name, args)
		return "", nil
	}
}

func noFixtureValue(t *testing.T, what string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range opFixtureSecrets {
		if strings.Contains(string(b), s) {
			t.Fatalf("%s carries a fixture value %q: %s", what, s, b)
		}
	}
}

func TestOnePasswordListingNeverCarriesAValue(t *testing.T) {
	Reset()
	defer Reset()
	var calls []string
	if err := Register("op", "onepassword", &OnePasswordVault{Exec: fakeOp(t, &calls)}); err != nil {
		t.Fatal(err)
	}
	items, err := ListItems(context.Background(), "op", "")
	if err != nil || len(items) != 2 {
		t.Fatalf("list: %v %+v", err, items)
	}
	noFixtureValue(t, "the listing", items)
	if items[0].Vault != "Agents" || items[0].Title != "Example App" || items[0].URLs[0] != "https://app.example.test/login" {
		t.Fatalf("metadata: %+v", items[0])
	}
	if got, _ := ListItems(context.Background(), "op", "app.example"); len(got) != 1 {
		t.Fatalf("query by website: %+v", got)
	}
	it, err := ItemDetail(context.Background(), "op", "aaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	noFixtureValue(t, "the item detail", it)
	want := map[string]string{
		"username": `{{ vault "op" "Agents/Example App/username" }}`,
		"password": `{{ vault "op" "Agents/Example App/password" }}`,
		"otp":      `{{ vault "op" "Agents/Example App/Security/one-time password?attribute=otp" }}`,
	}
	if !reflect.DeepEqual(it.Mapping, want) {
		t.Fatalf("mapping:\n got %v\nwant %v", it.Mapping, want)
	}
	if len(it.Fields) != 5 || it.Fields[2].Section != "Security" || it.Fields[2].Type != "OTP" {
		t.Fatalf("fields: %+v", it.Fields)
	}
	for _, f := range it.Fields {
		if strings.Contains(f.Ref, "op://") {
			t.Fatalf("the retired op:// scheme in a reference: %s", f.Ref)
		}
	}
	for _, c := range calls {
		if strings.Contains(c, " read") {
			t.Fatalf("a listing read a value: %s", c)
		}
	}
}

// The types a listing returns have no place for a value: a field added
// later has to pass here first.
func TestItemMetaHasNoValueField(t *testing.T) {
	allowed := map[string]bool{"ID": true, "Vault": true, "Title": true, "Category": true, "URLs": true, "Fields": true, "Mapping": true,
		"Label": true, "Type": true, "Purpose": true, "Section": true, "Ref": true}
	for _, typ := range []reflect.Type{reflect.TypeOf(ItemMeta{}), reflect.TypeOf(FieldMeta{}), reflect.TypeOf(opItem{}), reflect.TypeOf(opField{})} {
		for i := 0; i < typ.NumField(); i++ {
			n := typ.Field(i).Name
			if !allowed[n] && n != "Reference" {
				t.Errorf("%s.%s: a listing type gained a field — is it a value?", typ.Name(), n)
			}
		}
	}
}

func TestOnePasswordItemIDIsAnID(t *testing.T) {
	var calls []string
	o := &OnePasswordVault{Exec: fakeOp(t, &calls)}
	for _, id := range []string{"--format", "Agents/Example App", "aaaaaaaaaaaaaaaaaaaaaaaaa", ""} {
		if _, err := o.ItemDetail(context.Background(), id); err == nil {
			t.Errorf("ItemDetail(%q) ran", id)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("op ran: %v", calls)
	}
}

func TestOpMappingFallbacks(t *testing.T) {
	m := opMapping([]opField{
		{Type: "EMAIL", Label: "e-mail", Reference: "op://V/I/e-mail"},
		{Type: "CONCEALED", Label: "Passphrase", Reference: "op://V/I/Passphrase"},
	})
	if m["username"] != "V/I/e-mail" || m["password"] != "V/I/Passphrase" || m["otp"] != "" {
		t.Fatalf("fallbacks: %v", m)
	}
	m = opMapping([]opField{{Type: "STRING", Label: "Login", Reference: "op://V/I/Login"}, {Type: "CONCEALED", Label: "pin", Reference: "op://V/I/pin"}})
	if m["username"] != "V/I/Login" || m["password"] != "" {
		t.Fatalf("label fallback only for password-like labels: %v", m)
	}
	if _, ok := opKey(opField{Reference: "Agents/x"}); ok {
		t.Fatal("a field without op's reference has no key")
	}
}

func TestPassListsNamesOnly(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"web/app.gpg", "web/other.gpg", "mail.gpg", ".git/HEAD", "web/notes.txt"} {
		p := filepath.Join(dir, f)
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, []byte("ENCRYPTED"), 0o600)
	}
	os.Symlink(filepath.Join(dir, "mail.gpg"), filepath.Join(dir, "web", "link.gpg"))
	p := &PassVault{Dir: dir, Exec: func(ctx context.Context, stdin string, env []string, name string, args ...string) (string, error) {
		t.Fatalf("listing ran pass %v", args)
		return "", nil
	}}
	got, err := p.List(context.Background())
	if err != nil || !reflect.DeepEqual(got, []string{"mail", "web/app", "web/other"}) {
		t.Fatalf("pass list: %v %v", got, err)
	}
	p.Prefix = "web"
	if got, _ := p.List(context.Background()); !reflect.DeepEqual(got, []string{"app", "other"}) {
		t.Fatalf("prefixed: %v", got)
	}
}

func TestHashicorpListsPathsNeverData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "LIST" || !strings.HasPrefix(r.URL.Path, "/v1/secret/metadata/") {
			t.Errorf("listing asked %s %s — only LIST on metadata", r.Method, r.URL.Path)
			http.Error(w, "no", 400)
			return
		}
		w.Write([]byte(`{"data":{"keys":["app","team/"]}}`))
	}))
	defer srv.Close()
	h := &HashicorpVault{Addr: srv.URL, Token: "t"}
	items, err := h.ListItems(context.Background(), "agents")
	if err != nil || len(items) != 2 || items[0].ID != "agents/app" || items[1].Category != "folder" || items[1].ID != "agents/team" {
		t.Fatalf("hashicorp list: %v %+v", err, items)
	}
	if _, err := h.ListItems(context.Background(), "../x"); err == nil {
		t.Fatal("a listing escaping the mount ran")
	}
}

func TestFlatVaultListsEntryNames(t *testing.T) {
	Reset()
	defer Reset()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "app_pw"), []byte("Fixture-Pa55word!"), 0o600)
	Register("files", "file", &FileVault{Dir: dir})
	items, err := ListItems(context.Background(), "files", "")
	if err != nil || len(items) != 1 || items[0].Fields[0].Ref != `{{ vault "files" "app_pw" }}` {
		t.Fatalf("flat: %v %+v", err, items)
	}
	noFixtureValue(t, "a flat listing", items)
	Register("ro", "onepasswordless", &readOnly{})
	if _, err := ListItems(context.Background(), "ro", ""); err == nil || !strings.Contains(err.Error(), "by hand") {
		t.Fatalf("a vault with no listing says to enter the reference by hand: %v", err)
	}
}

type readOnly struct{}

func (readOnly) Read(ctx context.Context, key string) (string, error) { return "x", nil }
