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
		return &externalImpl{
			client:     cl,
			source:     cl, // *plugin.Client also satisfies pluginSourcer
			instance:   name,
			decl:       td,
			conn:       conn,
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
		out[k] = Field{Type: FieldType(f.Type), Required: f.Required, Enum: f.Enum, Desc: f.Desc}
	}
	return out
}

// reservedConnKeys are the ConnectorRef header fields — not part of the
// plugin's connection config.
var reservedConnKeys = map[string]bool{"type": true, "enabled": true, "options": true, "policy": true}

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
	out, err := e.client.Invoke(ctx, plugin.InvokeRequest{
		Instance: e.instance, Verb: verb, Options: opts, Connection: e.conn,
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
