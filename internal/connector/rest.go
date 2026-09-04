package connector

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// restDecl is the STATIC declaration for type: rest — the verbs and events
// are user-declared, so each instance materializes its own TypeDecl (see
// InstanceDecl).
var restDecl = &TypeDecl{
	Type: "rest",
	Desc: "REST: any HTTP API declared in config — base_url + auth + user-defined verbs, optional polled events.",
	Connection: Schema{
		"base_url": {Type: TString, Required: true, Desc: "API origin every verb path is joined to"},
		"auth":     {Type: TMap, Desc: "none | bearer{token} | basic{username,password} | header{name,value} | oauth2{grant,token_url,client_id,client_secret,refresh_token,scopes}"},
		"headers":  {Type: TMap, Desc: "default request headers (templated)"},
		"verbs":    {Type: TMap, Required: true, Desc: "name -> { method, path, query, headers, body, expect, output }"},
		"events":   {Type: TMap, Desc: "name -> { poll, request{method,path,query}, list, id, context } — a polled source"},
	},
	Events: []EventDecl{
		{Name: "<declared>", Dynamic: true, Desc: "a polled events: entry produced a new item",
			Context: Schema{"item": {Type: TMap, Desc: "the raw list item"}}},
	},
}

func init() { RegisterType(restDecl, newRESTConnImpl) }

// restVerbCfg is one user-declared verb.
type restVerbCfg struct {
	Method  string            `yaml:"method"`
	Path    string            `yaml:"path"`
	Query   map[string]string `yaml:"query"`
	Headers map[string]string `yaml:"headers"`
	Body    string            `yaml:"body"`
	Expect  []int             `yaml:"expect"`
	Output  map[string]string `yaml:"output"`
}

// restEventCfg is one user-declared polled event.
type restEventCfg struct {
	Poll    config.Duration `yaml:"poll"`
	Request struct {
		Method string            `yaml:"method"`
		Path   string            `yaml:"path"`
		Query  map[string]string `yaml:"query"`
	} `yaml:"request"`
	List    string            `yaml:"list"`
	ID      string            `yaml:"id"`
	Context map[string]string `yaml:"context"`
}

type restConn struct {
	BaseURL string                  `yaml:"base_url"`
	Auth    authConfig              `yaml:"auth"`
	Headers map[string]string       `yaml:"headers"`
	Verbs   map[string]restVerbCfg  `yaml:"verbs"`
	Events  map[string]restEventCfg `yaml:"events"`
}

type restImpl struct {
	name    string
	conn    restConn
	auth    *authenticator
	secrets map[string]any // named secrets: block, resolved for {{.secrets.*}}
}

func newRESTConnImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	var conn restConn
	if err := ref.Decode(&conn); err != nil {
		return nil, fmt.Errorf("connector %q: decode rest connection: %w", name, err)
	}
	ctx := context.Background()
	au, err := newAuthenticator(ctx, name, conn.Auth, deps.Secrets, deps.Log)
	if err != nil {
		return nil, err
	}
	return &restImpl{
		name: name, conn: conn, auth: au,
		secrets: resolveNamedSecrets(ctx, deps.Config, deps.Secrets),
	}, nil
}

// InstanceDecl materializes the user-declared verbs and events into a real
// TypeDecl, so validation/introspection/InvokeFinal see this instance's
// actual contract. Verb options are user-defined patterns → Open.
func (r *restImpl) InstanceDecl(base *TypeDecl) *TypeDecl {
	d := *base
	d.Verbs = nil
	for _, name := range sortedKeysOf(r.conn.Verbs) {
		v := r.conn.Verbs[name]
		outputs := Schema{}
		for o := range v.Output {
			outputs[o] = Field{Type: TAny}
		}
		d.Verbs = append(d.Verbs, VerbDecl{
			Name: name, Desc: v.Method + " " + v.Path,
			Open: true, Outputs: outputs,
		})
	}
	d.Events = nil
	for _, name := range sortedKeysOf(r.conn.Events) {
		ev := r.conn.Events[name]
		ctxSchema := Schema{"item": {Type: TMap, Desc: "the raw list item"}}
		for f := range ev.Context {
			ctxSchema[f] = Field{Type: TAny}
		}
		d.Events = append(d.Events, EventDecl{
			Name: name, Desc: "polled: " + ev.Request.Path, Context: ctxSchema,
		})
	}
	return &d
}

func sortedKeysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (r *restImpl) Validate() error {
	w := fmt.Sprintf("connector %q", r.name)
	if r.conn.BaseURL == "" {
		return fmt.Errorf("%s: base_url is required", w)
	}
	if err := r.conn.Auth.validate(w); err != nil {
		return err
	}
	if len(r.conn.Verbs) == 0 && len(r.conn.Events) == 0 {
		return fmt.Errorf("%s: declare at least one verb or event", w)
	}
	for name, v := range r.conn.Verbs {
		vw := fmt.Sprintf("%s verb %q", w, name)
		if v.Method == "" || v.Path == "" {
			return fmt.Errorf("%s: method: and path: are required", vw)
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
		if err := parseHTTPTemplates(vw, tmpls); err != nil {
			return err
		}
	}
	for name, ev := range r.conn.Events {
		ew := fmt.Sprintf("%s event %q", w, name)
		if ev.Request.Path == "" {
			return fmt.Errorf("%s: request.path is required", ew)
		}
		if ev.List == "" || ev.ID == "" {
			return fmt.Errorf("%s: list: and id: are required", ew)
		}
		tmpls := map[string]string{"list": ev.List, "id": ev.ID, "request.path": ev.Request.Path}
		for k, c := range ev.Context {
			tmpls["context."+k] = c
		}
		if err := parseHTTPTemplates(ew, tmpls); err != nil {
			return err
		}
	}
	return nil
}

func (r *restImpl) DeclaredEvents() []string { return sortedKeysOf(r.conn.Events) }

// Source lowers the connector's triggers onto its polled events.
func (r *restImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	if len(triggers) == 0 {
		return nil, nil
	}
	byEvent := map[string][]CompiledTrigger{}
	for _, t := range triggers {
		name := t.Spec.Event()
		if _, ok := r.conn.Events[name]; !ok {
			return nil, fmt.Errorf("trigger on %s: unknown rest event %q (declared: %s)", t.Spec.On, name, strings.Join(r.DeclaredEvents(), ", "))
		}
		byEvent[name] = append(byEvent[name], t)
	}
	var events []polledEvent
	for _, name := range sortedKeysOf(byEvent) {
		cfg := r.conn.Events[name]
		poll := cfg.Poll.D()
		if poll <= 0 {
			poll = 5 * time.Minute
		}
		method := cfg.Request.Method
		if method == "" {
			method = "GET"
		}
		events = append(events, polledEvent{
			Name: name, Poll: poll, Method: method,
			Path: cfg.Request.Path, Query: cfg.Request.Query,
			List: cfg.List, ID: cfg.ID, Context: cfg.Context,
			Triggers: byEvent[name],
		})
	}
	return &httpPoller{source: "rest", name: r.name, req: r, events: events}, nil
}

// pollRequest runs one polled event's request through the connector's base
// URL, headers, and auth.
func (r *restImpl) pollRequest(ctx context.Context, ev polledEvent) (httpAPIResponse, error) {
	scope := map[string]any{"secrets": r.secrets}
	fullURL, headers, err := r.buildRequest(ev.Path, ev.Query, nil, scope)
	if err != nil {
		return httpAPIResponse{}, err
	}
	return doHTTPAPI(ctx, r.auth, ev.Method, fullURL, headers, nil)
}

// buildRequest renders the path/query/headers over scope and joins the URL.
func (r *restImpl) buildRequest(path string, query, verbHeaders map[string]string, scope map[string]any) (string, map[string]string, error) {
	p, err := renderHTTPTemplate(path, scope)
	if err != nil {
		return "", nil, err
	}
	fullURL := strings.TrimRight(r.conn.BaseURL, "/") + "/" + strings.TrimLeft(p, "/")
	if len(query) > 0 {
		q := url.Values{}
		for _, k := range sortedKeysOf(query) {
			v, err := renderHTTPTemplate(query[k], scope)
			if err != nil {
				return "", nil, err
			}
			q.Set(k, v)
		}
		fullURL += "?" + q.Encode()
	}
	headers := map[string]string{}
	for _, hs := range []map[string]string{r.conn.Headers, verbHeaders} {
		for k, v := range hs {
			rv, err := renderHTTPTemplate(v, scope)
			if err != nil {
				return "", nil, err
			}
			headers[k] = rv
		}
	}
	return fullURL, headers, nil
}

func (r *restImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	v, ok := r.conn.Verbs[verb]
	if !ok {
		return nil, fmt.Errorf("rest %q: no verb %q", r.name, verb)
	}
	scope := map[string]any{"options": opts, "secrets": r.secrets}
	fullURL, headers, err := r.buildRequest(v.Path, v.Query, v.Headers, scope)
	if err != nil {
		return nil, fmt.Errorf("rest %s.%s: %w", r.name, verb, err)
	}
	var body []byte
	if v.Body != "" {
		rendered, err := renderHTTPTemplate(v.Body, scope)
		if err != nil {
			return nil, fmt.Errorf("rest %s.%s: body: %w", r.name, verb, err)
		}
		body = []byte(rendered)
	}
	resp, err := doHTTPAPI(ctx, r.auth, v.Method, fullURL, headers, body)
	if err != nil {
		return nil, fmt.Errorf("rest %s.%s: %w", r.name, verb, err)
	}
	if !expectStatus(v.Expect, resp.Status) {
		return nil, fmt.Errorf("rest %s.%s: HTTP %d: %s", r.name, verb, resp.Status, bodyTail(resp.Body))
	}
	return extractOutputsHTTP(v.Output, resp.scope(opts, r.secrets))
}
