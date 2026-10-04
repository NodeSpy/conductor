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
	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/secrets"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
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
//
// This is RegisterExternalTypeGroup for a plugin with only ONE group — the
// single-version case, and every existing caller/test that predates
// side-by-side versions. Both groupKey and installKey collapse to decl.Type
// itself, so calling this twice for the same type is, as it always was, an
// unconditional collision (there is no "sibling group" to tell it apart
// from a second, genuinely different plugin claiming the same name).
func RegisterExternalType(decl *TypeDecl, b Builder) error {
	return RegisterExternalTypeGroup(decl, b, decl.Type, decl.Type)
}

// RegisterExternalTypeGroup registers one resolved-version GROUP of a
// connector type backed by an out-of-process plugin (docs/wiki/Plugins.md
// "Side-by-side versions"). groupKey is internal/plugin's per-process-group
// key (Spec.GroupKey — "connectors/github@v1.2.3"); installKey is the
// plugin's plain install-state key (Spec.Key() — "connectors/github"),
// shared by every group of the SAME plugin regardless of version.
//
// A SECOND group registering the same type name is allowed exactly when it
// shares installKey with the first — another resolved version of the SAME
// plugin, which is the whole point of a side-by-side config. It is refused
// when the type is bundled, or when a DIFFERENT installKey (a genuinely
// different plugin) claims the same type name — two plugins providing the
// same type would silently redirect credentials to whichever registered
// last, exactly the collision RegisterExternalType has always refused.
//
// The first group registered for a type becomes its REPRESENTATIVE in
// typeReg/buildReg (what TypeDeclFor/Types/the bare single-arg Builder path
// return) — cosmetic only: every configured instance still resolves its OWN
// group via BindInstanceGroup + declFor, never the representative, once one
// has been bound for it.
func RegisterExternalTypeGroup(decl *TypeDecl, b Builder, groupKey, installKey string) error {
	regMu.Lock()
	defer regMu.Unlock()
	_, typeDup := typeReg[decl.Type]
	_, groupDup := groupDecl[groupKey]
	if typeDup {
		if !externalTypes[decl.Type] {
			return fmt.Errorf("connector type %q is bundled and cannot be replaced by a plugin", decl.Type)
		}
		if groupOwner[decl.Type] != installKey || groupDup {
			// Two plugins providing the same type would silently redirect
			// credentials to whichever registered last — refuse the collision.
			return fmt.Errorf("connector type %q is already provided by another plugin — two plugins cannot provide the same type", decl.Type)
		}
		// A sibling group of the SAME plugin (another resolved version):
		// allowed. The representative typeReg/buildReg entry stands.
	} else {
		typeReg[decl.Type] = decl
		buildReg[decl.Type] = b
		groupOwner[decl.Type] = installKey
	}
	externalTypes[decl.Type] = true
	groupDecl[groupKey] = decl
	groupBuild[groupKey] = b
	if groupsOf[decl.Type] == nil {
		groupsOf[decl.Type] = map[string]bool{}
	}
	groupsOf[decl.Type][groupKey] = true
	return nil
}

// UnregisterExternalType removes an external type — EVERY group registered
// under it — loadConnectorPlugins' rollback when a LATER plugin in the same
// boot attempt fails partway through registration (undoing the ones that
// already succeeded, within this one process's one attempt — a config
// change doesn't reach here at all: SIGHUP/`conductor reload` re-execs into
// a brand new process instead of unwinding this one's registrations), and
// test cleanup. It never removes a bundled type.
func UnregisterExternalType(typ string) {
	regMu.Lock()
	defer regMu.Unlock()
	if !externalTypes[typ] {
		return
	}
	for gk := range groupsOf[typ] {
		delete(groupDecl, gk)
		delete(groupBuild, gk)
		for inst, g := range instanceGroup {
			if g == gk {
				delete(instanceGroup, inst)
			}
		}
	}
	delete(groupsOf, typ)
	delete(typeReg, typ)
	delete(buildReg, typ)
	delete(externalTypes, typ)
	delete(groupOwner, typ)
}

// IsExternalType reports whether a registered type is plugin-backed.
func IsExternalType(typ string) bool {
	regMu.RLock()
	defer regMu.RUnlock()
	return externalTypes[typ]
}

