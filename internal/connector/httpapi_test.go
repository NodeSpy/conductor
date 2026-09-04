package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// buildAPIRegistry builds a registry with an injected secrets resolver.
func buildAPIRegistry(t *testing.T, y string, sec *secrets.Resolver) *Registry {
	t.Helper()
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(y), &cfg); err != nil {
		t.Fatal(err)
	}
	reg, err := Build(&cfg, Deps{Secrets: sec, Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// tempVault seeds a vault with entries and returns a resolver wired to it.
func tempVault(t *testing.T, entries map[string]string) *secrets.Resolver {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vault.json")
	material, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyFn := func() ([]byte, error) { return []byte(material), nil }
	v, err := secrets.InitVault(path, keyFn, "")
	if err != nil {
		t.Fatal(err)
	}
	for k, val := range entries {
		v.Set(k, val)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	r := secrets.New()
	r.VaultPath = path
	r.VaultKey = keyFn
	return r
}

// vaultValue reads one entry back from the resolver's vault.
func vaultValue(t *testing.T, r *secrets.Resolver, name string) string {
	t.Helper()
	v, err := secrets.OpenVault(r.VaultPath, r.VaultKey)
	if err != nil {
		t.Fatal(err)
	}
	val, err := v.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	return val
}

// TestRESTVerbRequestShape: templated path/query/body/headers reach the wire;
// outputs extract with type preservation; expect: gates success.
func TestRESTVerbRequestShape(t *testing.T) {
	type gotReq struct {
		method, path, query, body, accept, tenant string
	}
	reqCh := make(chan gotReq, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		reqCh <- gotReq{r.Method, r.URL.Path, r.URL.RawQuery, string(b),
			r.Header.Get("Accept"), r.Header.Get("X-Tenant")}
		fmt.Fprint(w, `{"Invoices":[{"InvoiceID":"inv-1","Total":42.5},{"InvoiceID":"inv-2"}],"page":1}`)
	}))
	defer srv.Close()

	reg := buildAPIRegistry(t, `
connectors:
  api:
    type: rest
    base_url: `+srv.URL+`
    headers:
      Accept: application/json
      X-Tenant: "{{.options.tenant}}"
    verbs:
      list:
        method: GET
        path: /Invoices/{{.options.contact}}
        query:
          where: "id={{.options.contact}}"
          order: "Date DESC"
        output:
          invoices: "{{.response.body.Invoices}}"
          first_id: "{{ (index .response.body.Invoices 0).InvoiceID }}"
          status: "{{.response.status}}"
      create:
        method: POST
        path: /Invoices
        body: "{{ .options.invoice | json }}"
        expect: [200]
        output: { id: "{{ (index .response.body.Invoices 0).InvoiceID }}" }
`, secrets.New())
	in, _ := reg.Get("api")
	if in.DisabledReason != "" {
		t.Fatalf("disabled: %s", in.DisabledReason)
	}

	out, err := in.Invoke(context.Background(), "list", map[string]any{"contact": "c-9", "tenant": "t-1"})
	if err != nil {
		t.Fatal(err)
	}
	req := <-reqCh
	if req.method != "GET" || req.path != "/Invoices/c-9" {
		t.Fatalf("path: %+v", req)
	}
	q, _ := url.ParseQuery(req.query)
	if q.Get("where") != "id=c-9" || q.Get("order") != "Date DESC" {
		t.Fatalf("query: %s", req.query)
	}
	if req.accept != "application/json" || req.tenant != "t-1" {
		t.Fatalf("headers: %+v", req)
	}
	// Type preservation: invoices stays an array; first_id is a string.
	invoices, ok := out["invoices"].([]any)
	if !ok || len(invoices) != 2 {
		t.Fatalf("invoices output kept its type: %T %v", out["invoices"], out["invoices"])
	}
	if out["first_id"] != "inv-1" || out["status"] != 200 {
		t.Fatalf("outputs: %+v", out)
	}

	// The json filter round-trips a structured option into the body.
	inv := map[string]any{"Contact": map[string]any{"ContactID": "c-9"}, "Total": 12.5}
	if _, err := in.Invoke(context.Background(), "create", map[string]any{"invoice": inv}); err != nil {
		t.Fatal(err)
	}
	req = <-reqCh
	var sent map[string]any
	if err := json.Unmarshal([]byte(req.body), &sent); err != nil {
		t.Fatalf("body is not JSON: %q", req.body)
	}
	if sent["Total"] != 12.5 || sent["Contact"].(map[string]any)["ContactID"] != "c-9" {
		t.Fatalf("json filter body: %v", sent)
	}
}

// TestRESTExpectStatus: a status outside expect: is a verb error carrying the
// status + body; the default accepts any 2xx.
func TestRESTExpectStatus(t *testing.T) {
	var status atomic.Int64
	status.Store(202)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
		fmt.Fprint(w, `{"detail":"nope"}`)
	}))
	defer srv.Close()
	reg := buildAPIRegistry(t, `
connectors:
  api:
    type: rest
    base_url: `+srv.URL+`
    verbs:
      anytwo: { method: GET, path: /a }
      strict: { method: GET, path: /b, expect: [200] }
`, secrets.New())
	in, _ := reg.Get("api")

	if _, err := in.Invoke(context.Background(), "anytwo", nil); err != nil {
		t.Fatalf("202 satisfies the default 2xx: %v", err)
	}
	if _, err := in.Invoke(context.Background(), "strict", nil); err == nil || !strings.Contains(err.Error(), "HTTP 202") {
		t.Fatalf("202 outside expect [200]: %v", err)
	}
	status.Store(404)
	if _, err := in.Invoke(context.Background(), "anytwo", nil); err == nil || !strings.Contains(err.Error(), `"detail":"nope"`) {
		t.Fatalf("404 fails with the body tail: %v", err)
	}
}

