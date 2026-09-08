package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// postSkill posts one op to an HTTPHandler test server with the given bearer.
func postSkill(t *testing.T, srv *httptest.Server, bearer string, req IPCRequest) (int, IPCResponse) {
	t.Helper()
	body, _ := json.Marshal(req)
	hreq, err := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		hreq.Header.Set("Authorization", bearer)
	}
	resp, err := srv.Client().Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out IPCResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestHTTPHandlerAuthAndVerb(t *testing.T) {
	var ranWith string
	SetLiveOps(LiveOps{
		Identify: func(token string, _ Peer) (Source, int, bool) {
			if token == "good" {
				return Source{Agent: "fixer", Repo: "o/r", Trigger: "review"}, 7, true
			}
			return Source{}, 0, false
		},
		RunVerb: func(_ context.Context, token, uses string, _ map[string]any, _ Peer) (map[string]any, error) {
			ranWith = token
			return map[string]any{"uses": uses, "ok": true}, nil
		},
	})
	t.Cleanup(func() { SetLiveOps(LiveOps{}) })

	srv := httptest.NewServer(HTTPHandler(nil, nil, nil))
	t.Cleanup(srv.Close)

	// A valid bearer + verb op runs, and the token comes from the HEADER — a
	// different token in the body must not be what the broker sees.
	code, resp := postSkill(t, srv, "Bearer good", IPCRequest{Op: "verb", Uses: "gh.comment", Token: "body-spoof"})
	if code != http.StatusOK || !resp.OK {
		t.Fatalf("verb with good bearer: code=%d resp=%+v", code, resp)
	}
	if ranWith != "good" {
		t.Fatalf("RunVerb saw token %q, want the header token %q (body token must be ignored)", ranWith, "good")
	}

	// Missing bearer → 401, no op runs.
	if code, resp := postSkill(t, srv, "", IPCRequest{Op: "verb", Uses: "gh.comment"}); code != http.StatusUnauthorized || resp.Error == "" {
		t.Fatalf("missing bearer: code=%d resp=%+v", code, resp)
	}

	// token_claim is local-only — refused over HTTP.
	if code, resp := postSkill(t, srv, "Bearer good", IPCRequest{Op: "token_claim", Claim: "c"}); code != http.StatusForbidden || !strings.Contains(resp.Error, "token_claim") {
		t.Fatalf("token_claim over HTTP: code=%d resp=%+v", code, resp)
	}

	// GET → 405.
	gr, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	gr.Body.Close()
	if gr.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", gr.StatusCode)
	}
}

// A token-carrying memory op derives its provenance from the broker's identity
// for the token, NOT from a body-supplied Source a caller could spoof.
func TestHandleIPCTokenBindsSource(t *testing.T) {
	m := testManager(t, NewMemBackend())
	SetLiveOps(LiveOps{
		Identify: func(token string, _ Peer) (Source, int, bool) {
			if token == "good" {
				return Source{Agent: "fixer", Repo: "o/r", Trigger: "review"}, 7, true
			}
			return Source{}, 0, false
		},
	})
	t.Cleanup(func() { SetLiveOps(LiveOps{}) })

	var audited []map[string]any
	audit := func(e map[string]any) { audited = append(audited, e) }

	// Body claims agent "evil"; the token maps to "fixer" — the write must be
	// attributed to "fixer".
	resp := handleIPC(m, IPCRequest{
		Op: "remember", Token: "good", Text: "note",
		Source: Source{Agent: "evil", Repo: "evil/repo"},
	}, Peer{}, audit, nil)
	if !resp.OK {
		t.Fatalf("remember: %+v", resp)
	}
	found := false
	for _, e := range audited {
		if e["event"] == "memory_remember" {
			found = true
			if e["agent"] != "fixer" || e["repo"] != "o/r" {
				t.Fatalf("write attributed to %v/%v, want fixer/o/r (body Source must be ignored)", e["agent"], e["repo"])
			}
		}
	}
	if !found {
		t.Fatalf("no memory_remember audit: %v", audited)
	}

	// A token the broker rejects → the op is denied, never falls back to the
	// body Source.
	if resp := handleIPC(m, IPCRequest{Op: "recall", Token: "bad", Substring: "x"}, Peer{}, nil, nil); resp.OK || !strings.Contains(resp.Error, "unauthorized session token") {
		t.Fatalf("recall with rejected token: %+v", resp)
	}
}

func TestBearerToken(t *testing.T) {
	cases := map[string]string{
		"Bearer abc":   "abc",
		"bearer abc":   "abc",
		"BEARER  abc ": "abc",
		"abc":          "",
		"":             "",
		"Basic abc":    "",
		"Bearer":       "",
	}
	for in, want := range cases {
		if got := bearerToken(in); got != want {
			t.Errorf("bearerToken(%q) = %q, want %q", in, got, want)
		}
	}
}