// RegisterExternalConnector bridges a started plugin TYPE into the connector
// registry: it maps the plugin's type-level Decl to a TypeDecl and registers a
// Builder that, per configured instance, resolves that instance's credentials
// and hands them to the instance's OWN plugin client per-call (least
// privilege, own-type-only — multi-instance isolation, docs/wiki/Plugins.md).
//
// clientFor is asked for the instance's *plugin.Client the first time this
// builder runs for it (plugin.Manager.InstanceClientFactory): by default that
// returns the one process every non-isolated instance of the plugin shares;
// an instance whose config set isolate: true instead gets a dedicated
// subprocess of its own. Either way the builder neither knows nor cares which
// — it just drives whatever client comes back, instance-scoped as always.
func RegisterExternalConnector(clientFor plugin.ClientFactory, spec plugin.Spec, decl *plugin.Decl) (*TypeDecl, error) {
	td := mapDecl(decl)
	builder := func(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
		cl, err := clientFor(name)
		if err != nil {
			return nil, fmt.Errorf("connector %q: %w", name, err)
		}
		// allow_secrets is THIS connector entry's own — never a sibling
		// instance's. Built per-instance, from ref (the config this builder
		// was handed for exactly this instance), not from spec.AllowSecrets
		// (the UNION across non-isolated instances multi-instance isolation
		// keeps — see config.PluginRef.Instances and plugin.SpecFromRef).
		// Using the union here would let one instance's allow_secrets list
		// wrongly restrict (or widen, if its own allow_secrets is empty and
		// it inherited the union's entries) a sibling instance's own secret
		// refs.
		allow := map[string]bool{}
		for _, s := range ref.AllowSecrets {
			allow[s] = true
		}
		conn, refs, err := resolveConnection(ref, deps.Secrets, allow)
		if err != nil {
			return nil, err
		}
		trackDeclaredSecrets(conn, td.Connection, deps.Secrets)
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
		deps.Auth.register(name, au)
		if err := enrichConnection(conn, ref, deps, allow); err != nil {
			return nil, err
		}
		// Q6: a per-instance declaration, when the plugin has one, REFINES
		// the shared type-level decl for this instance — not just for the
		// registry (Build reads it off externalImpl), but for everything
		// externalImpl itself does with a Decl (PollVerb, Verb lookups,
		// output validation), so the two can never silently disagree.
		// resolveInstanceDecl enforces that it is a refinement (events may
		// vary; nothing install-time review looked at may) before it is ever
		// used — see validateInstanceRefinement.
		effDecl := td
		id, rawID, err := resolveInstanceDecl(cl, name, decl, conn)
		if err != nil {
			return nil, err
		}
		if id != nil {
			effDecl = id
		}
		return &externalImpl{
			client:       cl,
			source:       cl, // *plugin.Client also satisfies pluginSourcer
			instance:     name,
			decl:         effDecl,
			instanceDecl: rawID,
			conn:         conn,
			auth:         au,
			secretRefs:   refs,
			pluginRef:    spec.Ref(),
			pluginType:   spec.Provides,
			audit:        deps.Audit,
			log:          log,
			lookup:       deps.Lookup,
		}, nil
	}
	groupKey := spec.GroupKey
	if groupKey == "" {
		// Built outside a Manager (a direct test construction, or a
		// single-group plugin where GroupKey was never set) — the plain
		// install key is its own, only, group.
		groupKey = spec.Key()
	}
	if err := RegisterExternalTypeGroup(td, builder, groupKey, spec.Key()); err != nil {
		return nil, err
	}
	return td, nil
}

// mapDecl converts the plugin wire Decl into a connector.TypeDecl.
func mapDecl(d *plugin.Decl) *TypeDecl {
	td := &TypeDecl{Type: d.Type, Desc: d.Desc, Connection: mapSchema(d.Connection), Semantics: d.Semantics}
	for _, v := range d.Verbs {
		td.Verbs = append(td.Verbs, VerbDecl{
			Name: v.Name, Desc: v.Desc, Usage: v.Usage, Ask: v.Ask, Open: v.Open,
			Options: mapSchema(v.Options), Outputs: mapSchema(v.Outputs), Semantics: v.Semantics,
		})
	}
	for _, e := range d.Events {
		td.Events = append(td.Events, EventDecl{
			Name: e.Name, Desc: e.Desc, Dynamic: e.Dynamic,
			Filters: mapSchema(e.Filters), Context: mapSchema(e.Context), Options: mapSchema(e.Options),
			// The unified filter surface crosses the wire like the rest of the
			// schema: a plugin that declares it owns its whole filter surface
			// exactly as a bundled connector does (EventDecl.FilterKeys).
			Facts: mapSchema(e.Facts), MatchKeys: mapSchema(e.MatchKeys),
			Semantics: e.Semantics,
		})
	}
	if pv := td.PollVerb(); pv != "" {
		if _, ok := td.Verb(pv); !ok {
			// The engine provides it: poll this instance now.
			td.Verbs = append(td.Verbs, VerbDecl{Name: pv, Desc: "poll this source now (its catch-up pass)",
				Outputs: Schema{"nudged": {Type: TInt}}})
		}
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
		out[k] = Field{Type: FieldType(f.Type), Required: f.Required, Enum: f.Enum, Desc: f.Desc, Scope: f.Scope, Secret: f.Secret}
	}
	return out
}

