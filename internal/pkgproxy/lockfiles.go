package pkgproxy

import (
	"context"
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"
)

// Lockfile readers: each turns a lockfile into requests to its own
// ecosystem (see Warmer). They read names and versions only — a URL in a
// lockfile is at most used for its file name, never fetched — and skip
// anything that is not from the public registry form they serve (git,
// path and tarball-URL dependencies).

var semverish = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+[-+0-9A-Za-z.]*$`)

// tomlPackages splits a TOML lockfile into its [[package]] tables.
func tomlPackages(b []byte) []string {
	parts := strings.Split(string(b), "[[package]]")
	if len(parts) < 2 {
		return nil
	}
	out := parts[1:]
	for i, p := range out {
		// A table ends at the next top-level table header.
		if j := tomlTable.FindStringIndex(p); j != nil {
			out[i] = p[:j[0]]
		}
	}
	return out
}

var (
	tomlTable   = regexp.MustCompile(`(?m)^\[[^\[]`)
	tomlName    = regexp.MustCompile(`(?m)^name = "([^"]+)"`)
	tomlVersion = regexp.MustCompile(`(?m)^version = "([^"]+)"`)
	tomlSource  = regexp.MustCompile(`(?m)^source = (.+)$`)
)

func tomlField(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// npm

// Lockfiles: npm, yarn (classic and berry), pnpm and bun.
func (n *NPM) Lockfiles() []string {
	return []string{"package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock"}
}

// WarmPaths are the tarballs the lockfiles pin.
func (n *NPM) WarmPaths(_ context.Context, files map[string][]byte) []string {
	var out []string
	add := func(name, ver string) {
		if name == "" || !semverish.MatchString(ver) {
			return // the ecosystem validates the name like any request's
		}
		out = append(out, name+"/-/"+path.Base(name)+"-"+ver+".tgz")
	}
	for _, f := range sortedKeys(files) {
		b := files[f]
		switch path.Base(f) {
		case "package-lock.json", "npm-shrinkwrap.json":
			var lock struct {
				Packages     map[string]npmLockEntry `json:"packages"`
				Dependencies map[string]npmLockEntry `json:"dependencies"`
			}
			if json.Unmarshal(b, &lock) != nil {
				continue
			}
			for key, e := range lock.Packages {
				i := strings.LastIndex(key, "node_modules/")
				if i < 0 || e.Link || !npmRegistryURL(e.Resolved) {
					continue
				}
				name := key[i+len("node_modules/"):]
				if e.Name != "" {
					name = e.Name
				}
				add(name, e.Version)
			}
			var walk func(map[string]npmLockEntry)
			walk = func(deps map[string]npmLockEntry) {
				for name, e := range deps {
					if npmRegistryURL(e.Resolved) {
						add(name, e.Version)
					}
					walk(e.Dependencies)
				}
			}
			walk(lock.Dependencies)
		case "yarn.lock":
			yarnLock(string(b), add)
		case "pnpm-lock.yaml":
			var lock struct {
				Packages map[string]struct {
					Resolution map[string]any `yaml:"resolution"`
				} `yaml:"packages"`
			}
			if yaml.Unmarshal(b, &lock) != nil {
				continue
			}
			for key, e := range lock.Packages {
				if _, tarball := e.Resolution["tarball"]; tarball || e.Resolution["type"] != nil {
					continue
				}
				key = strings.TrimPrefix(key, "/")
				if i := strings.Index(key, "("); i >= 0 {
					key = key[:i]
				}
				if i := strings.LastIndex(key, "@"); i > 0 {
					add(key[:i], key[i+1:])
				} else if i := strings.LastIndex(key, "/"); i > 0 { // lockfile v5: /name/1.2.3
					add(key[:i], key[i+1:])
				}
			}
		case "bun.lock":
			var lock struct {
				Packages map[string][]any `json:"packages"`
			}
			// bun.lock is JSON with trailing commas.
			clean := trailingComma.ReplaceAll(b, []byte("$1"))
			if json.Unmarshal(clean, &lock) != nil {
				continue
			}
			for _, e := range lock.Packages {
				if len(e) < 2 {
					continue
				}
				spec, _ := e[0].(string)
				if reg, _ := e[1].(string); reg != "" {
					continue // a non-default registry or a git/file source
				}
				if i := strings.LastIndex(spec, "@"); i > 0 {
					add(spec[:i], spec[i+1:])
				}
			}
		}
	}
	return out
}

type npmLockEntry struct {
	Name         string                  `json:"name"`
	Version      string                  `json:"version"`
	Resolved     string                  `json:"resolved"`
	Link         bool                    `json:"link"`
	Dependencies map[string]npmLockEntry `json:"dependencies"`
}

// npmRegistryURL: a resolved URL that is a registry tarball (not git, a
// path or a bare tarball host's file).
var trailingComma = regexp.MustCompile(`,(\s*[}\]])`)

func npmRegistryURL(u string) bool {
	u, _, _ = strings.Cut(u, "#") // yarn classic appends the sha1
	return (strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")) && strings.Contains(u, "/-/") && strings.HasSuffix(u, ".tgz")
}

var (
	yarnHeader     = regexp.MustCompile(`^"?(@?[^@"\s]+)@`)
	yarnVersion    = regexp.MustCompile(`^\s+version:? "?([^"\s]+)"?`)
	yarnResolution = regexp.MustCompile(`^\s+resolution: "?(@?[^@"]+)@npm:([^"]+)"?`)
	yarnResolved   = regexp.MustCompile(`^\s+resolved "([^"]+)"`)
)

// yarnLock reads classic (resolved URL) and berry (resolution: name@npm:v)
// lockfiles.
func yarnLock(s string, add func(name, ver string)) {
	var name, ver, resolved string
	berry := strings.Contains(s, "__metadata:")
	flush := func() {
		if name != "" && ver != "" && (berry || npmRegistryURL(resolved)) {
			add(name, ver)
		}
		name, ver, resolved = "", "", ""
	}
	for _, line := range strings.Split(s, "\n") {
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case !strings.HasPrefix(line, " "):
			flush()
			if m := yarnHeader.FindStringSubmatch(line); m != nil && !berry {
				name = m[1]
			}
		case berry:
			if m := yarnResolution.FindStringSubmatch(line); m != nil {
				name, ver = m[1], m[2]
			}
		default:
			if m := yarnVersion.FindStringSubmatch(line); m != nil {
				ver = m[1]
			} else if m := yarnResolved.FindStringSubmatch(line); m != nil {
				resolved = m[1]
			}
		}
	}
	flush()
}

// PyPI

// Lockfiles: pip requirements with hashes, uv, poetry, pdm and pipenv.
func (p *PyPI) Lockfiles() []string {
	return []string{"requirements*.txt", "uv.lock", "poetry.lock", "pdm.lock", "Pipfile.lock"}
}

var (
	pyReq     = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)(\[[^\]]*\])?\s*==\s*([^\s;\\]+)`)
	pyHash    = regexp.MustCompile(`--hash[= ]sha256:([0-9a-f]{64})`)
	tomlFile  = regexp.MustCompile(`file = "([^"]+)"`)
	tomlURL   = regexp.MustCompile(`url = "([^"]+)"`)
	pyWheelOS = map[string]string{"linux": "linux", "darwin": "macosx", "windows": "win"}
)

