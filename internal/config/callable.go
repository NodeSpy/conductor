package config

import (
	"fmt"
	"strings"
)

// CallableConfig is the `callable:` block (#36 §13): conductor's authenticated
// inbound invoke surface. An external orchestrator — n8n, cron, a queue, plain
// `curl`, or any MCP client — fires a named callable workflow, passes inputs,
// and gets a structured result back. The division of labour is deliberate: the
// caller owns generic automation + scheduling; conductor owns agents-on-code.
//
// The surface is vendor-neutral (any HTTP/cron/queue/MCP client drives it); n8n
// is only the first documented adapter. Off unless a `callable:` block is
// configured. It is a control surface that dispatches agents, so it is gated
// like one: authenticated, deny-by-default per-token workflow scope, and an
// explicit `callable: true` opt-in on each reachable trigger.
type CallableConfig struct {
	// Listen is the bind address for the invoke HTTP endpoints. They mount on
	// the shared inbound listener, so this may reuse a `listen:` a webhook /
	// sentry / rss connector already binds. Empty disables the service.
	Listen string `yaml:"listen"`
	// WaitTimeout bounds a synchronous `?wait=true` call (default 30s, hard-
	// capped so a long agent run can't pin a server goroutine indefinitely).
	// Async (poll / callback) invokes are unaffected.
	WaitTimeout Duration `yaml:"wait_timeout"`
	// Tokens are the caller credentials. Deny-by-default: with no tokens the
	// service refuses every request; each token grants an explicit,
	// enumerated workflow allow-list (no wildcard).
	Tokens []CallableToken `yaml:"tokens"`
	// CallbackAllowHTTP relaxes the https-only default for a `callback_url`
	// delivery target (#36 §13 review, item 1). A completion callback is a
	// daemon-side POST to a caller-supplied URL; by default only `https://` is
	// dialed. Set true only for a trusted internal callback endpoint reachable
	// over plaintext http.
	CallbackAllowHTTP bool `yaml:"callback_allow_http"`
	// CallbackAllowHosts opts `callback_url` targets back in even though they sit
	// in an otherwise-blocked range (loopback / private / link-local / CGNAT).
	// Each entry is a literal IP or a hostname:
	//   - a literal IP opts in that exact resolved address (rebinding-safe: DNS
	//     may point anywhere, only this address is dialed);
	//   - a hostname opts in the host by name — whatever it resolves to at dial
	//     time — for the common case of a callback sink on your own network
	//     (n8n / a queue / a container by service name) whose private IP is
	//     DHCP- or Docker-assigned and can't be pinned.
	// This is operator config, not caller-supplied: a `callback_url` whose host
	// is not listed here is still resolved-and-blocked by default. Empty = every
	// internal range is blocked.
	CallbackAllowHosts []string `yaml:"callback_allow_hosts"`
	// MaxWaitInflight caps concurrent synchronous `?wait=true` calls (#36 §13
	// review, item 5). Each holds a server goroutine until the run finishes or
	// wait_timeout (up to 5m), so an unbounded number lets a caller exhaust
	// goroutines. A wait past the cap degrades to async (202 + run_id) — the
	// caller polls GET /runs. Default 64; 0/unset uses the default.
	MaxWaitInflight int `yaml:"max_wait_inflight"`
	// MaxCallbackInflight caps concurrent in-flight completion callbacks (each a
	// background poll up to 5m). A callback past the cap is not scheduled (audited
	// `delivered: false`) rather than spawning an unbounded goroutine; the run
	// still completes and is readable via GET /runs. Default 64; 0/unset uses it.
	MaxCallbackInflight int `yaml:"max_callback_inflight"`
	// MaxSkew bounds how far a signed (HMAC) request's timestamp may sit from the
	// server clock (#36 §13 review, item 4). A request outside the window is
	// refused, so a captured signature stops verifying once the window passes.
	// Default 5m; 0/unset uses the default.
	MaxSkew Duration `yaml:"max_skew"`
	// MCPLocal opts the `conductor mcp callable` face out of the token model
	// (#36 §13 review, item 2). By default that face is held to the SAME model
	// as the HTTP surface: it must present a `--token`, its tool list is scoped
	// to that token's workflows, and the daemon re-checks the callable opt-in +
	// token scope and audits every invoke at dispatch time. Set true to restore
	// the old unscoped/unaudited behavior — every `callable: true` workflow is
	// exposed with no token and no `callable_invoke` audit, trusting the
	// same-user privilege boundary alone (like `conductor run`).
	MCPLocal bool `yaml:"mcp_local"`
}

// TokenByName returns the callable token with the given name, if any.
func (c CallableConfig) TokenByName(name string) (CallableToken, bool) {
	for _, t := range c.Tokens {
		if t.Name == name {
			return t, true
		}
	}
	return CallableToken{}, false
}

// CallableToken is one caller identity: a bearer secret OR an HMAC signature
// scheme (exactly one), plus the deny-by-default set of workflows it may
// invoke. The Name is what the audit trail records for every invoke.
type CallableToken struct {
	// Name identifies the caller in logs + audit (required, unique).
	Name string `yaml:"name"`
	// Bearer is the shared secret presented as `Authorization: Bearer <secret>`,
	// compared in constant time. Mutually exclusive with HMAC.
	Bearer string `yaml:"bearer"`
	// HMAC verifies a signature over the raw request body (reuses the
	// webhook-signature machinery). Mutually exclusive with Bearer.
	HMAC *CallableHMAC `yaml:"hmac"`
	// Workflows is the explicit allow-list of callable trigger names this token
	// may invoke. Deny-by-default: an empty list grants nothing. No wildcard —
	// a token fires only the workflows it is named for.
	Workflows []string `yaml:"workflows"`
}