// TestStaticAuthSchemes: bearer/basic/header land on the request.
func TestStaticAuthSchemes(t *testing.T) {
	hdr := make(chan http.Header, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr <- r.Header.Clone()
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	call := func(auth string) http.Header {
		reg := buildAPIRegistry(t, `
connectors:
  api:
    type: rest
    base_url: `+srv.URL+`
    auth:
      `+auth+`
    verbs:
      ping: { method: GET, path: / }
`, secrets.New())
		in, _ := reg.Get("api")
		if in.DisabledReason != "" {
			t.Fatalf("disabled: %s", in.DisabledReason)
		}
		if _, err := in.Invoke(context.Background(), "ping", nil); err != nil {
			t.Fatal(err)
		}
		return <-hdr
	}
	if got := call("type: bearer\n      token: tok-1").Get("Authorization"); got != "Bearer tok-1" {
		t.Fatalf("bearer: %q", got)
	}
	h := call("type: basic\n      username: u\n      password: p")
	if u, p, ok := (&http.Request{Header: h}).BasicAuth(); !ok || u != "u" || p != "p" {
		t.Fatal("basic auth")
	}
	if got := call("type: header\n      name: X-Api-Key\n      value: k-9").Get("X-Api-Key"); got != "k-9" {
		t.Fatalf("header scheme: %q", got)
	}
}

// oauthTestServer serves a token endpoint + an API endpoint that requires the
// current token.
type oauthTestServer struct {
	*httptest.Server
	tokenHits  atomic.Int64
	apiHits    atomic.Int64
	current    atomic.Value // string: the access token the API accepts
	nextRotate atomic.Value // string: refresh token to hand out ("" = keep)
	lastGrant  atomic.Value // string
	lastForm   atomic.Value // url.Values
}

func newOAuthServer(t *testing.T) *oauthTestServer {
	o := &oauthTestServer{}
	o.current.Store("at-1")
	o.nextRotate.Store("")
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		o.tokenHits.Add(1)
		_ = r.ParseForm()
		o.lastGrant.Store(r.Form.Get("grant_type"))
		o.lastForm.Store(r.Form)
		resp := map[string]any{"access_token": o.current.Load(), "expires_in": 3600}
		if rt := o.nextRotate.Load().(string); rt != "" {
			resp["refresh_token"] = rt
		}
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/api/thing", func(w http.ResponseWriter, r *http.Request) {
		o.apiHits.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+o.current.Load().(string) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	})
	o.Server = httptest.NewServer(mux)
	t.Cleanup(o.Close)
	return o
}

