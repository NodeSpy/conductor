package memory

import "fmt"

// Options mirrors the config `memory:` section — exactly one backend is
// selected, no default (config validation enforces the shape; this re-checks
// so programmatic callers get the same contract).
type Options struct {
	Store string // stores: entry name (durable, optionally fleet-shared)
	Dir   string // directory of Markdown files (type: file)
	Type  string // "file" (with Dir) or "memory" (ephemeral in-process)
}

// Build constructs the manager for a memory: section.
func Build(o Options) (*Manager, error) {
	set := 0
	if o.Store != "" {
		set++
	}
	if o.Dir != "" {
		set++
	}
	if o.Type == "memory" {
		set++
	}
	if set != 1 {
		return nil, fmt.Errorf("memory: pick exactly one backend — store: <stores: entry>, dir: <path>, or type: memory")
	}
	switch {
	case o.Store != "":
		b, err := NewStoreBackend(o.Store)
		if err != nil {
			return nil, err
		}
		return NewManager(b), nil
	case o.Dir != "":
		if o.Type != "" && o.Type != "file" {
			return nil, fmt.Errorf("memory: dir: implies type: file, got type: %q", o.Type)
		}
		b, err := NewDirBackend(o.Dir)
		if err != nil {
			return nil, err
		}
		return NewManager(b), nil
	default:
		return NewManager(NewMemBackend()), nil
	}
}
