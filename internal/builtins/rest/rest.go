// Package rest is the `rest` connector as a contract handler
// (docs/design/plugin-contract.md §1.10): any HTTP API declared in config —
// base_url + auth + user-defined verbs, with optional polled events — served
// in-process over the same protocol a spawned plugin speaks.
//
// A rest instance's verbs and events are USER-DECLARED, in its own config,
// so the type-level Describe() cannot name them: DescribeInstance (Q6,
// plugin-contract.md §3.9 G13) materializes the instance's actual verbs and
// events from its config, replacing the old InstanceDecler Go-side door.
package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NodeSpy/conductor/internal/builtins/httpconn"
	"github.com/NodeSpy/conductor/pkg/plugin"
	"github.com/NodeSpy/conductor/pkg/sourcekit"
)

// REST is the rest handler. Stateless but for the host channel: every call
// carries its instance's config/connection, exactly like a spawned plugin
// would receive it, but the poller needs a live channel back to the daemon
// (host.auth) rather than anything it could carry on a per-call connection
// snapshot — see SetHost.
type REST struct {
	host atomic.Pointer[plugin.HostConn]

	// errMu/lastPollErr/lastPollErrAt rate-limit repeated identical poll
	// errors through host.log (finding 9a): a dead upstream or an expired
	// static credential would otherwise log the SAME failure on every single
	// poll tick, forever.
	errMu         sync.Mutex
	lastPollErr   map[string]string
	lastPollErrAt map[string]time.Time
}

// New is the rest handler.
func New() *REST { return &REST{} }

// pollErrorLogInterval bounds how often the SAME poll error (instance+event)
// is logged again through host.log — the first occurrence, and the first
// occurrence of any DIFFERENTLY-WORDED error, always logs immediately. A var
// so a test shrinks it.
var pollErrorLogInterval = 5 * time.Minute

// logPollError reports a poll failure through host.log (finding 9a: pollOnce
// used to be completely silent on failure — the pre-contract native
// connector logged every one, git show 4cade34:internal/connector/
// httpapi.go httpPoller.pollOnce). Rate-limited per (instance, event) so a
// connector stuck failing the same way doesn't flood the daemon's log.
func (r *REST) logPollError(instance, name string, err error) {
	if err == nil {
		return
	}
	h := r.host.Load()
	if h == nil {
		return
	}
	key := instance + "\x00" + name
	msg := err.Error()
	r.errMu.Lock()
	stale := r.lastPollErr != nil && r.lastPollErr[key] == msg &&
		time.Since(r.lastPollErrAt[key]) < pollErrorLogInterval
	if !stale {
		if r.lastPollErr == nil {
			r.lastPollErr = map[string]string{}
			r.lastPollErrAt = map[string]time.Time{}
		}
		r.lastPollErr[key] = msg
		r.lastPollErrAt[key] = time.Now()
	}
	r.errMu.Unlock()
	if stale {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h.Log(ctx, instance, fmt.Sprintf("rest poll %s: %v", name, err))
}

// SetHost wires the host channel (pkg/plugin.HostAware): the poller asks it,
// on every poll, for this instance's CURRENT managed-auth access token
// (host.auth, plugin-contract.md §1), and again with refresh after its own
// 401 — rather than running on the connection snapshot start_source captured
// once at startup, which is what let an oauth2 polled source die silently
// after its token expired (the regression this fixes). Serve calls this once,
// before serving anything; nil-safe (an older daemon, or a handler run
// outside plugin.Serve, just never implements managed auth: ApplyAuth falls
// back to the connection's own static auth: unchanged).
func (r *REST) SetHost(h *plugin.HostConn) { r.host.Store(h) }

// Describe declares the type-level contract: connection shape only. Verbs
// and events are per-instance (DescribeInstance).
func (*REST) Describe() plugin.Decl {
	return plugin.Decl{
		Type: "rest", Kind: plugin.KindConnector,
		Desc: "REST: any HTTP API declared in config — base_url + auth + user-defined verbs, optional polled events.",
		Connection: plugin.Schema{
			"base_url": {Type: "string", Required: true, Desc: "API origin every verb path is joined to"},
			"auth":     {Type: "map", Desc: "none | bearer{token} | basic{username,password} | header{name,value} | oauth2{grant,token_url,client_id,client_secret,refresh_token,scopes}"},
			"headers":  {Type: "map", Desc: "default request headers (templated)"},
			"verbs":    {Type: "map", Required: true, Desc: "name -> { method, path, query, headers, body, expect, output }"},
			"events":   {Type: "map", Desc: "name -> { poll, request{method,path,query}, list, id, context } — a polled source"},
		},
	}
}

// --- config decoding (generic map[string]any, the wire shape every call carries) ---

type verbCfg struct {
	Method  string
	Path    string
	Query   map[string]string
	Headers map[string]string
	Body    string
	Expect  []int
	Output  map[string]string
}

type eventCfg struct {
	Poll    time.Duration
	Method  string
	Path    string
	Query   map[string]string
	List    string
	ID      string
	Context map[string]string
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func strMap(v any) map[string]string {
	m, _ := v.(map[string]any)
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = fmt.Sprint(v)
	}
	return out
}

func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	}
	return 0
}