func oauthConnYAML(o *oauthTestServer, grant, refreshRef string) string {
	y := `
connectors:
  api:
    type: rest
    base_url: ` + o.URL + `/api
    auth:
      type: oauth2
      grant: ` + grant + `
      token_url: ` + o.URL + `/token
      client_id: cid
      client_secret: csec
      scopes: [read, offline]
`
	if refreshRef != "" {
		y += "      refresh_token: " + refreshRef + "\n"
	}
	return y + `
    verbs:
      get: { method: GET, path: /thing, output: { ok: "{{.response.body.ok}}" } }
`
}

// TestOAuth2ClientCredentialsCaching: one token fetch serves many calls; the
// client creds ride Basic auth + the scope rides the form.
func TestOAuth2ClientCredentialsCaching(t *testing.T) {
	o := newOAuthServer(t)
	reg := buildAPIRegistry(t, oauthConnYAML(o, "client_credentials", ""), secrets.New())
	in, _ := reg.Get("api")
	for i := 0; i < 3; i++ {
		if out, err := in.Invoke(context.Background(), "get", nil); err != nil || out["ok"] != true {
			t.Fatalf("call %d: %v %v", i, out, err)
		}
	}
	if o.tokenHits.Load() != 1 {
		t.Fatalf("token endpoint hits: %d (cache miss)", o.tokenHits.Load())
	}
	if o.lastGrant.Load() != "client_credentials" {
		t.Fatalf("grant: %v", o.lastGrant.Load())
	}
	form := o.lastForm.Load().(url.Values)
	if form.Get("scope") != "read offline" || form.Get("client_id") != "cid" {
		t.Fatalf("form: %v", form)
	}
}

// TestOAuth2RefreshRotationPersists: the refresh_token grant uses the vault
// value; when the provider rotates it, the NEW token is written back to the
// vault entry.
func TestOAuth2RefreshRotationPersists(t *testing.T) {
	o := newOAuthServer(t)
	o.nextRotate.Store("rt-2") // the provider rotates on use (the Xero case)
	sec := tempVault(t, map[string]string{"xero_refresh": "rt-1"})
	reg := buildAPIRegistry(t, oauthConnYAML(o, "refresh_token", "vault:xero_refresh"), sec)
	in, _ := reg.Get("api")

	if _, err := in.Invoke(context.Background(), "get", nil); err != nil {
		t.Fatal(err)
	}
	form := o.lastForm.Load().(url.Values)
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "rt-1" {
		t.Fatalf("refresh form: %v", form)
	}
	if got := vaultValue(t, sec, "xero_refresh"); got != "rt-2" {
		t.Fatalf("rotated refresh token not persisted: %q", got)
	}
}

// TestOAuth2RetryOnceOn401: a token revoked out from under the cache triggers
// exactly one refresh + retry.
func TestOAuth2RetryOnceOn401(t *testing.T) {
	o := newOAuthServer(t)
	sec := tempVault(t, map[string]string{"rt": "rt-1"})
	reg := buildAPIRegistry(t, oauthConnYAML(o, "refresh_token", "vault:rt"), sec)
	in, _ := reg.Get("api")
	if _, err := in.Invoke(context.Background(), "get", nil); err != nil {
		t.Fatal(err)
	}
	// Revoke: the API now expects at-2; the cached at-1 gets a 401, the
	// connector refreshes once and succeeds.
	o.current.Store("at-2")
	if out, err := in.Invoke(context.Background(), "get", nil); err != nil || out["ok"] != true {
		t.Fatalf("401 retry: %v %v", out, err)
	}
	if o.tokenHits.Load() != 2 {
		t.Fatalf("token fetches: %d (want initial + one refresh)", o.tokenHits.Load())
	}
	// api hits: 1 ok + 1 401 + 1 retry ok = 3.
	if o.apiHits.Load() != 3 {
		t.Fatalf("api hits: %d", o.apiHits.Load())
	}
}

