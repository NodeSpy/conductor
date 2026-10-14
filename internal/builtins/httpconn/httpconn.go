// Package httpconn is the vendor-neutral HTTP calling machinery shared by the
// rest and graphql contract builtins (internal/builtins/rest,
// internal/builtins/graphql): templated request building (with the same
// injection-safe escaping the legacy internal/connector implementation had),
// response parsing, and declared-output extraction with type preservation.
//
// It carries NO auth lifecycle of its own: a connector's static auth
// (none/bearer/basic/header) is the caller's own resolved connection data,
// and managed OAuth2 is the host's job (internal/connector's
// buildManagedAuth, generic for any contract connector) — this package only
// executes the request a caller has already authenticated.
package httpconn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/template"
	"text/template/parse"
	"time"
)

// Client is the shared HTTP client for declared connectors: bounded, no
// special transport (these are ordinary request/response APIs).
var Client = &http.Client{Timeout: 30 * time.Second}

// MaxBody bounds a response body read.
const MaxBody = 8 << 20

// --- Templating --------------------------------------------------------

// TemplateFuncs is the declared-connector template function set: `json`
// encodes any value into a JSON body/fragment; `raw` is the explicit opt-out
// from the default body escaping (the value is spliced verbatim).
var TemplateFuncs = template.FuncMap{
	"json": func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	},
	"raw":      func(v any) any { return v },
	"_pathesc": pathEscapeValue,
	"_jsonesc": jsonEscapeValue,
}

// pathEscapeValue percent-escapes an interpolated value as a single URL path
// segment, so option values from event data cannot add segments (/), splice a
// query string (?/#), or carry encoded traversal into the request path.
func pathEscapeValue(v any) string {
	if v == nil {
		return "" // missingkey=zero: match the old "<no value>" strip
	}
	return url.PathEscape(fmt.Sprint(v))
}

// jsonEscapeValue JSON-encodes an interpolated value. Strings come back as
// their escaped content WITHOUT the surrounding quotes, so the common
// `"{{.options.title}}"` pattern stays valid JSON and a value like
// `","role":"admin` cannot splice new keys into the body. Numbers, bools,
// and structured values encode to their JSON forms.
func jsonEscapeValue(v any) (string, error) {
	if v == nil {
		return "", nil // missingkey=zero: match the old "<no value>" strip
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	s := strings.TrimRight(b.String(), "\n")
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1], nil
	}
	return s, nil
}

// RenderTemplate renders one template string over the request/response
// scope. missingkey=zero matches the flow runner's rendering.
func RenderTemplate(s string, data map[string]any) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	t, err := template.New("t").Option("missingkey=zero").Funcs(TemplateFuncs).Parse(s)
	if err != nil {
		return "", fmt.Errorf("template %q: %w", s, err)
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("template %q: %w", s, err)
	}
	return strings.ReplaceAll(b.String(), "<no value>", ""), nil
}

// RenderPathTemplate renders a verb/event path template with every
// interpolated action piped through pathEscapeValue, then refuses any
// rendered dot-segment. Literal text in the declared path is the config
// author's own; only the interpolated (event-derived) values are escaped.
func RenderPathTemplate(s string, data map[string]any) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	rendered, err := renderEscapedTemplate(s, "_pathesc", nil, data)
	if err != nil {
		return "", err
	}
	for _, seg := range strings.Split(rendered, "/") {
		if seg == "." || seg == ".." {
			return "", fmt.Errorf("path template %q: rendered path %q contains a dot-segment — refusing traversal", s, rendered)
		}
	}
	return rendered, nil
}

// RenderBodyTemplate renders a verb body template with every interpolated
// action piped through jsonEscapeValue, so values are JSON-encoded by
// default and cannot inject body structure. Pipelines that already end in
// `json` (a full JSON fragment) or `raw` (explicit opt-out for non-JSON
// bodies) are left alone.
func RenderBodyTemplate(s string, data map[string]any) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	return renderEscapedTemplate(s, "_jsonesc", map[string]bool{"json": true, "raw": true}, data)
}

func renderEscapedTemplate(s, esc string, skip map[string]bool, data map[string]any) (string, error) {
	t, err := template.New("t").Option("missingkey=zero").Funcs(TemplateFuncs).Parse(s)
	if err != nil {
		return "", fmt.Errorf("template %q: %w", s, err)
	}
	escapeActionList(t.Tree.Root, esc, skip)
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("template %q: %w", s, err)
	}
	return strings.ReplaceAll(b.String(), "<no value>", ""), nil
}

