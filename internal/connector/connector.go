// Package connector implements the connectors model: a connector is an
// external service with two faces — a source (`on: <conn>.<event>`) that
// turns inbound events into core.Triggers, and an action face
// (`uses: <conn>.<verb>`) invoked from workflow steps and hooks.
//
// Each connector type is self-describing via three declarations (TypeDecl):
// its events (each with a filter schema and a context schema), its verbs
// (each with an option schema and, for request-response verbs, an output
// schema), and its connection fields. Adding a connector type means declaring
// those three things; the trigger grammar, validation, and introspection all
// derive from the declarations without touching the engine.
//
// Under the hood the source face reuses the existing internal/integrations
// implementations: a connector "lowers" its triggers into the integration's
// own config structures, so filtering, dedup, and transport behavior are the
// same code paths legacy configs run — which is also what makes the legacy →
// connectors migration behavior-preserving.
package connector

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// FieldType is a schema field's type.
type FieldType string

const (
	TString   FieldType = "string"
	TInt      FieldType = "integer"
	TBool     FieldType = "boolean"
	TFloat    FieldType = "number"
	TList     FieldType = "list"
	TMap      FieldType = "map"
	TAny      FieldType = "any"
	TDuration FieldType = "duration"
)

// Field describes one schema field.
type Field struct {
	Type     FieldType
	Required bool
	Enum     []string
	Desc     string
}

// Schema is a set of named fields (option/filter/context/output schemas).
type Schema map[string]Field

// EventDecl declares one source event: the kind valid after `on: <conn>.`,
// the filter keys legal for it, the context facts it publishes, and any
// source-side per-trigger options.
type EventDecl struct {
	Name    string
	Desc    string
	Filters Schema
	Context Schema
	Options Schema
	// Dynamic marks event names that come from connection config (cron
	// schedules, webhook sources, rss feeds) rather than a fixed set.
	Dynamic bool
}

// VerbDecl declares one action verb: the name valid after `uses: <conn>.`,
// its option schema, and its outputs (request-response verbs).
type VerbDecl struct {
	Name    string
	Desc    string
	Options Schema
	Outputs Schema
	// Ask marks a request-response verb that presents to a human and blocks
	// for their answer.
	Ask bool
	// Open marks a verb whose option keys are user-defined (rest/graphql
	// declared verbs): unknown-key/type validation is skipped; template
	// references inside the options are still scope-checked.
	Open bool
}

// TypeDecl is a connector type's full self-description.
type TypeDecl struct {
	Type       string
	Desc       string
	Events     []EventDecl
	Verbs      []VerbDecl
	Connection Schema // documented connection fields, for `conductor schema`

	// Filter, when non-nil, evaluates a trigger's `filters:` against an
	// emitted event's context in the flow runner — the uniform path for
	// synthetic sources (slack, rss). nil means the lowered integration
	// evaluates filters itself (github's live-API gates, sentry/pagerduty's
	// rule matching), so the flow runner skips re-evaluation.
	Filter func(event string, filters, trigCtx map[string]any) (bool, error)
}

// Event looks up an event declaration by name; for Dynamic events the
// declared entry with Dynamic=true is the template all names share.
func (d *TypeDecl) Event(name string) (EventDecl, bool) {
	var dyn *EventDecl
	for i := range d.Events {
		if d.Events[i].Name == name {
			return d.Events[i], true
		}
		if d.Events[i].Dynamic {
			dyn = &d.Events[i]
		}
	}
	if dyn != nil {
		return *dyn, true
	}
	return EventDecl{}, false
}

// Verb looks up a verb declaration by name.
func (d *TypeDecl) Verb(name string) (VerbDecl, bool) {
	for _, v := range d.Verbs {
		if v.Name == name {
			return v, true
		}
	}
	return VerbDecl{}, false
}

// EventNames lists declared event names (sorted, for errors/introspection).
func (d *TypeDecl) EventNames() []string {
	out := make([]string, 0, len(d.Events))
	for _, e := range d.Events {
		out = append(out, e.Name)
	}
	sort.Strings(out)
	return out
}

