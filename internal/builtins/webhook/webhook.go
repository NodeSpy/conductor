// Package webhook is the `webhook` connector as a contract handler
// (docs/design/plugin-contract.md §1.10): a generic inbound JSON delivery via
// a field-mapping DSL, plus a generic outbound HTTP post verb, served
// in-process over the same protocol a spawned plugin speaks.
//
// Each configured `sources:` entry becomes one declared, concrete event (Q6:
// plugin.describe{instance, config} — the event set depends on the
// instance's own config, so the type-level Describe() alone cannot name
// them). A source's STATIC `repo:` (the operator's own word) declares
// target.assigned: true; a body-templated `repo:` — rendered from data the
// SENDER controls — declares target.assigned: false. Both are knowable from
// config alone, so the declaration is a literal, not a fact the plugin
// computes per delivery.
package webhook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/NodeSpy/conductor/internal/inbound"
	"github.com/NodeSpy/conductor/internal/netguard"
	"github.com/NodeSpy/conductor/pkg/plugin"
)

// deliveryIDHeader is the one header name this builtin treats as a
// vendor-neutral delivery id, when a sender sets it. It is UNSIGNED (no
// `sign:` scheme covers it, even when the body is HMAC-verified), so it is
// never trusted as a dedup gate: a source with no `dedup:` template fires on
// every delivery regardless (see deliveryFallbackKey) — this id only ever
// folds into the synthetic TARGET key alongside the body's own hash, purely
// to give deliveries with distinct ids distinct targets. Operators who want
// replay protection declare an explicit `dedup:` template over `{{.body...}}`
// (dedup: templates only see the parsed body, not headers); without one,
// conductor does not invent suppression from the body or this header behind
// the operator's back.
const deliveryIDHeader = "X-Delivery-Id"

const maxBody = 25 << 20

// Webhook is the webhook handler. HTTP and resolve are seams for tests.
type Webhook struct {
	// resolve overrides the DNS resolver the egress guard uses (tests only).
	resolve func(ctx context.Context, host string) ([]net.IPAddr, error)
}

// New is the webhook handler.
func New() *Webhook { return &Webhook{} }

// Describe declares the type-level webhook contract: connection shape, the
// `post` verb, and a single Dynamic placeholder event for a describe that
// has no instance config to materialize concrete sources from (plugin
// introspection, `conductor schema`, an older host that never asks
// per-instance).
func (*Webhook) Describe() plugin.Decl {
	return plugin.Decl{
		Type: "webhook", Kind: plugin.KindConnector,
		Desc: "Webhook: generic inbound JSON delivery via a field-mapping DSL; plus a generic outbound HTTP post verb.",
		Connection: plugin.Schema{
			"listen":        {Type: "string", Desc: "direct HTTP listener address, e.g. :8099"},
			"smee_url":      {Type: "string", Desc: "smee.io channel (no public ingress needed)"},
			"sources":       {Type: "map", Required: true, Desc: "name -> { path, sign: {header,secret,scheme}, match, title, dedup, repo }. dedup: a template rendering the operator's own signature for replay protection; unset (the default) fires on EVERY delivery — no dedup check at all, so a sender that legitimately re-POSTs an identical payload (heartbeat/status webhooks) is never silently suppressed"},
			"allow_private": {Type: "boolean", Desc: "permit webhook.post to reach loopback/private/link-local/CGNAT addresses (default false — blocked to prevent SSRF)"},
			"allow_hosts":   {Type: "list", Desc: "specific hosts (by name) or exact IPs that webhook.post may reach even in an otherwise-blocked range"},
		},
		Events: []plugin.Event{{
			Name: "<source>", Dynamic: true, Desc: "a configured source received a delivery",
			Context: plugin.Schema{
				"body":   {Type: "map"},
				"kind":   {Type: "string"},
				"title":  {Type: "string"},
				"repo":   {Type: "string"},
				"number": {Type: "integer"},
			},
		}},
		Verbs: []plugin.Verb{{
			Name: "post", Desc: "generic outbound HTTP request",
			Options: plugin.Schema{
				"url":     {Type: "string", Required: true},
				"method":  {Type: "string", Desc: "HTTP method (default POST)"},
				"headers": {Type: "map", Desc: "request headers"},
				"body":    {Type: "string", Desc: "raw request body"},
				"json":    {Type: "any", Desc: "value to JSON-marshal as the body (mutually exclusive with body)"},
				"timeout": {Type: "duration", Desc: "request timeout (default 30s)"},
			},
			Outputs: plugin.Schema{
				"status": {Type: "integer"},
				"body":   {Type: "string", Desc: "response body, capped at 64KB"},
			},
		}},
	}
}

