package connector

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// graphqlDecl is the STATIC declaration for type: graphql; verbs are
// user-declared, so each instance materializes its own TypeDecl.
var graphqlDecl = &TypeDecl{
	Type: "graphql",
	Desc: "GraphQL: one endpoint, verbs are named queries/mutations with templated variables.",
	Connection: Schema{
		"endpoint": {Type: TString, Required: true, Desc: "the GraphQL HTTP endpoint"},
		"auth":     {Type: TMap, Desc: "the shared auth block (none|bearer|basic|header|oauth2)"},
		"headers":  {Type: TMap, Desc: "default request headers (templated)"},
		"verbs":    {Type: TMap, Required: true, Desc: "name -> { query, variables, output }"},
	},
}

func init() { RegisterType(graphqlDecl, newGraphQLImpl) }

// gqlVerbCfg is one user-declared query/mutation.
type gqlVerbCfg struct {
	Query     string            `yaml:"query"`
	Variables map[string]string `yaml:"variables"`
	Output    map[string]string `yaml:"output"`
}

type gqlConn struct {
	Endpoint string                `yaml:"endpoint"`
	Auth     authConfig            `yaml:"auth"`
	Headers  map[string]string     `yaml:"headers"`
	Verbs    map[string]gqlVerbCfg `yaml:"verbs"`
}

type graphqlImpl struct {
	name    string
	conn    gqlConn
	auth    *authenticator
	secrets map[string]any
}

func newGraphQLImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	var conn gqlConn
	if err := ref.Decode(&conn); err != nil {
		return nil, fmt.Errorf("connector %q: decode graphql connection: %w", name, err)
	}
	ctx := context.Background()
	au, err := newAuthenticator(ctx, name, conn.Auth, deps.Secrets, deps.Log)
	if err != nil {
		return nil, err
	}
	return &graphqlImpl{
		name: name, conn: conn, auth: au,
		secrets: resolveNamedSecrets(ctx, deps.Config, deps.Secrets),
	}, nil
}

// InstanceDecl materializes the user-declared verbs (Open options).
func (g *graphqlImpl) InstanceDecl(base *TypeDecl) *TypeDecl {
	d := *base
	d.Verbs = nil
	for _, name := range sortedKeysOf(g.conn.Verbs) {
		v := g.conn.Verbs[name]
		outputs := Schema{}
		for o := range v.Output {
			outputs[o] = Field{Type: TAny}
		}
		d.Verbs = append(d.Verbs, VerbDecl{
			Name: name, Desc: "graphql operation",
			Open: true, Outputs: outputs,
		})
	}
	return &d
}

func (g *graphqlImpl) Validate() error {
	w := fmt.Sprintf("connector %q", g.name)
	if g.conn.Endpoint == "" {
		return fmt.Errorf("%s: endpoint is required", w)
	}
	if err := g.conn.Auth.validate(w); err != nil {
		return err
	}
	if len(g.conn.Verbs) == 0 {
		return fmt.Errorf("%s: declare at least one verb", w)
	}
	for name, v := range g.conn.Verbs {
		vw := fmt.Sprintf("%s verb %q", w, name)
		if v.Query == "" {
			return fmt.Errorf("%s: query: is required", vw)
		}
		tmpls := map[string]string{}
		for k, t := range v.Variables {
			tmpls["variables."+k] = t
		}
		for k, o := range v.Output {
			tmpls["output."+k] = o
		}
		if err := parseHTTPTemplates(vw, tmpls); err != nil {
			return err
		}
	}
	return nil
}

func (g *graphqlImpl) DeclaredEvents() []string { return nil }

func (g *graphqlImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	if len(triggers) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("connector %q (graphql) has no source events", g.name)
}

func (g *graphqlImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	v, ok := g.conn.Verbs[verb]
	if !ok {
		return nil, fmt.Errorf("graphql %q: no verb %q", g.name, verb)
	}
	scope := map[string]any{"options": opts, "secrets": g.secrets}

	// Variables render with type preservation (a sole {{.path}} keeps its
	// underlying list/map/number shape).
	vars := map[string]any{}
	for name, tmpl := range v.Variables {
		val, err := renderHTTPValue(tmpl, scope)
		if err != nil {
			return nil, fmt.Errorf("graphql %s.%s: variable %q: %w", g.name, verb, name, err)
		}
		vars[name] = val
	}
	payload, err := json.Marshal(map[string]any{"query": v.Query, "variables": vars})
	if err != nil {
		return nil, fmt.Errorf("graphql %s.%s: encode: %w", g.name, verb, err)
	}

	headers := map[string]string{"Content-Type": "application/json"}
	for k, h := range g.conn.Headers {
		rv, err := renderHTTPTemplate(h, scope)
		if err != nil {
			return nil, fmt.Errorf("graphql %s.%s: header %q: %w", g.name, verb, k, err)
		}
		headers[k] = rv
	}
	resp, err := doHTTPAPI(ctx, g.auth, "POST", g.conn.Endpoint, headers, payload)
	if err != nil {
		return nil, fmt.Errorf("graphql %s.%s: %w", g.name, verb, err)
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return nil, fmt.Errorf("graphql %s.%s: HTTP %d: %s", g.name, verb, resp.Status, bodyTail(resp.Body))
	}
	// A non-empty errors array is a failure even on HTTP 200.
	if m, ok := resp.Body.(map[string]any); ok {
		if errs, ok := m["errors"].([]any); ok && len(errs) > 0 {
			first := ""
			if em, ok := errs[0].(map[string]any); ok {
				first, _ = em["message"].(string)
			}
			return nil, fmt.Errorf("graphql %s.%s: %d error(s): %s", g.name, verb, len(errs), first)
		}
	}
	return extractOutputsHTTP(v.Output, resp.scope(opts, g.secrets))
}
