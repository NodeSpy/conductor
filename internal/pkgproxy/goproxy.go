package pkgproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
	"golang.org/x/mod/sumdb"
	"golang.org/x/mod/sumdb/dirhash"
	"golang.org/x/mod/sumdb/tlog"
	modzip "golang.org/x/mod/zip"
)

// GoConfig configures the Go module proxy.
type GoConfig struct {
	// Upstream is the one GOPROXY fetched from ("https://proxy.golang.org").
	Upstream string
	// SumDB is the checksum database, parsed (ParseSumDB).
	SumDB SumDB
	// Private are GOPRIVATE-style patterns served from Upstream without
	// checksum verification and never cached.
	Private []string
	// Cache is the shared artifact store.
	Cache *Cache
	// SumDir holds conductor's verified copy of the checksum database (its
	// latest signed tree head, tiles and records) — outside Cache, so
	// eviction never drops the tree head that pins the database's history.
	SumDir string
	// Fetcher is the way out (NewFetcher over Upstream and SumDB.URL).
	Fetcher *Fetcher
	// OnSecurity is told of a checksum database inconsistency (nil: none).
	OnSecurity func(msg string)
}

// Go serves the GOPROXY protocol (https://go.dev/ref/mod#goproxy-protocol)
// and the checksum-database proxy endpoints under it. A .mod or .zip is
// verified against the checksum database before it is cached or served, so
// what an agent gets — and what the cache holds — is exactly what every
// other Go user gets for that module version.
type Go struct {
	cfg      GoConfig
	upstream string
	private  string
	sum      *sumdb.Client
	ops      *sumOps
}

// Size limits on what is read from upstream.
const (
	maxMetaBytes = 1 << 20
	maxModBytes  = 16 << 20
	maxZipBytes  = modzip.MaxZipFile
)

// fillTimeout bounds one upstream fetch into the cache (a toolchain zip is
// large); it is not tied to the asking agent's request, since others may be
// waiting on the same fill.
const fillTimeout = 15 * time.Minute

// NewGo builds the Go ecosystem.
func NewGo(cfg GoConfig) (*Go, error) {
	if cfg.Cache == nil || cfg.Fetcher == nil || !filepath.IsAbs(cfg.SumDir) {
		return nil, errors.New("pkgproxy: go: cache, checksum database dir and fetcher are required")
	}
	if cfg.SumDB.Name == "" || cfg.SumDB.Key == "" {
		return nil, errors.New("pkgproxy: go: a checksum database is required")
	}
	up := strings.TrimRight(cfg.Upstream, "/")
	if !strings.HasPrefix(up, "https://") && !strings.HasPrefix(up, "http://") {
		return nil, fmt.Errorf("pkgproxy: go: upstream %q is not an http(s) URL", cfg.Upstream)
	}
	ops := &sumOps{db: cfg.SumDB, dir: filepath.Join(cfg.SumDir, cfg.SumDB.Name), fetch: cfg.Fetcher, onSec: cfg.OnSecurity}
	g := &Go{cfg: cfg, upstream: up, private: strings.Join(cfg.Private, ","), ops: ops}
	g.sum = sumdb.NewClient(ops)
	return g, nil
}

// Name is the ecosystem's URL segment.
func (g *Go) Name() string { return "go" }

// AgentEnv points the agent's go command at base: modules and the checksum
// database both through conductor, never direct, verification on. A
// private pattern skips only the checksum database, never the proxy.
func (g *Go) AgentEnv(base string) []string {
	return []string{
		"GOPROXY=" + base,
		"GOSUMDB=" + g.cfg.SumDB.GoEnv,
		"GONOSUMDB=" + g.private,
		"GOPRIVATE=", "GONOPROXY=", "GOINSECURE=", "GONOSUMCHECK=", "GONOVERIFY=",
	}
}

// goReq is one parsed GOPROXY request.
type goReq struct {
	mod     string // unescaped module path
	escMod  string
	ver     string // unescaped version ("" for list / latest)
	escVer  string
	file    string // list | latest | info | mod | zip
	query   bool   // a .info for a non-canonical query ("master")
	private bool
}

// queryVersion is what a non-canonical .info query may look like: a branch
// or tag name, no separators.
var queryVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

// canonical reports v is a version a .mod or .zip can name.
func canonical(v string) bool {
	if !semver.IsValid(v) {
		return false
	}
	c := semver.Canonical(v)
	return c == v || (semver.Build(v) == "+incompatible" && c+"+incompatible" == v)
}

