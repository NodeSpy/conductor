package connector

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// externalTypes tracks which registered connector types came from an EXTERNAL
// plugin (vs a bundled init()-registered one), so `plugin list` can tag them
// and so bundled types are never silently replaced.
var externalTypes = map[string]bool{}

// RegisterExternalType registers a connector type backed by an out-of-process
// plugin. Unlike RegisterType (init-time, panics on dup), it returns an error
// so a bad plugin disables cleanly, and it REFUSES to override a bundled type
// (external-overrides-bundled is disallowed — §7 open Q4). Safe to call at
// daemon boot after config load, before connector.Build.
func RegisterExternalType(decl *TypeDecl, b Builder) error {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := typeReg[decl.Type]; dup {
		if externalTypes[decl.Type] {
			// Two plugins providing the same type would silently redirect
			// credentials to whichever registered last — refuse the collision.
			return fmt.Errorf("connector type %q is already provided by another plugin — two plugins cannot provide the same type", decl.Type)
		}
		return fmt.Errorf("connector type %q is bundled and cannot be replaced by a plugin", decl.Type)
	}
	typeReg[decl.Type] = decl
	buildReg[decl.Type] = b
	externalTypes[decl.Type] = true
	return nil
}

// UnregisterExternalType removes an external type (config reload, test cleanup).
// It never touches a bundled type.
func UnregisterExternalType(typ string) {
	regMu.Lock()
	defer regMu.Unlock()
	if externalTypes[typ] {
		delete(typeReg, typ)
		delete(buildReg, typ)
		delete(externalTypes, typ)
	}
}

// IsExternalType reports whether a registered type is plugin-backed.
func IsExternalType(typ string) bool {
	regMu.RLock()
	defer regMu.RUnlock()
	return externalTypes[typ]
}

// RegisterExternalConnector bridges a started plugin into the connector
// registry: it maps the plugin's Decl to a TypeDecl and registers a Builder
// that, per configured instance, resolves that instance's credentials and hands
// them to the plugin subprocess per-call (least privilege, own-type-only).
func RegisterExternalConnector(cl *plugin.Client, spec plugin.Spec, decl *plugin.Decl) (*TypeDecl, error) {
	td := mapDecl(decl)
	allow := map[string]bool{}
	for _, s := range spec.AllowSecrets {
		allow[s] = true
	}
	builder := func(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
		conn, refs, err := resolveConnection(ref, deps.Secrets, allow)
		if err != nil {
			return nil, err
		}
		log := deps.Log
		if log == nil {
			log = func(string, ...any) {}
		}
		// Managed OAuth2: when the plugin declares Auth (endpoints) and/or the
		// instance carries an `auth:` block, conductor owns the token exchange
		// and injects the bearer per-call. nil au = the plugin authenticates
		// itself from its own connection fields (unchanged behavior).
		au, err := buildManagedAuth(name, decl.Auth, ref, deps)
		if err != nil {
			return nil, err
		}
		return &externalImpl{
			client:     cl,
			source:     cl, // *plugin.Client also satisfies pluginSourcer
			instance:   name,
			decl:       td,
			conn:       conn,
			auth:       au,
			secretRefs: refs,
			pluginRef:  spec.Ref(),
			pluginType: spec.Provides,
			audit:      deps.Audit,
			log:        log,
		}, nil
	}
	if err := RegisterExternalType(td, builder); err != nil {
		return nil, err
	}
	return td, nil
}

// mapDecl converts the plugin wire Decl into a connector.TypeDecl.
func mapDecl(d *plugin.Decl) *TypeDecl {
	td := &TypeDecl{Type: d.Type, Desc: d.Desc, Connection: mapSchema(d.Connection)}
	for _, v := range d.Verbs {
		td.Verbs = append(td.Verbs, VerbDecl{
			Name: v.Name, Desc: v.Desc, Usage: v.Usage, Ask: v.Ask,
			Options: mapSchema(v.Options), Outputs: mapSchema(v.Outputs),
		})
	}
	for _, e := range d.Events {
		td.Events = append(td.Events, EventDecl{
			Name: e.Name, Desc: e.Desc, Dynamic: e.Dynamic,
			Filters: mapSchema(e.Filters), Context: mapSchema(e.Context), Options: mapSchema(e.Options),
		})
	}
	return td
}