// WarmPaths are the distribution files the lockfiles pin. Wheels for other
// platforms than this one are skipped.
func (p *PyPI) WarmPaths(ctx context.Context, files map[string][]byte) []string {
	var out []string
	addFile := func(proj, file string) {
		proj = pyNormalize(proj)
		if pyName.MatchString(proj) && validPyFile(file) && pyWheelWanted(file) {
			out = append(out, "files/"+proj+"/"+file)
		}
	}
	// byHash adds proj's files whose sha256 is one of hashes, from the
	// upstream listing.
	byHash := func(proj string, hashes []string) {
		proj = pyNormalize(proj)
		if !pyName.MatchString(proj) || len(hashes) == 0 {
			return
		}
		b, err := p.meta.get(ctx, p.fetch, p.simpleURL(proj), pySimpleJSON, pyMetaMax)
		if err != nil {
			out = append(out, "simple/"+proj+"/") // surfaces the failure
			return
		}
		var page pySimple
		if json.Unmarshal(b, &page) != nil {
			return
		}
		for _, f := range page.Files {
			h, _ := f["hashes"].(map[string]any)
			sum, _ := h["sha256"].(string)
			name, _ := f["filename"].(string)
			if contains(hashes, sum) {
				addFile(proj, name)
			}
		}
	}
	for _, f := range sortedKeys(files) {
		b := files[f]
		switch base := path.Base(f); {
		case base == "uv.lock":
			for _, pkg := range tomlPackages(b) {
				src := tomlField(tomlSource, pkg)
				if !strings.Contains(src, "registry =") {
					continue
				}
				for _, m := range tomlURL.FindAllStringSubmatch(pkg, -1) {
					if u, err := url.Parse(m[1]); err == nil {
						addFile(tomlField(tomlName, pkg), path.Base(u.Path))
					}
				}
			}
		case base == "poetry.lock" || base == "pdm.lock":
			for _, pkg := range tomlPackages(b) {
				if strings.Contains(pkg, "[package.source]") {
					continue // a git, path or url source
				}
				for _, m := range tomlFile.FindAllStringSubmatch(pkg, -1) {
					addFile(tomlField(tomlName, pkg), m[1])
				}
			}
		case base == "Pipfile.lock":
			var lock map[string]map[string]struct {
				Version string   `json:"version"`
				Hashes  []string `json:"hashes"`
			}
			if json.Unmarshal(b, &lock) != nil {
				continue
			}
			for _, sec := range []string{"default", "develop"} {
				for name, e := range lock[sec] {
					var hs []string
					for _, h := range e.Hashes {
						if s, ok := strings.CutPrefix(h, "sha256:"); ok {
							hs = append(hs, s)
						}
					}
					byHash(name, hs)
				}
			}
		default: // requirements*.txt
			text := strings.ReplaceAll(string(b), "\\\n", " ")
			for _, line := range strings.Split(text, "\n") {
				m := pyReq.FindStringSubmatch(strings.TrimSpace(line))
				if m == nil {
					continue
				}
				var hs []string
				for _, h := range pyHash.FindAllStringSubmatch(line, -1) {
					hs = append(hs, h[1])
				}
				byHash(m[1], hs)
			}
		}
	}
	return out
}

