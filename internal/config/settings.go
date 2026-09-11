package config

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// `${settings.NAME}` — ONE parameterization mechanism, two places that use it.
//
// A pack declares `settings:` and its manifest is substituted at instantiation
// (pack_instantiate.go). A MAIN CONFIG declares a top-level `settings:` and is
// substituted at load, across every imported file, before the strict decode.
// Both go through the functions here so the syntax, the iteration bound, and
// the unknown-reference rule can't drift apart between them.
//
// It is a TEXT substitution on the config body, deliberately: the value lands
// wherever the reference sits — a scope allowlist entry, a channel, a repo
// glob, a prompt — with no per-field plumbing. That also means it is resolved
// at LOAD time and is the same for every dispatch. The per-event counterpart
// is `{{ }}` templating, which the scope allowlists render at dispatch (see
// internal/flow/resources.go and docs/design/scope-templating.md).
//
// Only the `settings.` prefix is substituted, so a step's shell `${VAR}` and
// the loader's own `${ENV_VAR}` expansion are untouched (both lack the dot).
var settingsRefRE = regexp.MustCompile(`\$\{settings\.([A-Za-z0-9_.-]+)\}`)

// envRefRE matches the `${env.NAME}` form a SETTING VALUE may use to take its
// value from the process environment (conductor.env is loaded into the
// environment by the CLI before the config is read, so a value parked there
// works with no extra wiring). It is deliberately only legal inside a setting
// value: the config body keeps the loader's plain `${NAME}` expansion.
var envRefRE = regexp.MustCompile(`\$\{env\.([A-Za-z_][A-Za-z0-9_]*)\}`)

// maxSettingPasses bounds the resolve/substitute iteration so a setting that
// references itself terminates instead of looping.
const maxSettingPasses = 8

// substituteRefsInScalar replaces every `${settings.NAME}` in ONE scalar
// value. An unknown name is LEFT IN PLACE — it is caught after decode, by
// unknownSettingRefs, so a comment that merely mentions the syntax doesn't
// fail a load while a real field that needs it does.
func substituteRefsInScalar(v string, settings map[string]string) string {
	if !strings.Contains(v, "${settings.") {
		return v
	}
	return settingsRefRE.ReplaceAllStringFunc(v, func(m string) string {
		name := settingsRefRE.FindStringSubmatch(m)[1]
		if val, ok := settings[name]; ok {
			return val
		}
		return m
	})
}