// VerbNames lists declared verb names (sorted).
func (d *TypeDecl) VerbNames() []string {
	out := make([]string, 0, len(d.Verbs))
	for _, v := range d.Verbs {
		out = append(out, v.Name)
	}
	sort.Strings(out)
	return out
}

// CompiledTrigger pairs a trigger spec with its index in the config's
// `triggers:` list — the stable reference the engine routes back through.
type CompiledTrigger struct {
	Index int
	Spec  config.TriggerSpec
}

// Ref returns the marker stored on lowered config.Action values (see
// config.Action.FlowRef) tying an emitted core.Trigger back to its spec.
func (t CompiledTrigger) Ref() string {
	return fmt.Sprintf("%d:%s", t.Index, t.Spec.On)
}

// Impl is a connector type's per-instance implementation.
type Impl interface {
	// Validate checks connection config at load time.
	Validate() error
	// Invoke runs a verb with merged, rendered options and returns its
	// outputs. Fire-and-forget verbs return a small ack map.
	Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error)
	// Source lowers this connector's triggers into the legacy integration
	// that provides the event transport, or (nil, nil) when the connector has
	// no source face or no triggers reference it.
	Source(triggers []CompiledTrigger) (core.Integration, error)
	// DeclaredEvents returns the instance's dynamic event names (cron
	// schedule names, webhook source names, rss feed names). Static types
	// return nil.
	DeclaredEvents() []string
}

// Instance is one configured connector.
type Instance struct {
	Name    string
	Decl    *TypeDecl
	Enabled bool
	// DefaultOptions are the connector's default verb options; call options
	// merge over them (the call wins).
	DefaultOptions map[string]any
	Policy         *config.Policy
	Impl           Impl

	// Disabled connectors (secret failed to resolve, invalid creds) carry the
	// reason so validate/introspection can report it. A disabled connector
	// opens no sources and rejects verb invocations.
	DisabledReason string

	limiter *rateLimiter
}

// Invoke merges options over the connector defaults and runs the verb,
// honoring the connector's rate limit.
func (in *Instance) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	return in.InvokeFinal(ctx, verb, MergeOptions(in.DefaultOptions, opts))
}

// InvokeFinal runs a verb with FINAL options — already merged over the
// connector defaults (and, in the flow runner, template-rendered after the
// merge so defaults may carry templates too).
func (in *Instance) InvokeFinal(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	if !in.Enabled {
		return nil, fmt.Errorf("connector %q is disabled", in.Name)
	}
	if in.DisabledReason != "" {
		return nil, fmt.Errorf("connector %q is disabled: %s", in.Name, in.DisabledReason)
	}
	if _, ok := in.Decl.Verb(verb); !ok {
		return nil, fmt.Errorf("connector %q (%s) has no verb %q (verbs: %s)",
			in.Name, in.Decl.Type, verb, strings.Join(in.Decl.VerbNames(), ", "))
	}
	if in.limiter != nil {
		if err := in.limiter.wait(ctx); err != nil {
			return nil, err
		}
	}
	return in.Impl.Invoke(ctx, verb, opts)
}

// MergeOptions overlays call options over connector defaults (call wins);
// nested maps merge recursively, everything else replaces.
func MergeOptions(defaults, call map[string]any) map[string]any {
	out := make(map[string]any, len(defaults)+len(call))
	for k, v := range defaults {
		out[k] = v
	}
	for k, v := range call {
		if dm, ok1 := out[k].(map[string]any); ok1 {
			if cm, ok2 := v.(map[string]any); ok2 {
				out[k] = MergeOptions(dm, cm)
				continue
			}
		}
		out[k] = v
	}
	return out
}

// InstanceDecler lets an Impl replace its type's static declaration with a
// per-instance one — rest/graphql materialize their user-declared verbs and
// events into a real TypeDecl so validation, introspection, and InvokeFinal
// see the instance's actual contract.
type InstanceDecler interface {
	InstanceDecl(base *TypeDecl) *TypeDecl
}

// Builder constructs a connector type's Impl from its instance name, the raw
// connection config, and shared runtime dependencies.
type Builder func(name string, ref config.ConnectorRef, deps Deps) (Impl, error)