func intList(v any) []int {
	l, _ := v.([]any)
	out := make([]int, 0, len(l))
	for _, e := range l {
		out = append(out, toInt(e))
	}
	return out
}

func verbs(cfg map[string]any) (map[string]verbCfg, []string) {
	raw, _ := cfg["verbs"].(map[string]any)
	out := make(map[string]verbCfg, len(raw))
	names := make([]string, 0, len(raw))
	for name, v := range raw {
		m, _ := v.(map[string]any)
		out[name] = verbCfg{
			Method: str(m, "method"), Path: str(m, "path"),
			Query: strMap(m["query"]), Headers: strMap(m["headers"]),
			Body: str(m, "body"), Expect: intList(m["expect"]), Output: strMap(m["output"]),
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return out, names
}

func events(cfg map[string]any) (map[string]eventCfg, []string) {
	raw, _ := cfg["events"].(map[string]any)
	out := make(map[string]eventCfg, len(raw))
	names := make([]string, 0, len(raw))
	for name, v := range raw {
		m, _ := v.(map[string]any)
		ec := eventCfg{Poll: 5 * time.Minute}
		if p, ok := m["poll"]; ok {
			if d, err := sourcekit.ParseDuration(p); err == nil && d > 0 {
				ec.Poll = d
			}
		}
		if req, ok := m["request"].(map[string]any); ok {
			ec.Method, ec.Path, ec.Query = str(req, "method"), str(req, "path"), strMap(req["query"])
		}
		if ec.Method == "" {
			ec.Method = "GET"
		}
		ec.List, ec.ID = str(m, "list"), str(m, "id")
		ec.Context = strMap(m["context"])
		out[name] = ec
		names = append(names, name)
	}
	sort.Strings(names)
	return out, names
}

// DescribeInstance materializes the instance's user-declared verbs and
// events into a real Decl (Q6): verb options are user-defined request
// shapes, so they are OPEN (unknown-key/type validation is skipped at the
// generic layer; template references are still scope-checked there).
func (r *REST) DescribeInstance(_ context.Context, instance string, cfg map[string]any) (plugin.Decl, error) {
	d := r.Describe()
	vcfg, vnames := verbs(cfg)
	d.Verbs = nil
	for _, name := range vnames {
		v := vcfg[name]
		outputs := plugin.Schema{}
		for o := range v.Output {
			outputs[o] = plugin.Field{Type: "any"}
		}
		d.Verbs = append(d.Verbs, openVerb(name, v.Method+" "+v.Path, outputs))
	}
	ecfg, enames := events(cfg)
	d.Events = nil
	for _, name := range enames {
		ev := ecfg[name]
		ctxSchema := plugin.Schema{"item": {Type: "map", Desc: "the raw list item"}}
		for f := range ev.Context {
			ctxSchema[f] = plugin.Field{Type: "any"}
		}
		d.Events = append(d.Events, plugin.Event{
			Name: name, Desc: "polled: " + ev.Path, Context: ctxSchema,
			// Synthetic target, named after the instance and event (config,
			// the operator's own word) — the same trust shape rss/cron have.
			Semantics: &plugin.EventSemantics{Target: &plugin.TargetSemantics{
				Label: "polled item", Assigned: json.RawMessage("true"),
			}},
		})
	}
	d.Desc = fmt.Sprintf("rest instance %s: %d verb(s), %d event(s)", instance, len(vnames), len(enames))
	return d, nil
}

// openVerb builds a Verb whose option keys are the operator's own
// user-defined request shape (a template's {{.options.*}} references), not a
// fixed schema conductor can enumerate — Verb.Open skips unknown-key/type
// validation for it (pkg/plugin/wire.go).
func openVerb(name, desc string, outputs plugin.Schema) plugin.Verb {
	return plugin.Verb{Name: name, Desc: desc, Open: true, Outputs: outputs}
}

// Validate checks the instance's structural config: base_url, at least one
// verb or event, every verb's method/path, every template, every event's
// list/id. The same checks restImpl.Validate made, run through the
// contract's own optional method (every contract connector's Validate runs
// unconditionally at load — internal/connector's externalImpl.Validate).
func (r *REST) Validate(_ context.Context, req plugin.ValidateRequest) (plugin.ValidateResult, error) {
	var problems []plugin.Problem
	baseURL := str(req.Config, "base_url")
	if baseURL == "" {
		problems = append(problems, plugin.Problem{Path: "base_url", Message: "base_url is required"})
	}
	if err := validateAuth(req.Config["auth"]); err != nil {
		problems = append(problems, plugin.Problem{Path: "auth", Message: err.Error()})
	}
	vcfg, vnames := verbs(req.Config)
	ecfg, enames := events(req.Config)
	if len(vnames) == 0 && len(enames) == 0 {
		problems = append(problems, plugin.Problem{Path: "", Message: "declare at least one verb or event"})
	}
	for _, name := range vnames {
		v := vcfg[name]
		vw := "verbs." + name
		if v.Method == "" || v.Path == "" {
			problems = append(problems, plugin.Problem{Path: vw, Message: "method: and path: are required"})
			continue
		}
		tmpls := map[string]string{"path": v.Path, "body": v.Body}
		for k, q := range v.Query {
			tmpls["query."+k] = q
		}
		for k, h := range v.Headers {
			tmpls["headers."+k] = h
		}
		for k, o := range v.Output {
			tmpls["output."+k] = o
		}
		if err := httpconn.ParseTemplates(vw, tmpls); err != nil {
			problems = append(problems, plugin.Problem{Path: vw, Message: err.Error()})
		}
	}
	for _, name := range enames {
		ev := ecfg[name]
		ew := "events." + name
		if ev.Path == "" {
			problems = append(problems, plugin.Problem{Path: ew, Message: "request.path is required"})
		}
		if ev.List == "" || ev.ID == "" {
			problems = append(problems, plugin.Problem{Path: ew, Message: "list: and id: are required"})
		}
		tmpls := map[string]string{"list": ev.List, "id": ev.ID, "request.path": ev.Path}
		for k, c := range ev.Context {
			tmpls["context."+k] = c
		}
		if err := httpconn.ParseTemplates(ew, tmpls); err != nil {
			problems = append(problems, plugin.Problem{Path: ew, Message: err.Error()})
		}
	}
	declared := map[string]bool{}
	for _, n := range enames {
		declared[n] = true
	}
	names := enames
	for i, t := range req.Triggers {
		if !declared[t.Event] {
			problems = append(problems, plugin.Problem{Path: fmt.Sprintf("triggers[%d]", i),
				Message: fmt.Sprintf("unknown rest event %q (declared: %s)", t.Event, strings.Join(names, ", "))})
		}
	}
	return plugin.ValidateResult{Problems: problems}, nil
}

// validateAuth structurally checks the `auth:` block — the same scheme table
// the generic managed-auth path (internal/connector buildManagedAuth) uses,
// duplicated here ONLY for the error-at-load behavior parity (buildManagedAuth
// already refuses an invalid block at connector construction; this lets a
// bad STATIC scheme — bearer/basic/header, which buildManagedAuth skips
// validating payload-free — surface the same way today's rest/graphql did).
func validateAuth(v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	switch str(m, "type") {
	case "", "none", "bearer", "basic", "header", "oauth2":
		return nil
	default:
		return fmt.Errorf("auth type must be none|bearer|basic|header|oauth2, got %q", str(m, "type"))
	}
}

// Invoke runs one declared verb: render path/query/headers/body, execute,
// check the expected status, extract outputs with type preservation.
func (r *REST) Invoke(req plugin.InvokeRequest) (plugin.InvokeResult, error) {
	vcfg, _ := verbs(req.Connection)
	v, ok := vcfg[req.Verb]
	if !ok {
		return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInvalid, fmt.Sprintf("rest %q: no verb %q", req.Instance, req.Verb), nil)
	}
	baseURL := str(req.Connection, "base_url")
	headers := strMap(req.Connection["headers"])
	secretsVal, _ := req.Connection["secrets"].(map[string]any)
	scope := map[string]any{"options": req.Options, "secrets": secretsVal}

	fullURL, hdrs, err := buildRequest(baseURL, v.Path, v.Query, mergeHeaders(headers, v.Headers), scope)
	if err != nil {
		return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInvalid, fmt.Sprintf("rest %s.%s: %v", req.Instance, req.Verb, err), nil)
	}
	var body []byte
	if v.Body != "" {
		rendered, err := httpconn.RenderBodyTemplate(v.Body, scope)
		if err != nil {
			return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInvalid, fmt.Sprintf("rest %s.%s: body: %v", req.Instance, req.Verb, err), nil)
		}
		body = []byte(rendered)
	}
	resp, err := httpconn.Do(context.Background(), v.Method, fullURL, hdrs, body, ApplyAuth(req.Connection))
	if err != nil {
		return plugin.InvokeResult{}, plugin.Fail(plugin.CodeUpstream, fmt.Sprintf("rest %s.%s: %v", req.Instance, req.Verb, err), nil)
	}
	if !httpconn.ExpectStatus(v.Expect, resp.Status) {
		return plugin.InvokeResult{}, plugin.Fail(plugin.CodeUpstream,
			fmt.Sprintf("rest %s.%s: HTTP %d: %s", req.Instance, req.Verb, resp.Status, httpconn.BodyTail(resp.Body)),
			map[string]any{"status": resp.Status, "retryable": resp.Status == 401 || resp.Status == 429 || resp.Status >= 500})
	}
	out, err := httpconn.ExtractOutputs(v.Output, resp.Scope(req.Options, secretsVal))
	if err != nil {
		return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInternalError, err.Error(), nil)
	}
	return plugin.InvokeResult{Outputs: out}, nil
}

