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
	vendor := packVendorDir(configDir)
	st := &packInstantiation{cfg: c}
	for _, name := range sortedPackKeys(c.Packs) {
		if err := st.instantiate(instantiateReq{
			chain:   []string{name},
			inst:    c.Packs[name],
			nodeDir: filepath.Join(vendor, name),
		}); err != nil {
			return err
		}
	}
	c.packWarnings = st.warnings
	return nil
}

// packInstantiation accumulates state across the recursive walk.
type packInstantiation struct {
	cfg      *Config
	warnings []string
	total    int
}

// instantiateReq is one node in the pack tree to instantiate.
type instantiateReq struct {
	chain   []string // instance-name chain from the root, e.g. [review base]
	inst    PackInstance
	nodeDir string
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

	man, err := loadPackManifest(req.nodeDir)
	if err != nil {
		return fmt.Errorf("pack %q: %w", ns, err)
	}
	// Security boundary: reject a pack that ships any bind-only section.
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

	// Effective settings: defaults <- preset <- instance overrides, then
	// substitute ${settings.NAME} into the pack body and re-decode.
	settings, err := effectiveSettings(man, req.inst, ns, st)
	if err != nil {
		return err
	}
	man, err = substituteSettings(req.nodeDir, settings)
	if err != nil {
		return fmt.Errorf("pack %q: %w", ns, err)
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

	// ---- Agents: default / bind / override, then namespace. ----
	for _, role := range sortedAgentKeys(man.Agents) {
		base := man.Agents[role]
		b := req.inst.Agents[role]
		switch {
		case b.IsBind():
			// Bound to a consumer global: refs to this role resolve to that
			// global directly (no namespaced copy is emitted).
			continue
		case b.IsOverride():
			merged, err := applyAgentOverride(base, b.Override)
			if err != nil {
				return fmt.Errorf("pack %q: agent %q override: %w", ns, role, err)
			}
			base = merged
		}
		rw.rebindAgent(&base)
		st.cfg.setAgent(rw.agentName(role), base)
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

	// ---- Pack policy (overridden) folds onto the pack's own triggers. ----
	packPolicyOverride := req.inst.Policy

	// ---- Triggers: ship DISARMED; arm from the instance block. ----
	for i := range man.Triggers {
		tr := man.Triggers[i]
		if tr.Name == "" {
			return fmt.Errorf("pack %q: a shipped trigger has no name (a pack trigger must be named so the consumer can arm it)", ns)
		}
		armName := tr.Name
		tr.Name = ns + "/" + tr.Name
		rw.rewriteTrigger(&tr)
		// Disarm: pack triggers arrive inert regardless of what the manifest set.
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
		if arm, ok := req.inst.Triggers[armName]; ok {
			if err := applyTriggerArm(&tr, arm); err != nil {
				return fmt.Errorf("pack %q: trigger %q: %w", ns, armName, err)
			}
			if arm.IsArmed() && len(arm.Repos) == 0 {
				st.warnf("pack %q: trigger %q armed with no repos — it will match nothing (a repo scope is the consent)", ns, armName)
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

	// ---- Recurse into pack dependencies (requires.packs). ----
	for _, alias := range sortedPackKeys(req.inst.Packs) {
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
	// Roles: a bound agent must provide the required skill capabilities.
	for role, rr := range req.Roles {
		b := inst.Agents[role]
		if !b.IsBind() {
			continue // default/override use the pack's bundled agent (assumed to satisfy)
		}
		prof, ok := st.cfg.Agents[b.Bind]
		if !ok {
			return fmt.Errorf("pack %q: role %q bound to agent %q which is not defined under agents:", ns, role, b.Bind)
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

// agentGrantsSkill reports whether a profile's skill policy grants a verb.
func agentGrantsSkill(p AgentProfile, want string) bool {
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
	sub := settingsRefRE.ReplaceAllFunc(raw, func(m []byte) []byte {
		name := string(settingsRefRE.FindSubmatch(m)[1])
		if val, ok := settings[name]; ok {
			return []byte(val)
		}
		return m // leave unknown refs; caught post-decode if in a real field
	})
	var man PackManifest
	if err := strictUnmarshal(sub, &man); err != nil {
		return nil, fmt.Errorf("parse manifest after settings substitution: %w", err)
	}
	// Any ${settings.NAME} still present in a decoded field is an unknown ref.
	body, err := yaml.Marshal(&man)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, m := range settingsRefRE.FindAllSubmatch(body, -1) {
		missing = append(missing, string(m[1]))
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

func (c *Config) setAgent(name string, p AgentProfile) {
	if c.Agents == nil {
		c.Agents = map[string]AgentProfile{}
	}
	c.Agents[name] = p
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

func applyAgentOverride(base AgentProfile, override map[string]any) (AgentProfile, error) {
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
	merged := mergeMaps(bm, override)
	mb, err := yaml.Marshal(merged)
	if err != nil {
		return base, err
	}
	var out AgentProfile
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
		acc = mergeMaps(acc, m)
	}
	if override != nil {
		acc = mergeMaps(acc, override)
	}
	if triggerPolicy != nil {
		b, _ := yaml.Marshal(triggerPolicy)
		var m map[string]any
		_ = yaml.Unmarshal(b, &m)
		acc = mergeMaps(acc, m)
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
		tr.Filters = mergeMaps(tr.Filters, arm.Filters)
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
func sortedAgentKeys(m map[string]AgentProfile) []string {
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