// Deps carries the shared runtime services connector implementations use.
type Deps struct {
	Secrets *secrets.Resolver
	Log     func(string, ...any)
	// UserToken returns the acts-as-you GitHub token (`gh auth token` or the
	// configured write token). nil in contexts with no github wiring.
	UserToken func() (string, error)
	// Config is the loaded config (identity defaults, hosts for the command
	// connector, …).
	Config *config.Config
}

var (
	regMu    sync.RWMutex
	typeReg  = map[string]*TypeDecl{}
	buildReg = map[string]Builder{}
)

// RegisterType makes a connector type available. Called from init() in each
// type's file; panics on duplicates (programmer error).
func RegisterType(decl *TypeDecl, b Builder) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := typeReg[decl.Type]; dup {
		panic("connector: type registered twice: " + decl.Type)
	}
	typeReg[decl.Type] = decl
	buildReg[decl.Type] = b
}

// Types lists registered connector types (sorted).
func Types() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(typeReg))
	for t := range typeReg {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// TypeDeclFor returns a registered type's declaration.
func TypeDeclFor(typ string) (*TypeDecl, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	d, ok := typeReg[typ]
	return d, ok
}

// Registry holds the built connector instances for one config.
type Registry struct {
	byName map[string]*Instance
	order  []string
}

// Build constructs every configured connector. A connector whose secrets or
// connection config fail to resolve is DISABLED (with the reason recorded)
// rather than failing the boot — a bad connector must not crash-loop the box.
// Structural errors (unknown type) still fail: they are config bugs, not
// runtime conditions.
func Build(cfg *config.Config, deps Deps) (*Registry, error) {
	r := &Registry{byName: map[string]*Instance{}}
	names := make([]string, 0, len(cfg.ConnectorsMap))
	for n := range cfg.ConnectorsMap {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		ref := cfg.ConnectorsMap[name]
		decl, ok := TypeDeclFor(ref.Type)
		if !ok {
			return nil, fmt.Errorf("connector %q: unknown type %q (known: %s)", name, ref.Type, strings.Join(Types(), ", "))
		}
		in := &Instance{
			Name:           name,
			Decl:           decl,
			Enabled:        ref.IsEnabled(),
			DefaultOptions: ref.Options,
			Policy:         ref.Policy,
		}
		if p := effectiveRateLimit(cfg.Policy, ref.Policy); p > 0 {
			in.limiter = newRateLimiter(p)
		}
		impl, err := buildReg[ref.Type](name, ref, deps)
		if err != nil {
			// Runtime construction failure (an unresolvable secret, unreadable
			// key file): disable the connector and keep booting.
			in.DisabledReason = err.Error()
			if deps.Log != nil {
				deps.Log("connector %q disabled: %v", name, err)
			}
		} else {
			in.Impl = impl
			if id, ok := impl.(InstanceDecler); ok {
				in.Decl = id.InstanceDecl(decl)
			}
			if verr := impl.Validate(); verr != nil {
				in.DisabledReason = verr.Error()
				if deps.Log != nil {
					deps.Log("connector %q disabled: %v", name, verr)
				}
			}
		}
		r.byName[name] = in
		r.order = append(r.order, name)
	}
	// Wire the stores: section into the kv registry — a bad store is a load
	// error, never a disabled connector.
	if err := buildStores(cfg, deps); err != nil {
		return nil, err
	}
	// The kv connector (the verbs over those stores) is always available —
	// no connection block, no credentials (config.Validate reserves the name).
	if _, exists := r.byName["kv"]; !exists {
		impl, err := buildReg["kv"]("kv", config.ConnectorRef{}, deps)
		if err != nil {
			return nil, fmt.Errorf("built-in kv store: %w", err)
		}
		in := &Instance{Name: "kv", Decl: kvDecl, Enabled: true, Impl: impl}
		r.byName["kv"] = in
		r.order = append(r.order, "kv")
	}
	return r, nil
}

// effectiveRateLimit resolves rate_limits.per_minute (connector over global).
func effectiveRateLimit(global, conn *config.Policy) int {
	p := config.MergePolicy(global, conn)
	if p.RateLimits == nil {
		return 0
	}
	return p.RateLimits.PerMinute
}

// Get returns a connector instance by name.
func (r *Registry) Get(name string) (*Instance, bool) {
	in, ok := r.byName[name]
	return in, ok
}

// Names lists configured connector names in stable order.
func (r *Registry) Names() []string { return append([]string(nil), r.order...) }

// ValidateCallOptions checks one verb call's options: the CALL's own keys
// must be declared (unknown key = typo = error) and type-check, while
// required keys may be satisfied by the connector's default options — a
// connector-wide default (say, a default channel or `as:`) that a particular
// verb doesn't declare is ignored for that verb rather than failing it.
func ValidateCallOptions(where string, s Schema, call, defaults map[string]any) error {
	for k, v := range call {
		f, ok := s[k]
		if !ok {
			known := make([]string, 0, len(s))
			for name := range s {
				known = append(known, name)
			}
			sort.Strings(known)
			return fmt.Errorf("%s: unknown key %q (valid: %s)", where, k, strings.Join(known, ", "))
		}
		if err := checkType(v, f); err != nil {
			return fmt.Errorf("%s: key %q: %v", where, k, err)
		}
	}
	for k, f := range s {
		if !f.Required {
			continue
		}
		if _, inCall := call[k]; inCall {
			continue
		}
		if _, inDefaults := defaults[k]; inDefaults {
			continue
		}
		return fmt.Errorf("%s: missing required key %q", where, k)
	}
	return nil
}

// ValidateSchema checks a value map against a schema: unknown keys and
// missing required keys are errors; typed keys are coerced-checked. where
// names the config location for the error message.
func ValidateSchema(where string, s Schema, values map[string]any) error {
	for k := range values {
		if _, ok := s[k]; !ok {
			known := make([]string, 0, len(s))
			for f := range s {
				known = append(known, f)
			}
			sort.Strings(known)
			return fmt.Errorf("%s: unknown key %q (valid: %s)", where, k, strings.Join(known, ", "))
		}
	}
	for k, f := range s {
		v, present := values[k]
		if !present {
			if f.Required {
				return fmt.Errorf("%s: missing required key %q", where, k)
			}
			continue
		}
		if err := checkType(v, f); err != nil {
			return fmt.Errorf("%s: key %q: %v", where, k, err)
		}
	}
	return nil
}

// checkType verifies one value against a field. Strings containing template
// actions ({{…}}) pass for any type — their real type exists only at render
// time.
func checkType(v any, f Field) error {
	if s, ok := v.(string); ok && strings.Contains(s, "{{") {
		return nil
	}
	switch f.Type {
	case TAny, "":
		return nil
	case TString:
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("want string, got %T", v)
		}
		if len(f.Enum) > 0 {
			for _, e := range f.Enum {
				if s == e {
					return nil
				}
			}
			return fmt.Errorf("must be one of %s, got %q", strings.Join(f.Enum, "|"), s)
		}
	case TInt:
		switch v.(type) {
		case int, int64, uint64, float64:
		default:
			return fmt.Errorf("want integer, got %T", v)
		}
	case TFloat:
		switch v.(type) {
		case int, int64, float64:
		default:
			return fmt.Errorf("want number, got %T", v)
		}
	case TBool:
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("want boolean, got %T", v)
		}
	case TList:
		switch v.(type) {
		case []any, []string:
		default:
			return fmt.Errorf("want list, got %T", v)
		}
	case TMap:
		if _, ok := v.(map[string]any); !ok {
			return fmt.Errorf("want map, got %T", v)
		}
	case TDuration:
		switch x := v.(type) {
		case string:
			if _, err := time.ParseDuration(x); err != nil {
				return fmt.Errorf("want duration (e.g. 30s): %v", err)
			}
		case int, int64, float64:
		default:
			return fmt.Errorf("want duration, got %T", v)
		}
	}
	return nil
}

// ContextKeys flattens a context schema to its dotted key set, the reference
// universe {{…}} validation checks trigger-context reads against.
func (s Schema) ContextKeys() map[string]bool {
	out := make(map[string]bool, len(s))
	for k := range s {
		out[k] = true
	}
	return out
}
