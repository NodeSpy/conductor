package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// MaxPackDepth caps pack-dependency nesting (mirror of MaxWorkflowDepth).
// Exceeding it errors with the dependency chain.
const MaxPackDepth = 8

// MaxTotalPacks is a backstop on the total number of resolved pack nodes across
// the whole tree (mirror of the workflow 1000-agent cap, sized for configs).
const MaxTotalPacks = 256

// runtimeVersion is the daemon version used for requires.conductor compat
// checks. main sets it via SetRuntimeVersion; "" or "dev" skips the check.
var runtimeVersion = "dev"

// SetRuntimeVersion records the running conductor version so pack
// requires.conductor constraints can be checked at load.
func SetRuntimeVersion(v string) { runtimeVersion = v }

// packVendorDir is where `conductor init` materializes fetched packs, relative
// to the config file's directory. Each top-level instance vendors to
// <vendor>/<instance>/, its dependencies to <instance>/.deps/<alias>/.
func packVendorDir(configDir string) string {
	return filepath.Join(configDir, ".conductor", "packs")
}

// PackWarnings are non-fatal notices raised while instantiating packs
// (deprecations, armed-but-unscoped triggers, unknown settings). Surfaced by
// `conductor validate` and logged at boot.
func (c *Config) PackWarnings() []string { return c.packWarnings }

// instantiatePacks resolves the top-level `packs:` block into the effective
// config: it reads each already-vendored pack, applies settings/binds/overrides,
// namespaces everything under the instance name, arms disarmed triggers, and
// merges the result into c. Offline and deterministic — the network fetch is
// `conductor init` (see pack_resolve.go). A no-op when `packs:` is absent, so
// configs without packs load byte-for-byte unchanged.
func (c *Config) instantiatePacks(configDir string) error {
	if len(c.Packs) == 0 {
		return nil
	}
	// `packs:` key-implies-`use:`: fill each instance's source from its use:
	// reference, or from the key itself (the official pack repo).
	if err := applyPackSourceDefaults(c.Packs); err != nil {
		return err
	}
	vendor := packVendorDir(configDir)
	st := &packInstantiation{cfg: c}
	for _, name := range sortedPackKeys(c.Packs) {
		if !validPackAlias(name) {
			return fmt.Errorf("pack instance name %q is invalid (letters, digits, '-', '_' only)", name)
		}
		if err := st.instantiate(instantiateReq{
			chain:   []string{name},
			inst:    c.Packs[name],
			nodeDir: filepath.Join(vendor, name),
		}); err != nil {
			return err
		}
	}
	// Tamper-evidence: compare each vendored tree against the lockfile digest and
	// warn on drift (a vendored file edited after `conductor init`). A warning,
	// not a hard error, so a benign re-vendor or a missing lockfile never
	// crash-loops the daemon.
	st.verifyLockDigests(configDir, vendor)
	c.packWarnings = st.warnings
	return nil
}

// verifyLockDigests re-hashes each vendored pack node and warns when it no
// longer matches the sha recorded in conductor.lock.yaml.
func (st *packInstantiation) verifyLockDigests(configDir, vendor string) {
	lock, err := ReadLockfile(configDir)
	if err != nil || lock == nil {
		return // no lockfile (e.g. instantiated without init) → nothing to verify
	}
	for _, e := range lock.Packs {
		nodeDir := vendor
		for _, seg := range strings.Split(e.Instance, "/") {
			if nodeDir == vendor {
				nodeDir = filepath.Join(nodeDir, seg)
			} else {
				nodeDir = filepath.Join(nodeDir, ".deps", seg)
			}
		}
		got, err := digestTree(nodeDir)
		if err != nil {
			continue
		}
		if e.Digest != "" && got != e.Digest {
			st.warnf("pack %q: vendored tree does not match the lockfile digest — it was modified after `conductor init` (re-run init to re-pin)", e.Instance)
		}
	}
}

// packInstantiation accumulates state across the recursive walk.
type packInstantiation struct {
	cfg      *Config
	warnings []string
	total    int
}