// sourceCfg is one configured `sources:` entry, decoded from the generic
// config map every contract call carries.
type sourceCfg struct {
	Path  string
	Sign  signCfg
	Match string
	Title string
	Dedup string
	Repo  string
}

type signCfg struct {
	Header string
	Secret string
	Scheme string
}

// sources decodes the instance's `sources:` map, in a stable order.
func sources(cfg map[string]any) (map[string]sourceCfg, []string) {
	raw, _ := cfg["sources"].(map[string]any)
	out := make(map[string]sourceCfg, len(raw))
	names := make([]string, 0, len(raw))
	for name, v := range raw {
		m, _ := v.(map[string]any)
		sc := sourceCfg{}
		sc.Path, _ = m["path"].(string)
		sc.Match, _ = m["match"].(string)
		sc.Title, _ = m["title"].(string)
		sc.Dedup, _ = m["dedup"].(string)
		sc.Repo, _ = m["repo"].(string)
		if sm, ok := m["sign"].(map[string]any); ok {
			sc.Sign.Header, _ = sm["header"].(string)
			sc.Sign.Secret, _ = sm["secret"].(string)
			sc.Sign.Scheme, _ = sm["scheme"].(string)
		}
		out[name] = sc
		names = append(names, name)
	}
	sort.Strings(names)
	return out, names
}

// DescribeInstance materializes one concrete EventDecl per configured
// source (Q6), each carrying the target.assigned the engine's generalized
// untrusted-target warning (internal/flow/skillverbs.go) and target
// semantics read statically — no event ever needs to be fired to know it.
func (w *Webhook) DescribeInstance(_ context.Context, instance string, cfg map[string]any) (plugin.Decl, error) {
	d := w.Describe()
	srcs, names := sources(cfg)
	d.Events = make([]plugin.Event, 0, len(names))
	for _, name := range names {
		sc := srcs[name]
		assigned := !strings.Contains(sc.Repo, "{{") // "" (synthetic) or a static literal: the operator's own word
		label := "webhook delivery"
		d.Events = append(d.Events, plugin.Event{
			Name: name, Desc: fmt.Sprintf("webhook source %q received a delivery", name),
			Context: plugin.Schema{
				"body":   {Type: "map"},
				"kind":   {Type: "string"},
				"title":  {Type: "string"},
				"repo":   {Type: "string"},
				"number": {Type: "integer"},
			},
			Semantics: &plugin.EventSemantics{
				Target: &plugin.TargetSemantics{
					Label:    label,
					Assigned: mustJSON(assigned),
				},
			},
		})
	}
	d.Desc = fmt.Sprintf("webhook instance %s: %d source(s)", instance, len(names))
	return d, nil
}

func mustJSON(v bool) json.RawMessage {
	if v {
		return json.RawMessage("true")
	}
	return json.RawMessage("false")
}

// Validate checks the instance's config and that every trigger names a
// declared source — the same checks `webhookImpl.Validate`/`Source` made,
// now run through the contract's own optional method instead of a
// connector.Impl Go interface.
func (w *Webhook) Validate(_ context.Context, req plugin.ValidateRequest) (plugin.ValidateResult, error) {
	var problems []plugin.Problem
	listen, _ := req.Config["listen"].(string)
	smeeURL, _ := req.Config["smee_url"].(string)
	if listen == "" && smeeURL == "" {
		problems = append(problems, plugin.Problem{Path: "listen", Message: "set `listen` and/or `smee_url`"})
	}
	srcs, names := sources(req.Config)
	if len(names) == 0 {
		problems = append(problems, plugin.Problem{Path: "sources", Message: "no sources"})
	}
	for _, name := range names {
		sc := srcs[name]
		p := "sources." + name
		if listen != "" && sc.Path == "" {
			problems = append(problems, plugin.Problem{Path: p + ".path", Message: "required with a listener"})
		}
		if smeeURL != "" && sc.Match == "" && len(names) > 1 {
			problems = append(problems, plugin.Problem{Path: p + ".match", Message: "required to route a smee channel across multiple sources"})
		}
		for field, tmpl := range map[string]string{"match": sc.Match, "title": sc.Title, "dedup": sc.Dedup, "repo": sc.Repo} {
			if _, err := template.New("t").Parse(tmpl); err != nil {
				problems = append(problems, plugin.Problem{Path: p + "." + field, Message: "bad template: " + err.Error()})
			}
		}
	}
	for i, t := range req.Triggers {
		if _, ok := srcs[t.Event]; !ok {
			problems = append(problems, plugin.Problem{Path: fmt.Sprintf("triggers[%d]", i),
				Message: fmt.Sprintf("unknown webhook source %q (declared: %s)", t.Event, strings.Join(names, ", "))})
		}
	}
	return plugin.ValidateResult{Problems: problems}, nil
}