// TestGraphQLVerb: variables bind with type preservation, a non-empty errors
// array fails on HTTP 200, and outputs extract from data.
func TestGraphQLVerb(t *testing.T) {
	var fail atomic.Bool
	reqCh := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		reqCh <- req
		if fail.Load() {
			fmt.Fprint(w, `{"data":null,"errors":[{"message":"customer not found"}]}`)
			return
		}
		fmt.Fprint(w, `{"data":{"orderCreate":{"order":{"id":"o-1","url":"https://shop/o-1"}}}}`)
	}))
	defer srv.Close()

	reg := buildAPIRegistry(t, `
connectors:
  shop:
    type: graphql
    endpoint: `+srv.URL+`
    auth: { type: bearer, token: tok }
    verbs:
      create_order:
        query: "mutation($cust: ID!, $lines: [LineInput!]!) { orderCreate(customerId: $cust, lines: $lines) { order { id url } } }"
        variables:
          cust: "{{.options.customer_id}}"
          lines: "{{.options.lines}}"
        output:
          id: "{{.response.data.orderCreate.order.id}}"
          url: "{{.response.data.orderCreate.order.url}}"
`, secrets.New())
	in, _ := reg.Get("shop")
	if in.DisabledReason != "" {
		t.Fatalf("disabled: %s", in.DisabledReason)
	}

	lines := []any{map[string]any{"sku": "A", "qty": 2}}
	out, err := in.Invoke(context.Background(), "create_order",
		map[string]any{"customer_id": "c-1", "lines": lines})
	if err != nil {
		t.Fatal(err)
	}
	req := <-reqCh
	if !strings.Contains(req["query"].(string), "orderCreate") {
		t.Fatal("query not sent")
	}
	vars := req["variables"].(map[string]any)
	if vars["cust"] != "c-1" {
		t.Fatalf("string variable: %v", vars)
	}
	if arr, ok := vars["lines"].([]any); !ok || len(arr) != 1 {
		t.Fatalf("lines variable kept its list type: %T", vars["lines"])
	}
	if out["id"] != "o-1" || out["url"] != "https://shop/o-1" {
		t.Fatalf("outputs: %+v", out)
	}

	// errors on HTTP 200 → verb failure naming the first message.
	fail.Store(true)
	_, err = in.Invoke(context.Background(), "create_order", map[string]any{"customer_id": "x", "lines": lines})
	<-reqCh
	if err == nil || !strings.Contains(err.Error(), "customer not found") {
		t.Fatalf("graphql errors must fail: %v", err)
	}
}

// TestPolledEventsDedup: the first poll seeds silently; later polls emit each
// new id exactly once, with the declared context extracted.
func TestPolledEventsDedup(t *testing.T) {
	var page atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items := `{"Invoices":[{"InvoiceID":"a","Total":1}]}`
		if page.Load() > 0 {
			items = `{"Invoices":[{"InvoiceID":"a","Total":1},{"InvoiceID":"b","Total":2}]}`
		}
		fmt.Fprint(w, items)
	}))
	defer srv.Close()

	reg := buildAPIRegistry(t, `
connectors:
  api:
    type: rest
    base_url: `+srv.URL+`
    verbs:
      noop: { method: GET, path: / }
    events:
      new_invoice:
        poll: 1h
        request: { path: /Invoices }
        list: "{{.response.body.Invoices}}"
        id: "{{.item.InvoiceID}}"
        context:
          title: "invoice {{.item.InvoiceID}}"
          total: "{{.item.Total}}"
`, secrets.New())
	in, _ := reg.Get("api")
	if in.DisabledReason != "" {
		t.Fatalf("disabled: %s", in.DisabledReason)
	}
	var spec config.TriggerSpec
	if err := yaml.Unmarshal([]byte("on: api.new_invoice\nname: v1"), &spec); err != nil {
		t.Fatal(err)
	}
	src, err := in.Impl.Source([]CompiledTrigger{{Index: 0, Spec: spec}})
	if err != nil || src == nil {
		t.Fatalf("source: %v %v", src, err)
	}
	poller := src.(*httpPoller)

	var got []core.Trigger
	emit := func(_ context.Context, tr core.Trigger) { got = append(got, tr) }
	seen := map[string]bool{}
	primed := false
	ev := poller.events[0]

	// First poll: seeds "a", emits nothing.
	poller.pollOnce(context.Background(), emit, ev, seen, &primed)
	if len(got) != 0 || !seen["a"] {
		t.Fatalf("first poll must seed silently: got=%d seen=%v", len(got), seen)
	}
	// Second poll: "b" is new → one trigger; "a" stays deduped.
	page.Add(1)
	poller.pollOnce(context.Background(), emit, ev, seen, &primed)
	if len(got) != 1 {
		t.Fatalf("second poll: %d triggers", len(got))
	}
	tr := got[0]
	if tr.Kind != "new_invoice" || tr.Dedup != "new_invoice\x00b" || tr.Source != "rest" {
		t.Fatalf("trigger: %+v", tr)
	}
	if tr.Title != "invoice b" || tr.Context["total"] != float64(2) {
		t.Fatalf("context extraction: title=%q ctx=%v", tr.Title, tr.Context)
	}
	if item, ok := tr.Context["item"].(map[string]any); !ok || item["InvoiceID"] != "b" {
		t.Fatalf("raw item: %v", tr.Context["item"])
	}
	// Third poll: nothing new.
	poller.pollOnce(context.Background(), emit, ev, seen, &primed)
	if len(got) != 1 {
		t.Fatalf("dedup across polls: %d triggers", len(got))
	}

	// An unknown event in a trigger is a lowering error.
	var bad config.TriggerSpec
	_ = yaml.Unmarshal([]byte("on: api.nope"), &bad)
	if _, err := in.Impl.Source([]CompiledTrigger{{Spec: bad}}); err == nil || !strings.Contains(err.Error(), `unknown rest event "nope"`) {
		t.Fatalf("unknown event: %v", err)
	}
}

