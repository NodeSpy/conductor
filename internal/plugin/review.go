package plugin

import (
	"fmt"

	"github.com/NodeSpy/conductor/internal/config"
)

// AddReview is what adding a `use:` reference means, worked out before
// anything is fetched: where it comes from, whether conductor trusts that
// source as configured, whether a build is already installed (and, when it
// is, the permissions the operator is accepting), and the config entry that
// wires it. `conductor plugin add` and the dashboard's "add a connection /
// runtime" flow both read it, so the two never disagree about what an add is.
type AddReview struct {
	Use      config.Use
	Kind     config.UseKind
	Instance string // the connector/runtime name it would be configured as
	// Builtin: compiled into conductor — nothing to install or trust.
	Builtin bool
	// Source is the trust-form source ("github.com/owner/repo"); empty for a
	// builtin or a local path (the operator's own disk).
	Source string
	// Official: the source is conductor's official plugin repo, trusted by
	// default.
	Official bool
	// Trusted: plugin_trust (or the official default) admits the source.
	// An untrusted source needs a plugin_trust.allow entry before it can be
	// installed.
	Trusted bool
	// Installed is set when a build satisfying this reference is already in
	// install state; Permissions then lists what it may do (Agreement).
	Installed   *Installed
	Permissions []string
	// Snippet is the config to add (useSnippet's text): a connectors:/
	// runtimes: block, or for an engine the step that names it.
	Snippet string
}

// ReviewAdd works out an AddReview for ref, read-only: it parses the
// reference, checks it against trust, and looks it up in state (which may be
// nil). instance defaults to the plugin's own name.
func ReviewAdd(kind config.UseKind, ref, instance string, trust *config.PackTrustConfig, state *InstallState) (AddReview, error) {
	u, err := config.ParseUse(kind, ref)
	if err != nil {
		return AddReview{}, err
	}
	if instance == "" {
		instance = u.Name
	}
	r := AddReview{Use: u, Kind: kind, Instance: instance, Builtin: u.IsBuiltin()}
	if r.Builtin {
		r.Trusted = true
		r.Snippet = UseSnippet(kind, instance, u.Name)
		return r, nil
	}
	if u.Origin != config.OriginLocal {
		r.Source = u.Source()
		r.Official = config.IsOfficialSource(r.Source)
	}
	r.Trusted = trust.PluginSourceAllowed(r.Source)
	if state != nil && u.Origin != config.OriginLocal {
		if inst, ok := state.GetForConstraint(u.InstallKey(), u); ok && inst.Path != "" {
			r.Installed = &inst
			r.Permissions = Agreement(u.Name, inst.Manifest)
		}
	}
	r.Snippet = UseSnippet(kind, instance, ref)
	return r, nil
}

// Agreement is the permission list an operator accepts by adding a plugin:
// its declared capabilities in plain words, plus what those mean on this
// machine.
func Agreement(name string, m Manifest) []string {
	out := append([]string{}, m.Lines()...)
	return append(out, HereLines(Spec{Name: name, Manifest: m})...)
}

// UseSnippet is the config the operator adds. Connectors and runtimes have a
// block; an ENGINE does not — its reference lives on the step that runs it,
// so printing `engines:` would name a block that does not exist.
func UseSnippet(kind config.UseKind, instance, ref string) string {
	if kind == config.UseKindEngine {
		return fmt.Sprintf("steps:\n  - { id: <step>, use: %s, code: \"…\" }\n", ref)
	}
	return fmt.Sprintf("%s:\n  %s:\n    use: %s\n", kind.Block(), instance, ref)
}