// reservedConnKeys are the ConnectorRef header fields — host-owned, never
// part of the plugin's connection config: what to run (use, type), whether
// (enabled), the host's confinement of it (network, isolation,
// allow_secrets), the engine's policy and option defaults, and `auth`, the
// daemon-managed OAuth2 block (see buildManagedAuth: conductor runs the token
// exchange and injects the bearer). A header key that leaked across would be
// a host setting the plugin could read and mistake for its own.
var reservedConnKeys = map[string]bool{
	"type": true, "use": true, "enabled": true, "options": true, "policy": true, "auth": true,
	"network": true, "isolation": true, "allow_secrets": true, "allow_env": true,
}

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
	for k, v := range raw {
		if reservedConnKeys[k] {
			continue
		}
		rv, err := resolveSecrets(k, v, sec, allow, &refs)
		if err != nil {
			return nil, nil, err
		}
		conn[k] = rv
	}
	sort.Strings(refs)
	return conn, refs, nil
}

// StagingDir is this instance's staging directory (plugin-contract.md Q7),
// "" when its host gives none: where its file-returning verbs write.
func (e *externalImpl) StagingDir() (string, error) {
	if st, ok := e.client.(interface{ StagingDir(string) (string, error) }); ok {
		return st.StagingDir(e.instance)
	}
	return "", nil
}

// trackDeclaredSecrets keeps the values of the connection fields the type
// declares secret out of logs and audit records, however they were written.
func trackDeclaredSecrets(conn map[string]any, declared Schema, sec *secrets.Resolver) {
	if sec == nil {
		return
	}
	for k, f := range declared {
		if s, ok := conn[k].(string); ok && f.Secret && s != "" {
			sec.Track(s)
		}
	}
}

// resolveSecrets resolves every secret reference in a connection value, at
// any depth (a webhook source's sign.secret, a nested auth block): each is
// checked against allow_secrets, resolved, and tracked for redaction.
func resolveSecrets(path string, v any, sec *secrets.Resolver, allow map[string]bool, refs *[]string) (any, error) {
	switch x := v.(type) {
	case string:
		if sec == nil || !secrets.IsRef(x) {
			return x, nil
		}
		if len(allow) > 0 && !allow[x] {
			return nil, fmt.Errorf("connection field %q references secret %q which is not in allow_secrets", path, x)
		}
		resolved, err := sec.Resolve(context.Background(), x)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", path, err)
		}
		sec.Track(resolved)
		*refs = append(*refs, x)
		return resolved, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			r, err := resolveSecrets(path+"."+k, e, sec, allow, refs)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			r, err := resolveSecrets(fmt.Sprintf("%s[%d]", path, i), e, sec, allow, refs)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	}
	return v, nil
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
		if len(a.AuthParams) == 0 {
			a.AuthParams = declAuth.AuthParams
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

// instanceDescriber is the per-instance Q6 describe capability of
// *plugin.Client — a seam so a fake invoker in tests need not implement it.
type instanceDescriber interface {
	DescribeInstance(ctx context.Context, instance string, config map[string]any) (*plugin.Decl, bool, error)
}

// instanceDescribeTimeout bounds the one-time per-instance describe call made
// while building a connector instance (mirrors the type-level describe's
// bound in inprocess.go/manager.go).
const instanceDescribeTimeout = 10 * time.Second

// resolveInstanceDecl asks cl for instance's own declaration (Q6,
// plugin-contract.md §1.4, §3.9 G13), when cl implements it. (nil, nil, nil)
// means the plugin has no per-instance declaration (CodeMethodNotFound) — the
// caller keeps the shared type-level decl.
//
// typeDecl is the SAME plugin's type-level declaration (spawned or
// in-process, both callers already have it to hand). Before the instance
// decl is used for anything, validateInstanceRefinement checks it is a
// REFINEMENT of typeDecl — not a replacement: this is the one place that
// check runs for every contract connector, spawned and in-process alike
// (RegisterExternalConnector and RegisterInProcessConnector both call this).
// A non-refining instance decl REFUSES the instance (the caller surfaces the
// error as the connector's DisabledReason), rather than silently letting a
// per-instance declaration redeclare a type-level host_only mints_credential
// verb as an ordinary one that a flow step could call directly.
//
// The second return is the RAW wire Decl (before mapDecl), when supported —
// RegisterExternalConnector stashes it on externalImpl.instanceDecl so
// cmd/conductor's hot-reload path can re-probe a plugin's NEW build against
// it before an in-place swap (finding: in-place reload otherwise ignores a
// changed per-instance declaration).
func resolveInstanceDecl(cl any, instance string, typeDecl *plugin.Decl, conn map[string]any) (*TypeDecl, *plugin.Decl, error) {
	id, ok := cl.(instanceDescriber)
	if !ok {
		return nil, nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), instanceDescribeTimeout)
	defer cancel()
	d, supported, err := id.DescribeInstance(ctx, instance, conn)
	if err != nil {
		return nil, nil, fmt.Errorf("connector %q: per-instance describe: %w", instance, err)
	}
	if !supported {
		return nil, nil, nil
	}
	if err := validateInstanceRefinement(typeDecl, d); err != nil {
		return nil, nil, fmt.Errorf("connector %q: %w", instance, err)
	}
	return mapDecl(d), d, nil
}