// StartSource runs the instance's direct listener and/or smee relay until ctx
// ends, emitting one event per matched delivery that isn't a duplicate of an
// operator-declared `dedup:` signature (a source with no `dedup:` has no
// dedup gate: every matched delivery fires). Routing to
// triggers is entirely generic (the daemon matches `on: <instance>.<source>`
// the same way it does for any other plugin source) — the plugin does not
// group deliveries by trigger/action the way the legacy integration did.
func (w *Webhook) StartSource(ctx context.Context, req plugin.StartSourceRequest, emit func(any) error) error {
	srcs, names := sources(req.Config)
	if len(names) == 0 {
		return fmt.Errorf("webhook[%s]: no sources", req.Instance)
	}
	listen, _ := req.Config["listen"].(string)
	smeeURL, _ := req.Config["smee_url"].(string)
	seen := inbound.NewDeliveryDedup(2048)

	if listen != "" {
		for _, name := range names {
			sc := srcs[name]
			inbound.Register(ctx, listen, sc.Path, w.handler(ctx, req.Instance, name, sc, seen, emit), discardLog)
		}
	}
	if smeeURL != "" {
		go func() {
			_ = inbound.Smee(ctx, smeeURL, discardLog, func(f inbound.Frame) {
				for _, name := range names {
					sc := srcs[name]
					sig := ""
					if sc.Sign.Header != "" {
						sig = f.Header(sc.Sign.Header)
					}
					w.deliver(req.Instance, name, sc, sig, f.Body, true, seen, emit, f.Header(deliveryIDHeader))
				}
			})
		}()
	}
	<-ctx.Done()
	return nil
}

func discardLog(string, ...any) {}

func (w *Webhook) handler(ctx context.Context, instance, name string, sc sourceCfg, seen *inbound.DeliveryDedup, emit func(any) error) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(rw, r.Body, maxBody))
		if err != nil {
			http.Error(rw, "read body", http.StatusBadRequest)
			return
		}
		if sc.Sign.Secret != "" && !inbound.VerifyHMAC(sc.Sign.Secret, body, r.Header.Get(sc.Sign.Header), sc.Sign.Scheme) {
			http.Error(rw, "bad signature", http.StatusUnauthorized)
			return
		}
		w.deliver(instance, name, sc, "", body, false, seen, emit, r.Header.Get(deliveryIDHeader))
		rw.WriteHeader(http.StatusAccepted)
	}
}