func mapSchema(s plugin.Schema) Schema {
	if len(s) == 0 {
		return nil
	}
	out := make(Schema, len(s))
	for k, f := range s {
		// Scope crosses the wire verbatim: an external plugin declares a
		// scoped option exactly like a bundled connector, and gets the same
		// enforcement on both surfaces without conductor knowing the
		// dimension's name.
		out[k] = Field{Type: FieldType(f.Type), Required: f.Required, Enum: f.Enum, Desc: f.Desc, Scope: f.Scope}
	}
	return out
}

// reservedConnKeys are the ConnectorRef header fields — not part of the
// plugin's connection config. `auth` is the daemon-managed OAuth2 block (see
// buildManagedAuth): conductor runs the token exchange and injects the bearer,
// so the block never crosses to the plugin as a connection field.
var reservedConnKeys = map[string]bool{"type": true, "enabled": true, "options": true, "policy": true, "auth": true}

// resolveConnection decodes an instance's connection block, resolves every
// secret reference (env:/vault), and returns the connection map to hand the
// plugin plus the names of the secret refs it carried (for audit). When an
// AllowSecrets allowlist is set, a secret ref not on it is refused.
func resolveConnection(ref config.ConnectorRef, sec *secrets.Resolver, allow map[string]bool) (map[string]any, []string, error) {
	var raw map[string]any
	if err := ref.Decode(&raw); err != nil {
		return nil, nil, fmt.Errorf("decode connection: %w", err)
	}
	conn := make(map[string]any, len(raw))
	var refs []string
	ctx := context.Background()
	for k, v := range raw {
		if reservedConnKeys[k] {
			continue
		}
		s, ok := v.(string)
		if !ok {
			conn[k] = v
			continue
		}
		if sec != nil && secrets.IsRef(s) {
			if len(allow) > 0 && !allow[s] {
				return nil, nil, fmt.Errorf("connection field %q references secret %q which is not in allow_secrets", k, s)
			}
			resolved, err := sec.Resolve(ctx, s)
			if err != nil {
				return nil, nil, fmt.Errorf("resolve %q: %w", k, err)
			}
			sec.Track(resolved)
			conn[k] = resolved
			refs = append(refs, s)
			continue
		}
		conn[k] = s
	}
	sort.Strings(refs)
	return conn, refs, nil
}

// buildManagedAuth wires conductor's OAuth2 authenticator for a plugin
// connector. The plugin's declared AuthSpec (declAuth) supplies the provider
// endpoints + default scopes; the instance's `auth:` config block supplies the
// grant, client_id/client_secret, token_vault, and any refresh_token seed. When
// neither asks for managed auth, it returns (nil, nil) and the plugin keeps
// authenticating from its own connection fields.
func buildManagedAuth(name string, declAuth *plugin.AuthSpec, ref config.ConnectorRef, deps Deps) (*authenticator, error) {
	var wrap struct {
		Auth authConfig `yaml:"auth"`
	}
	if err := ref.Decode(&wrap); err != nil {
		return nil, fmt.Errorf("connector %q: decode auth block: %w", name, err)
	}
	a := wrap.Auth
	if declAuth != nil {
		// The plugin bakes in the endpoints; the operator supplies only creds.
		if a.Type == "" {
			a.Type = "oauth2"
		}
		if a.TokenURL == "" {
			a.TokenURL = declAuth.TokenURL
		}
		if a.AuthURL == "" {
			a.AuthURL = declAuth.AuthURL
		}
		if a.DeviceAuthURL == "" {
			a.DeviceAuthURL = declAuth.DeviceAuthURL
		}
		if len(a.Scopes) == 0 {
			a.Scopes = append([]string(nil), declAuth.Scopes...)
		}
	}
	if a.Type == "" || a.Type == "none" {
		return nil, nil // no managed auth
	}
	if err := a.validate(fmt.Sprintf("connector %q", name)); err != nil {
		return nil, err
	}
	return newAuthenticator(context.Background(), name, a, deps.Secrets, deps.Log)
}

