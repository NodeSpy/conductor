package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// captureInvoker is a pluginInvoker seam that records the InvokeRequest it saw.
type captureInvoker struct{ got plugin.InvokeRequest }

func (c *captureInvoker) Invoke(_ context.Context, req plugin.InvokeRequest) (map[string]any, error) {
	c.got = req
	return map[string]any{"ok": true}, nil
}

// TestExternalImplInjectsManagedToken verifies that a plugin connector wired to
// a managed OAuth2 authenticator gets a fresh bearer injected into a PER-CALL
// copy of its connection (under plugin.AccessTokenKey), while the shared conn
// map is never mutated.
func TestExternalImplInjectsManagedToken(t *testing.T) {
	// A stub OAuth2 token endpoint (client_credentials — no vault needed).
	tokSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("token endpoint: parse form: %v", err)
		}
		if g := r.PostForm.Get("grant_type"); g != "client_credentials" {
			t.Errorf("grant_type = %q, want client_credentials", g)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-123", "expires_in": 3600, "token_type": "bearer"})
	}))
	defer tokSrv.Close()

	au, err := newAuthenticator(context.Background(), "xero",
		authConfig{Type: "oauth2", Grant: "client_credentials", TokenURL: tokSrv.URL, ClientID: "cid", ClientSecret: "sec"},
		secrets.New(), nil)
	if err != nil {
		t.Fatalf("newAuthenticator: %v", err)
	}

	cap := &captureInvoker{}
	baseConn := map[string]any{"tenant_id": "org-1"}
	e := &externalImpl{
		client:   cap,
		instance: "xero",
		decl:     &TypeDecl{}, // no declared verb → output validation skipped
		conn:     baseConn,
		auth:     au,
	}

	if _, err := e.Invoke(context.Background(), "invoices", map[string]any{"page": 1}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	// The plugin saw the injected token plus the original field.
	if got := cap.got.Connection[plugin.AccessTokenKey]; got != "tok-123" {
		t.Errorf("injected token = %v, want tok-123", got)
	}
	if got := cap.got.Connection["tenant_id"]; got != "org-1" {
		t.Errorf("tenant_id = %v, want org-1", got)
	}
	// The shared conn map must not have been mutated.
	if _, leaked := baseConn[plugin.AccessTokenKey]; leaked {
		t.Errorf("shared conn map was mutated with the token: %v", baseConn)
	}
}

// TestExternalImplNoManagedAuth verifies that without an authenticator the
// connection is forwarded unchanged (no reserved token key appears).
func TestExternalImplNoManagedAuth(t *testing.T) {
	cap := &captureInvoker{}
	e := &externalImpl{
		client:   cap,
		instance: "acme",
		decl:     &TypeDecl{},
		conn:     map[string]any{"token": "static"},
		auth:     nil,
	}
	if _, err := e.Invoke(context.Background(), "echo", nil); err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if _, present := cap.got.Connection[plugin.AccessTokenKey]; present {
		t.Errorf("unexpected %q key injected when no managed auth is configured", plugin.AccessTokenKey)
	}
	if cap.got.Connection["token"] != "static" {
		t.Errorf("static token field not forwarded")
	}
}
