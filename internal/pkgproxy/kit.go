package pkgproxy

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"
)

// This file is the kit every registry-shaped ecosystem (npm, PyPI, cargo,
// RubyGems, Maven, NuGet, Composer, pub, Hex, CRAN) is built from:
//
//   - metadata is fetched from the ecosystem's ONE configured upstream at a
//     URL built from validated names, held briefly in memory, and served
//     with its artifact URLs rewritten to point back at the proxy;
//   - an artifact request names a package, version and file; the proxy
//     looks the artifact up in the upstream's own metadata — its URL (which
//     may be on the registry's download host) and the integrity value the
//     registry publishes — so the agent never chooses a host;
//   - the artifact is downloaded once, checked against that value, and
//     cached content-addressed (by its digest) — or, where the registry
//     publishes none, by its upstream URL, with TLS to the fixed upstream as
//     the only guarantee.

// Digest is an integrity value a registry publishes for an artifact.
type Digest struct {
	Algo string // sha512 | sha256 | sha1 | md5
	Sum  []byte
}

// IsZero reports no published value.
func (d Digest) IsZero() bool { return d.Algo == "" }

// Strong reports a digest an artifact can be content-addressed by.
func (d Digest) Strong() bool { return d.Algo == "sha256" || d.Algo == "sha512" }

func (d Digest) String() string { return d.Algo + ":" + hex.EncodeToString(d.Sum) }

func newHash(algo string) hash.Hash {
	switch algo {
	case "sha512":
		return sha512.New()
	case "sha256":
		return sha256.New()
	case "sha1":
		return sha1.New()
	case "md5":
		return md5.New()
	}
	return nil
}

var digestLen = map[string]int{"sha512": 64, "sha256": 32, "sha1": 20, "md5": 16}

// HexDigest parses a hex digest of algo ("" error-free zero when s is empty).
func HexDigest(algo, s string) (Digest, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Digest{}, nil
	}
	b, err := hex.DecodeString(strings.ToLower(s))
	if err != nil || len(b) != digestLen[algo] {
		return Digest{}, fmt.Errorf("malformed %s digest %q", algo, s)
	}
	return Digest{Algo: algo, Sum: b}, nil
}

// B64Digest parses a base64 digest of algo.
func B64Digest(algo, s string) (Digest, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != digestLen[algo] {
		return Digest{}, fmt.Errorf("malformed %s digest %q", algo, s)
	}
	return Digest{Algo: algo, Sum: b}, nil
}

// SRIDigest parses a Subresource Integrity string ("sha512-<base64>", maybe
// several space-separated), keeping the strongest it understands.
func SRIDigest(s string) (Digest, error) {
	var best Digest
	rank := map[string]int{"sha1": 1, "sha256": 2, "sha512": 3}
	for _, f := range strings.Fields(s) {
		algo, v, ok := strings.Cut(f, "-")
		if !ok || rank[algo] == 0 {
			continue
		}
		if i := strings.IndexByte(v, '?'); i >= 0 {
			v = v[:i]
		}
		d, err := B64Digest(algo, v)
		if err != nil {
			return Digest{}, err
		}
		if rank[algo] > rank[best.Algo] {
			best = d
		}
	}
	if best.IsZero() && strings.TrimSpace(s) != "" {
		return Digest{}, fmt.Errorf("no usable integrity value in %q", s)
	}
	return best, nil
}

// errIntegrity: an artifact does not match the value its registry publishes.
var errIntegrity = errors.New("integrity mismatch")

// checkFile verifies the file at p against d (a zero d checks nothing).
func checkFile(p string, d Digest) error {
	if d.IsZero() {
		return nil
	}
	h := newHash(d.Algo)
	if h == nil {
		return fmt.Errorf("unsupported digest %s", d.Algo)
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := h.Sum(nil); !bytes.Equal(got, d.Sum) {
		return fmt.Errorf("%w: got %s:%s, the registry publishes %s", errIntegrity, d.Algo, hex.EncodeToString(got), d)
	}
	return nil
}

// registry is the state every registry-shaped ecosystem shares.
type registry struct {
	eco      string
	upstream string // base URL, no trailing slash
	private  []string
	cache    *Cache
	fetch    *Fetcher
	meta     *metaCache
}

// RegistryConfig configures one registry-shaped ecosystem.
type RegistryConfig struct {
	// Upstream is the one registry fetched from.
	Upstream string
	// Private are package-name patterns (path.Match globs) served from
	// Upstream that are never cached (integrity is still checked where the
	// registry publishes it).
	Private []string
	Cache   *Cache
	Fetcher *Fetcher
}

func newRegistry(eco, defUpstream string, cfg RegistryConfig) (*registry, error) {
	if cfg.Cache == nil || cfg.Fetcher == nil {
		return nil, fmt.Errorf("pkgproxy: %s: cache and fetcher are required", eco)
	}
	up := cfg.Upstream
	if up == "" {
		up = defUpstream
	}
	u, err := url.Parse(up)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("pkgproxy: %s: upstream %q is not an http(s) URL", eco, up)
	}
	return &registry{eco: eco, upstream: strings.TrimRight(up, "/"), private: cfg.Private,
		cache: cfg.Cache, fetch: cfg.Fetcher, meta: newMetaCache()}, nil
}