// StartSource polls every triggered event on its own cadence until ctx ends.
// The first poll per event seeds its dedup set silently (the rss cold-start
// behavior): only items seen on a LATER poll are new.
func (r *REST) StartSource(ctx context.Context, req plugin.StartSourceRequest, emit func(any) error) error {
	ecfg, _ := events(req.Config)
	byEvent := map[string]bool{}
	for _, t := range req.Triggers {
		if t.Enabled != nil && !*t.Enabled {
			continue
		}
		byEvent[t.Event] = true
	}
	i := 0
	for name := range byEvent {
		ev, ok := ecfg[name]
		if !ok {
			continue
		}
		go r.pollEvent(ctx, req.Instance, req.Config, name, ev, time.Duration(i)*3*time.Second, emit)
		i++
	}
	<-ctx.Done()
	return nil
}

func (r *REST) pollEvent(ctx context.Context, instance string, conn map[string]any, name string, ev eventCfg, stagger time.Duration, emit func(any) error) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(stagger):
	}
	seen := map[string]bool{}
	primed := false
	t := time.NewTicker(ev.Poll)
	defer t.Stop()
	for {
		r.pollOnce(ctx, instance, conn, name, ev, seen, &primed, emit)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (r *REST) pollOnce(ctx context.Context, instance string, conn map[string]any, name string, ev eventCfg, seen map[string]bool, primed *bool, emit func(any) error) {
	baseURL := str(conn, "base_url")
	resp, err := r.pollRequest(ctx, instance, conn, baseURL, ev, false)
	if err != nil {
		r.logPollError(instance, name, err)
		return
	}
	if resp.Status == http.StatusUnauthorized {
		// The same retry-once-on-401 a verb invoke gets generically
		// (internal/connector's buildManagedAuth/invokePlugin): ask the host
		// for a freshly minted token and try exactly once more. An instance
		// with no managed auth (host.Auth refuses) just gets the same 401
		// again — pollRequest falls back to the connection's own static
		// auth: unchanged, and this retry is a harmless no-op.
		if retried, rerr := r.pollRequest(ctx, instance, conn, baseURL, ev, true); rerr == nil {
			resp = retried
		} else {
			r.logPollError(instance, name, fmt.Errorf("401, and the refreshed-token retry also failed: %w", rerr))
		}
	}
	scope := map[string]any{"response": map[string]any{"status": resp.Status, "body": resp.Body, "headers": resp.Headers}}
	v, err := httpconn.RenderValue(ev.List, scope)
	if err != nil {
		r.logPollError(instance, name, fmt.Errorf("rendering list: %w", err))
		return
	}
	arr, ok := v.([]any)
	if !ok {
		r.logPollError(instance, name, fmt.Errorf("list rendered a %T, not an array", v))
		return
	}
	firstPoll := !*primed
	*primed = true
	for _, e := range arr {
		item, ok := e.(map[string]any)
		if !ok {
			continue
		}
		itemScope := map[string]any{"item": item}
		id, err := httpconn.RenderTemplate(ev.ID, itemScope)
		if err != nil || id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if firstPoll {
			continue
		}
		r.emitItem(instance, name, item, id, itemScope, ev, emit)
	}
}

// pollRequest executes one poll request, asking the host for THIS instance's
// current managed-auth access token first (host.auth) — on every call, not
// just the first — so a long-lived source never runs on a token snapshot
// frozen at start_source time (the regression: before host.auth, a rest
// instance's events: had no way to see a token the daemon minted after the
// source started, so it died silently once the start_source-time token, if
// any, expired). refresh asks the host for a freshly minted one (the 401
// retry in pollOnce). An instance with a STATIC auth scheme (none/bearer/
// basic/header, or no `auth:` at all) falls back unchanged to the
// connection's own `auth:`, exactly as before host.auth existed — host.auth
// always refuses a non-oauth2 instance, which is fine, ApplyAuth handles
// those itself.
//
// An OAUTH2 instance is different (finding 9b): before this, a host.auth
// error here just left callConn as the plain conn, and ApplyAuth has no
// case for auth.type "oauth2" at all (the daemon strips and manages that
// scheme itself, never passing it through) — so the request went out with
// NO Authorization header whatsoever, silently, to whatever upstream this
// instance polls. An oauth2 instance whose token can't be had must instead
// skip the request entirely.
func (r *REST) pollRequest(ctx context.Context, instance string, conn map[string]any, baseURL string, ev eventCfg, refresh bool) (httpconn.Response, error) {
	callConn := conn
	var tokErr error
	if h := r.host.Load(); h != nil {
		tok, err := h.Auth(instance).Token(ctx, refresh)
		switch {
		case err == nil && tok != "":
			callConn = withAccessToken(conn, tok)
		case err != nil:
			tokErr = err
		default:
			tokErr = fmt.Errorf("host.auth returned no token")
		}
	} else {
		tokErr = fmt.Errorf("no host.auth channel available")
	}
	if tokErr != nil && isOAuth2Auth(conn) {
		return httpconn.Response{}, fmt.Errorf("oauth2 instance %q: skipping poll — no managed token: %w", instance, tokErr)
	}
	fullURL, headers, err := buildRequest(baseURL, ev.Path, ev.Query, strMap(callConn["headers"]), map[string]any{"secrets": callConn["secrets"]})
	if err != nil {
		return httpconn.Response{}, err
	}
	return httpconn.Do(ctx, ev.Method, fullURL, headers, nil, ApplyAuth(callConn))
}

// isOAuth2Auth reports whether conn declares a managed oauth2 auth scheme —
// the one scheme ApplyAuth has NO static fallback for (the daemon strips and
// owns it entirely), so it is the one scheme a poll must never run with no
// usable token at all (finding 9b).
func isOAuth2Auth(conn map[string]any) bool {
	auth, _ := conn["auth"].(map[string]any)
	return str(auth, "type") == "oauth2"
}

// withAccessToken returns a COPY of conn carrying tok under
// plugin.AccessTokenKey — the same per-call-copy discipline
// internal/connector's invokePlugin uses for a verb call, so the shared
// connection snapshot every poll tick reads is never mutated out from under
// the others.
func withAccessToken(conn map[string]any, tok string) map[string]any {
	out := make(map[string]any, len(conn)+1)
	for k, v := range conn {
		out[k] = v
	}
	out[plugin.AccessTokenKey] = tok
	return out
}

func (r *REST) emitItem(instance, name string, item map[string]any, id string, scope map[string]any, ev eventCfg, emit func(any) error) {
	dedup := name + "\x00" + id
	trigCtx := map[string]any{"item": item}
	title := instance + ": " + name
	for field, tmpl := range ev.Context {
		v, err := httpconn.RenderValue(tmpl, scope)
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
	_ = emit(plugin.SourceEvent{
		Event: name, Instance: instance, Title: title, Dedup: dedup, Context: trigCtx,
	})
}

func mergeHeaders(sets ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, s := range sets {
		for k, v := range s {
			out[k] = v
		}
	}
	return out
}

// buildRequest renders the path/query/headers over scope and joins the URL.
func buildRequest(baseURL, path string, query, headerTmpls map[string]string, scope map[string]any) (string, map[string]string, error) {
	p, err := httpconn.RenderPathTemplate(path, scope)
	if err != nil {
		return "", nil, err
	}
	fullURL := strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(p, "/")
	if len(query) > 0 {
		q := url.Values{}
		for _, k := range sortedKeys(query) {
			v, err := httpconn.RenderTemplate(query[k], scope)
			if err != nil {
				return "", nil, err
			}
			q.Set(k, v)
		}
		fullURL += "?" + q.Encode()
	}
	headers := map[string]string{}
	for _, k := range sortedKeys(headerTmpls) {
		v, err := httpconn.RenderTemplate(headerTmpls[k], scope)
		if err != nil {
			return "", nil, err
		}
		headers[k] = v
	}
	return fullURL, headers, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ApplyAuth reads the connection's resolved auth and returns a function that
// sets it on an outbound request — either the managed OAuth2 bearer the host
// injected (plugin.AccessToken) or a static scheme (none/bearer/basic/
// header) the daemon passed through verbatim (internal/connector's
// enrichConnection: oauth2 is the only scheme the daemon strips and manages
// itself). Exported so graphql reuses the exact same scheme table.
func ApplyAuth(conn map[string]any) func(*http.Request) {
	return func(r *http.Request) {
		if tok := plugin.AccessToken(conn); tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
			return
		}
		auth, _ := conn["auth"].(map[string]any)
		switch str(auth, "type") {
		case "bearer":
			if tok := str(auth, "token"); tok != "" {
				r.Header.Set("Authorization", "Bearer "+tok)
			}
		case "basic":
			r.SetBasicAuth(str(auth, "username"), str(auth, "password"))
		case "header":
			if n := str(auth, "name"); n != "" {
				r.Header.Set(n, str(auth, "value"))
			}
		}
	}
}
