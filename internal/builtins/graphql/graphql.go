// Package graphql is the `graphql` connector as a contract handler
// (docs/design/plugin-contract.md §1.10): one endpoint, verbs are named
// queries/mutations with templated variables — served in-process over the
// same protocol a spawned plugin speaks.
//
// A graphql instance's verbs are USER-DECLARED (Q6: plugin.describe
// {instance, config} — plugin-contract.md §3.9 G13), materialized by
// DescribeInstance exactly like rest's.
package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/NodeSpy/conductor/internal/builtins/httpconn"
	"github.com/NodeSpy/conductor/internal/builtins/rest"
	"github.com/NodeSpy/conductor/pkg/plugin"
)

// GraphQL is the graphql handler. Stateless, like rest.REST.
type GraphQL struct{}

// New is the graphql handler.
func New() *GraphQL { return &GraphQL{} }

func (*GraphQL) Describe() plugin.Decl {
	return plugin.Decl{
		Type: "graphql", Kind: plugin.KindConnector,
		Desc: "GraphQL: one endpoint, verbs are named queries/mutations with templated variables.",
		Connection: plugin.Schema{
			"endpoint": {Type: "string", Required: true, Desc: "the GraphQL HTTP endpoint"},
			"auth":     {Type: "map", Desc: "the shared auth block (none|bearer|basic|header|oauth2)"},
			"headers":  {Type: "map", Desc: "default request headers (templated)"},
			"verbs":    {Type: "map", Required: true, Desc: "name -> { query, variables, output }"},
		},
	}
}

