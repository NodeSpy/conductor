package connector

import (
	"context"
	"fmt"
	"net/url"
	"strings"
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
	return e.open(ctx, localAddr, "")
}

// OpenPath is Open plus a listener's resolved HTTP path (listeners.go
// §2.4): when the exposure verb declares `exposes.path`, the path is passed
// in that option and the verb's returned URL is used as is (smee: the
// channel URL IS the public URL — the plugin's own relay posts to
// local+path). Otherwise the path is appended to the returned URL
// (joinExposedPath) — a byte-level tunnel (cloudflared, ngrok, the builtin
// `tunnel`) forwards the whole origin, path included, so the host must add
// it itself. path == "" is treated the same as "/": no path to add.
func (e exposureTunnel) OpenPath(ctx context.Context, localAddr, path string) (string, func() error, error) {
	return e.open(ctx, localAddr, path)
}

func (e exposureTunnel) open(ctx context.Context, localAddr, path string) (string, func() error, error) {
	in, v, err := exposureVerb(e.lookup, e.name)
	if err != nil {
		return "", nil, err
	}
	x := v.Semantics.Exposes
	opts := map[string]any{x.Local: localAddr}
	viaOption := x.Path != "" && path != ""
	if viaOption {
		opts[x.Path] = path
	}
	out, err := in.Impl.Invoke(ctx, v.Name, opts)
	if err != nil {
		return "", nil, fmt.Errorf("expose via %s: %w", e.name, err)
	}
	publicURL, _ := out[x.URL].(string)
	if publicURL == "" {
		return "", nil, fmt.Errorf("expose via %s: %s returned no %s", e.name, v.Name, x.URL)
	}
	if !viaOption && path != "" {
		publicURL = joinExposedPath(publicURL, path)
	}
	closeFn := func() error { return nil }
	if x.Release != "" && x.Lease != "" {
		lease := out[x.Lease]
		closeFn = func() error {
			_, err := in.Impl.Invoke(context.Background(), x.Release, map[string]any{x.Lease: lease})
			return err
		}
	}
	return publicURL, closeFn, nil
}

// joinExposedPath appends path to rawURL's own path component, careful not
// to introduce a double slash and to preserve any query string rawURL
// already carries. path == "" or "/" is a no-op (the default — nothing to
// add when the listener names no HTTP path of its own, or resolves to the
// root). A rawURL that fails to parse (never expected from a well-behaved
// exposure verb) falls back to a plain string join rather than dropping the
// path silently.
func joinExposedPath(rawURL, path string) string {
	if path == "" || path == "/" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return strings.TrimRight(rawURL, "/") + "/" + strings.TrimLeft(path, "/")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(path, "/")
	return u.String()
}