// deliver maps one raw body through a source's templates and emits ONE event,
// unless the match predicate rejects it or an operator-declared `dedup:`
// signature says this is a duplicate. A source with no `dedup:` has no dedup
// gate at all: it fires on every delivery. smeeSig/viaSmee: a smee relay
// re-serializes the body, so its signature check is best-effort and applied
// here rather than at a listener boundary.
//
// deliveryID is the sender's own delivery id (deliveryIDHeader), when it set
// one — used only to help compute a distinct synthetic TARGET key for a
// source that declares no `dedup:` template (below); it is never a dedup
// gate.
func (w *Webhook) deliver(instance, name string, sc sourceCfg, smeeSig string, body []byte, viaSmee bool, seen *inbound.DeliveryDedup, emit func(any) error, deliveryID string) {
	if viaSmee && sc.Sign.Secret != "" && !inbound.VerifyHMAC(sc.Sign.Secret, body, smeeSig, sc.Sign.Scheme) {
		return
	}
	parsed := parseBody(body)
	data := map[string]any{"body": parsed}

	if sc.Match != "" && strings.TrimSpace(render(sc.Match, data)) != "true" {
		return // this delivery isn't for this source
	}
	dedup := render(sc.Dedup, data)
	// synthKey is the per-delivery TARGET/synth key, never a dedup gate by
	// itself. A declared `dedup:` renders to the operator's own signature,
	// and remains the (only) dedup gate below, unaffected by any of this.
	//
	// A source with NO `dedup:` template fires on EVERY delivery — that is
	// the documented contract (`git show
	// 4cade34:internal/integrations/webhook/webhook.go`: `Dedup ""` = "fire
	// on every delivery"), because sources like a heartbeat/status webhook
	// legitimately re-POST an identical payload and must not be silently
	// suppressed. A sender that wants replay protection gets it by declaring
	// `dedup:`; conductor does not invent one from the body behind the
	// operator's back. synthKey still needs SOME value so two deliveries
	// don't collapse onto the same synthetic target: a hash of the body,
	// optionally combined with the sender's own (unsigned) delivery id when
	// set, gives each delivery its own target deterministically — but, since
	// there is no dedup: declared, it is used ONLY to compute that key, never
	// checked against `seen`. A sender who pre-sends under a future
	// delivery's id, or replays a byte-identical body under a new id, cannot
	// suppress anything this way: there is no gate left to fool.
	synthKey := dedup
	if sc.Dedup == "" {
		synthKey = deliveryFallbackKey(deliveryID, body)
	} else if !seen.Add(name + "\x00" + synthKey) {
		return // duplicate delivery (smee redelivery / retried POST) of an operator-declared dedup: signature
	}

	title := render(sc.Title, data)
	if title == "" {
		title = fmt.Sprintf("%s: %s", instance, name)
	}
	repo := strings.TrimSpace(render(sc.Repo, data))

	var tgt plugin.Target
	if repo == "" {
		synth := inbound.SyntheticTarget("webhook:"+name, name+synthKey)
		tgt = plugin.Target{Repo: synth.Repo, Number: synth.Number}
	} else {
		owner, nm, _ := strings.Cut(repo, "/")
		tgt = plugin.Target{Repo: repo, Owner: owner, Name: nm, Number: inbound.SyntheticTarget("", name+synthKey).Number}
	}

	_ = emit(plugin.SourceEvent{
		Event: name, Instance: instance, Title: title, Dedup: dedup,
		Target:  tgt,
		Context: map[string]any{"body": parsed, "kind": name, "title": title, "repo": repo, "number": tgt.Number},
	})
}

// deliveryFallbackKey is the TARGET/synth key for a source with no declared
// `dedup:` template: a content hash of the body, combined with the sender's
// own (unsigned) delivery id when it set one, purely so that two deliveries
// which happen to share an identical body (and nothing else) still get
// distinct synthetic targets when an id is available to tell them apart. It
// is NOT a dedup gate — a source with no `dedup:` fires on every delivery
// (see the call site in deliver) — so a sender choosing what id (or none) to
// send can influence the target a delivery lands on, but can never suppress
// a delivery this way.
func deliveryFallbackKey(deliveryID string, body []byte) string {
	key := "sha256:" + bodyHash(body)
	if deliveryID != "" {
		key += ":id:" + deliveryID
	}
	return key
}

func bodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func parseBody(body []byte) any {
	var m map[string]any
	if json.Unmarshal(body, &m) == nil {
		return m
	}
	return map[string]any{"raw": string(body)}
}

func render(tmpl string, data map[string]any) string {
	if tmpl == "" {
		return ""
	}
	t, err := template.New("t").Option("missingkey=zero").Parse(tmpl)
	if err != nil {
		return ""
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return ""
	}
	return b.String()
}

// --- the `post` verb: a generic, SSRF-guarded outbound HTTP request -------

var errWebhookBlockedIP = fmt.Errorf("webhook.post: refused: resolves to a blocked address (loopback/private/link-local); set allow_private or allow_hosts to permit it")

