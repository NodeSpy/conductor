package pkgproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// Pub serves a Dart pub hosted repository (dart pub, flutter pub). A
// package listing is relayed with every archive_url rewritten to point
// back here; an archive is resolved against the upstream listing — its URL
// and its published archive_sha256 — and verified before it is cached.
type Pub struct{ *registry }

// DefaultPubUpstream is pub.dev.
const DefaultPubUpstream = "https://pub.dev"

// NewPub builds the pub ecosystem.
func NewPub(cfg RegistryConfig) (*Pub, error) {
	r, err := newRegistry("pub", DefaultPubUpstream, cfg)
	if err != nil {
		return nil, err
	}
	return &Pub{r}, nil
}

// Name is the URL segment.
func (p *Pub) Name() string { return "pub" }

// AgentEnv points dart and flutter at base.
func (p *Pub) AgentEnv(base string) []string {
	return []string{"PUB_HOSTED_URL=" + base, "PUB_ENVIRONMENT=conductor", "DART_SUPPRESS_ANALYTICS=true", "FLUTTER_SUPPRESS_ANALYTICS=true"}
}

var (
	pubName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,63}$`)
	pubVer  = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.+-]{1,100})?$`)
)

const pubMetaMax = 64 << 20

// Serve routes api/packages/<name> and packages/<name>/versions/<ver>.tar.gz.
func (p *Pub) Serve(w http.ResponseWriter, r *http.Request, c *Call) {
	bad := func(msg string) {
		st, m := p.fail(w, http.StatusBadRequest, msg)
		c.Emit(Event{Artifact: "request", Status: st, Err: m})
	}
	parts := strings.Split(c.Rest, "/")
	switch {
	case len(parts) == 3 && parts[0] == "api" && parts[1] == "packages":
		if !pubName.MatchString(parts[2]) {
			bad("bad package name")
			return
		}
		p.serveListing(w, r, c, parts[2])
	case len(parts) == 4 && parts[0] == "packages" && parts[2] == "versions" && strings.HasSuffix(parts[3], ".tar.gz"):
		name, ver := parts[1], strings.TrimSuffix(parts[3], ".tar.gz")
		if !pubName.MatchString(name) || !pubVer.MatchString(ver) {
			bad("bad package or version")
			return
		}
		a, err := p.resolve(r.Context(), name, ver)
		if err != nil {
			var st int
			var msg string
			if nf, ok := err.(errNotFound); ok {
				st, msg = p.notFound(w, name+" "+ver, nf.msg)
			} else {
				st, msg = p.fail(w, http.StatusBadGateway, err.Error())
			}
			c.Emit(Event{Module: name, Version: ver, Artifact: "artifact", Status: st, Err: msg})
			return
		}
		p.serveArtifact(w, r, c, a, p.isPrivate(name))
	default:
		bad("not a package listing or archive path")
	}
}

const pubAccept = "application/vnd.pub.v2+json"

type pubListing struct {
	Name     string           `json:"name"`
	Latest   map[string]any   `json:"latest"`
	Versions []map[string]any `json:"versions"`
}

func (p *Pub) listing(ctx context.Context, name string) (*pubListing, error) {
	b, err := p.meta.get(ctx, p.fetch, p.upstream+"/api/packages/"+name, pubAccept, pubMetaMax)
	if err != nil {
		return nil, err
	}
	var l pubListing
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("%s listing: %w", name, err)
	}
	return &l, nil
}

func (p *Pub) serveListing(w http.ResponseWriter, r *http.Request, c *Call, name string) {
	ev := Event{Module: name, Artifact: "metadata", Upstream: true}
	if _, st, msg, ok := p.metadata(w, r.Context(), p.upstream+"/api/packages/"+name, pubAccept, name, pubMetaMax); !ok {
		ev.Status, ev.Err = st, msg
		c.Emit(ev)
		return
	}
	l, err := p.listing(r.Context(), name)
	if err != nil {
		ev.Status, ev.Err = p.fail(w, http.StatusBadGateway, err.Error())
		c.Emit(ev)
		return
	}
	fix := func(v map[string]any) bool {
		ver, _ := v["version"].(string)
		if !pubVer.MatchString(ver) {
			return false
		}
		v["archive_url"] = c.Base + "/packages/" + name + "/versions/" + ver + ".tar.gz"
		return true
	}
	vers := l.Versions[:0]
	for _, v := range l.Versions {
		if fix(v) {
			vers = append(vers, v)
		}
	}
	l.Versions = vers
	if l.Latest != nil && !fix(l.Latest) {
		l.Latest = nil
	}
	w.Header().Set("Content-Type", pubAccept)
	_ = json.NewEncoder(w).Encode(l)
}

// resolve finds name@ver's archive URL and sha256 in the upstream listing.
func (p *Pub) resolve(ctx context.Context, name, ver string) (artifact, error) {
	l, err := p.listing(ctx, name)
	if err != nil {
		return artifact{}, err
	}
	for _, v := range l.Versions {
		if s, _ := v["version"].(string); s != ver {
			continue
		}
		u, _ := v["archive_url"].(string)
		sum, _ := v["archive_sha256"].(string)
		d, err := HexDigest("sha256", sum)
		if err != nil {
			return artifact{}, fmt.Errorf("%s %s: %w", name, ver, err)
		}
		return artifact{Name: name, Version: ver, URL: u, Digest: d, ContentType: "application/gzip"}, nil
	}
	return artifact{}, errNotFound{msg: "no " + name + " " + ver + " in the listing"}
}
