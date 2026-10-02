package connector

import (
	"context"
	"fmt"
)

// exposureTunnel adapts a connector's `exposes` verb (plugin-contract.md
// §2.3) to the hand-off Tunnel: Open invokes the verb with the local
// address, and the close func invokes its release verb with the lease. The
// engine does the same for every exposure connector — a builtin (lan,
// tunnel) or a plugin — and names no tunnel vendor.
type exposureTunnel struct {
	lookup func(string) (*Instance, bool)
	name   string // the exposure connector instance
	from   string // the instance using it, for messages
}

// exposureVerb resolves name to its instance and its exposes verb.
func exposureVerb(lookup func(string) (*Instance, bool), name string) (*Instance, VerbDecl, error) {
	if lookup == nil {
		return nil, VerbDecl{}, fmt.Errorf("expose: %q: no connector registry", name)
	}
	in, ok := lookup(name)
	if !ok {
		return nil, VerbDecl{}, fmt.Errorf("expose: no connector named %q", name)
	}
	v, ok := in.Decl.ExposeVerb()
	if !ok {
		return nil, VerbDecl{}, fmt.Errorf("expose: connector %q (type %s) declares no exposes verb — it cannot expose an address", name, in.Decl.Type)
	}
	if in.Impl == nil || in.DisabledReason != "" {
		return nil, VerbDecl{}, fmt.Errorf("expose: connector %q is disabled: %s", name, in.DisabledReason)
	}
	return in, v, nil
}

func (e exposureTunnel) Open(ctx context.Context, localAddr string) (string, func() error, error) {
	in, v, err := exposureVerb(e.lookup, e.name)
	if err != nil {
		return "", nil, err
	}
	x := v.Semantics.Exposes
	out, err := in.Impl.Invoke(ctx, v.Name, map[string]any{x.Local: localAddr})
	if err != nil {
		return "", nil, fmt.Errorf("expose via %s: %w", e.name, err)
	}
	url, _ := out[x.URL].(string)
	if url == "" {
		return "", nil, fmt.Errorf("expose via %s: %s returned no %s", e.name, v.Name, x.URL)
	}
	closeFn := func() error { return nil }
	if x.Release != "" && x.Lease != "" {
		lease := out[x.Lease]
		closeFn = func() error {
			_, err := in.Impl.Invoke(context.Background(), x.Release, map[string]any{x.Lease: lease})
			return err
		}
	}
	return url, closeFn, nil
}