// enrichConnection adds, beyond resolveConnection's result, the extras any
// contract connector (spawned plugin or in-process builtin) may use:
//
//   - a non-oauth2 `auth:` block (none/bearer/basic/header) passed through
//     verbatim, secrets resolved — these are static, stateless credentials
//     with no refresh/rotation to own, so the daemon has no reason to hide
//     them from the connector (oauth2 is different: buildManagedAuth owns
//     the token exchange and injects only the resulting bearer, never the
//     client secret, via AccessTokenKey — see reservedConnKeys).
//   - the config's named `secrets:` block, as a connector's own templates
//     see it ({{.secrets.*}} — the rest/graphql convenience, generalized to
//     every contract connector rather than special-cased to those two).
func enrichConnection(conn map[string]any, ref config.ConnectorRef, deps Deps, allow map[string]bool) error {
	var wrap struct {
		Auth map[string]any `yaml:"auth"`
	}
	if err := ref.Decode(&wrap); err == nil && wrap.Auth != nil {
		if t, _ := wrap.Auth["type"].(string); t != "oauth2" {
			var refs []string
			resolved, err := resolveSecrets("auth", wrap.Auth, deps.Secrets, allow, &refs)
			if err != nil {
				return err
			}
			if m, ok := resolved.(map[string]any); ok {
				conn["auth"] = m
			}
		}
	}
	conn["secrets"] = resolveNamedSecrets(context.Background(), deps.Config, deps.Secrets)
	return nil
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
	// instanceDecl is the RAW wire Decl this instance's plugin answered
	// plugin.describe {instance} with at build time (Q6, plugin-contract.md
	// §1.4) — nil when the plugin never implements/answers it for this
	// instance, in which case the type-level Decl (above, as `decl`, mapped)
	// is this instance's whole surface too. cmd/conductor's hot-reload path
	// (reload.go) re-probes a plugin's NEW build against this before an
	// in-place swap: the type-level reload-surface check alone cannot see a
	// per-instance decl change (a rest/graphql-shaped type decl has no verbs
	// or events at all), so it would otherwise keep serving a changed
	// per-instance declaration under the stale cached one.
	instanceDecl *plugin.Decl
	// lookup resolves another configured connector instance by name — used
	// at source start to open this instance's declared `listeners` exposure
	// (listeners.go), the same registry lookup web's own `expose:` uses.
	lookup    func(string) (*Instance, bool)
	audit     func(map[string]any)
	auditOnce sync.Once
	log       func(string, ...any)
}

// PluginInstanceDecl pairs one live connector instance's identity and
// connection config with the raw per-instance Decl (Q6, plugin-contract.md
// §1.4) its plugin answered with at build time. cmd/conductor's hot-reload
// path (reload.go) uses this to redo that describe call against a plugin's
// NEW build before swapping its process in place, and refuse the swap unless
// every live instance's declared surface is still the same. Decl is nil when
// the plugin never implements/answers plugin.describe {instance} for this
// instance — nothing instance-specific to re-check then, since the
// type-level Decl (already gated by plugin.SameReloadSurface) is this
// instance's whole surface too.
type PluginInstanceDecl struct {
	Instance   string
	Connection map[string]any
	Decl       *plugin.Decl
}

