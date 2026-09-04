package connector

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/vaults"
)

// defaultRedirectURI is the localhost redirect the bootstrap listens on when
// the auth block sets none. It must be registered with the provider.
const defaultRedirectURI = "http://localhost:8400/callback"

// CodeSource obtains an authorization code for a consent URL — the CLI wires
// a localhost redirect listener; tests drive it directly.
type CodeSource func(consentURL, state string) (code string, err error)

// authConfigFor decodes and sanity-checks one connector's oauth2 auth block
// for the auth CLI.
func authConfigFor(cfg *config.Config, name string) (authConfig, error) {
	ref, ok := cfg.ConnectorsMap[name]
	if !ok {
		return authConfig{}, fmt.Errorf("no connector %q configured", name)
	}
	if ref.Type != "rest" && ref.Type != "graphql" {
		return authConfig{}, fmt.Errorf("connector %q is type %q — `connector auth` applies to rest/graphql oauth2 connectors", name, ref.Type)
	}
	var conn struct {
		Auth authConfig `yaml:"auth"`
	}
	if err := ref.Decode(&conn); err != nil {
		return authConfig{}, fmt.Errorf("connector %q: decode auth: %w", name, err)
	}
	if conn.Auth.Type != "oauth2" {
		return authConfig{}, fmt.Errorf("connector %q: auth type is %q — `connector auth` applies to oauth2", name, conn.Auth.Type)
	}
	return conn.Auth, nil
}

// AuthBootstrap is the one-time interactive OAuth2 login behind
// `conductor connector auth <name>`: run the grant's flow
// (authorization_code captures the consent redirect via codeFn; device
// prints a user code and polls), exchange the result at token_url, and store
// the access + refresh tokens (and expiry) in the connector's token_vault.
// It is the only interactive step and never runs on the restart path.
func AuthBootstrap(ctx context.Context, cfg *config.Config, sec *secrets.Resolver, name string, out io.Writer, codeFn CodeSource) error {
	a, err := authConfigFor(cfg, name)
	if err != nil {
		return err
	}
	switch a.Grant {
	case "authorization_code", "device", "refresh_token":
	default:
		return fmt.Errorf("connector %q: grant %q needs no interactive bootstrap (client_credentials fetches tokens on demand)", name, a.Grant)
	}
	if a.TokenVault == "" {
		return fmt.Errorf("connector %q: auth.token_vault must name a vaults: entry so the captured tokens (and their rotations) persist", name)
	}
	// The vaults registry must know the token vault (the CLI runs outside
	// the daemon build, so wire it here).
	if _, _, err := OpenVaultBackendRegistered(cfg, a.TokenVault, Deps{Secrets: sec, Config: cfg}); err != nil {
		return err
	}
	// Resolve the client credentials (they may be secret refs).
	au, err := newAuthenticatorForBootstrap(ctx, name, a, sec)
	if err != nil {
		return err
	}
	a = au.cfg

	var tr tokenResponse
	switch a.Grant {
	case "device":
		tr, err = deviceFlow(ctx, a, out)
	default:
		if a.AuthURL == "" {
			return fmt.Errorf("connector %q: auth.auth_url (the consent endpoint) is required for the bootstrap", name)
		}
		state := randomState()
		var code string
		code, err = codeFn(consentURL(a, state), state)
		if err == nil {
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
			tr, err = postTokenForm(ctx, a, form)
			if err != nil {
				err = fmt.Errorf("code exchange: %w", err)
			}
		}
	}
	if err != nil {
		return err
	}
	if tr.RefreshToken == "" {
		return fmt.Errorf("the provider returned no refresh token — check the offline/refresh scope (scopes: %s)", strings.Join(a.Scopes, " "))
	}
	accessKey, refreshKey, expiryKey := tokenVaultKeys(name)
	if err := vaults.Write(ctx, a.TokenVault, refreshKey, tr.RefreshToken); err != nil {
		return fmt.Errorf("store refresh token: %w", err)
	}
	if tr.AccessToken != "" {
		_ = vaults.Write(ctx, a.TokenVault, accessKey, tr.AccessToken)
		exp := time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
		_ = vaults.Write(ctx, a.TokenVault, expiryKey, exp.Format(time.RFC3339))
	}
	fmt.Fprintf(out, "authorized: tokens stored in vault %q under oauth/%s/* (access token valid %ds)\n", a.TokenVault, name, tr.ExpiresIn)
	return nil
}

// deviceResponse is the device-authorization endpoint's reply (RFC 8628).
type deviceResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// devicePollInterval is injectable so tests don't wait real seconds.
var devicePollInterval = func(seconds int) time.Duration {
	if seconds <= 0 {
		seconds = 5
	}
	return time.Duration(seconds) * time.Second
}

