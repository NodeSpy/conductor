package pkgproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// Composer serves a Composer v2 repository (Packagist). packages.json is
// conductor's own; p2 metadata is expanded from its minified form and every
// dist URL is rewritten to point back here, while source (VCS) entries are
// dropped — git-sourced packages are out of scope. A dist is resolved
// against the upstream metadata by its reference and verified against the
// published shasum when there is one; Packagist rarely publishes one (its
// dists are forge-generated archives), so most dists are cached by URL with
// TLS to the fixed hosts as the only guarantee.
type Composer struct{ *registry }

// DefaultComposerUpstream is Packagist's repository.
const DefaultComposerUpstream = "https://repo.packagist.org"

// NewComposer builds the Composer ecosystem.
func NewComposer(cfg RegistryConfig) (*Composer, error) {
	r, err := newRegistry("composer", DefaultComposerUpstream, cfg)
	if err != nil {
		return nil, err
	}
	return &Composer{r}, nil
}

// Name is the URL segment.
func (c *Composer) Name() string { return "composer" }

// AgentEnv: Composer's repositories live in its global config (HomeFiles).
func (c *Composer) AgentEnv(string) []string {
	return []string{"COMPOSER_DISABLE_XDEBUG_WARN=1", "COMPOSER_NO_AUDIT=1"}
}

// HomeFiles replaces Packagist with the proxy in Composer's global config.
func (c *Composer) HomeFiles(base string) map[string]string {
	b, _ := json.MarshalIndent(map[string]any{
		"config": map[string]any{"secure-http": false},
		"repositories": []any{
			map[string]any{"type": "composer", "url": base},
			map[string]any{"packagist.org": false},
		},
	}, "", "  ")
	return map[string]string{".composer/config.json": string(b) + "\n"}
}

var (
	composerPkg = regexp.MustCompile(`^[a-z0-9]([_.-]?[a-z0-9]+)*/[a-z0-9](([_.]|-{1,2})?[a-z0-9]+)*$`)
	composerRef = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
)

const composerMetaMax = 64 << 20

// Serve routes packages.json, p2/<vendor>/<name>[~dev].json and
// dists/<vendor>/<name>/<reference>.<zip|tar>.
func (c *Composer) Serve(w http.ResponseWriter, r *http.Request, call *Call) {
	bad := func(msg string) {
		st, m := c.fail(w, http.StatusBadRequest, msg)
		call.Emit(Event{Artifact: "request", Status: st, Err: m})
	}
	rest := call.Rest
	switch {
	case rest == "packages.json":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"packages": []any{}, "metadata-url": call.Base + "/p2/%package%.json"})
	case strings.HasPrefix(rest, "p2/") && strings.HasSuffix(rest, ".json"):
		pkg := strings.TrimSuffix(strings.TrimPrefix(rest, "p2/"), ".json")
		dev := strings.HasSuffix(pkg, "~dev")
		pkg = strings.TrimSuffix(pkg, "~dev")
		if !composerPkg.MatchString(pkg) {
			bad("bad package name")
			return
		}
		ev := Event{Module: pkg, Artifact: "metadata", Upstream: true}
		vers, st, msg, ok := c.versions(w, r.Context(), pkg, dev)
		if !ok {
			ev.Status, ev.Err = st, msg
			call.Emit(ev)
			return
		}
		for _, v := range vers {
			delete(v, "source")
			d, _ := v["dist"].(map[string]any)
			ref, _ := d["reference"].(string)
			typ, _ := d["type"].(string)
			if d == nil || !composerRef.MatchString(ref) || (typ != "zip" && typ != "tar") {
				delete(v, "dist")
				continue
			}
			d["url"] = call.Base + "/dists/" + pkg + "/" + ref + "." + typ
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"packages": map[string]any{pkg: vers}})
	case strings.HasPrefix(rest, "dists/"):
		p := strings.TrimPrefix(rest, "dists/")
		i := strings.LastIndex(p, "/")
		if i < 0 {
			bad("bad dist path")
			return
		}
		pkg, file := p[:i], p[i+1:]
		ref, typ, _ := strings.Cut(file, ".")
		if !composerPkg.MatchString(pkg) || !composerRef.MatchString(ref) || (typ != "zip" && typ != "tar") {
			bad("bad dist path")
			return
		}
		a, err := c.resolve(r.Context(), pkg, ref)
		if err != nil {
			var st int
			var msg string
			if nf, ok := err.(errNotFound); ok {
				st, msg = c.notFound(w, pkg+" "+ref, nf.msg)
			} else {
				st, msg = c.fail(w, http.StatusBadGateway, err.Error())
			}
			call.Emit(Event{Module: pkg, Version: ref, Artifact: "artifact", Status: st, Err: msg})
			return
		}
		c.serveArtifact(w, r, call, a, c.isPrivate(pkg))
	default:
		bad("not a packages.json, p2 or dist path")
	}
}

