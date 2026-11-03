package pkgproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Cargo serves a crates.io-style sparse index. config.json is conductor's
// own (downloads point back here); index files are relayed unchanged (each
// line carries the crate's sha256 "cksum"); a crate download is resolved
// against the upstream index line for that version and the upstream's own
// download URL, and verified before it is cached.
type Cargo struct{ *registry }

// DefaultCargoUpstream is the crates.io sparse index.
const DefaultCargoUpstream = "https://index.crates.io"

// NewCargo builds the cargo ecosystem.
func NewCargo(cfg RegistryConfig) (*Cargo, error) {
	r, err := newRegistry("cargo", DefaultCargoUpstream, cfg)
	if err != nil {
		return nil, err
	}
	return &Cargo{r}, nil
}

// Name is the URL segment.
func (c *Cargo) Name() string { return "cargo" }

// AgentEnv: cargo's source replacement lives in its config file
// (HomeFiles), whose URL names the identity alone — cargo reads no source
// table from the environment. Its User-Agent carries this launch's token,
// so its fetches are attributed to the launch's dispatch.
func (c *Cargo) AgentEnv(base string) []string {
	env := []string{"CARGO_REGISTRIES_CRATES_IO_PROTOCOL=sparse", "CARGO_NET_GIT_FETCH_WITH_CLI=false"}
	if u, err := url.Parse(base); err == nil {
		if tok, _, _ := strings.Cut(strings.TrimPrefix(u.Path, "/"), "/"); tok != "" {
			env = append(env, "CARGO_HTTP_USER_AGENT=cargo (conductor/"+tok+")")
		}
	}
	return env
}

// HomeFiles replaces crates.io with the proxy in ~/.cargo/config.toml.
func (c *Cargo) HomeFiles(base string) map[string]string {
	return map[string]string{".cargo/config.toml": "# Written by conductor: crates come through its package proxy.\n" +
		"[source.crates-io]\nreplace-with = \"conductor\"\n\n" +
		"[source.conductor]\nregistry = \"sparse+" + base + "/index/\"\n"}
}

// crateName is a crate name.
var crateName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// crateVersion is a semver version (with pre-release/build).
var crateVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.+-]{1,100})?$`)

// indexPath is where a crate's file lives in the index (lower-cased).
func indexPath(name string) string {
	n := strings.ToLower(name)
	switch len(n) {
	case 1:
		return "1/" + n
	case 2:
		return "2/" + n
	case 3:
		return "3/" + n[:1] + "/" + n
	}
	return n[:2] + "/" + n[2:4] + "/" + n
}

const cargoMetaMax = 32 << 20

// Serve routes index/config.json, index/<path>, and
// api/v1/crates/<crate>/<version>/download.
func (c *Cargo) Serve(w http.ResponseWriter, r *http.Request, call *Call) {
	bad := func(msg string) {
		st, m := c.fail(w, http.StatusBadRequest, msg)
		call.Emit(Event{Artifact: "request", Status: st, Err: m})
	}
	rest := call.Rest
	switch {
	case rest == "index/config.json":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"dl": call.Base + "/api/v1/crates", "api": call.Base})
	case strings.HasPrefix(rest, "index/"):
		p := strings.TrimPrefix(rest, "index/")
		i := strings.LastIndex(p, "/")
		name := p[i+1:]
		if !crateName.MatchString(name) || indexPath(name) != p {
			bad("bad index path")
			return
		}
		st, msg := c.passthrough(w, r.Context(), c.upstream+"/"+p, "", name, cargoMetaMax, "text/plain")
		if msg != "" {
			call.Emit(Event{Module: name, Artifact: "metadata", Upstream: true, Status: st, Err: msg})
		}
	case strings.HasPrefix(rest, "api/v1/crates/"):
		f := strings.Split(strings.TrimPrefix(rest, "api/v1/crates/"), "/")
		if len(f) != 3 || f[2] != "download" || !crateName.MatchString(f[0]) || !crateVersion.MatchString(f[1]) {
			bad("bad crate download path")
			return
		}
		a, err := c.resolve(r.Context(), f[0], f[1])
		if err != nil {
			var st int
			var msg string
			if nf, ok := err.(errNotFound); ok {
				st, msg = c.notFound(w, f[0]+" "+f[1], nf.msg)
			} else {
				st, msg = c.fail(w, http.StatusBadGateway, err.Error())
			}
			call.Emit(Event{Module: f[0], Version: f[1], Artifact: "artifact", Status: st, Err: msg})
			return
		}
		c.serveArtifact(w, r, call, a, c.isPrivate(f[0]))
	default:
		bad("not a sparse-index or download path")
	}
}

// resolve finds name@version in the upstream index — its cksum — and builds
// the upstream's own download URL from its config.json.
func (c *Cargo) resolve(ctx context.Context, name, version string) (artifact, error) {
	b, err := c.meta.get(ctx, c.fetch, c.upstream+"/"+indexPath(name), "", cargoMetaMax)
	if err != nil {
		return artifact{}, err
	}
	var d Digest
	var canon string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var line struct {
			Name  string `json:"name"`
			Vers  string `json:"vers"`
			Cksum string `json:"cksum"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil || line.Vers != version {
			continue
		}
		if d, err = HexDigest("sha256", line.Cksum); err != nil {
			return artifact{}, err
		}
		canon = line.Name
	}
	if d.IsZero() || !crateName.MatchString(canon) {
		return artifact{}, errNotFound{msg: "no " + name + " " + version + " in the index"}
	}
	cb, err := c.meta.get(ctx, c.fetch, c.upstream+"/config.json", "", 1<<20)
	if err != nil {
		return artifact{}, fmt.Errorf("index config: %w", err)
	}
	var cfg struct {
		DL string `json:"dl"`
	}
	if err := json.Unmarshal(cb, &cfg); err != nil || cfg.DL == "" {
		return artifact{}, fmt.Errorf("index config has no dl")
	}
	u := cfg.DL
	if !strings.ContainsAny(u, "{") {
		u += "/{crate}/{version}/download"
	}
	lower := strings.ToLower(canon)
	prefix := strings.TrimSuffix(indexPath(canon), "/"+lower)
	u = strings.NewReplacer("{crate}", canon, "{version}", version, "{prefix}", prefix,
		"{lowerprefix}", strings.ToLower(prefix), "{sha256-checksum}", fmt.Sprintf("%x", d.Sum)).Replace(u)
	return artifact{Name: canon, Version: version, URL: u, Digest: d, ContentType: "application/x-tar"}, nil
}