// InstancesUsingPlugin returns the registry's live connector instances backed
// by the external plugin GROUP at groupKey (internal/plugin's Spec.GroupKey —
// "connectors/<name>" when the plugin has only one resolved-version group,
// "connectors/<name>@<resolved>" when more than one is configured side by
// side). It is the set a hot-reload pass must re-check per-instance
// declarations for before an in-place swap of that GROUP's process —
// never a sibling group's instances, which run as an entirely separate
// process and are reloaded (or not) independently.
//
// Matching goes through BindInstanceGroup's bindings (instanceGroup) rather
// than a bare type-name compare: two groups of the same plugin NAME share
// the same pluginType, so a name-only match would wrongly pull in a sibling
// group's instances the moment more than one version is configured. An
// instance never bound (should not happen for a live external connector,
// which loadConnectorPlugins always binds before Build) falls back to the
// plain type-name compare, matching every group of the name — the
// pre-versioning behavior, safe when there is only ever one.
func InstancesUsingPlugin(r *Registry, groupKey string) []PluginInstanceDecl {
	if r == nil {
		return nil
	}
	typeKey, _, _ := strings.Cut(groupKey, "@")
	var out []PluginInstanceDecl
	for _, name := range r.order {
		in := r.byName[name]
		ei, ok := in.Impl.(*externalImpl)
		if !ok {
			continue
		}
		if gk, bound := InstanceGroup(ei.instance); bound {
			if gk != groupKey {
				continue
			}
		} else if "connectors/"+ei.pluginType != typeKey {
			continue
		}
		out = append(out, PluginInstanceDecl{Instance: ei.instance, Connection: ei.conn, Decl: ei.instanceDecl})
	}
	return out
}

// InstancePID returns the OS process id of the subprocess backing a live
// connector instance's plugin client (0, false for a non-plugin Impl, an
// in-process builtin, or a plugin that has not started yet). Purely
// observational — e.g. `conductor connectors ls` and the e2e suite's
// distinct-process check (multi-instance isolation, docs/wiki/Plugins.md):
// two configured instances of one external plugin get two different pids by
// default, since each now runs its own process.
func InstancePID(in *Instance) (int, bool) {
	if in == nil || in.Impl == nil {
		return 0, false
	}
	ei, ok := in.Impl.(*externalImpl)
	if !ok {
		return 0, false
	}
	pr, ok := ei.client.(interface{ PID() int })
	if !ok {
		return 0, false
	}
	pid := pr.PID()
	return pid, pid > 0
}

// pluginValidator is the plugin.validate capability of *plugin.Client — a
// seam so a fake invoker in tests need not implement it.
type pluginValidator interface {
	Validate(ctx context.Context, req sdk.ValidateRequest) ([]sdk.Problem, error)
}

// Validate runs the plugin's own config checks (plugin.validate), when it
// implements one. This is the ONE place every contract connector's checks
// run unconditionally at load — a verb-only connector (most rest/graphql/
// webhook instances declare no events) never gets a pluginSourceIntegration
// built, so pluginSourceIntegration.Validate's call to the same method would
// otherwise never fire for it. A plugin with no checks of its own
// (CodeMethodNotFound) is valid.
func (e *externalImpl) Validate() error {
	pv, ok := e.client.(pluginValidator)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), instanceDescribeTimeout)
	defer cancel()
	problems, err := pv.Validate(ctx, sdk.ValidateRequest{Instance: e.instance, Config: e.conn})
	if err == plugin.ErrNotSupported {
		return nil
	}
	if err != nil {
		return fmt.Errorf("connector %q: validate: %w", e.instance, err)
	}
	if len(problems) == 0 {
		return nil
	}
	lines := make([]string, 0, len(problems))
	for _, p := range problems {
		if p.Path != "" {
			lines = append(lines, p.Path+": "+p.Message)
		} else {
			lines = append(lines, p.Message)
		}
	}
	return fmt.Errorf("connector %q:\n  %s", e.instance, strings.Join(lines, "\n  "))
}

