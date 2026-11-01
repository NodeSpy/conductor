package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// PACK-SHIPPED MEMORY.
//
// A pack may ship memory ENTRIES — the same Markdown-with-frontmatter files
// a `memory: { dir: … }` backend keeps — as a read-only layer recalled
// alongside the consumer's own memory:
//
//	# conductor-pack.yaml
//	memory:
//	  entries: memory        # a directory inside the pack
//
// What a pack still may not ship is a memory BACKEND (`store:`, `dir:`,
// `type:`): which store or filesystem location holds this daemon's memory is
// infrastructure identity, bind-only like stores and connectors. Entries are
// content — notes, standards, facts — and travel with the pack the way its
// prompts do. They are never written: the consumer's writes still go to its
// own backend, and forgetting a pack entry is refused (memory.ErrReadOnly).
//
// The directory is resolved against the pack's VENDORED copy (the node dir
// instantiate reads the manifest from), whatever the source — a local path,
// a public repo, or a private one fetched with the consumer's auth — and it
// must stay inside it: an entries path that climbs out of the pack, or a
// symlink that points out, is a load error, so a pack can never make the
// daemon read files it did not ship.

// PackMemoryEntriesKey is the one key a pack manifest's `memory:` block may
// carry.
const PackMemoryEntriesKey = "entries"

// PackMemoryLayer is one installed pack's read-only memory entries.
type PackMemoryLayer struct {
	// Instance is the pack's namespace in this config (`reviewer`,
	// `reviewer/dep`) — what a refused Forget names.
	Instance string
	// Pack is the pack's canonical name (pack.name).
	Pack string
	// Dir is the absolute entries directory inside the vendored pack.
	Dir string
}

// memoryEntries reads the manifest's `memory:` block. ok is false when the
// manifest has none. Any key but `entries:` is a backend declaration and is
// reported in bad, for checkNoEnvironment to refuse by name.
func (m *PackManifest) memoryEntries() (entries string, bad []string, ok bool, err error) {
	n := m.Memory
	if n.Kind == 0 {
		return "", nil, false, nil
	}
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return "", nil, false, nil
	}
	if n.Kind != yaml.MappingNode {
		return "", []string{"memory"}, true, nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i].Value, n.Content[i+1]
		if k != PackMemoryEntriesKey {
			bad = append(bad, "memory."+k)
			continue
		}
		if v.Kind != yaml.ScalarNode {
			return "", nil, true, fmt.Errorf("memory.entries: want a directory inside the pack, got a %s", nodeKindName(v))
		}
		entries = strings.TrimSpace(v.Value)
	}
	sort.Strings(bad)
	if len(bad) == 0 && entries == "" {
		return "", nil, true, fmt.Errorf("memory: a pack's memory: block takes entries: <directory inside the pack>")
	}
	return entries, bad, true, nil
}

// resolvePackMemoryDir resolves a manifest's entries path against the pack's
// vendored directory and proves it stays there.
func resolvePackMemoryDir(nodeDir, entries string) (string, error) {
	if filepath.IsAbs(entries) || strings.HasPrefix(entries, "~") {
		return "", fmt.Errorf("memory.entries %q: must be a path inside the pack, not an absolute one", entries)
	}
	root, err := filepath.Abs(nodeDir)
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	dir := filepath.Join(root, filepath.FromSlash(entries))
	if !within(root, dir) {
		return "", fmt.Errorf("memory.entries %q: climbs out of the pack", entries)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("memory.entries %q: %w", entries, err)
	}
	if !within(root, real) {
		return "", fmt.Errorf("memory.entries %q: resolves outside the pack (symlink)", entries)
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("memory.entries %q: %w", entries, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("memory.entries %q: not a directory", entries)
	}
	return real, nil
}

// within reports whether path is root or below it.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// addPackMemory records one instantiated pack's entries layer.
func (c *Config) addPackMemory(l PackMemoryLayer) {
	if c.packMemory == nil {
		c.packMemory = map[string]PackMemoryLayer{}
	}
	c.packMemory[l.Instance] = l
}

// PackMemoryLayers lists the read-only memory layers the installed packs
// ship, ordered by instance name so recall's merge is deterministic.
func (c *Config) PackMemoryLayers() []PackMemoryLayer {
	if c == nil || len(c.packMemory) == 0 {
		return nil
	}
	names := make([]string, 0, len(c.packMemory))
	for n := range c.packMemory {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]PackMemoryLayer, 0, len(names))
	for _, n := range names {
		out = append(out, c.packMemory[n])
	}
	return out
}
