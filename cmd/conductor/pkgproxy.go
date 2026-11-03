package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/pkgproxy"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// startPackageProxy starts conductor's package proxy for sandboxed agents
// (sandbox.packages): a loopback server every agent's package managers are
// pointed at, fetching from each ecosystem's configured upstream with the
// daemon's own network, into one shared cache. Its state lives in
// <root>/.pkgproxy — a dot name no identity home can have — and is never
// shown to an agent. Each upstream fetch and each refusal is audited with
// the identity and dispatch that asked.
func startPackageProxy(cfg *config.Config, root string, logf func(string, ...any), audit func(map[string]any)) (*pkgproxy.Server, func(), error) {
	dir := filepath.Join(root, ".pkgproxy")
	key, err := pkgproxy.LoadKey(filepath.Join(dir, "key"))
	if err != nil {
		return nil, nil, fmt.Errorf("package proxy key: %w", err)
	}
	cache, err := pkgproxy.OpenCache(filepath.Join(dir, "cache"), cfg.Sandbox.PackageCacheMax())
	if err != nil {
		return nil, nil, fmt.Errorf("package proxy cache: %w", err)
	}
	fail := func(err error) (*pkgproxy.Server, func(), error) { cache.Close(); return nil, nil, err }
	var ecos []pkgproxy.Ecosystem
	var names []string
	if cfg.GoPackageProxyOn() {
		gc := cfg.Sandbox.GoPackages()
		db, err := pkgproxy.ParseSumDB(gc.SumDB)
		if err != nil {
			return fail(fmt.Errorf("sandbox.packages.go.sumdb: %w", err))
		}
		fetch, err := pkgproxy.NewFetcher(gc.Upstream, db.URL)
		if err != nil {
			return fail(err)
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
			return fail(err)
		}
		ecos = append(ecos, goEco)
		names = append(names, "go (checksum database "+db.Name+")")
	}
	for _, rc := range cfg.Registries() {
		// Each registry's fetcher trusts its own upstream's host alone; a
		// file host its metadata names is reached over https only.
		fetch, err := pkgproxy.NewFetcher(rc.Upstream)
		if err != nil {
			return fail(fmt.Errorf("sandbox.packages.%s: %w", rc.Name, err))
		}
		e, err := newRegistryEcosystem(rc.Name, pkgproxy.RegistryConfig{Upstream: rc.Upstream, Private: rc.Private, Cache: cache, Fetcher: fetch})
		if err != nil {
			return fail(fmt.Errorf("sandbox.packages.%s: %w", rc.Name, err))
		}
		ecos = append(ecos, e)
		names = append(names, rc.Name)
	}
	srv, err := pkgproxy.New(key, ecos...)
	if err != nil {
		return fail(err)
	}
	srv.OnEvent = func(e pkgproxy.Event) {
		if e.Err != "" {
			logf("package proxy: %s %s@%s (%s) for %s refused: %s", e.Ecosystem, e.Module, e.Version, e.Artifact, e.Requester.Identity, e.Err)
		}
		if audit == nil {
			return
		}
		row := map[string]any{
			"event": "package_fetch", "ecosystem": e.Ecosystem, "module": e.Module, "version": e.Version,
			"artifact": e.Artifact, "upstream": e.Upstream, "status": e.Status,
			"identity": e.Requester.Identity, "dispatch": e.Requester.Dispatch,
		}
		if e.Err != "" {
			row["event"], row["error"] = "package_refused", e.Err
		}
		audit(row)
	}
	addr, err := listenRemembered(srv, filepath.Join(dir, "port"))
	if err != nil {
		return fail(fmt.Errorf("package proxy listen: %w", err))
	}
	logf("package proxy: %s for sandboxed agents at %s (cache %s max)", strings.Join(names, ", "), addr, humanBytes(cfg.Sandbox.PackageCacheMax()))
	return srv, func() { srv.Close(); cache.Close() }, nil
}

// newRegistryEcosystem builds the sandbox.packages.<name> registry.
func newRegistryEcosystem(name string, rc pkgproxy.RegistryConfig) (pkgproxy.Ecosystem, error) {
	switch name {
	case "npm":
		return pkgproxy.NewNPM(rc)
	case "pypi":
		return pkgproxy.NewPyPI(rc)
	case "cargo":
		return pkgproxy.NewCargo(rc)
	case "rubygems":
		return pkgproxy.NewRubyGems(rc)
	case "maven":
		return pkgproxy.NewMaven(rc)
	case "nuget":
		return pkgproxy.NewNuGet(rc)
	case "composer":
		return pkgproxy.NewComposer(rc)
	case "pub":
		return pkgproxy.NewPub(rc)
	case "hex":
		return pkgproxy.NewHex(rc)
	case "cran":
		return pkgproxy.NewCRAN(rc)
	}
	return nil, fmt.Errorf("no such package registry %q", name)
}

// sandboxOwnPorts are loopback ports a Linux sandbox's own network uses
// already (the egress forwarder, a sandboxed `opencode serve`): the proxy's
// relay listens inside the sandbox at its host port, so it must not be one.
var sandboxOwnPorts = map[string]bool{"4096": true}

func init() {
	if _, p, err := net.SplitHostPort(sandbox.ForwardAddr); err == nil {
		sandboxOwnPorts[p] = true
	}
}

// listenRemembered binds the port the previous daemon used (an agent that
// outlived it still has that address in its environment), else any port
// the sandbox does not use itself, and records it.
func listenRemembered(srv *pkgproxy.Server, portFile string) (string, error) {
	if b, err := os.ReadFile(portFile); err == nil {
		if port := strings.TrimSpace(string(b)); port != "" && !sandboxOwnPorts[port] {
			if addr, err := srv.Listen(net.JoinHostPort("127.0.0.1", port)); err == nil {
				return addr, nil
			}
		}
	}
	for try := 0; ; try++ {
		addr, err := srv.Listen("127.0.0.1:0")
		if err != nil {
			return "", err
		}
		_, port, _ := net.SplitHostPort(addr)
		if sandboxOwnPorts[port] && try < 5 {
			srv.Close()
			continue
		}
		_ = os.WriteFile(portFile, []byte(port+"\n"), 0o600)
		return addr, nil
	}
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

// prefetchRequester is who a prefetch's fetches are audited as.
var prefetchRequester = pkgproxy.Requester{Identity: "conductor:prefetch"}

// lockfileWarm is sandbox.prefetch for the registries: a dispatch's
// lockfiles, read while its checkout is still conductor's, warm the proxy's
// cache through the registries' own resolution — no project tool runs on
// the host. nil when prefetch names no enabled registry.
func lockfileWarm(cfg *config.Config, srv *pkgproxy.Server) func(context.Context, string) error {
	var only []string
	for _, r := range cfg.Registries() {
		if cfg.Sandbox.PrefetchNames(r.Name) {
			only = append(only, r.Name)
		}
	}
	if len(only) == 0 {
		return nil
	}
	patterns := srv.Lockfiles(only)
	return func(ctx context.Context, dir string) error {
		files, err := agentsandbox.SnapshotLockfiles(dir, patterns)
		if err != nil || len(files) == 0 {
			return err
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		defer cancel()
		_, err = srv.Warm(ctx, files, prefetchRequester, only)
		return err
	}
}