// TestDeclaredValidation: structural rejections disable the connector with a
// reason naming the problem.
func TestDeclaredValidation(t *testing.T) {
	cases := []struct{ name, yaml, wantErr string }{
		{"rest no base_url", `
connectors:
  api: { type: rest, verbs: { v: { method: GET, path: / } } }`, "base_url is required"},
		{"rest verb no method/path", `
connectors:
  api:
    type: rest
    base_url: http://x
    verbs: { v: { method: GET } }`, "method: and path: are required"},
		{"rest nothing declared", `
connectors:
  api: { type: rest, base_url: http://x }`, "at least one verb or event"},
		{"rest bad template", `
connectors:
  api:
    type: rest
    base_url: http://x
    verbs: { v: { method: GET, path: "/x/{{.broken" } }`, "bad path template"},
		{"rest event missing id", `
connectors:
  api:
    type: rest
    base_url: http://x
    events: { e: { request: { path: /x }, list: "{{.response.body.X}}" } }`, "list: and id: are required"},
		{"oauth2 missing token_url", `
connectors:
  api:
    type: rest
    base_url: http://x
    auth: { type: oauth2, grant: client_credentials, client_id: c }
    verbs: { v: { method: GET, path: / } }`, "needs token_url"},
		{"oauth2 bad grant", `
connectors:
  api:
    type: rest
    base_url: http://x
    auth: { type: oauth2, grant: implicit, token_url: http://t, client_id: c }
    verbs: { v: { method: GET, path: / } }`, "grant must be"},
		{"unknown auth type", `
connectors:
  api:
    type: rest
    base_url: http://x
    auth: { type: magic }
    verbs: { v: { method: GET, path: / } }`, "auth type must be"},
		{"graphql no endpoint", `
connectors:
  shop: { type: graphql, verbs: { v: { query: "query { x }" } } }`, "endpoint is required"},
		{"graphql verb no query", `
connectors:
  shop:
    type: graphql
    endpoint: http://x
    verbs: { v: { output: { a: "{{.response.data.a}}" } } }`, "query: is required"},
	}
	for _, c := range cases {
		reg := buildAPIRegistry(t, c.yaml, secrets.New())
		var in *Instance
		for _, n := range reg.Names() {
			if n == "kv" || n == "sql" { // the always-on built-ins, never the one under test
				continue
			}
			in, _ = reg.Get(n)
		}
		if in.DisabledReason == "" || !strings.Contains(in.DisabledReason, c.wantErr) {
			t.Errorf("%s: reason %q, want %q", c.name, in.DisabledReason, c.wantErr)
		}
	}
}

