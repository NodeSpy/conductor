package pkgproxy

import (
	"context"
	"net/http"
	"regexp"
	"strings"
)

// RubyGems serves the RubyGems compact index (bundler, gem) and the legacy
// specs files. Index files carry no URLs and are relayed unchanged; a .gem
// is resolved against the gem's compact-index info line for that version
// (its published sha256 "checksum") and verified before it is cached.
type RubyGems struct{ *registry }

// DefaultRubyGemsUpstream is rubygems.org.
const DefaultRubyGemsUpstream = "https://rubygems.org"

// NewRubyGems builds the RubyGems ecosystem.
func NewRubyGems(cfg RegistryConfig) (*RubyGems, error) {
	r, err := newRegistry("rubygems", DefaultRubyGemsUpstream, cfg)
	if err != nil {
		return nil, err
	}
	return &RubyGems{r}, nil
}

// Name is the URL segment.
func (g *RubyGems) Name() string { return "rubygems" }

// AgentEnv: gem and bundler read their sources from HOME (HomeFiles).
func (g *RubyGems) AgentEnv(string) []string {
	return []string{"BUNDLE_DISABLE_VERSION_CHECK=true"}
}

// HomeFiles mirrors rubygems.org to the proxy for bundler, and makes it
// gem's only source.
func (g *RubyGems) HomeFiles(base string) map[string]string {
	return map[string]string{
		".bundle/config": "---\nBUNDLE_MIRROR__HTTPS://RUBYGEMS__ORG/: \"" + base + "/\"\n",
		".gemrc":         "---\n:sources:\n- " + base + "/\n:update_sources: false\n",
	}
}

var (
	gemName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	gemFile = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,200}\.gem$`)
	gemSpec = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,200}\.gemspec\.rz$`)
)

const gemMetaMax = 256 << 20

// Serve routes versions, names, info/<gem>, gems/<file>.gem, the specs
// files and quick/Marshal.4.8/<spec>.
func (g *RubyGems) Serve(w http.ResponseWriter, r *http.Request, c *Call) {
	bad := func(msg string) {
		st, m := g.fail(w, http.StatusBadRequest, msg)
		c.Emit(Event{Artifact: "request", Status: st, Err: m})
	}
	rest := c.Rest
	relay := func(p, what string) {
		st, msg := g.passthrough(w, r.Context(), g.upstream+"/"+p, "", what, gemMetaMax, "")
		if msg != "" {
			c.Emit(Event{Module: what, Artifact: "metadata", Upstream: true, Status: st, Err: msg})
		}
	}
	switch {
	case rest == "versions" || rest == "names" ||
		rest == "specs.4.8.gz" || rest == "latest_specs.4.8.gz" || rest == "prerelease_specs.4.8.gz":
		relay(rest, rest)
	case strings.HasPrefix(rest, "info/"):
		name := strings.TrimPrefix(rest, "info/")
		if !gemName.MatchString(name) || strings.Contains(name, "..") {
			bad("bad gem name")
			return
		}
		relay(rest, name)
	case strings.HasPrefix(rest, "quick/Marshal.4.8/"):
		f := strings.TrimPrefix(rest, "quick/Marshal.4.8/")
		if !gemSpec.MatchString(f) || strings.Contains(f, "..") {
			bad("bad gemspec name")
			return
		}
		relay(rest, f)
	case strings.HasPrefix(rest, "gems/"):
		f := strings.TrimPrefix(rest, "gems/")
		if !gemFile.MatchString(f) || strings.Contains(f, "..") {
			bad("bad gem file name")
			return
		}
		a, err := g.resolve(r.Context(), f)
		if err != nil {
			var st int
			var msg string
			if nf, ok := err.(errNotFound); ok {
				st, msg = g.notFound(w, f, nf.msg)
			} else {
				st, msg = g.fail(w, http.StatusBadGateway, err.Error())
			}
			c.Emit(Event{Module: f, Artifact: "artifact", Status: st, Err: msg})
			return
		}
		g.serveArtifact(w, r, c, a, g.isPrivate(a.Name))
	default:
		bad("not a compact-index, specs or gem path")
	}
}

// resolve splits file ("name-version[-platform].gem") at each dash before a
// digit — a version always starts with one — and looks the version up in
// that name's info file, taking the published checksum.
func (g *RubyGems) resolve(ctx context.Context, file string) (artifact, error) {
	stem := strings.TrimSuffix(file, ".gem")
	var lastErr error = errNotFound{msg: "no gem matches " + file}
	for i := 0; i < len(stem)-1; i++ {
		if stem[i] != '-' || stem[i+1] < '0' || stem[i+1] > '9' {
			continue
		}
		name, ver := stem[:i], stem[i+1:]
		if !gemName.MatchString(name) {
			continue
		}
		b, err := g.meta.get(ctx, g.fetch, g.upstream+"/info/"+name, "", gemMetaMax)
		if err != nil {
			lastErr = err
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			v, rest, ok := strings.Cut(line, " ")
			if !ok || v != ver {
				continue
			}
			_, req, _ := strings.Cut(rest, "|")
			for _, kv := range strings.Split(req, ",") {
				if sum, ok := strings.CutPrefix(strings.TrimSpace(kv), "checksum:"); ok {
					d, err := HexDigest("sha256", sum)
					if err != nil {
						return artifact{}, err
					}
					return artifact{Name: name, Version: ver, URL: g.upstream + "/gems/" + file, Digest: d}, nil
				}
			}
			return artifact{}, errNotFound{msg: name + " " + ver + " has no published checksum"}
		}
	}
	return artifact{}, lastErr
}