// parseGoPath validates rest ("<escaped module>/@v/<escaped version>.zip",
// "…/@v/list", "…/@latest") against the module grammar. The module path
// and version are unescaped, checked, and must re-escape to exactly what was
// asked, so one module has one spelling — one upstream URL, one cache key.
func parseGoPath(rest string) (*goReq, error) {
	var q goReq
	switch {
	case strings.HasSuffix(rest, "/@latest"):
		q.escMod, q.file = strings.TrimSuffix(rest, "/@latest"), "latest"
	default:
		i := strings.LastIndex(rest, "/@v/")
		if i < 0 {
			return nil, errors.New("not a module proxy path")
		}
		q.escMod = rest[:i]
		f := rest[i+len("/@v/"):]
		switch {
		case f == "list":
			q.file = "list"
		case strings.HasSuffix(f, ".info"):
			q.escVer, q.file = strings.TrimSuffix(f, ".info"), "info"
		case strings.HasSuffix(f, ".mod"):
			q.escVer, q.file = strings.TrimSuffix(f, ".mod"), "mod"
		case strings.HasSuffix(f, ".zip"):
			q.escVer, q.file = strings.TrimSuffix(f, ".zip"), "zip"
		default:
			return nil, fmt.Errorf("unknown module file %q", f)
		}
	}
	mod, err := module.UnescapePath(q.escMod)
	if err != nil {
		return nil, err
	}
	if err := module.CheckPath(mod); err != nil {
		return nil, err
	}
	if e, err := module.EscapePath(mod); err != nil || e != q.escMod {
		return nil, fmt.Errorf("module path %q is not canonically escaped", q.escMod)
	}
	q.mod = mod
	if q.file == "list" || q.file == "latest" {
		return &q, nil
	}
	v, err := module.UnescapeVersion(q.escVer)
	if err != nil {
		return nil, err
	}
	if e, err := module.EscapeVersion(v); err != nil || e != q.escVer {
		return nil, fmt.Errorf("version %q is not canonically escaped", q.escVer)
	}
	q.ver = v
	if canonical(v) {
		if err := module.Check(mod, v); err != nil {
			return nil, err
		}
		return &q, nil
	}
	if q.file == "info" && queryVersion.MatchString(v) {
		q.query = true
		return &q, nil
	}
	return nil, fmt.Errorf("version %q is not a canonical semantic version", v)
}

// upstreamURL builds the upstream address from validated parts only.
func (g *Go) upstreamURL(q *goReq) string {
	base := g.upstream + "/" + q.escMod
	switch q.file {
	case "list":
		return base + "/@v/list"
	case "latest":
		return base + "/@latest"
	}
	return base + "/@v/" + q.escVer + "." + q.file
}

// cacheKey is where a canonical artifact lives in the cache.
func (q *goReq) cacheKey() string {
	return "go/" + q.escMod + "/@v/" + q.escVer + "." + q.file
}

// Serve answers one Go request.
func (g *Go) Serve(w http.ResponseWriter, r *http.Request, c *Call) {
	rest, emit := c.Rest, c.Emit
	if s, ok := strings.CutPrefix(rest, "sumdb/"); ok {
		g.serveSumDB(w, r, s, emit)
		return
	}
	q, err := parseGoPath(rest)
	if err != nil {
		emit(Event{Artifact: "request", Status: http.StatusBadRequest, Err: err.Error()})
		http.Error(w, "conductor package proxy: "+err.Error(), http.StatusBadRequest)
		return
	}
	q.private = g.private != "" && module.MatchPrefixPatterns(g.private, q.mod)
	ev := Event{Module: q.mod, Version: q.ver, Artifact: q.file}
	switch {
	case q.file == "list" || q.file == "latest" || q.query || q.private:
		// Mutable answers (and private modules) are never cached: passed
		// through from upstream, bounded.
		ev.Upstream = true
		ev.Status, ev.Err = g.passthrough(w, r.Context(), q)
	default:
		ev.Upstream, ev.Status, ev.Err = g.serveCached(w, r, q)
	}
	if ev.Upstream || ev.Err != "" || q.file == "zip" {
		emit(ev)
	}
}

// fail answers an error the agent's go command prints verbatim (it shows a
// proxy's response text), so it says what happened and that nothing will be
// fetched any other way.
func fail(w http.ResponseWriter, status int, msg string) (int, string) {
	http.Error(w, "conductor package proxy: "+msg, status)
	return status, msg
}