type verbCfg struct {
	Query     string
	Variables map[string]string
	Output    map[string]string
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

func verbs(cfg map[string]any) (map[string]verbCfg, []string) {
	raw, _ := cfg["verbs"].(map[string]any)
	out := make(map[string]verbCfg, len(raw))
	names := make([]string, 0, len(raw))
	for name, v := range raw {
		m, _ := v.(map[string]any)
		out[name] = verbCfg{Query: str(m, "query"), Variables: strMap(m["variables"]), Output: strMap(m["output"])}
		names = append(names, name)
	}
	sort.Strings(names)
	return out, names
}

// DescribeInstance materializes the instance's user-declared verbs (Q6).
func (g *GraphQL) DescribeInstance(_ context.Context, instance string, cfg map[string]any) (plugin.Decl, error) {
	d := g.Describe()
	vcfg, names := verbs(cfg)
	d.Verbs = nil
	for _, name := range names {
		v := vcfg[name]
		outputs := plugin.Schema{}
		for o := range v.Output {
			outputs[o] = plugin.Field{Type: "any"}
		}
		d.Verbs = append(d.Verbs, plugin.Verb{Name: name, Desc: "graphql operation", Open: true, Outputs: outputs})
	}
	d.Desc = fmt.Sprintf("graphql instance %s: %d verb(s)", instance, len(names))
	return d, nil
}

// Validate checks the instance's structural config: endpoint, auth, at least
// one verb, every verb's query and templates.
func (g *GraphQL) Validate(_ context.Context, req plugin.ValidateRequest) (plugin.ValidateResult, error) {
	var problems []plugin.Problem
	if str(req.Config, "endpoint") == "" {
		problems = append(problems, plugin.Problem{Path: "endpoint", Message: "endpoint is required"})
	}
	if err := validateAuth(req.Config["auth"]); err != nil {
		problems = append(problems, plugin.Problem{Path: "auth", Message: err.Error()})
	}
	vcfg, names := verbs(req.Config)
	if len(names) == 0 {
		problems = append(problems, plugin.Problem{Path: "verbs", Message: "declare at least one verb"})
	}
	for _, name := range names {
		v := vcfg[name]
		vw := "verbs." + name
		if v.Query == "" {
			problems = append(problems, plugin.Problem{Path: vw, Message: "query: is required"})
			continue
		}
		tmpls := map[string]string{}
		for k, t := range v.Variables {
			tmpls["variables."+k] = t
		}
		for k, o := range v.Output {
			tmpls["output."+k] = o
		}
		if err := httpconn.ParseTemplates(vw, tmpls); err != nil {
			problems = append(problems, plugin.Problem{Path: vw, Message: err.Error()})
		}
	}
	return plugin.ValidateResult{Problems: problems}, nil
}

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

// Invoke runs one declared operation: bind variables with type preservation,
// POST the query, fail on a non-empty errors array even at HTTP 200, extract
// outputs from {{.response.data.*}}.
func (g *GraphQL) Invoke(req plugin.InvokeRequest) (plugin.InvokeResult, error) {
	vcfg, _ := verbs(req.Connection)
	v, ok := vcfg[req.Verb]
	if !ok {
		return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInvalid, fmt.Sprintf("graphql %q: no verb %q", req.Instance, req.Verb), nil)
	}
	secretsVal, _ := req.Connection["secrets"].(map[string]any)
	scope := map[string]any{"options": req.Options, "secrets": secretsVal}

	vars := map[string]any{}
	for name, tmpl := range v.Variables {
		val, err := httpconn.RenderValue(tmpl, scope)
		if err != nil {
			return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInvalid, fmt.Sprintf("graphql %s.%s: variable %q: %v", req.Instance, req.Verb, name, err), nil)
		}
		vars[name] = val
	}
	payload, err := json.Marshal(map[string]any{"query": v.Query, "variables": vars})
	if err != nil {
		return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInternalError, err.Error(), nil)
	}

	headers := map[string]string{"Content-Type": "application/json"}
	for k, h := range strMap(req.Connection["headers"]) {
		rv, err := httpconn.RenderTemplate(h, scope)
		if err != nil {
			return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInvalid, fmt.Sprintf("graphql %s.%s: header %q: %v", req.Instance, req.Verb, k, err), nil)
		}
		headers[k] = rv
	}
	endpoint := str(req.Connection, "endpoint")
	resp, err := httpconn.Do(context.Background(), "POST", endpoint, headers, payload, rest.ApplyAuth(req.Connection))
	if err != nil {
		return plugin.InvokeResult{}, plugin.Fail(plugin.CodeUpstream, fmt.Sprintf("graphql %s.%s: %v", req.Instance, req.Verb, err), nil)
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return plugin.InvokeResult{}, plugin.Fail(plugin.CodeUpstream,
			fmt.Sprintf("graphql %s.%s: HTTP %d: %s", req.Instance, req.Verb, resp.Status, httpconn.BodyTail(resp.Body)),
			map[string]any{"status": resp.Status, "retryable": resp.Status == 401 || resp.Status == 429 || resp.Status >= 500})
	}
	if m, ok := resp.Body.(map[string]any); ok {
		if errs, ok := m["errors"].([]any); ok && len(errs) > 0 {
			first := ""
			if em, ok := errs[0].(map[string]any); ok {
				first, _ = em["message"].(string)
			}
			// retryable (finding 10): before this branch, an errors-array-on-
			// HTTP-200 answer still went through the caller's own retry:
			// (execWithRetry/noStepRetry only exclude rate_limited/not_ready
			// and an upstream answer explicitly marked NOT retryable — a nil
			// data map meant absent, which noStepRetry/execWithRetry both read
			// as "not retryable", silently turning every GraphQL
			// errors-on-200 into a non-retryable failure regardless of the
			// step's own retry: block). Restore retryable: true by default —
			// the step's retry: decides, as before — except when EVERY error
			// carries a known client-side extensions.code: a query that can
			// never succeed no matter how many times it's retried.
			return plugin.InvokeResult{}, plugin.Fail(plugin.CodeUpstream,
				fmt.Sprintf("graphql %s.%s: %d error(s): %s", req.Instance, req.Verb, len(errs), first),
				map[string]any{"retryable": !allClientSideGraphQLErrors(errs)})
		}
	}
	out, err := httpconn.ExtractOutputs(v.Output, resp.Scope(req.Options, secretsVal))
	if err != nil {
		return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInternalError, err.Error(), nil)
	}
	return plugin.InvokeResult{Outputs: out}, nil
}

// clientSideGraphQLCodes are GraphQL errors[].extensions.code values the
// GraphQL spec's community conventions (and major server implementations)
// use for a request that is malformed or invalid as written — retrying it
// verbatim would just fail the same way forever.
var clientSideGraphQLCodes = map[string]bool{
	"GRAPHQL_VALIDATION_FAILED": true,
	"GRAPHQL_PARSE_FAILED":      true,
	"BAD_USER_INPUT":            true,
}

// allClientSideGraphQLErrors reports whether EVERY error in errs carries a
// known client-side extensions.code. Anything else — an unrecognized or
// absent code, a transient backend error — defaults to retryable: the caller
// (allClientSideGraphQLErrors' one call site) fails closed toward
// "retryable: true" unless every error is POSITIVELY identified as
// client-side, matching finding 10: before the nil-data regression, the
// step's own retry: always got a say.
func allClientSideGraphQLErrors(errs []any) bool {
	if len(errs) == 0 {
		return false
	}
	for _, e := range errs {
		em, ok := e.(map[string]any)
		if !ok {
			return false
		}
		ext, _ := em["extensions"].(map[string]any)
		code, _ := ext["code"].(string)
		if !clientSideGraphQLCodes[code] {
			return false
		}
	}
	return true
}
