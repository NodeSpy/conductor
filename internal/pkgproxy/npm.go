package pkgproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// NPM serves the npm registry protocol (npm, yarn classic and berry, pnpm,
// bun). A packument is fetched from the upstream and served with every
// version's dist.tarball pointing back here; a tarball request is resolved
// against the upstream packument — its tarball URL and its dist.integrity
// (sha512, or the legacy sha1 shasum) — and verified before it is cached.
type NPM struct{ *registry }

// DefaultNPMUpstream is the public npm registry.
const DefaultNPMUpstream = "https://registry.npmjs.org"

// NewNPM builds the npm ecosystem.
func NewNPM(cfg RegistryConfig) (*NPM, error) {
	r, err := newRegistry("npm", DefaultNPMUpstream, cfg)
	if err != nil {
		return nil, err
	}
	return &NPM{r}, nil
}

// Name is the URL segment.
func (n *NPM) Name() string { return "npm" }

// AgentEnv points npm, yarn (classic and berry), pnpm and bun at base, with
// the calls that would reach elsewhere (audit, update checks) off.
func (n *NPM) AgentEnv(base string) []string {
	b := base + "/"
	return []string{
		"npm_config_registry=" + b, "NPM_CONFIG_REGISTRY=" + b,
		// A lockfile's resolved URLs name the public registry: rewrite them
		// to the configured one, whatever host they name.
		"npm_config_replace_registry_host=always",
		"npm_config_audit=false", "npm_config_fund=false", "npm_config_update_notifier=false",
		"YARN_REGISTRY=" + b, "YARN_NPM_REGISTRY_SERVER=" + base, "YARN_UNSAFE_HTTP_WHITELIST=127.0.0.1",
		"YARN_ENABLE_TELEMETRY=0", "YARN_ENABLE_GLOBAL_CACHE=0",
		"BUN_CONFIG_REGISTRY=" + b,
	}
}

// npmName is a package name: an optional @scope/ and a name, url-safe.
// Legacy names may carry capitals.
var npmName = regexp.MustCompile(`^(@[A-Za-z0-9~-][A-Za-z0-9._~-]*/)?[A-Za-z0-9~-][A-Za-z0-9._~-]*$`)

// npmFile is a tarball's file name.
var npmFile = regexp.MustCompile(`^[A-Za-z0-9~-][A-Za-z0-9._~+-]*\.tgz$`)

func validNPMName(s string) bool { return len(s) <= 214 && npmName.MatchString(s) }

// parseNPMPath splits rest into a package name and, for a tarball, its file.
func parseNPMPath(rest string) (name, file string, err error) {
	p, err := url.PathUnescape(rest)
	if err != nil {
		return "", "", err
	}
	if i := strings.Index(p, "/-/"); i >= 0 {
		name, file = p[:i], p[i+3:]
		if !npmFile.MatchString(file) || strings.Contains(file, "..") {
			return "", "", fmt.Errorf("bad tarball name %q", file)
		}
	} else {
		name = p
	}
	if !validNPMName(name) || strings.Contains(name, "..") {
		return "", "", fmt.Errorf("bad package name %q", name)
	}
	return name, file, nil
}

// packumentURL is the upstream packument for name (a scope's slash escaped).
func (n *NPM) packumentURL(name string) string {
	return n.upstream + "/" + strings.Replace(name, "/", "%2f", 1)
}

const (
	npmAbbrev  = "application/vnd.npm.install-v1+json"
	npmMetaMax = 128 << 20
)

