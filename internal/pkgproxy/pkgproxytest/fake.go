// Package pkgproxytest is a local Go module world for tests: a fake GOPROXY
// upstream serving in-memory modules and a real (in-memory) checksum
// database signing their hashes — no internet.
package pkgproxytest

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/sumdb"
	"golang.org/x/mod/sumdb/dirhash"
	"golang.org/x/mod/sumdb/note"
	modzip "golang.org/x/mod/zip"
)

// Module is one module version the fake upstream serves.
type Module struct {
	Path, Version string
	Files         map[string]string // name → content; must include go.mod
}

// World is a fake upstream plus its checksum database.
type World struct {
	Upstream *httptest.Server
	SumDB    *httptest.Server
	// GOSUMDB is the checksum database in GOSUMDB form ("name+key url").
	GOSUMDB string
	// Name is the checksum database's name, VKey its "name+key".
	Name, VKey string

	mu      sync.Mutex
	mods    map[string]*built // path@version
	sums    map[string][]byte // path@version → the go.sum lines the database signs
	hits    map[string]int    // upstream request path → count
	tamper  map[string][]byte // upstream path → served instead (poisoning)
	redir   map[string]string // upstream path → redirect location
	Delay   time.Duration
	reqPath []string
}

type built struct {
	mod, zip []byte
	info     string
}

// zf is one in-memory file of a module zip.
type zf struct {
	name string
	data []byte
}

func (f zf) Path() string                 { return f.name }
func (f zf) Lstat() (os.FileInfo, error)  { return zfInfo(f), nil }
func (f zf) Open() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(f.data)), nil }

type zfInfo zf

func (i zfInfo) Name() string       { return path.Base(i.name) }
func (i zfInfo) Size() int64        { return int64(len(i.data)) }
func (i zfInfo) Mode() os.FileMode  { return 0o644 }
func (i zfInfo) ModTime() time.Time { return time.Time{} }
func (i zfInfo) IsDir() bool        { return false }
func (i zfInfo) Sys() any           { return nil }

func writeTemp(t testing.TB, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "m.zip")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// New starts the world with mods. The checksum database knows every module
// in mods; Unlisted modules are served by the upstream but unknown to it.
func New(t testing.TB, mods []Module, unlisted ...Module) *World {
	t.Helper()
	w := &World{mods: map[string]*built{}, sums: map[string][]byte{}, hits: map[string]int{}, tamper: map[string][]byte{}, redir: map[string]string{}}
	for _, m := range mods {
		w.add(t, m, true)
	}
	for _, m := range unlisted {
		w.add(t, m, false)
	}
	w.Name = "sum.test"
	skey, vkey, err := note.GenerateKey(rand.Reader, w.Name)
	if err != nil {
		t.Fatal(err)
	}
	w.VKey = vkey
	ts := sumdb.NewTestServer(skey, func(path, vers string) ([]byte, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if b, ok := w.sums[path+"@"+vers]; ok {
			return b, nil
		}
		return nil, fmt.Errorf("unknown module %s@%s", path, vers)
	})
	srv := sumdb.NewServer(ts)
	w.SumDB = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		w.hits["sumdb:"+r.URL.Path]++
		w.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/lookup/") {
			esc := strings.TrimPrefix(r.URL.Path, "/lookup/")
			ep, ev, _ := strings.Cut(esc, "@")
			p, _ := module.UnescapePath(ep)
			v, _ := module.UnescapeVersion(ev)
			w.mu.Lock()
			_, ok := w.sums[p+"@"+v]
			w.mu.Unlock()
			if !ok {
				http.Error(rw, "not found: "+p+"@"+v, http.StatusNotFound)
				return
			}
		}
		srv.ServeHTTP(rw, r)
	}))
	t.Cleanup(w.SumDB.Close)
	w.GOSUMDB = vkey + " " + w.SumDB.URL
	w.Upstream = httptest.NewServer(http.HandlerFunc(w.serve))
	t.Cleanup(w.Upstream.Close)
	return w
}