// isPrivate reports name matching a configured private pattern.
func (g *registry) isPrivate(name string) bool {
	for _, p := range g.private {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
		if strings.HasSuffix(p, "/*") && strings.HasPrefix(name, strings.TrimSuffix(p, "*")) {
			return true
		}
	}
	return false
}

// fail writes a refusal the client prints and returns it for the event.
func (g *registry) fail(w http.ResponseWriter, status int, msg string) (int, string) {
	http.Error(w, "conductor package proxy ("+g.eco+"): "+msg, status)
	return status, msg
}

// notFound says a package is not at the upstream, and that nothing will be
// fetched any other way.
func (g *registry) notFound(w http.ResponseWriter, what, detail string) (int, string) {
	msg := fmt.Sprintf("%s not found at %s", what, g.upstream)
	if detail != "" {
		msg += ": " + detail
	}
	msg += fmt.Sprintf(" — conductor fetches from its configured upstream only; a private package needs sandbox.packages.%s.private with an upstream that serves it", g.eco)
	return g.fail(w, http.StatusNotFound, msg)
}

// errNotFound is an upstream 404/410.
type errNotFound struct{ msg string }

func (e errNotFound) Error() string { return "not found upstream: " + e.msg }

// metaTTL is how long upstream metadata is reused: long enough that one
// install's artifact lookups reuse the metadata it just served, short
// enough that a new release is seen.
const metaTTL = 5 * time.Minute

// metaMax bounds the in-memory metadata cache.
const metaMax = 256 << 20

// metaCache holds recent upstream metadata in memory, one fetch at a time
// per URL.
type metaCache struct {
	mu      sync.Mutex
	entries map[string]*metaEntry
	size    int
}

type metaEntry struct {
	body  []byte
	at    time.Time
	ready chan struct{}
	err   error
}

func newMetaCache() *metaCache { return &metaCache{entries: map[string]*metaEntry{}} }

// get fetches u (with accept) through f, reusing a recent answer. limit
// bounds the body.
func (m *metaCache) get(ctx context.Context, f *Fetcher, u, accept string, limit int64) ([]byte, error) {
	key := accept + "\x00" + u
	m.mu.Lock()
	if e, ok := m.entries[key]; ok {
		m.mu.Unlock()
		<-e.ready
		if e.err == nil && time.Since(e.at) < metaTTL {
			return e.body, nil
		}
		m.mu.Lock()
		if m.entries[key] == e {
			delete(m.entries, key)
			m.size -= len(e.body)
		}
	}
	e := &metaEntry{ready: make(chan struct{})}
	m.entries[key] = e
	m.mu.Unlock()

	e.body, e.err = fetchBody(ctx, f, u, accept, limit)
	e.at = time.Now()
	close(e.ready)
	m.mu.Lock()
	if e.err != nil {
		if m.entries[key] == e {
			delete(m.entries, key)
		}
	} else {
		m.size += len(e.body)
		for k, old := range m.entries {
			if m.size <= metaMax {
				break
			}
			if k != key {
				select {
				case <-old.ready:
					delete(m.entries, k)
					m.size -= len(old.body)
				default:
				}
			}
		}
	}
	m.mu.Unlock()
	return e.body, e.err
}