// pyWheelWanted: an sdist, a pure wheel, or a wheel for this OS and arch.
func pyWheelWanted(file string) bool {
	if !strings.HasSuffix(file, ".whl") {
		return true
	}
	tags := strings.Split(strings.TrimSuffix(file, ".whl"), "-")
	plat := tags[len(tags)-1]
	if plat == "any" {
		return true
	}
	if !strings.Contains(plat, pyWheelOS[runtime.GOOS]) {
		return false
	}
	switch runtime.GOARCH {
	case "amd64":
		return strings.Contains(plat, "x86_64") || strings.Contains(plat, "universal2") || strings.Contains(plat, "amd64")
	case "arm64":
		return strings.Contains(plat, "aarch64") || strings.Contains(plat, "arm64") || strings.Contains(plat, "universal2")
	}
	return true
}

// cargo

// Lockfiles: Cargo.lock.
func (c *Cargo) Lockfiles() []string { return []string{"Cargo.lock"} }

// WarmPaths are the crates.io crates Cargo.lock pins.
func (c *Cargo) WarmPaths(_ context.Context, files map[string][]byte) []string {
	var out []string
	for _, f := range sortedKeys(files) {
		for _, pkg := range tomlPackages(files[f]) {
			src := tomlField(tomlSource, pkg)
			if !strings.Contains(src, "crates.io-index") && !strings.Contains(src, "index.crates.io") {
				continue
			}
			name, ver := tomlField(tomlName, pkg), tomlField(tomlVersion, pkg)
			if crateName.MatchString(name) && crateVersion.MatchString(ver) {
				out = append(out, "api/v1/crates/"+name+"/"+ver+"/download")
			}
		}
	}
	return out
}

