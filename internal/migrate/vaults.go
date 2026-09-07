package migrate

// The vaults pass rewrites the pre-vaults secret model onto the vaults:
// framework — a hard, no-silent-loss requirement: the runtime no longer
// resolves the scheme URIs, so every one of them must leave the config here.
//
//	vault:entry            -> {{ vault "local" "entry" }}   + vaults: local (conductor)
//	op://Item/field        -> {{ vault "op" "Item/field" }} + vaults: op (onepassword)
//	pass:name              -> {{ vault "pass" "name" }}     + vaults: pass (pass)
//	file:/dir/name         -> {{ vault "files" "name" }}    + vaults: files (file, dir: /dir)
//	secrets: {x: <ref>}    -> block removed; every {{.secrets.x}} usage is
//	                          rewritten inline (vault ref -> the vault call,
//	                          env:VAR -> ${VAR}, literal -> the literal)
//	refresh_token: vault:x -> token_vault: local + the vault call as the seed
//
// env:VAR and ${VAR} are the baseline and stay untouched. Existing vaults:
// entries are reused when they match (a conductor entry at the default path,
// a single onepassword/pass entry, a file entry with the same dir); fresh
// names bump a numeric suffix past any collision with connectors or vaults.
// The pass runs on every migrated file — inside the legacy transform's
// output AND standalone on a connectors-schema file that still carries the
// old model.

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// secretsUseRe matches one {{.secrets.NAME}} template usage.
var secretsUseRe = regexp.MustCompile(`\{\{\s*\.secrets\.([A-Za-z0-9_-]+)\s*\}\}`)