// CallableHMAC configures signed-request verification for a token. Unlike a
// webhook signature (body only), a callable caller signs timestamp + method +
// path + body, and presents the timestamp in TimestampHeader — so a captured
// signature is bound to one endpoint at one moment and cannot be replayed (#36
// §13 review, item 4).
type CallableHMAC struct {
	Secret string `yaml:"secret"`
	Header string `yaml:"header"` // e.g. X-Conductor-Signature
	Scheme string `yaml:"scheme"` // hex (default) | base64; a "sha256=" prefix is stripped
	// TimestampHeader carries the unix-seconds timestamp the caller signed and
	// presents. Default X-Conductor-Timestamp.
	TimestampHeader string `yaml:"timestamp_header"`
}

// TimestampHeaderName is the header the signed timestamp is read from, defaulting
// to X-Conductor-Timestamp when unset.
func (h *CallableHMAC) TimestampHeaderName() string {
	if h != nil && h.TimestampHeader != "" {
		return h.TimestampHeader
	}
	return "X-Conductor-Timestamp"
}

// Enabled reports whether the callable service should start.
func (c CallableConfig) Enabled() bool { return c.Listen != "" && len(c.Tokens) > 0 }

// Allows reports whether this token is scoped to invoke the named workflow.
func (t CallableToken) Allows(name string) bool {
	for _, w := range t.Workflows {
		if w == name {
			return true
		}
	}
	return false
}

// IsCallable reports whether this trigger opted into the callable surface.
func (t TriggerSpec) IsCallable() bool { return t.Callable != nil && *t.Callable }

// callableTriggers indexes the manual triggers that opted in with
// `callable: true`, by name.
func (c *Config) callableTriggers() map[string]TriggerSpec {
	m := map[string]TriggerSpec{}
	for _, t := range c.Triggers {
		if t.IsCallable() && t.Name != "" {
			m[t.Name] = t
		}
	}
	return m
}

// CallableTriggerNames returns the set of manual trigger names that opted into
// the callable surface (`callable: true`), for the invoke service's trigger-side
// gate. Exported for the daemon wiring in package main.
func (c *Config) CallableTriggerNames() map[string]bool {
	m := map[string]bool{}
	for name := range c.callableTriggers() {
		m[name] = true
	}
	return m
}

// validateCallable checks the `callable:` block: every token is authenticated
// exactly one way, and every scoped workflow name actually exists and opted in.
// It runs after NormalizeTriggers, so it sees scalar-On triggers.
func (c *Config) validateCallable() error {
	cc := c.Callable
	// A trigger may only opt in with `callable: true` if it is an addressable
	// manual trigger — the invoke surface fires it by name through the manual
	// machinery.
	for i, t := range c.Triggers {
		if !t.IsCallable() {
			continue
		}
		if !t.Manual() {
			return fmt.Errorf("config: triggers[%d]: `callable: true` requires `on: manual` — the invoke surface fires a named manual trigger", i)
		}
		if t.Name == "" {
			return fmt.Errorf("config: triggers[%d]: a `callable: true` trigger requires a name: (it is invoked by name)", i)
		}
	}

	if len(cc.Tokens) == 0 {
		if cc.Listen != "" {
			return fmt.Errorf("config: callable.listen is set but callable.tokens is empty — the service would refuse every request (deny-by-default). Add at least one token or remove callable.listen")
		}
		return nil
	}
	if cc.Listen == "" {
		return fmt.Errorf("config: callable.tokens is set but callable.listen is empty — nothing would serve the invoke endpoints")
	}

	callable := c.callableTriggers()
	seen := map[string]bool{}
	for i, tok := range cc.Tokens {
		if tok.Name == "" {
			return fmt.Errorf("config: callable.tokens[%d]: a token requires a name: (recorded as the caller identity in the audit)", i)
		}
		if seen[tok.Name] {
			return fmt.Errorf("config: callable.tokens[%d]: duplicate token name %q", i, tok.Name)
		}
		seen[tok.Name] = true

		hasBearer := tok.Bearer != ""
		hasHMAC := tok.HMAC != nil && tok.HMAC.Secret != ""
		switch {
		case hasBearer && hasHMAC:
			return fmt.Errorf("config: callable.tokens[%q]: set exactly one of bearer: or hmac: — not both", tok.Name)
		case !hasBearer && !hasHMAC:
			return fmt.Errorf("config: callable.tokens[%q]: a token needs a non-empty bearer: secret or an hmac: block — an empty credential is never accepted", tok.Name)
		}
		if hasHMAC {
			if tok.HMAC.Header == "" {
				return fmt.Errorf("config: callable.tokens[%q]: hmac.header is required (the request header carrying the signature)", tok.Name)
			}
			switch strings.ToLower(tok.HMAC.Scheme) {
			case "", "hex", "base64":
			default:
				return fmt.Errorf("config: callable.tokens[%q]: hmac.scheme must be hex|base64, got %q", tok.Name, tok.HMAC.Scheme)
			}
		}
		for _, w := range tok.Workflows {
			if _, ok := callable[w]; !ok {
				return fmt.Errorf("config: callable.tokens[%q]: workflow %q is not an addressable callable trigger — declare a manual trigger `name: %s` with `callable: true`", tok.Name, w, w)
			}
		}
	}
	return nil
}