// RubyGems

// Lockfiles: Gemfile.lock.
func (g *RubyGems) Lockfiles() []string { return []string{"Gemfile.lock", "gems.locked"} }

var gemSpecLine = regexp.MustCompile(`^    ([A-Za-z0-9][A-Za-z0-9._-]*) \(([0-9][A-Za-z0-9._-]*)\)$`)

// WarmPaths are the gems the GEM sections from rubygems.org pin.
func (g *RubyGems) WarmPaths(_ context.Context, files map[string][]byte) []string {
	var out []string
	for _, f := range sortedKeys(files) {
		section, remote := "", ""
		for _, line := range strings.Split(string(files[f]), "\n") {
			if line != "" && !strings.HasPrefix(line, " ") {
				section, remote = line, ""
				continue
			}
			if r, ok := strings.CutPrefix(line, "  remote: "); ok {
				remote = strings.TrimSpace(r)
			}
			if section != "GEM" || !(strings.Contains(remote, "rubygems.org") || strings.TrimRight(remote, "/") == g.upstream) {
				continue
			}
			if m := gemSpecLine.FindStringSubmatch(line); m != nil {
				out = append(out, "gems/"+m[1]+"-"+m[2]+".gem")
			}
		}
	}
	return out
}

// Maven

// Lockfiles: Gradle dependency locks.
func (m *Maven) Lockfiles() []string { return []string{"gradle.lockfile", "buildscript-gradle.lockfile"} }

// WarmPaths are each locked module's .pom and .jar.
func (m *Maven) WarmPaths(_ context.Context, files map[string][]byte) []string {
	var out []string
	for _, f := range sortedKeys(files) {
		for _, line := range strings.Split(string(files[f]), "\n") {
			coord, _, _ := strings.Cut(strings.TrimSpace(line), "=")
			parts := strings.Split(coord, ":")
			if strings.HasPrefix(coord, "#") || len(parts) != 3 {
				continue
			}
			g, a, v := parts[0], parts[1], parts[2]
			dir := strings.ReplaceAll(g, ".", "/") + "/" + a + "/" + v + "/" + a + "-" + v
			out = append(out, dir+".pom", dir+".jar")
		}
	}
	return out
}

// NuGet

// Lockfiles: packages.lock.json.
func (n *NuGet) Lockfiles() []string { return []string{"packages.lock.json"} }

// WarmPaths are the packages the lock resolves.
func (n *NuGet) WarmPaths(_ context.Context, files map[string][]byte) []string {
	var out []string
	for _, f := range sortedKeys(files) {
		var lock struct {
			Dependencies map[string]map[string]struct {
				Type     string `json:"type"`
				Resolved string `json:"resolved"`
			} `json:"dependencies"`
		}
		if json.Unmarshal(files[f], &lock) != nil {
			continue
		}
		for _, deps := range lock.Dependencies {
			for id, d := range deps {
				if d.Type == "Project" || d.Resolved == "" {
					continue
				}
				id, ver := strings.ToLower(id), strings.ToLower(d.Resolved)
				out = append(out, "v3-flatcontainer/"+id+"/"+ver+"/"+id+"."+ver+".nupkg")
			}
		}
	}
	return out
}

// Composer

// Lockfiles: composer.lock.
func (c *Composer) Lockfiles() []string { return []string{"composer.lock"} }