func (c *Composer) p2URL(pkg string, dev bool) string {
	u := c.upstream + "/p2/" + pkg
	if dev {
		u += "~dev"
	}
	return u + ".json"
}

// versions reads pkg's upstream p2 file, expanded; ok false means the
// response was written.
func (c *Composer) versions(w http.ResponseWriter, ctx context.Context, pkg string, dev bool) ([]map[string]any, int, string, bool) {
	b, st, msg, ok := c.metadata(w, ctx, c.p2URL(pkg, dev), "", pkg, composerMetaMax)
	if !ok {
		return nil, st, msg, false
	}
	vers, err := composerExpand(b, pkg)
	if err != nil {
		st, msg := c.fail(w, http.StatusBadGateway, err.Error())
		return nil, st, msg, false
	}
	return vers, http.StatusOK, "", true
}

// composerExpand reads pkg's version list from a p2 document, expanding
// the "composer/2.0" minified form (each entry lists only what changed
// from the one before; "__unset" removes a key).
func composerExpand(b []byte, pkg string) ([]map[string]any, error) {
	var doc struct {
		Minified string                      `json:"minified"`
		Packages map[string][]map[string]any `json:"packages"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s metadata: %w", pkg, err)
	}
	list := doc.Packages[pkg]
	if doc.Minified != "composer/2.0" {
		return list, nil
	}
	out := make([]map[string]any, 0, len(list))
	var prev map[string]any
	for _, v := range list {
		cur := make(map[string]any, len(prev)+len(v))
		for k, x := range prev {
			cur[k] = x
		}
		for k, x := range v {
			if s, ok := x.(string); ok && s == "__unset" {
				delete(cur, k)
				continue
			}
			cur[k] = x
		}
		out = append(out, cur)
		prev = cur
	}
	return out, nil
}

// resolve finds the dist with reference ref in pkg's upstream metadata
// (tagged, then dev versions): its URL and shasum.
func (c *Composer) resolve(ctx context.Context, pkg, ref string) (artifact, error) {
	var lastErr error = errNotFound{msg: pkg + " has no dist " + ref}
	for _, dev := range []bool{false, true} {
		b, err := c.meta.get(ctx, c.fetch, c.p2URL(pkg, dev), "", composerMetaMax)
		if err != nil {
			lastErr = err
			continue
		}
		vers, err := composerExpand(b, pkg)
		if err != nil {
			return artifact{}, err
		}
		for _, v := range vers {
			d, _ := v["dist"].(map[string]any)
			if r, _ := d["reference"].(string); r != ref {
				continue
			}
			u, _ := d["url"].(string)
			sum, _ := d["shasum"].(string)
			dg, err := HexDigest("sha1", sum)
			if err != nil {
				return artifact{}, fmt.Errorf("%s %s: %w", pkg, ref, err)
			}
			ver, _ := v["version"].(string)
			return artifact{Name: pkg, Version: ver, URL: u, Digest: dg}, nil
		}
	}
	return artifact{}, lastErr
}