func (g *Go) notFound(w http.ResponseWriter, q *goReq, upstreamMsg string) (int, string) {
	msg := fmt.Sprintf("%s not found at %s", q.mod, g.upstream)
	if upstreamMsg != "" {
		msg += ": " + upstreamMsg
	}
	msg += " — conductor fetches public modules from its configured upstream only (never direct); a private module needs sandbox.packages.go.private with an upstream that serves it"
	return fail(w, http.StatusNotFound, msg)
}

// passthrough relays an uncached answer.
func (g *Go) passthrough(w http.ResponseWriter, ctx context.Context, q *goReq) (int, string) {
	limit := int64(maxMetaBytes)
	switch q.file {
	case "mod":
		limit = maxModBytes
	case "zip":
		limit = maxZipBytes
	}
	ctx, cancel := context.WithTimeout(ctx, fillTimeout)
	defer cancel()
	resp, err := g.cfg.Fetcher.Get(ctx, g.upstreamURL(q))
	if err != nil {
		return fail(w, http.StatusBadGateway, fmt.Sprintf("upstream %s unreachable: %v", g.upstream, err))
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return g.notFound(w, q, strings.TrimSpace(string(b)))
	}
	if resp.StatusCode != http.StatusOK {
		return fail(w, http.StatusBadGateway, fmt.Sprintf("upstream %s answered %s", g.upstream, resp.Status))
	}
	if q.file == "zip" {
		w.Header().Set("Content-Type", "application/zip")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return http.StatusOK, "copy: " + err.Error()
	}
	if n > limit {
		return http.StatusOK, "upstream answer exceeded the size limit (truncated)"
	}
	return http.StatusOK, ""
}

// errUpstreamMissing carries an upstream 404/410 out of a cache fill.
type errUpstreamMissing struct{ msg string }

func (e errUpstreamMissing) Error() string { return "not found upstream: " + e.msg }