// WarmPaths are the dists the lock pins.
func (c *Composer) WarmPaths(_ context.Context, files map[string][]byte) []string {
	var out []string
	for _, f := range sortedKeys(files) {
		type pkg struct {
			Name string `json:"name"`
			Dist struct {
				Type      string `json:"type"`
				Reference string `json:"reference"`
			} `json:"dist"`
		}
		var lock struct {
			Packages    []pkg `json:"packages"`
			PackagesDev []pkg `json:"packages-dev"`
		}
		if json.Unmarshal(files[f], &lock) != nil {
			continue
		}
		for _, p := range append(lock.Packages, lock.PackagesDev...) {
			if (p.Dist.Type == "zip" || p.Dist.Type == "tar") && p.Dist.Reference != "" {
				out = append(out, "dists/"+p.Name+"/"+p.Dist.Reference+"."+p.Dist.Type)
			}
		}
	}
	return out
}

// pub

// Lockfiles: pubspec.lock.
func (p *Pub) Lockfiles() []string { return []string{"pubspec.lock"} }

// WarmPaths are the hosted packages the lock pins.
func (p *Pub) WarmPaths(_ context.Context, files map[string][]byte) []string {
	var out []string
	for _, f := range sortedKeys(files) {
		var lock struct {
			Packages map[string]struct {
				Source      string `yaml:"source"`
				Version     string `yaml:"version"`
				Description any    `yaml:"description"`
			} `yaml:"packages"`
		}
		if yaml.Unmarshal(files[f], &lock) != nil {
			continue
		}
		for name, e := range lock.Packages {
			if e.Source != "hosted" {
				continue
			}
			if d, ok := e.Description.(map[string]any); ok {
				if u, _ := d["url"].(string); u != "" && !strings.Contains(u, "pub.dev") && !strings.Contains(u, "pub.dartlang.org") && strings.TrimRight(u, "/") != p.upstream {
					continue
				}
			}
			out = append(out, "packages/"+name+"/versions/"+e.Version+".tar.gz")
		}
	}
	return out
}

// Hex

// Lockfiles: mix.lock and rebar.lock.
func (h *Hex) Lockfiles() []string { return []string{"mix.lock", "rebar.lock"} }

var (
	mixLockHex   = regexp.MustCompile(`\{:hex,\s*:"?([a-z0-9_]+)"?,\s*"([^"]+)"`)
	rebarLockPkg = regexp.MustCompile(`\{pkg,\s*<<"([a-z0-9_]+)">>,\s*<<"([^"]+)">>`)
)

// WarmPaths are the hexpm tarballs the locks pin.
func (h *Hex) WarmPaths(_ context.Context, files map[string][]byte) []string {
	var out []string
	for _, f := range sortedKeys(files) {
		re := mixLockHex
		if path.Base(f) == "rebar.lock" {
			re = rebarLockPkg
		}
		for _, m := range re.FindAllStringSubmatch(string(files[f]), -1) {
			out = append(out, "tarballs/"+m[1]+"-"+m[2]+".tar")
		}
	}
	return out
}

// CRAN

// Lockfiles: renv.lock.
func (c *CRAN) Lockfiles() []string { return []string{"renv.lock"} }

// WarmPaths are the CRAN source packages the lock pins: the current
// archive when the index lists that version, else the Archive copy.
func (c *CRAN) WarmPaths(ctx context.Context, files map[string][]byte) []string {
	var out []string
	for _, f := range sortedKeys(files) {
		var lock struct {
			Packages map[string]struct {
				Package    string `json:"Package"`
				Version    string `json:"Version"`
				Source     string `json:"Source"`
				Repository string `json:"Repository"`
			} `json:"Packages"`
		}
		if json.Unmarshal(files[f], &lock) != nil {
			continue
		}
		for _, p := range lock.Packages {
			if p.Source != "Repository" || (p.Repository != "CRAN" && !strings.Contains(p.Repository, "r-project.org")) {
				continue
			}
			file := p.Package + "_" + p.Version + ".tar.gz"
			if _, ok := c.md5(ctx, c.upstream+"/src/contrib/PACKAGES", p.Package, p.Version); ok {
				out = append(out, "src/contrib/"+file)
			} else {
				out = append(out, "src/contrib/Archive/"+p.Package+"/"+file)
			}
		}
	}
	return out
}
