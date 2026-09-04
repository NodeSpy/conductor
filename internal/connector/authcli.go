package connector

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// defaultRedirectURI is the localhost redirect the bootstrap listens on when
// the auth block sets none. It must be registered with the provider.
const defaultRedirectURI = "http://localhost:8400/callback"

// CodeSource obtains an authorization code for a consent URL — the CLI wires
// a localhost redirect listener; tests drive it directly.
type CodeSource func(consentURL, state string) (code string, err error)

// AuthBootstrap is the one-time interactive OAuth2 seeding behind
// `conductor connector auth <name>`: build the consent URL, obtain the code
// (via codeFn), exchange it at token_url, and store the refresh token in the
// vault: ref the connector's auth block names. It is the only interactive
// step and never runs on the restart path.
func AuthBootstrap(ctx context.Context, cfg *config.Config, sec *secrets.Resolver, name string, out io.Writer, codeFn CodeSource) error {
	ref, ok := cfg.ConnectorsMap[name]
	if !ok {
		return fmt.Errorf("no connector %q configured", name)
	}
	if ref.Type != "rest" && ref.Type != "graphql" {
		return fmt.Errorf("connector %q is type %q — `connector auth` applies to rest/graphql oauth2 connectors", name, ref.Type)
	}
	var conn struct {
		Auth authConfig `yaml:"auth"`
	}
	if err := ref.Decode(&conn); err != nil {
		return fmt.Errorf("connector %q: decode auth: %w", name, err)
	}
	a := conn.Auth
	if a.Type != "oauth2" {
		return fmt.Errorf("connector %q: auth type is %q — the bootstrap applies to oauth2", name, a.Type)
	}
	if a.Grant != "authorization_code" && a.Grant != "refresh_token" {
		return fmt.Errorf("connector %q: grant %q needs no interactive bootstrap (client_credentials fetches tokens on demand)", name, a.Grant)
	}
	if a.AuthURL == "" {
		return fmt.Errorf("connector %q: auth.auth_url (the consent endpoint) is required for the bootstrap", name)
	}
	vaultName, isVault := strings.CutPrefix(a.RefreshToken, "vault:")
	if !isVault || vaultName == "" {
		return fmt.Errorf("connector %q: auth.refresh_token must be a vault: reference so the token (and its rotations) persist — got %q", name, a.RefreshToken)
	}
	// Resolve the client credentials (they may be secret refs).
	au, err := newAuthenticatorForBootstrap(ctx, name, a, sec)
	if err != nil {
		return err
	}
	a = au.cfg

	state := randomState()
	consent := consentURL(a, state)
	code, err := codeFn(consent, state)
	if err != nil {
		return err
	}

	redirect := a.RedirectURI
	if redirect == "" {
		redirect = defaultRedirectURI
	}
	form := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirect},
		"client_id":    {a.ClientID},
	}
	tr, err := postTokenForm(ctx, a, form)
	if err != nil {
		return fmt.Errorf("code exchange: %w", err)
	}
	if tr.RefreshToken == "" {
		return fmt.Errorf("the provider returned no refresh token — check the offline/refresh scope (scopes: %s)", strings.Join(a.Scopes, " "))
	}
	if err := sec.StoreVault(vaultName, tr.RefreshToken); err != nil {
		return fmt.Errorf("store refresh token: %w", err)
	}
	fmt.Fprintf(out, "authorized: refresh token stored at vault:%s (access token valid %ds)\n", vaultName, tr.ExpiresIn)
	return nil
}

// newAuthenticatorForBootstrap resolves credentials without touching the
// refresh token (the bootstrap is what seeds it).
func newAuthenticatorForBootstrap(ctx context.Context, name string, a authConfig, sec *secrets.Resolver) (*authenticator, error) {
	b := a
	b.RefreshToken = "" // may not resolve yet; irrelevant for the exchange
	return newAuthenticator(ctx, name, b, sec, nil)
}

// consentURL builds the provider's authorization URL.
func consentURL(a authConfig, state string) string {
	redirect := a.RedirectURI
	if redirect == "" {
		redirect = defaultRedirectURI
	}
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {a.ClientID},
		"redirect_uri":  {redirect},
		"state":         {state},
	}
	if len(a.Scopes) > 0 {
		q.Set("scope", strings.Join(a.Scopes, " "))
	}
	sep := "?"
	if strings.Contains(a.AuthURL, "?") {
		sep = "&"
	}
	return a.AuthURL + sep + q.Encode()
}

func randomState() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// LocalCodeCapture returns a CodeSource that prints the consent URL and
// captures the provider's redirect on the localhost listener named by
// redirectURI (the handoff web-listener pattern: one request, then done).
func LocalCodeCapture(redirectURI string, out io.Writer, timeout time.Duration) CodeSource {
	if redirectURI == "" {
		redirectURI = defaultRedirectURI
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return func(consent, state string) (string, error) {
		u, err := url.Parse(redirectURI)
		if err != nil {
			return "", fmt.Errorf("redirect_uri %q: %w", redirectURI, err)
		}
		addr := u.Host
		if u.Port() == "" {
			addr = net.JoinHostPort(u.Hostname(), "80")
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return "", fmt.Errorf("listen on %s for the redirect: %w", addr, err)
		}
		defer ln.Close()

		fmt.Fprintf(out, "open this URL, sign in, and approve access:\n\n  %s\n\nwaiting for the redirect on %s …\n", consent, redirectURI)

		type result struct {
			code string
			err  error
		}
		got := make(chan result, 1)
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if u.Path != "" && r.URL.Path != u.Path {
				http.NotFound(w, r)
				return
			}
			q := r.URL.Query()
			if q.Get("state") != state {
				http.Error(w, "state mismatch", http.StatusBadRequest)
				got <- result{err: fmt.Errorf("redirect state mismatch")}
				return
			}
			code := q.Get("code")
			if code == "" {
				http.Error(w, "no code in redirect", http.StatusBadRequest)
				got <- result{err: fmt.Errorf("redirect carried no code (error=%s)", q.Get("error"))}
				return
			}
			fmt.Fprint(w, "Authorized. You can close this tab.")
			got <- result{code: code}
		})}
		go func() { _ = srv.Serve(ln) }()
		defer srv.Close()

		select {
		case r := <-got:
			return r.code, r.err
		case <-time.After(timeout):
			return "", fmt.Errorf("no redirect within %s", timeout)
		}
	}
}