func (w *Webhook) Invoke(req plugin.InvokeRequest) (plugin.InvokeResult, error) {
	if req.Verb != "post" {
		return plugin.InvokeResult{}, plugin.Fail(plugin.CodeMethodNotFound, "webhook: unknown verb "+req.Verb, nil)
	}
	url, _ := req.Options["url"].(string)
	if url == "" {
		return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInvalid, "webhook.post: options.url is required", nil)
	}
	method, _ := req.Options["method"].(string)
	if method == "" {
		method = http.MethodPost
	}
	bodyStr, _ := req.Options["body"].(string)
	jsonVal, hasJSON := req.Options["json"]
	if bodyStr != "" && hasJSON {
		return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInvalid, "webhook.post: set options.body or options.json, not both", nil)
	}
	var reader io.Reader
	contentType := ""
	switch {
	case hasJSON:
		b, err := json.Marshal(jsonVal)
		if err != nil {
			return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInvalid, "webhook.post: marshal options.json: "+err.Error(), nil)
		}
		reader = bytes.NewReader(b)
		contentType = "application/json"
	case bodyStr != "":
		reader = strings.NewReader(bodyStr)
	}
	ctx := context.Background()
	httpReq, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInvalid, "webhook.post: "+err.Error(), nil)
	}
	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}
	if hdrs, ok := req.Options["headers"].(map[string]any); ok {
		for k, v := range hdrs {
			httpReq.Header.Set(k, fmt.Sprintf("%v", v))
		}
	}
	timeout := 30 * time.Second
	if d, err := toDuration(req.Options["timeout"]); err != nil {
		return plugin.InvokeResult{}, plugin.Fail(plugin.CodeInvalid, "webhook.post: options.timeout: "+err.Error(), nil)
	} else if d > 0 {
		timeout = d
	}

	allowPrivate, _ := req.Connection["allow_private"].(bool)
	allowHosts := toStrings(req.Connection["allow_hosts"])
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:         w.safeDial(allowPrivate, allowHosts),
			TLSHandshakeTimeout: 10 * time.Second,
			DisableKeepAlives:   true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			// Never follow a redirect off the vetted host.
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return plugin.InvokeResult{}, plugin.Fail(plugin.CodeUpstream, "webhook.post: "+err.Error(), nil)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return plugin.InvokeResult{}, plugin.Fail(plugin.CodeUpstream, "webhook.post: read response: "+err.Error(), nil)
	}
	return plugin.InvokeResult{Outputs: map[string]any{"status": resp.StatusCode, "body": string(respBody)}}, nil
}

// safeDial mirrors the callable callback poster's guarded dialer: it resolves
// the target host daemon-side and connects only to a resolved IP that passes
// netguard, so a caller-supplied url whose host resolves to loopback, cloud
// metadata (169.254.169.254), RFC1918/ULA, link-local, or CGNAT is refused by
// default. Because it dials the exact IP it just vetted (not a re-resolved
// name), DNS rebinding can't slip an internal address past the check.
func (w *Webhook) safeDial(allowPrivate bool, allowHosts []string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		var ips []net.IPAddr
		if w.resolve != nil {
			ips, err = w.resolve(ctx, host)
		} else {
			ips, err = net.DefaultResolver.LookupIPAddr(ctx, host)
		}
		if err != nil {
			return nil, err
		}
		hostAllowed := allowPrivate
		ipAllowed := map[string]bool{}
		for _, h := range allowHosts {
			h = strings.TrimSpace(h)
			if h == "" {
				continue
			}
			if ip := net.ParseIP(h); ip != nil {
				ipAllowed[ip.String()] = true
			} else if strings.EqualFold(h, host) {
				hostAllowed = true
			}
		}
		var blocked bool
		var d net.Dialer
		var lastErr error
		for _, ipa := range ips {
			if netguard.Blocked(ipa.IP) && !hostAllowed && !ipAllowed[ipa.IP.String()] {
				blocked = true
				continue
			}
			conn, derr := d.DialContext(ctx, network, net.JoinHostPort(ipa.IP.String(), port))
			if derr == nil {
				return conn, nil
			}
			lastErr = derr
		}
		if blocked {
			return nil, errWebhookBlockedIP
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("webhook.post: no address for %q", host)
	}
}

func toDuration(v any) (time.Duration, error) {
	switch x := v.(type) {
	case nil:
		return 0, nil
	case string:
		if x == "" {
			return 0, nil
		}
		return time.ParseDuration(x)
	case int:
		return time.Duration(x) * time.Second, nil
	case int64:
		return time.Duration(x) * time.Second, nil
	case float64:
		return time.Duration(x) * time.Second, nil
	}
	return 0, fmt.Errorf("want a duration, got %T", v)
}

func toStrings(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			} else {
				out = append(out, fmt.Sprintf("%v", e))
			}
		}
		return out
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	}
	return nil
}