// DeclaredEvents returns nil: an external plugin's per-instance, config-named
// (Dynamic) event names live out in the plugin's own connection config, not
// here, so the daemon can't enumerate them at config-validate time. Trigger
// validation treats an empty declared set for a Dynamic event as "accept any
// name" (the Dynamic template already matched; the plugin validates the name at
// StartSource) — see internal/flow/validate.go.
//
// This is ALWAYS nil for every contract connector, spawned or in-process
// builtin alike (RegisterInProcessConnector wraps cron/rss/webhook/rest/
// graphql in an externalImpl exactly like a spawned plugin — none of them has
// a different Impl type this method could be overridden on). The
// Impl.DeclaredEvents interface method stays for a hypothetical Go-native
// (non-contract) Impl that CAN enumerate its dynamic names and wants trigger
// validation to stay strict about them, but nothing in this codebase is that
// today.
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
	declared := map[string]bool{}
	sem := map[string]*sdk.EventSemantics{}
	var dynamic *EventDecl
	for i, ev := range e.decl.Events {
		declared[ev.Name] = true
		sem[ev.Name] = ev.Semantics
		if ev.Dynamic {
			dynamic = &e.decl.Events[i]
		}
	}
	base := &pluginSourceIntegration{
		source:    e.source,
		instance:  e.instance,
		typ:       e.pluginType,
		config:    e.conn,
		triggers:  mine,
		log:       e.log,
		declared:  declared,
		sem:       sem,
		dynamic:   dynamic,
		listeners: listenersOf(e.decl),
		lookup:    e.lookup,
	}
	return base, nil
}

// Invoke forwards the verb to the plugin with this instance's credentials and
// schema-validates the untrusted response.
func (e *externalImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	if pv := e.decl.PollVerb(); pv != "" && verb == pv {
		return pollNow(ctx, e.instance)
	}
	if v, ok := e.decl.Verb(verb); ok && v.Ask && v.Semantics != nil && v.Semantics.OpensConversation != nil {
		// An ask over a declared conversation: the plugin posts, the
		// engine waits for the reply (runAsk over the conversation channel).
		return runAsk(ctx, &conversationChannel{e: e, verb: v, opts: opts}, opts)
	}
	return e.invokePlugin(ctx, verb, opts)
}

// invokePlugin forwards the verb to the plugin with this instance's
// credentials and schema-validates the untrusted response.
func (e *externalImpl) invokePlugin(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
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
	staging := ""
	if st, ok := e.client.(interface{ StagingDir(string) (string, error) }); ok {
		dir, err := st.StagingDir(e.instance)
		if err != nil {
			return nil, fmt.Errorf("connector %q: staging dir: %w", e.instance, err)
		}
		staging = dir
	}
	call := func(forceFresh bool) (map[string]any, error) {
		conn := e.conn
		if e.auth != nil && e.auth.oauth2() {
			if forceFresh {
				e.auth.invalidate()
			}
			// Managed OAuth2: mint/refresh the token and inject it into a
			// per-call COPY of the connection (never mutate the shared map).
			// The plugin reads it via plugin.AccessToken and does no token
			// handling itself. Every OTHER scheme (none/bearer/basic/header)
			// is static — enrichConnection already passed its resolved
			// `auth:` block through verbatim, and the plugin applies it
			// itself; there is no lifecycle for the host to own.
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
		return e.client.Invoke(ctx, plugin.InvokeRequest{
			Instance: e.instance, Verb: verb, Options: opts, Connection: conn, Staging: staging,
		})
	}
	out, err := call(false)
	if err != nil && e.auth != nil && e.auth.oauth2() && plugin.UpstreamUnauthorized(err) {
		// The upstream rejected the bearer we handed the plugin (§1.11
		// CodeUpstream, status 401) — the same "retry once on 401" every
		// managed-OAuth2 connector gets, generically: the plugin never
		// touches the token's lifecycle, so it cannot retry this itself.
		out, err = call(true)
	}
	if err != nil {
		// An answered JSON-RPC error (plugin-contract.md §1.11) becomes a
		// *ContractError here, carrying Code/Data the rest of the engine acts
		// on; a transport failure or timeout passes through unchanged.
		return nil, contractErrorFrom(err)
	}
	// Untrusted output (§8.2): validate against the declared verb outputs.
	if vd, ok := e.decl.Verb(verb); ok && !vd.Open && len(vd.Outputs) > 0 {
		if err := ValidateSchema(fmt.Sprintf("plugin %s verb %s output", e.pluginRef, verb), vd.Outputs, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}
