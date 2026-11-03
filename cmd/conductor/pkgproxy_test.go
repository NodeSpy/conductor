package main

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/pkgproxy"
)

// With the sandbox on, every ecosystem is served by default and each
// client's env/HOME config points at the proxy; a disabled one is not.
func TestStartPackageProxyAllEcosystems(t *testing.T) {
	off := false
	cfg := &config.Config{Sandbox: &config.SandboxConfig{Packages: &config.SandboxPackages{NPM: &config.RegistryPackages{Enabled: &off}}}}
	var logs []string
	srv, stop, err := startPackageProxy(cfg, t.TempDir(), func(f string, a ...any) { logs = append(logs, f) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	env, files := srv.AgentConfig(pkgproxy.Requester{Identity: "profile:x", Dispatch: "d"})
	all := strings.Join(env, "\n")
	for _, want := range []string{"GOPROXY=http://127.0.0.1:", "PIP_INDEX_URL=", "UV_DEFAULT_INDEX=", "PUB_HOSTED_URL=", "HEX_MIRROR=", "RENV_CONFIG_REPOS_OVERRIDE="} {
		if !strings.Contains(all, want) {
			t.Errorf("env lacks %s", want)
		}
	}
	if strings.Contains(all, "npm_config_registry") {
		t.Error("a disabled ecosystem is still configured")
	}
	for _, f := range []string{".cargo/config.toml", ".m2/settings.xml", ".gradle/init.d/conductor.gradle", ".nuget/NuGet/NuGet.Config", ".composer/config.json", ".gemrc", ".Rprofile"} {
		if files[f] == "" {
			t.Errorf("no HOME file %s", f)
		}
	}
	if lockfileWarm(cfg, srv) != nil {
		t.Error("a lockfile warm without sandbox.prefetch naming a registry")
	}
	cfg.Sandbox.Prefetch = []string{"cargo", "npm"}
	if lockfileWarm(cfg, srv) == nil {
		t.Error("prefetch [cargo] warms nothing")
	}
}

// config's registry defaults are the ecosystems' own.
func TestRegistryDefaultsMatchPkgproxy(t *testing.T) {
	for name, def := range map[string]string{
		"npm": pkgproxy.DefaultNPMUpstream, "pypi": pkgproxy.DefaultPyPIUpstream, "cargo": pkgproxy.DefaultCargoUpstream,
		"rubygems": pkgproxy.DefaultRubyGemsUpstream, "maven": pkgproxy.DefaultMavenUpstream, "nuget": pkgproxy.DefaultNuGetUpstream,
		"composer": pkgproxy.DefaultComposerUpstream, "pub": pkgproxy.DefaultPubUpstream, "hex": pkgproxy.DefaultHexUpstream,
		"cran": pkgproxy.DefaultCRANUpstream,
	} {
		if config.RegistryDefaults[name] != def {
			t.Errorf("%s: config %q, pkgproxy %q", name, config.RegistryDefaults[name], def)
		}
		if _, err := newRegistryEcosystem(name, pkgproxy.RegistryConfig{}); err == nil {
			t.Errorf("%s builds with no cache or fetcher", name)
		}
	}
	if len(config.RegistryNames) != 10 {
		t.Fatalf("registries: %v", config.RegistryNames)
	}
}