// deviceFlow runs the RFC 8628 device-authorization grant: request a user
// code, print where to enter it, and poll token_url until the human
// approves (or the code expires).
func deviceFlow(ctx context.Context, a authConfig, out io.Writer) (tokenResponse, error) {
	form := url.Values{"client_id": {a.ClientID}}
	if len(a.Scopes) > 0 {
		form.Set("scope", strings.Join(a.Scopes, " "))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.DeviceAuthURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if a.ClientSecret != "" {
		req.SetBasicAuth(a.ClientID, a.ClientSecret)
	}
	resp, err := httpAPIClient.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("device authorization: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, httpAPIMaxBody))
	resp.Body.Close()
	var dr deviceResponse
	_ = json.Unmarshal(body, &dr)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || dr.DeviceCode == "" {
		return tokenResponse{}, fmt.Errorf("device authorization: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	where := dr.VerificationURIComplete
	if where == "" {
		where = dr.VerificationURI
	}
	fmt.Fprintf(out, "open %s and enter code %s\nwaiting for approval …\n", where, dr.UserCode)

	deadline := time.Now().Add(time.Duration(dr.ExpiresIn) * time.Second)
	if dr.ExpiresIn <= 0 {
		deadline = time.Now().Add(10 * time.Minute)
	}
	interval := devicePollInterval(dr.Interval)
	for {
		if time.Now().After(deadline) {
			return tokenResponse{}, fmt.Errorf("device code expired before approval")
		}
		select {
		case <-ctx.Done():
			return tokenResponse{}, ctx.Err()
		case <-time.After(interval):
		}
		poll := url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {dr.DeviceCode},
			"client_id":   {a.ClientID},
		}
		tr, perr := postTokenFormRaw(ctx, a, poll)
		if perr != nil {
			return tokenResponse{}, perr
		}
		switch tr.Error {
		case "":
			if tr.AccessToken != "" {
				return tr, nil
			}
			return tokenResponse{}, fmt.Errorf("device token: empty response")
		case "authorization_pending":
			continue
		case "slow_down":
			interval += devicePollInterval(5)
			continue
		default:
			reason := tr.Error
			if tr.ErrorDesc != "" {
				reason += ": " + tr.ErrorDesc
			}
			return tokenResponse{}, fmt.Errorf("device token: %s", reason)
		}
	}
}

// AuthStatus is one oauth2 connector's login state for `connector auth ls`.
type AuthStatus struct {
	Name       string
	Grant      string
	TokenVault string
	HasRefresh bool
	Expiry     string // RFC 3339, "" when unknown
}

// AuthList reports each rest/graphql oauth2 connector's stored-token state,
// reading the token_vault directly (no daemon needed).
func AuthList(ctx context.Context, cfg *config.Config, deps Deps) ([]AuthStatus, error) {
	names := make([]string, 0, len(cfg.ConnectorsMap))
	for n, ref := range cfg.ConnectorsMap {
		if ref.Type == "rest" || ref.Type == "graphql" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var out []AuthStatus
	for _, n := range names {
		a, err := authConfigFor(cfg, n)
		if err != nil {
			continue // not oauth2
		}
		st := AuthStatus{Name: n, Grant: a.Grant, TokenVault: a.TokenVault}
		st.HasRefresh = a.RefreshToken != ""
		if a.TokenVault != "" {
			if b, _, err := OpenVaultBackendRegistered(cfg, a.TokenVault, deps); err == nil {
				_, refreshKey, expiryKey := tokenVaultKeys(n)
				if v, err := b.Read(ctx, refreshKey); err == nil && v != "" {
					st.HasRefresh = true
				}
				if v, err := b.Read(ctx, expiryKey); err == nil {
					st.Expiry = v
				}
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// AuthRevoke clears a connector's stored tokens from its token_vault.
func AuthRevoke(ctx context.Context, cfg *config.Config, deps Deps, name string) error {
	a, err := authConfigFor(cfg, name)
	if err != nil {
		return err
	}
	if a.TokenVault == "" {
		return fmt.Errorf("connector %q has no token_vault: — nothing stored to revoke", name)
	}
	b, typ, err := OpenVaultBackendRegistered(cfg, a.TokenVault, deps)
	if err != nil {
		return err
	}
	d, ok := b.(vaults.Deleter)
	if !ok {
		return fmt.Errorf("token_vault %q (%s) does not support deletes", a.TokenVault, typ)
	}
	accessKey, refreshKey, expiryKey := tokenVaultKeys(name)
	cleared := 0
	for _, k := range []string{accessKey, refreshKey, expiryKey} {
		if err := d.Delete(ctx, k); err == nil {
			cleared++
		}
	}
	if cleared == 0 {
		return fmt.Errorf("no stored tokens for %q in vault %q", name, a.TokenVault)
	}
	return nil
}

// OpenVaultBackendRegistered opens one named vault AND registers it in the
// vaults registry when absent — CLI paths (auth bootstrap/ls/revoke) run
// outside the daemon's Build, but the authenticator and vaults.Write resolve
// through the registry.
func OpenVaultBackendRegistered(cfg *config.Config, name string, deps Deps) (vaults.Backend, string, error) {
	if b, err := vaults.Use(name); err == nil {
		return b, vaults.Type(name), nil
	}
	b, typ, err := OpenVaultBackend(cfg, name, deps)
	if err != nil {
		return nil, "", err
	}
	if deps.Secrets != nil {
		vaults.SetTaint(deps.Secrets.Track)
	}
	if err := vaults.Register(name, typ, b); err != nil {
		return nil, "", err
	}
	return b, typ, nil
}

// postTokenFormRaw posts a token form and returns the decoded response
// WITHOUT treating oauth error payloads as Go errors (the device poll reads
// authorization_pending/slow_down from them).
func postTokenFormRaw(ctx context.Context, a authConfig, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if a.ClientSecret != "" {
		req.SetBasicAuth(a.ClientID, a.ClientSecret)
	}
	resp, err := httpAPIClient.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("oauth2 token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, httpAPIMaxBody))
	var tr tokenResponse
	_ = json.Unmarshal(body, &tr)
	if tr.AccessToken == "" && tr.Error == "" {
		return tokenResponse{}, fmt.Errorf("oauth2 token: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return tr, nil
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