// fetchBody GETs u and reads at most limit bytes of a 200.
func fetchBody(ctx context.Context, f *Fetcher, u, accept string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	resp, err := f.GetAccept(ctx, u, accept)
	if err != nil {
		return nil, fmt.Errorf("upstream unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, errNotFound{msg: strings.TrimSpace(string(b))}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream answered %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("upstream metadata is larger than %d bytes", limit)
	}
	return b, nil
}

// metadata fetches upstream metadata and maps a failure onto the response;
// ok false means the response was written.
func (g *registry) metadata(w http.ResponseWriter, ctx context.Context, u, accept, what string, limit int64) (body []byte, status int, msg string, ok bool) {
	b, err := g.meta.get(ctx, g.fetch, u, accept, limit)
	if err != nil {
		var nf errNotFound
		if errors.As(err, &nf) {
			status, msg = g.notFound(w, what, nf.msg)
		} else {
			status, msg = g.fail(w, http.StatusBadGateway, fmt.Sprintf("%s: %s: %v", what, g.upstream, err))
		}
		return nil, status, msg, false
	}
	return b, http.StatusOK, "", true
}

// artifact is one downloadable file, resolved from upstream metadata.
type artifact struct {
	Name, Version string
	URL           string // the upstream's own URL for it
	Digest        Digest // what the registry publishes (zero: nothing)
	Limit         int64
	ContentType   string
}

// maxArtifact bounds one artifact download.
const maxArtifact = 1 << 30

func (a artifact) key(eco string) string {
	if a.Digest.Strong() {
		return eco + "/" + a.Digest.Algo + "/" + hex.EncodeToString(a.Digest.Sum)
	}
	h := sha256.Sum256([]byte(a.URL))
	return eco + "/url/" + hex.EncodeToString(h[:])
}

// ensure makes sure a verified copy of a is cached (private: never), and
// reports whether it fetched it.
func (g *registry) ensure(a artifact) (bool, error) {
	return g.cache.Fill(a.key(g.eco), func(tmp *os.File) error { return g.download(a, tmp) })
}

// download fetches a into tmp and verifies it.
func (g *registry) download(a artifact, tmp *os.File) error {
	ctx, cancel := context.WithTimeout(context.Background(), fillTimeout)
	defer cancel()
	limit := a.Limit
	if limit <= 0 {
		limit = maxArtifact
	}
	resp, err := g.fetch.GetDerived(ctx, a.URL)
	if err != nil {
		return fmt.Errorf("upstream unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return errNotFound{msg: strings.TrimSpace(string(b))}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream answered %s for %s", resp.Status, a.Name)
	}
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("download %s %s: %w", a.Name, a.Version, err)
	}
	if n > limit {
		return fmt.Errorf("%s %s is larger than %d bytes", a.Name, a.Version, limit)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := checkFile(tmp.Name(), a.Digest); err != nil {
		return fmt.Errorf("%s %s from %s: %w — not served, not cached", a.Name, a.Version, a.URL, err)
	}
	return nil
}

// serveArtifact serves a: from the cache (filled, verified, on a miss), or
// for a private package straight from a verified temp copy.
func (g *registry) serveArtifact(w http.ResponseWriter, r *http.Request, c *Call, a artifact, private bool) {
	ev := Event{Module: a.Name, Version: a.Version, Artifact: "artifact"}
	defer func() { c.Emit(ev) }()
	var f *os.File
	if private {
		tmp, err := os.CreateTemp("", "conductor-pkg-")
		if err != nil {
			ev.Status, ev.Err = g.fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer os.Remove(tmp.Name())
		defer tmp.Close()
		ev.Upstream = true
		if err := g.download(a, tmp); err != nil {
			ev.Status, ev.Err = g.downloadFailed(w, a, err)
			return
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			ev.Status, ev.Err = g.fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		f = tmp
	} else {
		fetched, err := g.ensure(a)
		ev.Upstream = fetched
		if err != nil {
			ev.Status, ev.Err = g.downloadFailed(w, a, err)
			return
		}
		cf, err := g.cache.Open(a.key(g.eco))
		if err != nil {
			ev.Status, ev.Err = g.fail(w, http.StatusServiceUnavailable, "cache: "+err.Error())
			return
		}
		defer cf.Close()
		f = cf
	}
	if a.ContentType != "" {
		w.Header().Set("Content-Type", a.ContentType)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	ev.Status = http.StatusOK
	http.ServeContent(w, r, "", time.Time{}, f)
}

func (g *registry) downloadFailed(w http.ResponseWriter, a artifact, err error) (int, string) {
	var nf errNotFound
	switch {
	case errors.As(err, &nf):
		return g.notFound(w, a.Name+" "+a.Version, nf.msg)
	case errors.Is(err, errIntegrity):
		return g.fail(w, http.StatusBadGateway, "SECURITY: "+err.Error())
	}
	return g.fail(w, http.StatusBadGateway, err.Error())
}

// passthrough relays an upstream answer (status, body) as-is, bounded,
// for metadata that needs no rewriting.
func (g *registry) passthrough(w http.ResponseWriter, ctx context.Context, u, accept, what string, limit int64, contentType string) (int, string) {
	b, status, msg, ok := g.metadata(w, ctx, u, accept, what, limit)
	if !ok {
		return status, msg
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	_, _ = w.Write(b)
	return http.StatusOK, ""
}