// pluginInvoker is the subset of *plugin.Client externalImpl drives (the seam
// lets tests inject a fake without a live subprocess).
type pluginInvoker interface {
	Invoke(ctx context.Context, req plugin.InvokeRequest) (map[string]any, error)
}

// externalImpl is the RPC-proxy connector.Impl: it forwards Invoke to the
// plugin subprocess and validates the response against the declared Decl.
type externalImpl struct {
	client     pluginInvoker
	source     pluginSourcer
	instance   string
	decl       *TypeDecl
	conn       map[string]any
	auth       *authenticator // managed OAuth2, nil when the plugin self-authenticates
	secretRefs []string
	pluginRef  string
	pluginType string
	audit      func(map[string]any)
	auditOnce  sync.Once
	log        func(string, ...any)
}

// Validate is a no-op: the plugin was verified and described at registration.
func (e *externalImpl) Validate() error { return nil }

// DeclaredEvents returns the plugin's static event names (dynamic source
// streaming is a documented follow-up).
func (e *externalImpl) DeclaredEvents() []string { return nil }

// Source returns a streaming integration when this plugin declares events AND
// some configured trigger references it; otherwise (nil, nil) for a verb-only
// plugin. The daemon matches the plugin's streamed events to these triggers and
// resolves the action (see pluginsource.go), so the plugin stays a dumb source.
func (e *externalImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	if len(e.decl.Events) == 0 || e.source == nil {
		return nil, nil
	}
	var mine []CompiledTrigger
	for _, t := range triggers {
		if t.Spec.On == "" || strings.HasPrefix(t.Spec.On, e.instance+".") {
			mine = append(mine, t)
		}
	}
	if len(mine) == 0 {
		return nil, nil
	}
	return &pluginSourceIntegration{
		source:   e.source,
		instance: e.instance,
		typ:      e.pluginType,
		config:   e.conn,
		triggers: mine,
		log:      e.log,
	}, nil
}

// Invoke forwards the verb to the plugin with this instance's credentials and
// schema-validates the untrusted response.
func (e *externalImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	// Audit the credential hand-off once per instance (name, never value).
	if len(e.secretRefs) > 0 {
		e.auditOnce.Do(func() {
			if e.audit != nil {
				e.audit(map[string]any{
					"event": "plugin_credential", "action": "issue",
					"connector": e.instance, "type": e.pluginType,
					"plugin": e.pluginRef, "secret_refs": e.secretRefs,
				})
			}
		})
	}
	conn := e.conn
	if e.auth != nil {
		// Managed OAuth2: mint/refresh the token and inject it into a per-call
		// COPY of the connection (never mutate the shared map). The plugin reads
		// it via plugin.AccessToken and does no token handling itself.
		tok, err := e.auth.accessToken(ctx)
		if err != nil {
			return nil, fmt.Errorf("connector %q: oauth2: %w", e.instance, err)
		}
		conn = make(map[string]any, len(e.conn)+1)
		for k, v := range e.conn {
			conn[k] = v
		}
		conn[plugin.AccessTokenKey] = tok
	}
	out, err := e.client.Invoke(ctx, plugin.InvokeRequest{
		Instance: e.instance, Verb: verb, Options: opts, Connection: conn,
	})
	if err != nil {
		return nil, err
	}
	// Untrusted output (§8.2): validate against the declared verb outputs.
	if vd, ok := e.decl.Verb(verb); ok && !vd.Open && len(vd.Outputs) > 0 {
		if err := ValidateSchema(fmt.Sprintf("plugin %s verb %s output", e.pluginRef, verb), vd.Outputs, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}