// Serve answers a packument or a tarball request.
func (n *NPM) Serve(w http.ResponseWriter, r *http.Request, c *Call) {
	name, file, err := parseNPMPath(c.Rest)
	if err != nil {
		st, msg := n.fail(w, http.StatusBadRequest, err.Error())
		c.Emit(Event{Artifact: "request", Status: st, Err: msg})
		return
	}
	if file == "" {
		accept := ""
		if strings.Contains(r.Header.Get("Accept"), npmAbbrev) {
			accept = npmAbbrev
		}
		ev := Event{Module: name, Artifact: "metadata", Upstream: true}
		b, st, msg, ok := n.metadata(w, r.Context(), n.packumentURL(name), accept, name, npmMetaMax)
		if !ok {
			ev.Status, ev.Err = st, msg
			c.Emit(ev)
			return
		}
		out, err := rewriteNPM(b, name, c.Base)
		if err != nil {
			ev.Status, ev.Err = n.fail(w, http.StatusBadGateway, "packument: "+err.Error())
			c.Emit(ev)
			return
		}
		if accept != "" {
			w.Header().Set("Content-Type", accept)
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		_, _ = w.Write(out)
		return
	}
	a, err := n.resolve(r.Context(), name, file)
	if err != nil {
		var st int
		var msg string
		if nf, ok := err.(errNotFound); ok {
			st, msg = n.notFound(w, name+" "+file, nf.msg)
		} else {
			st, msg = n.fail(w, http.StatusBadGateway, err.Error())
		}
		c.Emit(Event{Module: name, Artifact: "artifact", Status: st, Err: msg})
		return
	}
	n.serveArtifact(w, r, c, a, n.isPrivate(name))
}

// npmPackument is the part of a packument the proxy reads.
type npmPackument struct {
	Versions map[string]struct {
		Dist struct {
			Tarball   string `json:"tarball"`
			Integrity string `json:"integrity"`
			Shasum    string `json:"shasum"`
		} `json:"dist"`
	} `json:"versions"`
}

// resolve finds file in name's upstream packument: its URL and digest.
func (n *NPM) resolve(ctx context.Context, name, file string) (artifact, error) {
	b, err := n.meta.get(ctx, n.fetch, n.packumentURL(name), npmAbbrev, npmMetaMax)
	if err != nil {
		return artifact{}, err
	}
	var pk npmPackument
	if err := json.Unmarshal(b, &pk); err != nil {
		return artifact{}, fmt.Errorf("packument for %s: %w", name, err)
	}
	for v, ver := range pk.Versions {
		u, err := url.Parse(ver.Dist.Tarball)
		if err != nil || path.Base(u.Path) != file {
			continue
		}
		d, err := SRIDigest(ver.Dist.Integrity)
		if err != nil {
			return artifact{}, fmt.Errorf("%s@%s: %w", name, v, err)
		}
		if d.IsZero() {
			if d, err = HexDigest("sha1", ver.Dist.Shasum); err != nil {
				return artifact{}, fmt.Errorf("%s@%s: %w", name, v, err)
			}
		}
		return artifact{Name: name, Version: v, URL: ver.Dist.Tarball, Digest: d, ContentType: "application/octet-stream"}, nil
	}
	return artifact{}, errNotFound{msg: "no version of " + name + " has the tarball " + file}
}

// rewriteNPM points every version's dist.tarball at base, keeping the rest
// of the packument exactly as the upstream sent it.
func rewriteNPM(b []byte, name, base string) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	raw, ok := doc["versions"]
	if !ok {
		return b, nil
	}
	var versions map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &versions); err != nil {
		return nil, err
	}
	for v, ver := range versions {
		dr, ok := ver["dist"]
		if !ok {
			continue
		}
		var dist map[string]json.RawMessage
		if err := json.Unmarshal(dr, &dist); err != nil {
			return nil, fmt.Errorf("version %s: %w", v, err)
		}
		var tb string
		_ = json.Unmarshal(dist["tarball"], &tb)
		u, err := url.Parse(tb)
		file := ""
		if err == nil {
			file = path.Base(u.Path)
		}
		if !npmFile.MatchString(file) {
			// A tarball the proxy could not name is not offered at all.
			delete(dist, "tarball")
		} else {
			dist["tarball"], _ = json.Marshal(base + "/" + name + "/-/" + file)
		}
		ver["dist"], _ = json.Marshal(dist)
	}
	doc["versions"], _ = json.Marshal(versions)
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