// instantiateReq is one node in the pack tree to instantiate.
type instantiateReq struct {
	chain     []string // instance-name chain from the root, e.g. [review base]
	nameChain []string // canonical pack names of the ancestry (cycle detection by identity)
	inst      PackInstance
	nodeDir   string
	// connForward/storeForward/secretForward/handoffForward carry a parent's
	// concrete environment bindings so a dependency's required environment can be
	// forwarded down (the concrete binding only ever happens at the top).
	connForward, storeForward, secretForward, handoffForward map[string]string
}

func (st *packInstantiation) instantiate(req instantiateReq) error {
	ns := strings.Join(req.chain, "/")
	if len(req.chain) > MaxPackDepth {
		return fmt.Errorf("pack %q: dependency depth %d exceeds the limit %d (chain: %s)", ns, len(req.chain), MaxPackDepth, strings.Join(req.chain, " -> "))
	}
	st.total++
	if st.total > MaxTotalPacks {
		return fmt.Errorf("pack %q: total resolved packs exceeds the limit %d", ns, MaxTotalPacks)
	}

	rawMan, err := loadPackManifest(req.nodeDir)
	if err != nil {
		return fmt.Errorf("pack %q: %w", ns, err)
	}
	// Cycle detection by pack IDENTITY: the same canonical pack name repeating in
	// the ancestry is a cycle even when reached under a different alias.
	if contains(req.nameChain, rawMan.Pack.Name) {
		return fmt.Errorf("pack cycle: %s -> %s", strings.Join(req.nameChain, " -> "), rawMan.Pack.Name)
	}

	// Effective settings (defaults <- preset <- instance overrides) come from the
	// raw manifest, then ${settings.NAME} is substituted and the manifest is
	// re-decoded. ALL manifest-level security/compat checks below run on the
	// FINAL (substituted) manifest, so a value that injects a bind-only section
	// or a templated compat gate is still caught.
	settings, err := effectiveSettings(rawMan, req.inst, ns, st)
	if err != nil {
		return err
	}
	man, err := substituteSettings(req.nodeDir, settings)
	if err != nil {
		return fmt.Errorf("pack %q: %w", ns, err)
	}
	// Security boundary: reject a pack that ships any bind-only section (checked
	// on the substituted manifest so a settings value can't smuggle one in).
	if err := man.checkNoEnvironment(); err != nil {
		return fmt.Errorf("pack %q: %w", ns, err)
	}
	// Daemon-version compatibility (§16).
	if err := checkConductorConstraint(man.Pack.Requires.Conductor, runtimeVersion); err != nil {
		return fmt.Errorf("pack %q: %w", ns, err)
	}
	// Deprecation notice (§22): warn on load, never force an update.
	if man.Pack.Deprecated != "" {
		st.warnf("pack %q (%s): deprecated: %s", ns, man.Pack.Name, man.Pack.Deprecated)
	}
	// Confinement visibility (§13): structural refs are namespace-rebound, but a
	// free-form {{ vault|secret|kv "name" }} template in a prompt/code/option is
	// NOT — it reaches the consumer's global environment by name, bypassing the
	// requires:/bind boundary. We can't statically confine a free-form template,
	// so surface each one at install time — the review's "silent undeclared reach"
	// gap becomes a visible warning at `conductor init`/`plan`.
	for _, w := range scanEnvReach(man) {
		st.warnf("pack %q: %s", ns, w)
	}

	// Resolve environment bindings for this node. A binding may be given
	// concretely on this instance, or forwarded from a parent.
	env := envBindings{
		conn:    mergeStrMaps(req.connForward, req.inst.Connectors),
		store:   mergeStrMaps(req.storeForward, req.inst.Stores),
		secret:  mergeStrMaps(req.secretForward, req.inst.Secrets),
		handoff: mergeStrMaps(req.handoffForward, req.inst.Handoffs),
	}

	// Validate the instance satisfies the pack interface (requires:).
	if err := st.validateRequires(ns, man, req.inst, env); err != nil {
		return err
	}

	// Build the rename/bind maps that namespace pack-local names and rebind
	// environment references.
	rw := newRefRewriter(ns, man, req.inst, env)

	// ---- Mirrored-section overlay (§5.3): the consumer's steps:/models:
	// blocks deep-merge onto the pack's members BY NAME before anything is
	// namespaced, so the overrides address the pack's own vocabulary. ----
	if err := st.applyStepOverlay(ns, req.inst, man.Steps); err != nil {
		return err
	}
	if err := st.applyFleetOverlay(ns, req.inst, man.Models); err != nil {
		return err
	}

	// ---- Step templates: default / bind / override, then namespace. ----
	for _, role := range sortedStepKeys(man.Steps) {
		bundled := man.Steps[role]
		// A pack step may not pin infrastructure (runtime/host) — a pack
		// defines behavior, not environment. model: is allowed: it names a
		// FLEET, which resolves against whatever the consumer actually has.
		if bundled.Host != "" || bundled.Runtime != "" {
			return fmt.Errorf("pack %q: step %q pins runtime/host — a pack defines behavior, not environment; leave it to the consumer's default runtime or bind the role to one of their steps:", ns, role)
		}
		// An OVERRIDE was already merged by applyStepOverlay above (which is
		// the single place it happens, so guidance stacks exactly once); a
		// BIND swaps in one of the consumer's own templates and emits no
		// namespaced copy.
		base := bundled
		if req.inst.Steps[role].IsBind() {
			continue
		}
		// Secret-broker containment: a pack step may only allow_secrets names
		// it DECLARED in requires.secrets (and the consumer bound). Otherwise
		// a pack could guess a consumer's secret names and have the broker
		// issue them.
		if base.Skill != nil {
			for _, sec := range base.Skill.AllowSecrets {
				if _, ok := man.Pack.Requires.Secrets[sec]; !ok {
					return fmt.Errorf("pack %q: step %q skill.allow_secrets %q is not a declared requires.secrets entry — a pack may only reach secrets it declares and the consumer binds", ns, role, sec)
				}
			}
		}
		rw.rebindStep(&base)
		rw.rewriteStepExtends(&base)
		st.cfg.setStep(rw.agentName(role), base)
	}

	// ---- Fleets: the pack's named models, namespaced, with the consumer's
	// per-fleet override applied (the top rung of the ladder, §2.3/§5.3). ----
	for _, name := range sortedNames(man.Models) {
		fleet := man.Models[name]
		if over, ok := req.inst.Models[name]; ok && over.Set() {
			fleet = over
		}
		st.cfg.setFleet(rw.agentName(name), fleet)
	}
	for name := range req.inst.Models {
		if _, ok := man.Models[name]; !ok {
			return fmt.Errorf("pack %q: models: %q names no fleet shipped by this pack (shipped: %s)", ns, name, sortedKeys(man.Models))
		}
	}

	// ---- Checks: namespace names + rewrite refs. ----
	for _, name := range sortedStepKeys(man.Checks) {
		step := man.Checks[name]
		rw.rewriteStep(&step)
		st.cfg.setCheck(rw.checkName(name), step)
	}

	// ---- Workflows: namespace names + rewrite refs. ----
	for _, name := range sortedWorkflowKeys(man.Workflows) {
		wf := man.Workflows[name]
		rw.rewriteWorkflow(&wf)
		st.cfg.setWorkflow(rw.workflowName(name), wf)
	}

	// Lint for references the rewriter cannot reach: a `{{ vault … }}` template
	// (packs bind secrets via requires.secrets + the broker, not vaults) and a
	// bound environment name used inside a `code:` body (code is not rebound).
	st.lintPackRefs(ns, man, env)

	// ---- Pack policy (overridden) folds onto the pack's own triggers. ----
	packPolicyOverride := req.inst.Policy

	// ---- Triggers ----
	//
	// Scope lives on the CONNECTOR, not the pack (§5.2): each trigger binds
	// to the consumer's connector of its own source type, and a source they
	// have none of goes dormant + surfaced rather than failing the load.
	if err := st.validateSourceDeclarations(ns, man, req.inst, man.Triggers); err != nil {
		return err
	}
	// The overlay addresses triggers by the name the PACK gave them, so it
	// runs before source binding rewrites `on:`.
	if err := st.applyTriggerOverlay(ns, req.inst, man.Triggers,
		func(i int) string { return triggerOverlayName(man.Triggers[i]) }); err != nil {
		return err
	}
	if err := st.bindPackSources(ns, man, req.inst, man.Triggers); err != nil {
		return err
	}

	// Ship DISARMED; arm from the instance block.
	for i := range man.Triggers {
		tr := man.Triggers[i]
		if tr.Name == "" {
			return fmt.Errorf("pack %q: a shipped trigger has no name (a pack trigger must be named so the consumer can arm it)", ns)
		}
		armName := tr.Name
		tr.Name = ns + "/" + tr.Name
		rw.rewriteTrigger(&tr)
		// Disarm: pack triggers arrive inert regardless of what the manifest
		// set. A trigger already parked by source dormancy (§5.2) or by an
		// `on:` overlay's `enabled: false` stays parked — arming it would
		// re-enable something the consumer cannot serve or explicitly
		// switched off.
		dormant := tr.Enabled != nil && !*tr.Enabled
		disabled := false
		tr.Enabled = &disabled
		clearTriggerRepos(&tr)
		// Fold pack policy (bundled <- instance override) onto the trigger.
		if man.Policy != nil || packPolicyOverride != nil {
			pol, err := mergePolicy(man.Policy, packPolicyOverride, tr.Policy)
			if err != nil {
				return fmt.Errorf("pack %q: policy: %w", ns, err)
			}
			tr.Policy = pol
		}
		// Arm from the instance block (consent = enabled + repos).
		if arm, ok := req.inst.Triggers[armName]; ok && !dormant {
			if err := applyTriggerArm(&tr, arm); err != nil {
				return fmt.Errorf("pack %q: trigger %q: %w", ns, armName, err)
			}
			// Fail closed: an armed trigger on a repo-scoped (github) source
			// MUST name its repos. The github matcher treats an empty repo set
			// as "match every repo" — falling back to the connector's global
			// scope — so an armed-but-unscoped pack trigger would silently run
			// on repos the consumer never granted THIS pack. The repo list is
			// the pack's consent boundary, so its absence is a hard error, not
			// a warning. (Non-repo sources like `manual`/`rss` are exempt.)
			if arm.IsArmed() && !triggerScopesRepos(&tr) && st.sourceIsRepoScoped(tr.On) {
				return fmt.Errorf("pack %q: trigger %q is armed (enabled: true) but names no repos — a github pack trigger must scope its repos (the repo list is the consent). Add e.g. triggers: { %s: { enabled: true, repos: [owner/repo] } }", ns, armName, armName)
			}
		}
		st.cfg.Triggers = append(st.cfg.Triggers, tr)
	}
	// An arm that names no shipped trigger is a config error.
	for armName := range req.inst.Triggers {
		if !hasTrigger(man.Triggers, armName) {
			return fmt.Errorf("pack %q: triggers: %q names no trigger shipped by this pack (shipped: %s)", ns, armName, triggerNames(man.Triggers))
		}
	}

	// ---- Recurse into pack dependencies: every declared requires.packs dep
	// (auto-pulled), plus any override the consumer supplied. ----
	for _, alias := range depAliases(man.Pack.Requires.Packs, req.inst.Packs) {
		if !validPackAlias(alias) {
			return fmt.Errorf("pack %q: dependency alias %q is invalid (letters, digits, '-', '_' only)", ns, alias)
		}
		if _, ok := man.Pack.Requires.Packs[alias]; !ok {
			return fmt.Errorf("pack %q: packs: %q is not a declared dependency (requires.packs: %s)", ns, alias, depNames(man.Pack.Requires.Packs))
		}
		// Cycle detection: an instance name repeating in the chain is a cycle.
		if contains(req.chain, alias) {
			return fmt.Errorf("pack cycle: %s", strings.Join(append(append([]string{}, req.chain...), alias), " -> "))
		}
		child := req.inst.Packs[alias]
		if err := st.instantiate(instantiateReq{
			chain:          append(append([]string{}, req.chain...), alias),
			nameChain:      append(append([]string{}, req.nameChain...), man.Pack.Name),
			inst:           child,
			nodeDir:        filepath.Join(req.nodeDir, ".deps", alias),
			connForward:    child.Connectors,
			storeForward:   child.Stores,
			secretForward:  child.Secrets,
			handoffForward: child.Handoffs,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (st *packInstantiation) warnf(format string, a ...any) {
	st.warnings = append(st.warnings, fmt.Sprintf(format, a...))
}

// ---------------------------------------------------------------------------
// requires: validation — sockets + role capabilities.
// ---------------------------------------------------------------------------

func (st *packInstantiation) validateRequires(ns string, man *PackManifest, inst PackInstance, env envBindings) error {
	req := man.Pack.Requires
	// Connectors: every required connector must be bound to a defined global.
	for _, name := range req.Connectors {
		bound, ok := env.conn[name]
		if !ok {
			return fmt.Errorf("pack %q: requires connector %q — bind it: connectors: { %s: <your-connector> }", ns, name, name)
		}
		if _, ok := st.cfg.ConnectorsMap[bound]; !ok {
			return fmt.Errorf("pack %q: connector binding %s -> %q names no connector in your config (defined: %s)", ns, name, bound, connectorNames(st.cfg))
		}
	}
	// Stores.
	for _, name := range req.Stores {
		bound, ok := env.store[name]
		if !ok {
			return fmt.Errorf("pack %q: requires store %q — bind it: stores: { %s: <your-store> }", ns, name, name)
		}
		if _, ok := st.cfg.Stores[bound]; !ok {
			return fmt.Errorf("pack %q: store binding %s -> %q names no store in your config", ns, name, bound)
		}
	}
	// Handoffs.
	for _, name := range req.Handoffs {
		bound, ok := env.handoff[name]
		if !ok {
			return fmt.Errorf("pack %q: requires handoff %q — bind it: handoffs: { %s: <your-handoff> }", ns, name, name)
		}
		if _, ok := st.cfg.Handoffs[bound]; !ok {
			return fmt.Errorf("pack %q: handoff binding %s -> %q names no handoff in your config", ns, name, bound)
		}
	}
	// Secrets: bound to a secret/vault reference the consumer owns.
	for name := range req.Secrets {
		bound, ok := env.secret[name]
		if !ok {
			return fmt.Errorf("pack %q: requires secret %q — bind it: secrets: { %s: <your-secret-or-vault-ref> }", ns, name, name)
		}
		if err := st.cfg.checkSecretRef(bound); err != nil {
			return fmt.Errorf("pack %q: secret binding %s -> %q: %w", ns, name, bound, err)
		}
	}
	// Roles: a bound step must provide the required skill capabilities.
	for role, rr := range req.Roles {
		b := inst.Steps[role]
		if !b.IsBind() {
			continue // default/override use the pack's bundled step (assumed to satisfy)
		}
		prof, ok := st.cfg.Steps[b.Bind]
		if !ok {
			return fmt.Errorf("pack %q: role %q bound to step %q which is not defined under steps:", ns, role, b.Bind)
		}
		for _, want := range rr.Skill {
			// The requirement is written pack-side (github.submit_review); rebind
			// its connector prefix to the consumer's before comparing to grants.
			wantBound := want
			if conn, verb, ok := strings.Cut(want, "."); ok {
				if bound, ok := env.conn[conn]; ok {
					wantBound = bound + "." + verb
				}
			}
			if !agentGrantsSkill(prof, wantBound) {
				st.warnf("pack %q: role %q is bound to agent %q, which does not grant skill %q the pack expects", ns, role, b.Bind, wantBound)
			}
		}
	}
	return nil
}

// checkSecretRef reports whether a bound secret reference resolves against the
// consumer's secrets:/vaults: (or is an env-form ref).
func (c *Config) checkSecretRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("empty secret binding")
	}
	// A bare name may be a `secrets:` entry.
	if _, ok := c.SecretRefs[ref]; ok {
		return nil
	}
	// A "<vault>/<key>" form names a vault.
	if v, _, ok := strings.Cut(ref, "/"); ok {
		if _, ok := c.Vaults[v]; ok {
			return nil
		}
	}
	// An env:/op://… style reference or ${...} is accepted as-is (resolved at
	// runtime by the broker/expander).
	if strings.Contains(ref, ":") || strings.HasPrefix(ref, "$") {
		return nil
	}
	return fmt.Errorf("names no secrets: entry, vault, or reference form (env:… / <vault>/<key>)")
}

// agentGrantsSkill reports whether a step's skill policy grants a verb.
func agentGrantsSkill(p Step, want string) bool {
	if p.Skill == nil {
		return false
	}
	for _, v := range p.Skill.Verbs {
		if v == want || v == "*" {
			return true
		}
		if ok, _ := filepath.Match(v, want); ok {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Settings.
// ---------------------------------------------------------------------------

var settingsRefRE = regexp.MustCompile(`\$\{settings\.([A-Za-z0-9_.-]+)\}`)

// envReachRE matches a {{ vault|secret|kv "NAME" … }} runtime env-access template
// call. Structural refs (agents/workflows/connectors in uses/on/hooks/store/…)
// are namespace-rebound, but these free-form template funcs are not: they resolve
// the consumer's GLOBAL vault/secret/store by name, so a pack can reach undeclared
// environment through them. We can't confine a free-form template statically —
// scanEnvReach surfaces each so the operator sees it (§13 confinement is
// structural, not total).
var envReachRE = regexp.MustCompile(`\{\{-?\s*(vault|secret|kv)\s+"([^"]+)"`)

// scanEnvReach reports every {{ vault|secret|kv "name" }} reach in the pack's
// behavior — undeclared, unrebound access to the consumer's global environment.
func scanEnvReach(man *PackManifest) []string {
	body, err := yaml.Marshal(man)
	if err != nil {
		return nil
	}
	kind := map[string]string{"vault": "vault", "secret": "secret", "kv": "store"}
	seen := map[string]bool{}
	var out []string
	for _, m := range envReachRE.FindAllSubmatch(body, -1) {
		fn, name := string(m[1]), string(m[2])
		if seen[fn+" "+name] {
			continue
		}
		seen[fn+" "+name] = true
		out = append(out, fmt.Sprintf("reaches consumer %s %q via a {{ %s }} template — pack templates are NOT namespace-rebound, so this accesses the consumer's global environment directly (undeclared/unconfined); bind it through requires: or remove it", kind[fn], name, fn))
	}
	sort.Strings(out)
	return out
}

// effectiveSettings resolves defaults <- preset <- instance overrides.
func effectiveSettings(man *PackManifest, inst PackInstance, ns string, st *packInstantiation) (map[string]string, error) {
	eff := map[string]any{}
	for name, spec := range man.Settings {
		if spec.Default != nil {
			eff[name] = spec.Default
		}
	}
	if inst.Preset != "" {
		vals, ok := man.Presets[inst.Preset]
		if !ok {
			return nil, fmt.Errorf("pack %q: preset %q is not defined (presets: %s)", ns, inst.Preset, presetNames(man.Presets))
		}
		for k, v := range vals {
			eff[k] = v
		}
	}
	for k, v := range inst.Settings {
		if _, declared := man.Settings[k]; !declared {
			st.warnf("pack %q: setting %q is not declared by the pack (declared: %s)", ns, k, settingNames(man.Settings))
		}
		eff[k] = v
	}
	// Stringify for substitution.
	out := make(map[string]string, len(eff))
	for k, v := range eff {
		out[k] = stringifySetting(v)
	}
	return out, nil
}

func stringifySetting(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		return fmt.Sprintf("%v", x)
	}
}

// substituteSettings re-reads the manifest, substitutes ${settings.NAME}, and
// re-decodes. Only the `settings.` prefix is substituted, so a step's runtime
// ${VAR} (shell) is left untouched. Known refs are replaced everywhere (fields
// and comments alike — harmless in comments); a genuinely-unknown ref is caught
// AFTER decode by re-marshaling the manifest (which drops comments) and scanning
// the real string fields, so a pack comment that merely mentions the syntax does
// not error.
func substituteSettings(nodeDir string, settings map[string]string) (*PackManifest, error) {
	raw, err := os.ReadFile(filepath.Join(nodeDir, PackManifestFile))
	if err != nil {
		return nil, err
	}
	// Iterate so a declared setting whose VALUE itself contains ${settings.other}
	// resolves too (bounded to avoid a self-referential loop).
	sub := raw
	for i := 0; i < 8; i++ {
		next := settingsRefRE.ReplaceAllFunc(sub, func(m []byte) []byte {
			name := string(settingsRefRE.FindSubmatch(m)[1])
			if val, ok := settings[name]; ok {
				return []byte(val)
			}
			return m // leave unknown refs; caught post-decode if in a real field
		})
		if string(next) == string(sub) {
			break
		}
		sub = next
	}
	var man PackManifest
	if err := strictUnmarshal(sub, &man); err != nil {
		return nil, fmt.Errorf("parse manifest after settings substitution: %w", err)
	}
	// A ${settings.NAME} still present in a decoded field, whose NAME is not a
	// declared setting, is a genuine unknown reference. (A declared name that
	// survives — e.g. it arrived inside a setting value that itself was a literal
	// placeholder — is left as-is rather than failing the load.)
	body, err := yaml.Marshal(&man)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, m := range settingsRefRE.FindAllSubmatch(body, -1) {
		name := string(m[1])
		if _, declared := settings[name]; !declared {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		missing = uniq(missing)
		sort.Strings(missing)
		return nil, fmt.Errorf("unknown setting reference(s): %s", strings.Join(missing, ", "))
	}
	return &man, nil
}

// loadPackManifest reads and strict-decodes a pack manifest from nodeDir.
func loadPackManifest(nodeDir string) (*PackManifest, error) {
	p := filepath.Join(nodeDir, PackManifestFile)
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("not fetched (no %s at %s) — run `conductor init`", PackManifestFile, nodeDir)
		}
		return nil, err
	}
	var man PackManifest
	if err := strictUnmarshal(raw, &man); err != nil {
		return nil, fmt.Errorf("parse %s: %w", PackManifestFile, err)
	}
	if man.Pack.Name == "" {
		return nil, fmt.Errorf("%s: pack.name is required", PackManifestFile)
	}
	return &man, nil
}

// ---------------------------------------------------------------------------
// Small helpers.
// ---------------------------------------------------------------------------

type envBindings struct {
	conn, store, secret, handoff map[string]string
}

func (c *Config) setStep(name string, p Step) {
	if c.Steps == nil {
		c.Steps = map[string]Step{}
	}
	c.Steps[name] = p
}

func (c *Config) setFleet(name string, f FleetSpec) {
	if c.Models == nil {
		c.Models = map[string]FleetSpec{}
	}
	c.Models[name] = f
}

func (c *Config) setWorkflow(name string, w WorkflowDef) {
	if c.Workflows == nil {
		c.Workflows = map[string]WorkflowDef{}
	}
	c.Workflows[name] = w
}

func (c *Config) setCheck(name string, s Step) {
	if c.Checks == nil {
		c.Checks = map[string]Step{}
	}
	c.Checks[name] = s
}

func applyStepOverride(base Step, override map[string]any) (Step, error) {
	var bm map[string]any
	b, err := yaml.Marshal(base)
	if err != nil {
		return base, err
	}
	if err := yaml.Unmarshal(b, &bm); err != nil {
		return base, err
	}
	if bm == nil {
		bm = map[string]any{}
	}
	merged := deepOverride(bm, override) // replace semantics so an override can narrow a list
	mb, err := yaml.Marshal(merged)
	if err != nil {
		return base, err
	}
	var out Step
	if err := strictUnmarshal(mb, &out); err != nil {
		return base, err
	}
	return out, nil
}

// mergePolicy deep-merges a pack's bundled policy, the instance's policy
// override, and the trigger's own policy into one *Policy.
func mergePolicy(bundled *Policy, override map[string]any, triggerPolicy *Policy) (*Policy, error) {
	acc := map[string]any{}
	if bundled != nil {
		b, _ := yaml.Marshal(bundled)
		var m map[string]any
		_ = yaml.Unmarshal(b, &m)
		acc = deepOverride(acc, m)
	}
	if override != nil {
		acc = deepOverride(acc, override)
	}
	if triggerPolicy != nil {
		b, _ := yaml.Marshal(triggerPolicy)
		var m map[string]any
		_ = yaml.Unmarshal(b, &m)
		acc = deepOverride(acc, m)
	}
	if len(acc) == 0 {
		return triggerPolicy, nil
	}
	mb, _ := yaml.Marshal(acc)
	var out Policy
	if err := strictUnmarshal(mb, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func applyTriggerArm(tr *TriggerSpec, arm TriggerArm) error {
	tr.Enabled = arm.Enabled
	if tr.Filters == nil {
		tr.Filters = map[string]any{}
	}
	// Repos are the consent: they populate the trigger's repos filter.
	if len(arm.Repos) > 0 {
		repos := make([]any, len(arm.Repos))
		for i, r := range arm.Repos {
			repos[i] = r
		}
		tr.Filters["repos"] = repos
	}
	if arm.Filters != nil {
		tr.Filters = deepOverride(tr.Filters, arm.Filters)
	}
	if arm.Policy != nil {
		pol, err := mergePolicy(nil, arm.Policy, tr.Policy)
		if err != nil {
			return err
		}
		tr.Policy = pol
	}
	return nil
}

func clearTriggerRepos(tr *TriggerSpec) {
	if tr.Filters != nil {
		delete(tr.Filters, "repos")
	}
}

// triggerScopesRepos reports whether tr carries a non-empty repos filter.
func triggerScopesRepos(tr *TriggerSpec) bool {
	v, ok := tr.Filters["repos"]
	if !ok {
		return false
	}
	switch r := v.(type) {
	case []any:
		return len(r) > 0
	case []string:
		return len(r) > 0
	default:
		return false
	}
}

// sourceIsRepoScoped reports whether a trigger's `on:` names a github-type
// connector — the only source whose matcher treats an empty repo set as "match
// every repo", which makes an explicit repo scope load-bearing for consent.
// Bare sources like the built-in `manual` (no ".") and non-github connectors
// have no repo scope and are exempt.
func (st *packInstantiation) sourceIsRepoScoped(on string) bool {
	conn, _, ok := strings.Cut(on, ".")
	if !ok {
		return false
	}
	ref, ok := st.cfg.ConnectorsMap[conn]
	return ok && ref.TypeName() == "github"
}

func hasTrigger(trs []TriggerSpec, name string) bool {
	for _, t := range trs {
		if t.Name == name {
			return true
		}
	}
	return false
}

func triggerNames(trs []TriggerSpec) string {
	var ns []string
	for _, t := range trs {
		if t.Name != "" {
			ns = append(ns, t.Name)
		}
	}
	sort.Strings(ns)
	if len(ns) == 0 {
		return "(none)"
	}
	return strings.Join(ns, ", ")
}

func mergeStrMaps(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func uniq(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func connectorNames(c *Config) string {
	var ns []string
	for n := range c.ConnectorsMap {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	return strings.Join(ns, ", ")
}

func presetNames(m map[string]map[string]any) string { return sortedJoin(mapKeys(m)) }
func settingNames(m map[string]SettingSpec) string   { return sortedJoin(mapKeys(m)) }
func depNames(m map[string]PackDepReq) string        { return sortedJoin(mapKeys(m)) }

func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func sortedJoin(ss []string) string {
	sort.Strings(ss)
	if len(ss) == 0 {
		return "(none)"
	}
	return strings.Join(ss, ", ")
}

func sortedPackKeys(m map[string]PackInstance) []string { s := mapKeys(m); sort.Strings(s); return s }
func sortedAgentKeysUnused(m map[string]Step) []string {
	s := mapKeys(m)
	sort.Strings(s)
	return s
}
func sortedWorkflowKeys(m map[string]WorkflowDef) []string {
	s := mapKeys(m)
	sort.Strings(s)
	return s
}
func sortedStepKeys(m map[string]Step) []string { s := mapKeys(m); sort.Strings(s); return s }
