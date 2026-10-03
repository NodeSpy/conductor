// Shared machinery for the generic managed-auth path every contract
// connector gets (internal/connector/external.go's buildManagedAuth): the
// declared-in-config auth block (static schemes + oauth2 with cached tokens,
// pre-expiry refresh, and refresh-token rotation written back to its vault:
// ref), plus the config's named `secrets:` block a connector's own templates
// may read. Request templating, execution, and output extraction for the
// rest/graphql builtins now live in internal/builtins/httpconn, which has no
// reason to depend on connector internals (oauth2 lifecycle is the host's
// job; a connector only ever sees the resolved bearer or static scheme).
package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/vaults"
)

// httpAPIClient is the shared client for declared connectors: bounded, no
// special transport (these are ordinary request/response APIs).
var httpAPIClient = &http.Client{Timeout: 30 * time.Second}

const httpAPIMaxBody = 8 << 20

// authConfig is the shared `auth:` block on rest/graphql connections.
type authConfig struct {
	Type string `yaml:"type"` // none | bearer | basic | header | oauth2

	Token    string `yaml:"token"`    // bearer
	Username string `yaml:"username"` // basic
	Password string `yaml:"password"`
	Name     string `yaml:"name"`  // header: the header to set
	Value    string `yaml:"value"` // header: its value

	// oauth2
	Grant        string `yaml:"grant"` // client_credentials | refresh_token | authorization_code | device
	TokenURL     string `yaml:"token_url"`
	AuthURL      string `yaml:"auth_url"` // consent endpoint (authorization_code bootstrap)
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	// TokenVault names the vaults: entry conductor stores and rotates the
	// captured tokens in (keys oauth/<connector>/access_token,
	// …/refresh_token, …/expiry). Required for the interactive grants
	// (authorization_code, device); recommended for refresh_token so
	// provider rotations survive the daemon's own restarts.
	TokenVault string `yaml:"token_vault"`
	// RefreshToken is an optional SEED — a value or secret reference used
	// until the token_vault holds a rotated one. The captured/rotated
	// token in token_vault always wins.
	RefreshToken  string   `yaml:"refresh_token"`
	RedirectURI   string   `yaml:"redirect_uri"`    // authorization_code bootstrap (default http://localhost:8400/callback)
	DeviceAuthURL string   `yaml:"device_auth_url"` // device grant: the device-authorization endpoint
	Scopes        []string `yaml:"scopes"`
	// AuthParams are extra consent-URL query params (e.g. Google's
	// access_type=offline). A plugin bakes them into Decl.Auth; an operator may
	// also set auth_params: in the auth block.
	AuthParams map[string]string `yaml:"auth_params"`
}

// tokenVaultKeys returns the fixed key set a connector's tokens live under
// in its token_vault.
func tokenVaultKeys(connector string) (access, refresh, expiry string) {
	p := "oauth/" + connector
	return p + "/access_token", p + "/refresh_token", p + "/expiry"
}

// validateAuth structurally checks an auth block at build time.
func (a authConfig) validate(where string) error {
	switch a.Type {
	case "", "none":
		return nil
	case "bearer":
		if a.Token == "" {
			return fmt.Errorf("%s: auth bearer needs token:", where)
		}
	case "basic":
		if a.Username == "" || a.Password == "" {
			return fmt.Errorf("%s: auth basic needs username: and password:", where)
		}
	case "header":
		if a.Name == "" || a.Value == "" {
			return fmt.Errorf("%s: auth header needs name: and value:", where)
		}
	case "oauth2":
		if a.TokenURL == "" || a.ClientID == "" {
			return fmt.Errorf("%s: auth oauth2 needs token_url: and client_id:", where)
		}
		switch a.Grant {
		case "client_credentials":
		case "refresh_token":
			// The refresh token may be seeded later by `conductor connector
			// auth`, so its absence is a runtime condition, not a config error.
		case "authorization_code", "device":
			// The interactive grants store their captured tokens in a vault.
			if a.TokenVault == "" {
				return fmt.Errorf("%s: auth oauth2 grant %s needs token_vault: (a vaults: entry the captured tokens live in)", where, a.Grant)
			}
			if a.Grant == "device" && a.DeviceAuthURL == "" {
				return fmt.Errorf("%s: auth oauth2 grant device needs device_auth_url:", where)
			}
		default:
			return fmt.Errorf("%s: auth oauth2 grant must be client_credentials|refresh_token|authorization_code|device, got %q", where, a.Grant)
		}
	default:
		return fmt.Errorf("%s: auth type must be none|bearer|basic|header|oauth2, got %q", where, a.Type)
	}
	return nil
}

