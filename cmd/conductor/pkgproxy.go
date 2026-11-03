package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/pkgproxy"
)

// startPackageProxy starts conductor's package proxy for sandboxed agents
// (sandbox.packages): a loopback server every agent's package manager is
// pointed at, fetching from the configured upstream with the daemon's own
// network. Its state lives in <root>/.pkgproxy — a dot name no identity home
// can have — and is never shown to an agent. Each upstream fetch and each
// refusal is audited with the identity and dispatch that asked.
func startPackageProxy(cfg *config.Config, root string, logf func(string, ...any), audit func(map[string]any)) (*pkgproxy.Server, func(), error) {
	dir := filepath.Join(root, ".pkgproxy")
	key, err := pkgproxy.LoadKey(filepath.Join(dir, "key"))
	if err != nil {
		return nil, nil, fmt.Errorf("package proxy key: %w", err)
	}
	gc := cfg.Sandbox.GoPackages()
	db, err := pkgproxy.ParseSumDB(gc.SumDB)
	if err != nil {
		return nil, nil, fmt.Errorf("sandbox.packages.go.sumdb: %w", err)
	}
	fetch, err := pkgproxy.NewFetcher(gc.Upstream, db.URL)
	if err != nil {
		return nil, nil, err
	}
	cache, err := pkgproxy.OpenCache(filepath.Join(dir, "cache"), cfg.Sandbox.PackageCacheMax())
	if err != nil {
		return nil, nil, fmt.Errorf("package proxy cache: %w", err)
	}
	goEco, err := pkgproxy.NewGo(pkgproxy.GoConfig{
		Upstream: gc.Upstream, SumDB: db, Private: gc.Private,
		Cache: cache, SumDir: filepath.Join(dir, "sumdb"), Fetcher: fetch,
		OnSecurity: func(msg string) {
			logf("SECURITY: package proxy: checksum database %s: %s", db.Name, msg)
			if audit != nil {
				audit(map[string]any{"event": "package_proxy_security", "sumdb": db.Name, "error": msg})
			}
		},
	})
	if err != nil {
		cache.Close()
		return nil, nil, err
	}
	srv, err := pkgproxy.New(key, goEco)
	if err != nil {
		cache.Close()
		return nil, nil, err
	}
	srv.OnEvent = func(e pkgproxy.Event) {
		if e.Err != "" {
			logf("package proxy: %s %s@%s (%s) for %s refused: %s", e.Ecosystem, e.Module, e.Version, e.Artifact, e.Caller.Identity, e.Err)
		}
		if audit == nil {
			return
		}
		row := map[string]any{
			"event": "package_fetch", "ecosystem": e.Ecosystem, "module": e.Module, "version": e.Version,
			"artifact": e.Artifact, "upstream": e.Upstream, "status": e.Status,
			"identity": e.Caller.Identity, "dispatch": e.Caller.Dispatch,
		}
		if e.Err != "" {
			row["event"], row["error"] = "package_refused", e.Err
		}
		audit(row)
	}
	addr, err := listenRemembered(srv, filepath.Join(dir, "port"))
	if err != nil {
		cache.Close()
		return nil, nil, fmt.Errorf("package proxy listen: %w", err)
	}
	logf("package proxy: Go modules for sandboxed agents at %s (upstream %s, checksum database %s, cache %s max)", addr, gc.Upstream, db.Name, humanBytes(cfg.Sandbox.PackageCacheMax()))
	return srv, func() { srv.Close(); cache.Close() }, nil
}

// listenRemembered binds the port the previous daemon used (an agent that
// outlived it still has that address in its environment), else any port,
// and records it.
func listenRemembered(srv *pkgproxy.Server, portFile string) (string, error) {
	if b, err := os.ReadFile(portFile); err == nil {
		if port := strings.TrimSpace(string(b)); port != "" {
			if addr, err := srv.Listen(net.JoinHostPort("127.0.0.1", port)); err == nil {
				return addr, nil
			}
		}
	}
	addr, err := srv.Listen("127.0.0.1:0")
	if err != nil {
		return "", err
	}
	if _, port, err := net.SplitHostPort(addr); err == nil {
		_ = os.WriteFile(portFile, []byte(port+"\n"), 0o600)
	}
	return addr, nil
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30 && n%(1<<30) == 0:
		return fmt.Sprintf("%dGB", n>>30)
	case n >= 1<<20:
		return fmt.Sprintf("%dMB", n>>20)
	}
	return fmt.Sprintf("%dB", n)
}