// SubstituteRefs is THE substitution primitive: every `${…}` conductor
// resolves into a config — settings and environment, main config and pack
// manifest — goes through it, and it has one job beyond replacing text.
//
// A substituted value must be able to supply a VALUE and never STRUCTURE.
//
// The first implementation spliced into the raw config BYTES before the
// parse, which made a setting value arbitrary document text:
//
//	settings: { chan: "ops\"\n    trust: full #" }
//	policy: { agent_authored: { approve_via: "${settings.chan}" } }
//
// closed the quoted scalar, opened a SIBLING KEY, and commented out the
// trailing quote. Load returned no error, `trust: full` was in effect, and no
// `trust:` line appeared anywhere in the operator's config. A pack shipping a
// booby-trapped default, or a value from a shared environment, owned the
// consumer's policy.
//
// So substitution happens inside the PARSED tree, in scalar values only, and
// the result is re-encoded — which quotes whatever the value turned out to
// be. Three steps, because a reference can sit where the document does not
// yet parse (`app: { secret: ${VAR} }` — a bare `${` opens a flow mapping):
//
//  1. replace each reference with an INERT TOKEN, so the document parses
//     wherever the reference sat;
//  2. parse, and replace tokens inside SCALAR VALUES with resolved values;
//  3. re-encode, then restore any token the walk did not reach (one inside a
//     comment) to its original reference text.
//
// resolve maps one matched reference to its replacement; reporting false
// leaves the reference in place for a later pass, or for the unknown-reference
// error to name.
func SubstituteRefs(body []byte, re *regexp.Regexp, resolve func(ref string) (string, bool)) ([]byte, error) {
	if !re.Match(body) {
		return body, nil
	}
	// 1. Placeholders. The token is alphanumeric, so it is a legal plain
	// scalar in every context a reference can appear in.
	refs := map[string]string{}
	i := 0
	prefix := refTokenPrefix(body)
	tokenized := re.ReplaceAllFunc(body, func(m []byte) []byte {
		tok := fmt.Sprintf("%s%dZ", prefix, i)
		i++
		refs[tok] = string(m)
		return []byte(tok)
	})
	var doc yaml.Node
	if err := yaml.Unmarshal(tokenized, &doc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	// 2. Scalars only.
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n == nil || n.Kind == yaml.AliasNode {
			return
		}
		if n.Kind == yaml.ScalarNode {
			orig := n.Value
			for tok, ref := range refs {
				if !strings.Contains(n.Value, tok) {
					continue
				}
				val, ok := resolve(ref)
				if !ok {
					val = ref // unknown: put the reference back, verbatim
				}
				n.Value = strings.ReplaceAll(n.Value, tok, val)
			}
			if n.Value != orig {
				// A scalar that was ENTIRELY one reference re-infers its type,
				// so `port: ${PORT}` still decodes as a number. Clearing the
				// tag only lets the encoder choose between !!str/!!int/!!bool
				// for the resolved value — it cannot make the value anything
				// but a scalar.
				if _, whole := refs[orig]; whole && n.Style == 0 {
					n.Tag = ""
				}
			}
			return
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(&doc)
	if doc.Kind == 0 {
		return body, nil
	}
	out, err := encodeNode(&doc)
	if err != nil {
		return nil, err
	}
	// 3. A token the walk never reached sat in a comment; restore its text.
	for tok, ref := range refs {
		out = bytes.ReplaceAll(out, []byte(tok), []byte(ref))
	}
	return out, nil
}

// refTokenPrefix picks a placeholder prefix the body does not already
// contain, so a config that happens to mention one cannot collide with it.
func refTokenPrefix(body []byte) string {
	base := "ZconductorRefZ"
	for n := 0; ; n++ {
		p := base
		if n > 0 {
			p = fmt.Sprintf("%s%dZ", base, n)
		}
		if !bytes.Contains(body, []byte(p)) {
			return p
		}
	}
}

// encodeNode re-serializes a substituted tree.
func encodeNode(n *yaml.Node) ([]byte, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// substituteInBody applies the declared settings to a config body.
func substituteInBody(body []byte, settings map[string]string) ([]byte, error) {
	if len(settings) == 0 {
		return body, nil
	}
	// Iterated so a setting whose VALUE contains another reference resolves
	// too; bounded so a self-referential value terminates.
	out := body
	for i := 0; i < maxSettingPasses; i++ {
		next, err := SubstituteRefs(out, settingsRefRE, func(ref string) (string, bool) {
			name := settingsRefRE.FindStringSubmatch(ref)[1]
			v, ok := settings[name]
			return v, ok
		})
		if err != nil {
			return nil, err
		}
		if bytes.Equal(next, out) {
			return out, nil
		}
		out = next
	}
	return out, nil
}

// unknownSettingRefs lists the `${settings.NAME}` references still present in a
// DECODED body (marshal the decoded struct, so comments are gone) whose name no
// setting declares — the typo, and the "I forgot to declare it" case. Returns
// them sorted and de-duplicated.
func unknownSettingRefs(body []byte, settings map[string]string) []string {
	var missing []string
	seen := map[string]bool{}
	for _, m := range settingsRefRE.FindAllSubmatch(body, -1) {
		name := string(m[1])
		if _, declared := settings[name]; declared || seen[name] {
			continue
		}
		seen[name] = true
		missing = append(missing, name)
	}
	sort.Strings(missing)
	return missing
}

// resolveSettingValues turns a declared `settings:` block into the flat
// name→value map the substitutor takes, resolving each value in order:
//
//	${env.NAME}        the process environment (conductor.env included, since
//	                   the CLI loads it into the environment before this runs)
//	${settings.other}  another setting, chained, bounded by maxSettingPasses
//
// An empty result is an ERROR, not an empty string: a setting that silently
// expands to nothing turns `channel: ["${settings.review_channel}"]` into an
// allowlist entry matching nothing, and the operator finds out from a refusal
// at 3am rather than from the load.
func resolveSettingValues(raw map[string]string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(raw))
	names := make([]string, 0, len(raw))
	for k, v := range raw {
		out[k] = v
		names = append(names, k)
	}
	sort.Strings(names) // deterministic error order
	// Environment first: a value is a literal or an ${env.X} pull, never a
	// shell expansion.
	var missingEnv []string
	for _, name := range names {
		var miss []string
		out[name] = envRefRE.ReplaceAllStringFunc(out[name], func(m string) string {
			ev := envRefRE.FindStringSubmatch(m)[1]
			v, ok := os.LookupEnv(ev)
			if !ok {
				miss = append(miss, fmt.Sprintf("%s (settings.%s)", ev, name))
			}
			return v
		})
		missingEnv = append(missingEnv, miss...)
	}
	if len(missingEnv) > 0 {
		return nil, fmt.Errorf("settings: undefined environment variable(s): %s — define them in conductor.env or the environment",
			strings.Join(missingEnv, ", "))
	}
	// Then chaining, so `${settings.a}` inside another setting's value works.
	for i := 0; i < maxSettingPasses; i++ {
		changed := false
		for _, name := range names {
			next := substituteRefsInScalar(out[name], out)
			if next != out[name] {
				out[name], changed = next, true
			}
		}
		if !changed {
			break
		}
	}
	var empty []string
	for _, name := range names {
		if strings.TrimSpace(out[name]) == "" {
			empty = append(empty, name)
		}
	}
	if len(empty) > 0 {
		return nil, fmt.Errorf("settings: %s resolved to an empty value — a setting that expands to nothing silently empties whatever field references it; give it a value, or drop it",
			strings.Join(empty, ", "))
	}
	return out, nil
}

// settingsFromDoc reads a parsed config document's top-level `settings:` block
// into the flat map, without a strict decode (this runs BEFORE the decode the
// substitution feeds).
func settingsFromDoc(doc map[string]any) (map[string]string, error) {
	blk, ok := doc["settings"]
	if !ok || blk == nil {
		return nil, nil
	}
	m, ok := blk.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("settings: want a map of name -> value, got %T", blk)
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		name := strings.TrimSpace(k)
		if name == "" {
			return nil, fmt.Errorf("settings: empty setting name")
		}
		out[name] = stringifySetting(v)
	}
	return out, nil
}

// ExpandSettings resolves a single config document's own `settings:` block and
// substitutes every `${settings.NAME}` in it. It is the whole mechanism for a
// one-file config; Load uses the same pieces across an import graph.
//
// Exported so a caller that builds a Config from a body rather than from disk
// (tests, `conductor config` tooling) gets the same parameterization a real
// load gives, instead of a subtly different config.
func ExpandSettings(body []byte) ([]byte, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	declared, err := settingsFromDoc(doc)
	if err != nil {
		return nil, err
	}
	resolved, err := resolveSettingValues(declared)
	if err != nil {
		return nil, err
	}
	return substituteInBody(body, resolved)
}

// checkSettingRefs reports `${settings.X}` references that survived into the
// DECODED config and name nothing declared. Called after the decode, on the
// re-marshaled struct, so only real fields count.
func checkSettingRefs(c *Config) error {
	body, err := yaml.Marshal(c)
	if err != nil {
		return nil // a config that won't marshal has bigger problems; not ours to report
	}
	missing := unknownSettingRefs(body, c.Settings)
	if len(missing) == 0 {
		return nil
	}
	declared := "none"
	if len(c.Settings) > 0 {
		names := make([]string, 0, len(c.Settings))
		for k := range c.Settings {
			names = append(names, k)
		}
		sort.Strings(names)
		declared = strings.Join(names, ", ")
	}
	return fmt.Errorf("config references undeclared setting(s): %s — add them to the top-level settings: block (declared: %s)",
		strings.Join(missing, ", "), declared)
}