// authenticator applies the resolved auth to outbound requests, owning the
// oauth2 token cache and refresh/rotation lifecycle for one connector.
type authenticator struct {
	cfg  authConfig // credential fields already secret-resolved
	name string     // the connector's name (token_vault key prefix)
	sec  *secrets.Resolver
	log  func(string, ...any)

	mu      sync.Mutex
	access  string
	expiry  time.Time
	refresh string // current refresh token value
}

// newAuthenticator resolves an auth block's credential fields. With a
// token_vault, the vault's captured refresh token (from `conductor connector
// auth` or a prior rotation) takes precedence over the refresh_token: seed.
func newAuthenticator(ctx context.Context, name string, a authConfig, sec *secrets.Resolver, logf func(string, ...any)) (*authenticator, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	au := &authenticator{cfg: a, name: name, sec: sec, log: logf}
	resolve := func(field string, v *string) error {
		if *v == "" {
			return nil
		}
		r, err := sec.Resolve(ctx, *v)
		if err != nil {
			return fmt.Errorf("connector %q: auth %s: %w", name, field, err)
		}
		if r != "" {
			sec.Track(r)
		}
		*v = r
		return nil
	}
	for _, f := range []struct {
		name string
		v    *string
	}{
		{"token", &au.cfg.Token}, {"password", &au.cfg.Password}, {"value", &au.cfg.Value},
		{"client_id", &au.cfg.ClientID}, {"client_secret", &au.cfg.ClientSecret},
	} {
		if err := resolve(f.name, f.v); err != nil {
			return nil, err
		}
	}
	// The refresh token resolves too, but the RAW ref is kept for rotation.
	rt := a.RefreshToken
	if err := resolve("refresh_token", &rt); err != nil {
		return nil, err
	}
	au.refresh = rt
	// token_vault: the vault must exist and be writable (rotation writes
	// back); a captured/rotated refresh token there beats the seed.
	if a.Type == "oauth2" && a.TokenVault != "" {
		b, err := vaults.Use(a.TokenVault)
		if err != nil {
			return nil, fmt.Errorf("connector %q: token_vault: %w", name, err)
		}
		if _, ok := b.(vaults.Writer); !ok {
			return nil, fmt.Errorf("connector %q: token_vault %q (%s) is read-only — token storage/rotation needs a writable vault", name, a.TokenVault, vaults.Type(a.TokenVault))
		}
		_, refreshKey, _ := tokenVaultKeys(name)
		if v, err := vaults.Read(ctx, a.TokenVault, refreshKey); err == nil && v != "" {
			au.refresh = v
		}
	}
	return au, nil
}

// persistTokensLocked writes the current access token, expiry, and (when
// rotated) refresh token into the token_vault — best-effort: a write failure
// is logged, not fatal (the in-memory tokens still work until restart).
// Caller holds au.mu.
func (au *authenticator) persistTokensLocked(ctx context.Context, rotatedRefresh string) {
	if au.cfg.TokenVault == "" {
		return
	}
	accessKey, refreshKey, expiryKey := tokenVaultKeys(au.name)
	store := func(key, value string) {
		if value == "" {
			return
		}
		if err := vaults.Write(ctx, au.cfg.TokenVault, key, value); err != nil {
			au.log("oauth2: token could not be persisted to vault %q key %s: %v", au.cfg.TokenVault, key, err)
		}
	}
	store(accessKey, au.access)
	store(expiryKey, au.expiry.Format(time.RFC3339))
	store(refreshKey, rotatedRefresh)
}