// escapeActionList walks the parse tree and pipes every output action
// through esc. Branch bodies (if/range/with) are walked recursively; their
// condition pipes produce no output and are left alone.
func escapeActionList(list *parse.ListNode, esc string, skip map[string]bool) {
	if list == nil {
		return
	}
	for _, n := range list.Nodes {
		switch n := n.(type) {
		case *parse.ActionNode:
			escapeActionPipe(n.Pipe, esc, skip)
		case *parse.IfNode:
			escapeActionList(n.List, esc, skip)
			escapeActionList(n.ElseList, esc, skip)
		case *parse.RangeNode:
			escapeActionList(n.List, esc, skip)
			escapeActionList(n.ElseList, esc, skip)
		case *parse.WithNode:
			escapeActionList(n.List, esc, skip)
			escapeActionList(n.ElseList, esc, skip)
		}
	}
}

func escapeActionPipe(pipe *parse.PipeNode, esc string, skip map[string]bool) {
	if pipe == nil || len(pipe.Cmds) == 0 || len(pipe.Decl) > 0 {
		return // assignments ({{$x := ...}}) print nothing
	}
	last := pipe.Cmds[len(pipe.Cmds)-1]
	if len(last.Args) > 0 {
		if id, ok := last.Args[0].(*parse.IdentifierNode); ok && skip[id.Ident] {
			return
		}
	}
	pipe.Cmds = append(pipe.Cmds, &parse.CommandNode{
		NodeType: parse.NodeCommand,
		Pos:      pipe.Position(),
		Args:     []parse.Node{parse.NewIdentifier(esc)},
	})
}

// RenderValue renders a template string, preserving the underlying type when
// the whole string is one {{.path}} reference — so
// `invoices: "{{.response.body.Invoices}}"` stays an array.
func RenderValue(s string, data map[string]any) (any, error) {
	if path, ok := soleRef(s); ok {
		if v, found := lookupPath(data, path); found {
			return v, nil
		}
		return nil, nil
	}
	return RenderTemplate(s, data)
}

// soleRef reports whether s is exactly one {{.a.b}} action.
func soleRef(s string) (string, bool) {
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

// lookupPath walks a dotted path through nested maps.
func lookupPath(data map[string]any, path string) (any, bool) {
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

// ParseTemplates parse-checks every template a declared connector will
// render, so a syntax typo disables the connector at load, not at 3am.
func ParseTemplates(where string, tmpls map[string]string) error {
	for what, s := range tmpls {
		if s == "" || !strings.Contains(s, "{{") {
			continue
		}
		if _, err := template.New("t").Funcs(TemplateFuncs).Parse(s); err != nil {
			return fmt.Errorf("%s: bad %s template: %v", where, what, err)
		}
	}
	return nil
}

// --- Request execution ---------------------------------------------------

// Response is the parsed response exposed to output templates.
type Response struct {
	Status  int
	Body    any // parsed JSON (map/array), or {"raw": text} for non-JSON
	Headers map[string]any
}

// Scope builds the template scope for output extraction.
func (r Response) Scope(opts map[string]any, secretsVals map[string]any) map[string]any {
	resp := map[string]any{"status": r.Status, "body": r.Body, "headers": r.Headers}
	if m, ok := r.Body.(map[string]any); ok {
		if data, has := m["data"]; has {
			resp["data"] = data // graphql convenience: {{.response.data.*}}
		}
	}
	return map[string]any{"options": opts, "response": resp, "secrets": secretsVals}
}

// Do executes one request. applyAuth (nil-safe) sets whatever
// Authorization/credential the caller resolved; Do performs no retry of its
// own — a connector reports an upstream 401 as a CodeUpstream error so the
// HOST can decide whether a freshly-minted managed credential is worth a
// retry (internal/connector's generic oauth2 retry-once).
func Do(ctx context.Context, method, fullURL string, headers map[string]string, body []byte, applyAuth func(*http.Request)) (Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, rd)
	if err != nil {
		return Response{}, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if applyAuth != nil {
		applyAuth(req)
	}
	resp, err := Client.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody))
	if err != nil {
		return Response{}, err
	}
	out := Response{Status: resp.StatusCode, Headers: map[string]any{}}
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

// ExpectStatus reports whether a status satisfies the verb's expect: set
// (empty = any 2xx).
func ExpectStatus(expect []int, status int) bool {
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

// BodyTail summarizes a response body for error messages.
func BodyTail(body any) string {
	b, _ := json.Marshal(body)
	s := string(b)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// ExtractOutputs renders each declared output over the response scope.
func ExtractOutputs(output map[string]string, scope map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for name, tmpl := range output {
		v, err := RenderValue(tmpl, scope)
		if err != nil {
			return nil, fmt.Errorf("output %q: %w", name, err)
		}
		out[name] = v
	}
	return out, nil
}
