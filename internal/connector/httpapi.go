// Shared machinery for the generic rest/graphql connector types (#36 §7):
// the declared-in-config auth block (static schemes + oauth2 with cached
// tokens, pre-expiry refresh, one 401 retry, and refresh-token rotation
// written back to its vault: ref), templated request building, response
// parsing, output extraction, and the polled-events source.
package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/inbound"
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
	cfg        authConfig // credential fields already secret-resolved
	name       string     // the connector's name (token_vault key prefix)
	refreshRef string     // LEGACY: the raw refresh_token vault: ref (pre-token_vault rotation target)
	sec        *secrets.Resolver
	log        func(string, ...any)

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
	au := &authenticator{cfg: a, name: name, refreshRef: a.RefreshToken, sec: sec, log: logf}
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
		// LEGACY pre-token_vault rotation target: refresh_token: vault:<entry>.
		if name, ok := strings.CutPrefix(au.refreshRef, "vault:"); ok && au.cfg.TokenVault == "" {
			if err := au.sec.StoreVault(name, tr.RefreshToken); err != nil {
				au.log("oauth2: rotated refresh token could not be persisted to vault:%s: %v", name, err)
			}
		}
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

// ---------------------------------------------------------------------------
// Templating
// ---------------------------------------------------------------------------

// httpTemplateFuncs is the declared-connector template function set: `json`
// encodes any value into a JSON body/fragment.
var httpTemplateFuncs = template.FuncMap{
	"json": func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	},
}

// renderHTTPTemplate renders one template string over the request/response
// scope. missingkey=zero matches the flow runner's rendering.
func renderHTTPTemplate(s string, data map[string]any) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	t, err := template.New("t").Option("missingkey=zero").Funcs(httpTemplateFuncs).Parse(s)
	if err != nil {
		return "", fmt.Errorf("template %q: %w", s, err)
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("template %q: %w", s, err)
	}
	return strings.ReplaceAll(b.String(), "<no value>", ""), nil
}

// renderHTTPValue renders a template string, preserving the underlying type
// when the whole string is one {{.path}} reference — so
// `invoices: "{{.response.body.Invoices}}"` stays an array.
func renderHTTPValue(s string, data map[string]any) (any, error) {
	if path, ok := soleHTTPRef(s); ok {
		if v, found := lookupHTTPPath(data, path); found {
			return v, nil
		}
		return nil, nil
	}
	return renderHTTPTemplate(s, data)
}

// soleHTTPRef reports whether s is exactly one {{.a.b}} action.
func soleHTTPRef(s string) (string, bool) {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "{{") || !strings.HasSuffix(t, "}}") || strings.Count(t, "{{") != 1 {
		return "", false
	}
	inner := strings.TrimSpace(t[2 : len(t)-2])
	if !strings.HasPrefix(inner, ".") {
		return "", false
	}
	path := strings.TrimPrefix(inner, ".")
	if path == "" || strings.ContainsAny(path, " \t|(){}\"'") {
		return "", false
	}
	return path, true
}