// apply sets the auth on one outbound request (fetching/refreshing the oauth2
// access token as needed).
func (au *authenticator) apply(ctx context.Context, req *http.Request) error {
	switch au.cfg.Type {
	case "", "none":
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+au.cfg.Token)
	case "basic":
		req.SetBasicAuth(au.cfg.Username, au.cfg.Password)
	case "header":
		req.Header.Set(au.cfg.Name, au.cfg.Value)
	case "oauth2":
		tok, err := au.accessToken(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	return nil
}

// oauth2 reports whether this authenticator manages an oauth2 token (the 401
// retry-once path only applies then).
func (au *authenticator) oauth2() bool { return au.cfg.Type == "oauth2" }

// accessToken returns a valid cached token or fetches a fresh one. A minute
// of skew keeps a token from expiring mid-request.
func (au *authenticator) accessToken(ctx context.Context) (string, error) {
	au.mu.Lock()
	defer au.mu.Unlock()
	if au.access != "" && time.Now().Before(au.expiry.Add(-time.Minute)) {
		return au.access, nil
	}
	return au.fetchTokenLocked(ctx)
}

// invalidate drops the cached access token (the 401 retry path).
func (au *authenticator) invalidate() {
	au.mu.Lock()
	au.access = ""
	au.mu.Unlock()
}

// tokenResponse is the OAuth2 token endpoint's reply.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// fetchTokenLocked runs the grant against token_url. Caller holds au.mu.
func (au *authenticator) fetchTokenLocked(ctx context.Context) (string, error) {
	form := url.Values{"client_id": {au.cfg.ClientID}}
	switch au.cfg.Grant {
	case "client_credentials":
		form.Set("grant_type", "client_credentials")
		if len(au.cfg.Scopes) > 0 {
			form.Set("scope", strings.Join(au.cfg.Scopes, " "))
		}
	case "refresh_token", "authorization_code", "device":
		// The interactive grants run on the refresh token after the one-time
		// `conductor connector auth` bootstrap.
		if au.refresh == "" {
			return "", fmt.Errorf("oauth2: no refresh token yet — run `conductor connector auth <name>` once to seed it")
		}
		form.Set("grant_type", "refresh_token")
		form.Set("refresh_token", au.refresh)
	default:
		return "", fmt.Errorf("oauth2: unsupported grant %q", au.cfg.Grant)
	}
	tr, err := postTokenForm(ctx, au.cfg, form)
	if err != nil {
		return "", err
	}
	au.access = tr.AccessToken
	au.sec.Track(tr.AccessToken)
	ttl := time.Duration(tr.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	au.expiry = time.Now().Add(ttl)
	// Rotation: the provider issued a NEW refresh token — adopt it and
	// persist it (with the fresh access token + expiry) to the token_vault,
	// so the connector keeps working across the daemon's own restarts.
	rotated := ""
	if tr.RefreshToken != "" && tr.RefreshToken != au.refresh {
		au.refresh = tr.RefreshToken
		au.sec.Track(tr.RefreshToken)
		rotated = tr.RefreshToken
	}
	au.persistTokensLocked(ctx, rotated)
	return au.access, nil
}

// postTokenForm posts one form to the token endpoint. The client credentials
// ride a Basic Authorization header when a secret is configured (the common
// provider requirement, e.g. Xero); client_id stays in the form either way.
func postTokenForm(ctx context.Context, a authConfig, form url.Values) (tokenResponse, error) {
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || tr.AccessToken == "" {
		reason := tr.Error
		if tr.ErrorDesc != "" {
			reason += ": " + tr.ErrorDesc
		}
		if reason == "" {
			reason = strings.TrimSpace(string(body))
			if len(reason) > 200 {
				reason = reason[:200] + "…"
			}
		}
		return tokenResponse{}, fmt.Errorf("oauth2 token: HTTP %d: %s", resp.StatusCode, reason)
	}
	return tr, nil
}

// resolveNamedSecrets resolves the config's named secrets: block into the
// {{.secrets.*}} template scope (best-effort: an unresolvable name is empty,
// matching the flow runner's behavior).
func resolveNamedSecrets(ctx context.Context, cfg *config.Config, sec *secrets.Resolver) map[string]any {
	out := map[string]any{}
	if cfg == nil {
		return out
	}
	for name, ref := range cfg.SecretRefs {
		if v, err := sec.Resolve(ctx, ref); err == nil {
			// A named secret exposed to the template scope is still a secret:
			// track it so it's redacted from logs/audit wherever a step
			// interpolates {{.secrets.<name>}}, exactly like the bearer/oauth
			// credentials tracked at resolve time above.
			if v != "" {
				sec.Track(v)
			}
			out[name] = v
		} else {
			out[name] = ""
		}
	}
	return out
}