// TestAuthBootstrapExchange: the interactive bootstrap builds the consent
// URL, exchanges the captured code, and stores the refresh token in the
// vault ref — the token values never print.
func TestAuthBootstrapExchange(t *testing.T) {
	var form url.Values
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.Form
		if u, p, _ := r.BasicAuth(); u != "cid" || p != "csec" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"access_token":"at-1","expires_in":1800,"refresh_token":"rt-first"}`)
	}))
	defer tokenSrv.Close()

	sec := tempVault(t, nil)
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(`
connectors:
  xero:
    type: rest
    base_url: http://api
    auth:
      type: oauth2
      grant: refresh_token
      token_url: `+tokenSrv.URL+`
      auth_url: https://login.example/authorize
      client_id: cid
      client_secret: csec
      refresh_token: vault:xero_refresh
      scopes: [accounting, offline_access]
    verbs: { v: { method: GET, path: / } }
`), &cfg); err != nil {
		t.Fatal(err)
	}

	var sb strings.Builder
	var gotConsent string
	err := AuthBootstrap(context.Background(), &cfg, sec, "xero", &sb, func(consent, state string) (string, error) {
		gotConsent = consent
		if state == "" {
			t.Error("empty state")
		}
		return "code-123", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(gotConsent)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("client_id") != "cid" ||
		q.Get("scope") != "accounting offline_access" || q.Get("redirect_uri") == "" {
		t.Fatalf("consent URL: %s", gotConsent)
	}
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "code-123" {
		t.Fatalf("exchange form: %v", form)
	}
	if got := vaultValue(t, sec, "xero_refresh"); got != "rt-first" {
		t.Fatalf("refresh token stored: %q", got)
	}
	if strings.Contains(sb.String(), "rt-first") || strings.Contains(sb.String(), "at-1") {
		t.Fatalf("token values must not print: %s", sb.String())
	}

	// Guardrails: non-vault refresh ref, non-oauth2 auth, unknown connector.
	var cfg2 config.Config
	_ = yaml.Unmarshal([]byte(`
connectors:
  plain:
    type: rest
    base_url: http://x
    auth: { type: bearer, token: t }
    verbs: { v: { method: GET, path: / } }
`), &cfg2)
	if err := AuthBootstrap(context.Background(), &cfg2, sec, "plain", io.Discard, nil); err == nil || !strings.Contains(err.Error(), "oauth2") {
		t.Fatalf("non-oauth2: %v", err)
	}
	if err := AuthBootstrap(context.Background(), &cfg, sec, "ghost", io.Discard, nil); err == nil {
		t.Fatal("unknown connector must error")
	}
}

// TestLocalCodeCapture: the localhost listener captures the redirect,
// verifies state, and answers the browser.
func TestLocalCodeCapture(t *testing.T) {
	type res struct {
		code string
		err  error
	}
	// drive starts a capture on a fresh localhost port and plays the
	// provider's redirect against it, surfacing listener errors instead of
	// dialing a dead port for the full deadline.
	drive := func(state, redirectQuery string) (res, string) {
		redirect := "http://" + freeLocalAddr(t) + "/callback"
		capture := LocalCodeCapture(redirect, io.Discard, 5*time.Second)
		done := make(chan res, 1)
		go func() {
			c, err := capture("https://login.example/consent", state)
			done <- res{c, err}
		}()
		var body string
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			select {
			case r := <-done: // listener failed before we could dial
				return r, body
			default:
			}
			resp, err := http.Get(redirect + redirectQuery)
			if err == nil {
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				body = string(b)
				return <-done, body
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("no listener on %s within deadline", redirect)
		return res{}, ""
	}

	r, body := drive("st-1", "?code=abc&state=st-1")
	if r.err != nil || r.code != "abc" {
		t.Fatalf("captured: %q %v", r.code, r.err)
	}
	if !strings.Contains(body, "Authorized") {
		t.Fatalf("browser reply: %s", body)
	}

	// A state mismatch is rejected.
	if r, _ := drive("expected", "?code=x&state=wrong"); r.err == nil {
		t.Fatal("state mismatch must error")
	}
}

// freeLocalAddr reserves a localhost port and releases it for the code under
// test to bind.
func freeLocalAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}