// lookupHTTPPath walks a dotted path through nested maps.
func lookupHTTPPath(data map[string]any, path string) (any, bool) {
	cur := any(data)
	for _, key := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[key]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// parseHTTPTemplates parse-checks every template a declared connector will
// render, so a syntax typo disables the connector at load, not at 3am.
func parseHTTPTemplates(where string, tmpls map[string]string) error {
	for what, s := range tmpls {
		if s == "" || !strings.Contains(s, "{{") {
			continue
		}
		if _, err := template.New("t").Funcs(httpTemplateFuncs).Parse(s); err != nil {
			return fmt.Errorf("%s: bad %s template: %v", where, what, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Request execution
// ---------------------------------------------------------------------------

// httpAPIResponse is the parsed response exposed to output templates.
type httpAPIResponse struct {
	Status  int
	Body    any // parsed JSON (map/array), or {"raw": text} for non-JSON
	Headers map[string]any
}

// scope builds the template scope for output extraction.
func (r httpAPIResponse) scope(opts map[string]any, secretsVals map[string]any) map[string]any {
	resp := map[string]any{"status": r.Status, "body": r.Body, "headers": r.Headers}
	if m, ok := r.Body.(map[string]any); ok {
		if data, has := m["data"]; has {
			resp["data"] = data // graphql convenience: {{.response.data.*}}
		}
	}
	return map[string]any{"options": opts, "response": resp, "secrets": secretsVals}
}

// doHTTPAPI executes one authed request, retrying exactly once on a 401 when
// the auth is oauth2 (a token revoked out from under the cache).
func doHTTPAPI(ctx context.Context, au *authenticator, method, fullURL string, headers map[string]string, body []byte) (httpAPIResponse, error) {
	send := func() (*http.Response, error) {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, fullURL, rd)
		if err != nil {
			return nil, err
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if body != nil && req.Header.Get("Content-Type") == "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if err := au.apply(ctx, req); err != nil {
			return nil, err
		}
		return httpAPIClient.Do(req)
	}

	resp, err := send()
	if err != nil {
		return httpAPIResponse{}, err
	}
	if resp.StatusCode == http.StatusUnauthorized && au.oauth2() {
		resp.Body.Close()
		au.invalidate()
		if resp, err = send(); err != nil {
			return httpAPIResponse{}, err
		}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, httpAPIMaxBody))
	if err != nil {
		return httpAPIResponse{}, err
	}
	out := httpAPIResponse{Status: resp.StatusCode, Headers: map[string]any{}}
	for k := range resp.Header {
		out.Headers[k] = resp.Header.Get(k)
	}
	var parsed any
	if len(raw) > 0 && json.Unmarshal(raw, &parsed) == nil {
		out.Body = parsed
	} else {
		out.Body = map[string]any{"raw": string(raw)}
	}
	return out, nil
}

// expectStatus reports whether a status satisfies the verb's expect: set
// (empty = any 2xx).
func expectStatus(expect []int, status int) bool {
	if len(expect) == 0 {
		return status >= 200 && status < 300
	}
	for _, e := range expect {
		if e == status {
			return true
		}
	}
	return false
}

// bodyTail summarizes a response body for error messages.
func bodyTail(body any) string {
	b, _ := json.Marshal(body)
	s := string(b)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// extractOutputsHTTP renders each declared output over the response scope.
func extractOutputsHTTP(output map[string]string, scope map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for name, tmpl := range output {
		v, err := renderHTTPValue(tmpl, scope)
		if err != nil {
			return nil, fmt.Errorf("output %q: %w", name, err)
		}
		out[name] = v
	}
	return out, nil
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
			out[name] = v
		} else {
			out[name] = ""
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Polled events
// ---------------------------------------------------------------------------

// polledEvent is one declared events: entry, normalized for the poller.
type polledEvent struct {
	Name     string
	Poll     time.Duration
	Method   string
	Path     string
	Query    map[string]string
	List     string            // template → the array
	ID       string            // template over {{.item.*}}
	Context  map[string]string // field -> template over {{.item.*}}
	Triggers []CompiledTrigger
}

// pollRequester executes one polled request and returns the response —
// implemented by the rest connector (which owns base URL + headers + auth).
type pollRequester interface {
	pollRequest(ctx context.Context, ev polledEvent) (httpAPIResponse, error)
}

// httpPoller is the core.Integration a rest connector's events: lower into:
// each event polls on its cadence, seeds its backlog silently on the first
// poll (the rss cold-start behavior), dedups by the id: template, and emits
// one trigger per action variant for each new item.
type httpPoller struct {
	source string // trigger source ("rest")
	name   string // connector instance name
	req    pollRequester
	events []polledEvent
}

func (p *httpPoller) Name() string    { return p.name }
func (p *httpPoller) Validate() error { return nil }

func (p *httpPoller) Start(ctx context.Context, emit core.EmitFunc) error {
	for i, ev := range p.events {
		i, ev := i, ev
		go p.pollEvent(ctx, emit, i, ev)
	}
	log.Printf("%s[%s]: %d polled event(s)", p.source, p.name, len(p.events))
	<-ctx.Done()
	return ctx.Err()
}

func (p *httpPoller) pollEvent(ctx context.Context, emit core.EmitFunc, idx int, ev polledEvent) {
	seen := map[string]bool{}
	primed := false
	// Stagger events so they don't all fetch on the same tick.
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Duration(idx) * 3 * time.Second):
	}
	t := time.NewTicker(ev.Poll)
	defer t.Stop()
	for {
		p.pollOnce(ctx, emit, ev, seen, &primed)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (p *httpPoller) pollOnce(ctx context.Context, emit core.EmitFunc, ev polledEvent, seen map[string]bool, primed *bool) {
	resp, err := p.req.pollRequest(ctx, ev)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("%s[%s]: event %q poll: %v", p.source, p.name, ev.Name, err)
		}
		return
	}
	items, err := pollItems(ev, resp)
	if err != nil {
		log.Printf("%s[%s]: event %q: %v", p.source, p.name, ev.Name, err)
		return
	}
	firstPoll := !*primed
	*primed = true
	for _, item := range items {
		scope := map[string]any{"item": item}
		id, err := renderHTTPTemplate(ev.ID, scope)
		if err != nil || id == "" {
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		if firstPoll {
			continue // seed the backlog silently
		}
		p.emitItem(ctx, emit, ev, id, item, scope)
	}
}

// pollItems lifts the declared list out of a poll response.
func pollItems(ev polledEvent, resp httpAPIResponse) ([]map[string]any, error) {
	scope := map[string]any{"response": map[string]any{
		"status": resp.Status, "body": resp.Body, "headers": resp.Headers,
	}}
	v, err := renderHTTPValue(ev.List, scope)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("list: %q did not resolve to an array (got %T)", ev.List, v)
	}
	items := make([]map[string]any, 0, len(arr))
	for _, e := range arr {
		if m, ok := e.(map[string]any); ok {
			items = append(items, m)
		}
	}
	return items, nil
}

func (p *httpPoller) emitItem(ctx context.Context, emit core.EmitFunc, ev polledEvent, id string, item map[string]any, scope map[string]any) {
	dedup := ev.Name + "\x00" + id
	target := inbound.SyntheticTarget(p.source+":"+p.name+":"+ev.Name, dedup)
	trigCtx := map[string]any{"item": item}
	title := p.name + ": " + ev.Name
	for field, tmpl := range ev.Context {
		v, err := renderHTTPValue(tmpl, scope)
		if err != nil {
			continue
		}
		trigCtx[field] = v
		if field == "title" {
			if s, ok := v.(string); ok && s != "" {
				title = s
			}
		}
	}
	for _, ct := range ev.Triggers {
		act := lowerAction(ct)
		act = inbound.ForceNoCheckout(act)
		emit(ctx, core.Trigger{
			Source:   p.source,
			Instance: p.name,
			Kind:     ev.Name,
			Variant:  act.Name,
			Target:   target,
			Title:    title,
			Dedup:    dedup,
			Context:  trigCtx,
			Action:   act,
		})
	}
}