// BuildZip is m's module zip.
func BuildZip(t testing.TB, m Module) []byte {
	t.Helper()
	var files []modzip.File
	names := make([]string, 0, len(m.Files))
	for n := range m.Files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		files = append(files, zf{name: n, data: []byte(m.Files[n])})
	}
	var buf bytes.Buffer
	if err := modzip.Create(&buf, module.Version{Path: m.Path, Version: m.Version}, files); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func (w *World) add(t testing.TB, m Module, listed bool) {
	t.Helper()
	b := &built{mod: []byte(m.Files["go.mod"]), zip: BuildZip(t, m), info: fmt.Sprintf(`{"Version":%q,"Time":"2026-01-01T00:00:00Z"}`, m.Version)}
	w.mods[m.Path+"@"+m.Version] = b
	if !listed {
		return
	}
	zh, err := dirhash.HashZip(writeTemp(t, b.zip), dirhash.Hash1)
	if err != nil {
		t.Fatal(err)
	}
	mh, err := dirhash.Hash1([]string{"go.mod"}, func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b.mod)), nil })
	if err != nil {
		t.Fatal(err)
	}
	w.sums[m.Path+"@"+m.Version] = []byte(fmt.Sprintf("%s %s %s\n%s %s/go.mod %s\n", m.Path, m.Version, zh, m.Path, m.Version, mh))
}

func (w *World) serve(rw http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	w.mu.Lock()
	w.hits[p]++
	w.reqPath = append(w.reqPath, p)
	tam, tampered := w.tamper[p]
	loc, redirected := w.redir[p]
	delay := w.Delay
	w.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	if redirected {
		http.Redirect(rw, r, loc, http.StatusFound)
		return
	}
	if tampered {
		rw.Write(tam)
		return
	}
	rest := strings.TrimPrefix(p, "/")
	var escMod, f string
	if strings.HasSuffix(rest, "/@latest") {
		escMod, f = strings.TrimSuffix(rest, "/@latest"), "@latest"
	} else if i := strings.LastIndex(rest, "/@v/"); i >= 0 {
		escMod, f = rest[:i], rest[i+4:]
	} else {
		http.NotFound(rw, r)
		return
	}
	mod, err := module.UnescapePath(escMod)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var vers []string
	for k := range w.mods {
		if pth, v, _ := strings.Cut(k, "@"); pth == mod {
			vers = append(vers, v)
		}
	}
	sort.Strings(vers)
	switch {
	case f == "list":
		if len(vers) == 0 {
			http.Error(rw, "not found: "+mod, http.StatusGone)
			return
		}
		fmt.Fprintln(rw, strings.Join(vers, "\n"))
		return
	case f == "@latest":
		if len(vers) == 0 {
			http.Error(rw, "not found: "+mod, http.StatusGone)
			return
		}
		fmt.Fprint(rw, w.mods[mod+"@"+vers[len(vers)-1]].info)
		return
	}
	dot := strings.LastIndex(f, ".")
	if dot < 0 {
		http.NotFound(rw, r)
		return
	}
	v, _ := module.UnescapeVersion(f[:dot])
	b, ok := w.mods[mod+"@"+v]
	if !ok {
		http.Error(rw, "not found: "+mod+"@"+v, http.StatusNotFound)
		return
	}
	switch f[dot:] {
	case ".info":
		fmt.Fprint(rw, b.info)
	case ".mod":
		rw.Write(b.mod)
	case ".zip":
		rw.Write(b.zip)
	default:
		http.NotFound(rw, r)
	}
}

// Hits is how often the upstream served path ("/example.com/m/@v/v1.0.0.zip").
func (w *World) Hits(path string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hits[path]
}

// SumDBRequests is how many requests the checksum database got.
func (w *World) SumDBRequests() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for k, c := range w.hits {
		if strings.HasPrefix(k, "sumdb:") {
			n += c
		}
	}
	return n
}

// Requests are every path the upstream was asked for, in order.
func (w *World) Requests() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.reqPath...)
}

// Tamper makes the upstream serve body for path instead of the real thing.
func (w *World) Tamper(path string, body []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if body == nil {
		delete(w.tamper, path)
		return
	}
	w.tamper[path] = body
}

// Redirect makes the upstream answer path with a 302 to loc.
func (w *World) Redirect(path, loc string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.redir[path] = loc
}

// Zip is a module version's zip as the upstream serves it.
func (w *World) Zip(path, version string) []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.mods[path+"@"+version].zip
}