// applyVaultsPass rewrites one (env-masked) YAML document. Returns the
// rewritten bytes when anything changed.
func applyVaultsPass(masked []byte, notes *[]string) (out []byte, changed bool, err error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(masked, &doc); err != nil {
		return nil, false, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, false, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, false, nil
	}
	rw := &vaultsRewrite{
		root:     root,
		taken:    map[string]bool{"kv": true, "sql": true, "manual": true},
		existing: map[string]map[string]string{},
		fileDirs: map[string]string{},
		newDefs:  map[string]map[string]string{},
		rewrote:  map[*yaml.Node]bool{},
		notes:    notes,
	}
	rw.scanExisting()
	secretsRefs := rw.removeSecretsBlock()
	rw.walkScalars(root, "", func(n *yaml.Node, parentKey string) error {
		return rw.rewriteScalar(n, parentKey)
	})
	if err := rw.err; err != nil {
		return nil, false, err
	}
	rw.addTokenVaults(root)
	if err := rw.rewriteSecretsUses(root, secretsRefs); err != nil {
		return nil, false, err
	}
	rw.appendVaults()
	if !rw.changed {
		return nil, false, nil
	}
	b, err := marshalDoc(&doc)
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

type vaultsRewrite struct {
	root     *yaml.Node
	taken    map[string]bool              // names already used by connectors/vaults/reserved
	existing map[string]map[string]string // existing vaults: name -> {type, dir, path}
	fileDirs map[string]string            // dir -> vault name (existing or new)
	newDefs  map[string]map[string]string // new vaults: entries to append
	rewrote  map[*yaml.Node]bool          // scalar nodes this pass rewrote (idempotency)
	local    string                       // the conductor vault name (once allocated)
	op       string
	pass     string
	notes    *[]string
	changed  bool
	err      error
}

// scanExisting records connector and vault names (collision universe) and
// reusable existing vault entries.
func (rw *vaultsRewrite) scanExisting() {
	for _, section := range []string{"connectors", "vaults"} {
		m := mapValue(rw.root, section)
		if m == nil {
			continue
		}
		for i := 0; i+1 < len(m.Content); i += 2 {
			name := m.Content[i].Value
			rw.taken[name] = true
			if section != "vaults" {
				continue
			}
			def := map[string]string{}
			v := m.Content[i+1]
			if v.Kind == yaml.MappingNode {
				for j := 0; j+1 < len(v.Content); j += 2 {
					def[v.Content[j].Value] = v.Content[j+1].Value
				}
			}
			rw.existing[name] = def
			switch def["type"] {
			case "conductor":
				if rw.local == "" && def["path"] == "" {
					rw.local = name
				}
			case "onepassword":
				if rw.op == "" {
					rw.op = name
				}
			case "pass":
				if rw.pass == "" {
					rw.pass = name
				}
			case "file":
				if d := def["dir"]; d != "" {
					rw.fileDirs[filepath.Clean(d)] = name
				}
			}
		}
	}
}

// allocate returns candidate when free, else candidate2, candidate3, ….
func (rw *vaultsRewrite) allocate(candidate string) string {
	name := candidate
	for i := 2; rw.taken[name]; i++ {
		name = fmt.Sprintf("%s%d", candidate, i)
	}
	rw.taken[name] = true
	return name
}

func (rw *vaultsRewrite) conductorVault() string {
	if rw.local == "" {
		rw.local = rw.allocate("local")
		rw.newDefs[rw.local] = map[string]string{"type": "conductor"}
	}
	return rw.local
}

func (rw *vaultsRewrite) opVault() string {
	if rw.op == "" {
		rw.op = rw.allocate("op")
		rw.newDefs[rw.op] = map[string]string{"type": "onepassword"}
	}
	return rw.op
}

func (rw *vaultsRewrite) passVault() string {
	if rw.pass == "" {
		rw.pass = rw.allocate("pass")
		rw.newDefs[rw.pass] = map[string]string{"type": "pass"}
	}
	return rw.pass
}

func (rw *vaultsRewrite) fileVault(dir string) string {
	dir = filepath.Clean(dir)
	if name, ok := rw.fileDirs[dir]; ok {
		return name
	}
	name := rw.allocate("files")
	rw.fileDirs[dir] = name
	rw.newDefs[name] = map[string]string{"type": "file", "dir": dir}
	return name
}

// legacyRef maps one full-string legacy reference to its vault call ("" =
// not a legacy ref). parentKey "path" is exempt (a sqlite file: DSN is a
// path, not a secret reference).
func (rw *vaultsRewrite) legacyRef(s, parentKey string) string {
	if strings.ContainsAny(s, " \t\n") {
		return ""
	}
	switch {
	case strings.HasPrefix(s, "vault:") && len(s) > len("vault:"):
		return fmt.Sprintf("{{ vault %q %q }}", rw.conductorVault(), strings.TrimPrefix(s, "vault:"))
	case strings.HasPrefix(s, "op://") && len(s) > len("op://"):
		return fmt.Sprintf("{{ vault %q %q }}", rw.opVault(), strings.TrimPrefix(s, "op://"))
	case strings.HasPrefix(s, "pass:") && len(s) > len("pass:"):
		return fmt.Sprintf("{{ vault %q %q }}", rw.passVault(), strings.TrimPrefix(s, "pass:"))
	case strings.HasPrefix(s, "file:") && len(s) > len("file:") && parentKey != "path":
		p := strings.TrimPrefix(s, "file:")
		return fmt.Sprintf("{{ vault %q %q }}", rw.fileVault(filepath.Dir(p)), filepath.Base(p))
	}
	return ""
}

// rewriteScalar rewrites one scalar value in place when it is a legacy ref.
func (rw *vaultsRewrite) rewriteScalar(n *yaml.Node, parentKey string) error {
	repl := rw.legacyRef(n.Value, parentKey)
	if repl == "" {
		return nil
	}
	*rw.notes = append(*rw.notes, fmt.Sprintf("secret ref %s → %s", n.Value, repl))
	n.Value = repl
	n.Style = yaml.SingleQuotedStyle
	n.Tag = "!!str"
	rw.rewrote[n] = true
	rw.changed = true
	return nil
}

// walkScalars visits every scalar VALUE in the tree with its mapping key.
func (rw *vaultsRewrite) walkScalars(n *yaml.Node, parentKey string, fn func(*yaml.Node, string) error) {
	if rw.err != nil {
		return
	}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			v := n.Content[i+1]
			if v.Kind == yaml.ScalarNode {
				if err := fn(v, key); err != nil {
					rw.err = err
					return
				}
				continue
			}
			rw.walkScalars(v, key, fn)
		}
	case yaml.SequenceNode, yaml.DocumentNode:
		for _, c := range n.Content {
			if c.Kind == yaml.ScalarNode {
				if err := fn(c, parentKey); err != nil {
					rw.err = err
					return
				}
				continue
			}
			rw.walkScalars(c, parentKey, fn)
		}
	}
}

// removeSecretsBlock detaches the root secrets: mapping and returns its
// name -> original-ref map.
func (rw *vaultsRewrite) removeSecretsBlock() map[string]string {
	refs := map[string]string{}
	for i := 0; i+1 < len(rw.root.Content); i += 2 {
		if rw.root.Content[i].Value != "secrets" {
			continue
		}
		m := rw.root.Content[i+1]
		if m.Kind == yaml.MappingNode {
			for j := 0; j+1 < len(m.Content); j += 2 {
				refs[m.Content[j].Value] = m.Content[j+1].Value
			}
		}
		rw.root.Content = append(rw.root.Content[:i], rw.root.Content[i+2:]...)
		rw.changed = true
		*rw.notes = append(*rw.notes, fmt.Sprintf("secrets: block removed (%d entr%s) — usages rewritten inline", len(refs), plural(len(refs), "y", "ies")))
		break
	}
	return refs
}

