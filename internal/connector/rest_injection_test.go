package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/secrets"
)

// Regression tests for the rest-connector template injection: verb options
// come from workflow scope (webhook/PR/issue text), so rendered path and
// body values are attacker-controlled.

type injReq struct {
	escapedPath, rawQuery, body string
}

func injectionREST(t *testing.T, verbs string) (*Registry, chan injReq) {
	t.Helper()
	reqCh := make(chan injReq, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		reqCh <- injReq{r.URL.EscapedPath(), r.URL.RawQuery, string(b)}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)
	return buildAPIRegistry(t, `
connectors:
  api:
    type: rest
    base_url: `+srv.URL+`
    verbs:
`+verbs, secrets.New()), reqCh
}

// A path option of ../../admin/keys must not traverse: the value is escaped
// into a single path segment, and no request reaches /admin/keys.
func TestRESTPathTraversalNeutralized(t *testing.T) {
	reg, reqCh := injectionREST(t, `
      get:
        method: GET
        path: /api/items/{{.options.id}}
`)
	in, _ := reg.Get("api")
	if _, err := in.Invoke(context.Background(), "get", map[string]any{"id": "../../admin/keys"}); err != nil {
		t.Fatal(err)
	}
	req := <-reqCh
	if strings.Contains(req.escapedPath, "/../") || strings.Contains(req.escapedPath, "/admin/") {
		t.Fatalf("traversal reached the wire: %q", req.escapedPath)
	}
	if req.escapedPath != "/api/items/..%2F..%2Fadmin%2Fkeys" {
		t.Fatalf("value was not escaped as one segment: %q", req.escapedPath)
	}
}

// A bare ".." option escapes to a literal dot-segment (PathEscape leaves
// dots alone) — that render is refused outright, before any request.
func TestRESTPathDotSegmentRefused(t *testing.T) {
	reg, reqCh := injectionREST(t, `
      get:
        method: GET
        path: /api/items/{{.options.id}}/keys
`)
	in, _ := reg.Get("api")
	_, err := in.Invoke(context.Background(), "get", map[string]any{"id": ".."})
	if err == nil || !strings.Contains(err.Error(), "dot-segment") {
		t.Fatalf("dot-segment path was not refused: %v", err)
	}
	select {
	case req := <-reqCh:
		t.Fatalf("request reached the wire: %+v", req)
	default:
	}
}

// A path option of "x?admin=true&y=" must not splice a query string: the ?
// is escaped and the request carries no query.
func TestRESTQueryInjectionNeutralized(t *testing.T) {
	reg, reqCh := injectionREST(t, `
      search:
        method: GET
        path: /search/{{.options.q}}
`)
	in, _ := reg.Get("api")
	if _, err := in.Invoke(context.Background(), "search", map[string]any{"q": "x?admin=true&y="}); err != nil {
		t.Fatal(err)
	}
	req := <-reqCh
	if req.rawQuery != "" {
		t.Fatalf("injected query reached the wire: %q", req.rawQuery)
	}
	if !strings.Contains(req.escapedPath, "%3F") {
		t.Fatalf("? was not escaped: %q", req.escapedPath)
	}
}

// A body option of `","role":"admin` must not splice JSON structure: values
// are JSON-encoded by default, so the title stays a string and role stays
// what the config declared.
func TestRESTBodyJSONInjectionNeutralized(t *testing.T) {
	reg, reqCh := injectionREST(t, `
      create:
        method: POST
        path: /items
        body: '{"title": "{{.options.title}}", "role": "user"}'
`)
	in, _ := reg.Get("api")
	title := `","role":"admin`
	if _, err := in.Invoke(context.Background(), "create", map[string]any{"title": title}); err != nil {
		t.Fatal(err)
	}
	req := <-reqCh
	var sent map[string]any
	if err := json.Unmarshal([]byte(req.body), &sent); err != nil {
		t.Fatalf("body is not valid JSON: %q", req.body)
	}
	if sent["role"] != "user" {
		t.Fatalf("body structure injection succeeded: %q", req.body)
	}
	if sent["title"] != title {
		t.Fatalf("title was mangled: %v", sent["title"])
	}
}

// The explicit opt-outs still splice verbatim: `| json` emits a full JSON
// fragment, `| raw` is the non-JSON-body escape hatch. Numbers interpolate
// bare (unquoted) by default.
func TestRESTBodyEscapeOptOuts(t *testing.T) {
	reg, reqCh := injectionREST(t, `
      create:
        method: POST
        path: /items
        body: '{"payload": {{.options.payload | json}}, "n": {{.options.n}}}'
      form:
        method: POST
        path: /form
        body: 'user={{.options.user | raw}}'
`)
	in, _ := reg.Get("api")
	payload := map[string]any{"a": float64(1)}
	if _, err := in.Invoke(context.Background(), "create", map[string]any{"payload": payload, "n": 7}); err != nil {
		t.Fatal(err)
	}
	req := <-reqCh
	var sent map[string]any
	if err := json.Unmarshal([]byte(req.body), &sent); err != nil {
		t.Fatalf("body is not valid JSON: %q", req.body)
	}
	if sent["payload"].(map[string]any)["a"] != float64(1) || sent["n"] != float64(7) {
		t.Fatalf("opt-out body: %q", req.body)
	}
	if _, err := in.Invoke(context.Background(), "form", map[string]any{"user": `bo"b`}); err != nil {
		t.Fatal(err)
	}
	req = <-reqCh
	if req.body != `user=bo"b` {
		t.Fatalf("raw opt-out mangled the value: %q", req.body)
	}
}