// serveCached serves a canonical .info/.mod/.zip from the cache, filling it
// from upstream (verified) on a miss.
func (g *Go) serveCached(w http.ResponseWriter, r *http.Request, q *goReq) (fetched bool, status int, msg string) {
	key := q.cacheKey()
	fetched, err := g.cfg.Cache.Fill(key, func(tmp *os.File) error { return g.fetchVerified(q, tmp) })
	if err != nil {
		var nf errUpstreamMissing
		switch {
		case errors.As(err, &nf):
			status, msg = g.notFound(w, q, nf.msg)
		case strings.Contains(err.Error(), errSumNotFound):
			status, msg = fail(w, http.StatusNotFound, fmt.Sprintf("%s@%s is not in the checksum database %s — conductor serves only checksum-verified modules; a private module needs sandbox.packages.go.private with an upstream that serves it (%v)", q.mod, q.ver, g.cfg.SumDB.Name, err))
		case errors.Is(err, errChecksum):
			status, msg = fail(w, http.StatusBadGateway, "SECURITY: "+err.Error())
		default:
			status, msg = fail(w, http.StatusBadGateway, err.Error())
		}
		return fetched, status, msg
	}
	f, err := g.cfg.Cache.Open(key)
	if err != nil {
		// Evicted between fill and open: rare; the client retries.
		status, msg = fail(w, http.StatusServiceUnavailable, "cache: "+err.Error())
		return fetched, status, msg
	}
	defer f.Close()
	switch q.file {
	case "zip":
		w.Header().Set("Content-Type", "application/zip")
	case "info":
		w.Header().Set("Content-Type", "application/json")
	default:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	fi, _ := f.Stat()
	var mod time.Time
	if fi != nil {
		mod = fi.ModTime()
	}
	http.ServeContent(w, r, "", mod, f)
	return fetched, http.StatusOK, ""
}

// errChecksum: upstream content does not match the checksum database.
var errChecksum = errors.New("checksum mismatch")

// fetchVerified downloads one artifact into tmp and verifies it: a .mod and
// a .zip against the checksum database's go.sum lines; a .info must at least
// name the version asked for.
func (g *Go) fetchVerified(q *goReq, tmp *os.File) error {
	ctx, cancel := context.WithTimeout(context.Background(), fillTimeout)
	defer cancel()
	limit := int64(maxMetaBytes)
	switch q.file {
	case "mod":
		limit = maxModBytes
	case "zip":
		limit = maxZipBytes
	}
	resp, err := g.cfg.Fetcher.Get(ctx, g.upstreamURL(q))
	if err != nil {
		return fmt.Errorf("upstream %s unreachable: %w", g.upstream, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return errUpstreamMissing{msg: strings.TrimSpace(string(b))}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream %s answered %s", g.upstream, resp.Status)
	}
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("download %s@%s.%s: %w", q.mod, q.ver, q.file, err)
	}
	if n > limit {
		return fmt.Errorf("%s@%s.%s is larger than %d bytes", q.mod, q.ver, q.file, limit)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	switch q.file {
	case "info":
		b, err := os.ReadFile(tmp.Name())
		if err != nil {
			return err
		}
		if !bytes.Contains(b, []byte(`"`+q.ver+`"`)) {
			return fmt.Errorf("upstream .info for %s@%s does not name that version", q.mod, q.ver)
		}
		return nil
	case "mod":
		b, err := os.ReadFile(tmp.Name())
		if err != nil {
			return err
		}
		h, err := dirhash.Hash1([]string{"go.mod"}, func(string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(b)), nil
		})
		if err != nil {
			return err
		}
		return g.verify(q.mod, q.ver+"/go.mod", h)
	case "zip":
		h, err := dirhash.HashZip(tmp.Name(), dirhash.Hash1)
		if err != nil {
			return fmt.Errorf("%s@%s: not a module zip: %w", q.mod, q.ver, err)
		}
		return g.verify(q.mod, q.ver, h)
	}
	return nil
}

// verify checks hash against the checksum database's line for mod vers.
func (g *Go) verify(mod, vers, hash string) error {
	lines, err := g.sum.Lookup(mod, vers)
	if err != nil {
		return fmt.Errorf("checksum database %s: %w", g.cfg.SumDB.Name, err)
	}
	want := mod + " " + vers + " " + hash
	for _, l := range lines {
		if l == want {
			return nil
		}
	}
	if len(lines) == 0 {
		return fmt.Errorf("checksum database %s: %s %s: %s", g.cfg.SumDB.Name, mod, vers, errSumNotFound)
	}
	return fmt.Errorf("%w: %s %s from %s hashes to %s, the checksum database %s says %q — not served, not cached",
		errChecksum, mod, vers, g.upstream, hash, g.cfg.SumDB.Name, lines)
}

// lookupPath is a checksum database lookup: "lookup/<escaped module>@<escaped version>".
func checkLookup(s string) bool {
	escMod, escVer, ok := strings.Cut(s, "@")
	if !ok {
		return false
	}
	mod, err := module.UnescapePath(escMod)
	if err != nil || module.CheckPath(mod) != nil {
		return false
	}
	if e, _ := module.EscapePath(mod); e != escMod {
		return false
	}
	v, err := module.UnescapeVersion(escVer)
	if err != nil || !canonical(v) {
		return false
	}
	e, _ := module.EscapeVersion(v)
	return e == escVer
}

// serveSumDB proxies the checksum database for the agent's own go command
// (which verifies every answer against the database's key itself):
// "<name>/supported", "<name>/latest", "<name>/lookup/…", "<name>/tile/…".
// Only the configured database is served, at its configured URL; tiles and
// records conductor already verified are answered from its own copy.
func (g *Go) serveSumDB(w http.ResponseWriter, r *http.Request, s string, emit func(Event)) {
	name, p, _ := strings.Cut(s, "/")
	if name != g.cfg.SumDB.Name {
		fail(w, http.StatusNotFound, fmt.Sprintf("checksum database %q is not proxied here (only %s)", name, g.cfg.SumDB.Name))
		return
	}
	switch {
	case p == "supported":
		w.WriteHeader(http.StatusOK)
		return
	case p == "latest":
	case strings.HasPrefix(p, "lookup/") && checkLookup(strings.TrimPrefix(p, "lookup/")):
	case strings.HasPrefix(p, "tile/"):
		if _, err := tlog.ParseTilePath(p); err != nil {
			fail(w, http.StatusBadRequest, "bad tile path")
			return
		}
	default:
		emit(Event{Artifact: "sumdb", Status: http.StatusBadRequest, Err: "bad checksum database path"})
		fail(w, http.StatusBadRequest, "bad checksum database path")
		return
	}
	if p != "latest" {
		if b, err := g.ops.ReadCache(name + "/" + p); err == nil {
			_, _ = w.Write(b)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	resp, err := g.cfg.Fetcher.Get(ctx, g.cfg.SumDB.URL+"/"+p)
	if err != nil {
		fail(w, http.StatusBadGateway, fmt.Sprintf("checksum database %s unreachable: %v", name, err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		code := http.StatusBadGateway
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			code = http.StatusNotFound
		}
		fail(w, code, fmt.Sprintf("checksum database %s: %s %s", name, resp.Status, strings.TrimSpace(string(b))))
		return
	}
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 4<<20))
}