// rewriteSecretsUses replaces every {{.secrets.NAME}} usage with the
// equivalent inline form of the block's original reference. A leftover
// .secrets. read afterwards (a shape the regex can't see, or an undeclared
// name) is a hard error — never silent loss.
func (rw *vaultsRewrite) rewriteSecretsUses(root *yaml.Node, refs map[string]string) error {
	inline := func(name string) (string, error) {
		ref, ok := refs[name]
		if !ok {
			return "", fmt.Errorf("{{.secrets.%s}} references no secrets: entry — migrate it by hand", name)
		}
		if repl := rw.legacyRef(ref, ""); repl != "" {
			return repl, nil
		}
		if v, isEnv := strings.CutPrefix(ref, "env:"); isEnv {
			return "${" + v + "}", nil
		}
		return ref, nil // a literal: it already sat in the config file
	}
	var werr error
	rw.walkScalars(root, "", func(n *yaml.Node, _ string) error {
		if !strings.Contains(n.Value, ".secrets.") {
			return nil
		}
		out := secretsUseRe.ReplaceAllStringFunc(n.Value, func(m string) string {
			name := secretsUseRe.FindStringSubmatch(m)[1]
			repl, err := inline(name)
			if err != nil {
				werr = err
				return m
			}
			return repl
		})
		if werr != nil {
			return werr
		}
		if strings.Contains(out, ".secrets.") {
			return fmt.Errorf("a .secrets. reference in %q is not a plain {{.secrets.<name>}} read — migrate it by hand", snippetOf(n.Value))
		}
		if out != n.Value {
			*rw.notes = append(*rw.notes, fmt.Sprintf("secrets usage rewritten: %s", snippetOf(out)))
			n.Value = out
			rw.changed = true
		}
		return nil
	})
	if rw.err != nil {
		return rw.err
	}
	return werr
}

// addTokenVaults gives every oauth2 auth block whose refresh_token was a
// vault: ref (now the rewritten seed) a token_vault: pointing at the
// conductor vault, so rotation keeps persisting.
func (rw *vaultsRewrite) addTokenVaults(n *yaml.Node) {
	if n.Kind == yaml.MappingNode {
		var refreshFromVault, hasTokenVault, isOAuth bool
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, v := n.Content[i].Value, n.Content[i+1]
			switch key {
			case "refresh_token":
				// Only a seed THIS pass produced from a vault: ref counts —
				// re-running on an already-migrated file must change nothing.
				if v.Kind == yaml.ScalarNode && rw.rewrote[v] && strings.Contains(v.Value, "{{ vault ") {
					refreshFromVault = true
				}
			case "token_vault":
				hasTokenVault = true
			case "type":
				if v.Value == "oauth2" {
					isOAuth = true
				}
			}
		}
		if isOAuth && refreshFromVault && !hasTokenVault {
			name := rw.conductorVault()
			n.Content = append(n.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "token_vault"},
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name})
			*rw.notes = append(*rw.notes, fmt.Sprintf("oauth2 refresh_token seed kept; token_vault: %s added for rotation", name))
			rw.changed = true
		}
	}
	for _, c := range n.Content {
		rw.addTokenVaults(c)
	}
}

// appendVaults merges the freshly needed entries into (or creates) the
// root vaults: mapping.
func (rw *vaultsRewrite) appendVaults() {
	if len(rw.newDefs) == 0 {
		return
	}
	m := mapValue(rw.root, "vaults")
	if m == nil {
		m = &yaml.Node{Kind: yaml.MappingNode}
		rw.root.Content = append(rw.root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "vaults"}, m)
	}
	names := make([]string, 0, len(rw.newDefs))
	for n := range rw.newDefs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		def := rw.newDefs[name]
		val := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}
		for _, k := range []string{"type", "dir"} {
			if def[k] == "" {
				continue
			}
			val.Content = append(val.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k},
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: def[k]})
		}
		m.Content = append(m.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, val)
		*rw.notes = append(*rw.notes, fmt.Sprintf("vaults: entry added: %s (%s)", name, def["type"]))
	}
	rw.changed = true
}

// mapValue returns the mapping node under key at root ("" when absent or
// not a mapping).
func mapValue(root *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key && root.Content[i+1].Kind == yaml.MappingNode {
			return root.Content[i+1]
		}
	}
	return nil
}

// marshalDoc renders a document node with the project's 2-space indent.
func marshalDoc(doc *yaml.Node) ([]byte, error) {
	var sb strings.Builder
	enc := yaml.NewEncoder(&sb)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return []byte(sb.String()), nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func snippetOf(s string) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}
