package core

import (
	"fmt"
	"sort"
	"sync"
)

// Constructor builds an Integration instance of a given type from its raw
// config. The config is passed as an opaque decoder so core stays decoupled
// from the concrete config package.
type Constructor func(name string, decode func(any) error) (Integration, error)

var (
	regMu    sync.RWMutex
	registry = map[string]Constructor{}
)

// Register makes an integration type available to the engine. Call from an
// integration package's init(). Panics on a duplicate type (programmer error).
func Register(typ string, c Constructor) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := registry[typ]; dup {
		panic("core: integration type registered twice: " + typ)
	}
	registry[typ] = c
}

// Build instantiates the integration of the named type. decode unmarshals the
// instance's raw config into a type-specific struct.
func Build(typ, name string, decode func(any) error) (Integration, error) {
	regMu.RLock()
	c, ok := registry[typ]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown integration type %q (known: %v)", typ, Types())
	}
	return c(name, decode)
}

// Types lists the registered integration types (sorted, for stable errors).
func Types() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(registry))
	for t := range registry {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
