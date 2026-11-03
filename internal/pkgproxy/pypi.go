package pkgproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// PyPI serves the Python package index: the simple API (PEP 503 HTML and
// PEP 691 JSON — pip, uv, pdm, pipenv) and the JSON API (/pypi/<p>/json).
// Upstream metadata is always read as PEP 691 JSON; every file URL is
// rewritten to point back here, and a file request is resolved against the
// upstream's own listing — its URL (on the registry's file host) and its
// published sha256 — and verified before it is cached.
type PyPI struct{ *registry }

// DefaultPyPIUpstream is the public index.
const DefaultPyPIUpstream = "https://pypi.org"

// NewPyPI builds the PyPI ecosystem.
func NewPyPI(cfg RegistryConfig) (*PyPI, error) {
	r, err := newRegistry("pypi", DefaultPyPIUpstream, cfg)
	if err != nil {
		return nil, err
	}
	return &PyPI{r}, nil
}

// Name is the URL segment.
func (p *PyPI) Name() string { return "pypi" }

// AgentEnv points pip, uv, pdm and pipenv at base's simple index, with no
// extra index and no interpreter downloads.
func (p *PyPI) AgentEnv(base string) []string {
	idx := base + "/simple/"
	return []string{
		"PIP_INDEX_URL=" + idx, "PIP_DISABLE_PIP_VERSION_CHECK=1",
		"UV_DEFAULT_INDEX=" + idx, "UV_INDEX_URL=" + idx, "UV_PYTHON_DOWNLOADS=never",
		"PDM_PYPI_URL=" + idx, "PIPENV_PYPI_MIRROR=" + idx,
	}
}

// pyName is a project name (PEP 508).
var pyName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]{0,200}[A-Za-z0-9])?$`)

// pyFile is a distribution file name: no separators.
var pyFile = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+!-]{0,250}$`)

var pySep = regexp.MustCompile(`[-_.]+`)

// pyNormalize is PEP 503 normalization.
func pyNormalize(s string) string { return strings.ToLower(pySep.ReplaceAllString(s, "-")) }

func validPyFile(f string) bool {
	if !pyFile.MatchString(f) || strings.Contains(f, "..") {
		return false
	}
	for _, ext := range []string{".whl", ".tar.gz", ".zip", ".tar.bz2", ".tgz", ".egg"} {
		if strings.HasSuffix(f, ext) {
			return true
		}
	}
	return false
}

const (
	pySimpleJSON = "application/vnd.pypi.simple.v1+json"
	pyMetaMax    = 64 << 20
)

// Serve routes simple/<project>/, files/<project>/<file> and
// pypi/<project>[/<version>]/json.
func (p *PyPI) Serve(w http.ResponseWriter, r *http.Request, c *Call) {
	parts := strings.Split(strings.TrimSuffix(c.Rest, "/"), "/")
	bad := func(msg string) {
		st, m := p.fail(w, http.StatusBadRequest, msg)
		c.Emit(Event{Artifact: "request", Status: st, Err: m})
	}
	switch {
	case len(parts) == 2 && parts[0] == "simple":
		if !pyName.MatchString(parts[1]) {
			bad("bad project name")
			return
		}
		p.serveSimple(w, r, c, pyNormalize(parts[1]))
	case len(parts) == 3 && parts[0] == "files":
		if !pyName.MatchString(parts[1]) || !validPyFile(parts[2]) {
			bad("bad project or file name")
			return
		}
		proj := pyNormalize(parts[1])
		a, err := p.resolve(r.Context(), proj, parts[2])
		if err != nil {
			var st int
			var msg string
			if nf, ok := err.(errNotFound); ok {
				st, msg = p.notFound(w, proj+" "+parts[2], nf.msg)
			} else {
				st, msg = p.fail(w, http.StatusBadGateway, err.Error())
			}
			c.Emit(Event{Module: proj, Artifact: "artifact", Status: st, Err: msg})
			return
		}
		p.serveArtifact(w, r, c, a, p.isPrivate(proj))
	case (len(parts) == 3 || len(parts) == 4) && parts[0] == "pypi" && parts[len(parts)-1] == "json":
		if !pyName.MatchString(parts[1]) || (len(parts) == 4 && !pyFile.MatchString(parts[2])) {
			bad("bad project or version")
			return
		}
		p.serveJSONAPI(w, r, c, parts[1:len(parts)-1])
	default:
		bad("not a simple-index, file or JSON API path")
	}
}

// pySimple is a PEP 691 project page.
type pySimple struct {
	Meta     map[string]any   `json:"meta"`
	Name     string           `json:"name"`
	Files    []map[string]any `json:"files"`
	Versions []string         `json:"versions,omitempty"`
}

func (p *PyPI) simpleURL(proj string) string { return p.upstream + "/simple/" + proj + "/" }

func (p *PyPI) serveSimple(w http.ResponseWriter, r *http.Request, c *Call, proj string) {
	ev := Event{Module: proj, Artifact: "metadata", Upstream: true}
	b, st, msg, ok := p.metadata(w, r.Context(), p.simpleURL(proj), pySimpleJSON, proj, pyMetaMax)
	if !ok {
		ev.Status, ev.Err = st, msg
		c.Emit(ev)
		return
	}
	var page pySimple
	if err := json.Unmarshal(b, &page); err != nil {
		ev.Status, ev.Err = p.fail(w, http.StatusBadGateway, "simple index: "+err.Error())
		c.Emit(ev)
		return
	}
	var files []map[string]any
	for _, f := range page.Files {
		name, _ := f["filename"].(string)
		if !validPyFile(name) {
			continue
		}
		f["url"] = c.Base + "/files/" + proj + "/" + name
		// Separate metadata files are not proxied: clients read a wheel's
		// own (uv with a range request).
		delete(f, "core-metadata")
		delete(f, "dist-info-metadata")
		delete(f, "data-dist-info-metadata")
		files = append(files, f)
	}
	page.Files = files
	if strings.Contains(r.Header.Get("Accept"), pySimpleJSON) {
		w.Header().Set("Content-Type", pySimpleJSON)
		_ = json.NewEncoder(w).Encode(page)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var out bytes.Buffer
	out.WriteString("<!DOCTYPE html>\n<html><head><meta name=\"pypi:repository-version\" content=\"1.0\"></head><body>\n")
	for _, f := range files {
		name, _ := f["filename"].(string)
		href := f["url"].(string)
		if h, ok := f["hashes"].(map[string]any); ok {
			if s, ok := h["sha256"].(string); ok {
				href += "#sha256=" + s
			}
		}
		out.WriteString(`<a href="` + html.EscapeString(href) + `"`)
		if rp, ok := f["requires-python"].(string); ok && rp != "" {
			out.WriteString(` data-requires-python="` + html.EscapeString(rp) + `"`)
		}
		switch y := f["yanked"].(type) {
		case bool:
			if y {
				out.WriteString(` data-yanked=""`)
			}
		case string:
			out.WriteString(` data-yanked="` + html.EscapeString(y) + `"`)
		}
		out.WriteString(">" + html.EscapeString(name) + "</a><br>\n")
	}
	out.WriteString("</body></html>\n")
	_, _ = w.Write(out.Bytes())
}

// resolve finds file in proj's upstream listing: its URL and sha256.
func (p *PyPI) resolve(ctx context.Context, proj, file string) (artifact, error) {
	b, err := p.meta.get(ctx, p.fetch, p.simpleURL(proj), pySimpleJSON, pyMetaMax)
	if err != nil {
		return artifact{}, err
	}
	var page pySimple
	if err := json.Unmarshal(b, &page); err != nil {
		return artifact{}, fmt.Errorf("simple index for %s: %w", proj, err)
	}
	for _, f := range page.Files {
		if name, _ := f["filename"].(string); name != file {
			continue
		}
		u, _ := f["url"].(string)
		ref, err := url.Parse(p.simpleURL(proj))
		if err != nil {
			return artifact{}, err
		}
		abs, err := ref.Parse(u) // a listing's URL may be relative to it
		if err != nil {
			return artifact{}, fmt.Errorf("%s: bad file URL %q", file, u)
		}
		abs.Fragment = ""
		var d Digest
		if h, ok := f["hashes"].(map[string]any); ok {
			if s, ok := h["sha256"].(string); ok {
				if d, err = HexDigest("sha256", s); err != nil {
					return artifact{}, fmt.Errorf("%s: %w", file, err)
				}
			}
		}
		return artifact{Name: proj, Version: file, URL: abs.String(), Digest: d}, nil
	}
	return artifact{}, errNotFound{msg: proj + " has no file " + file}
}

// serveJSONAPI relays /pypi/<project>[/<version>]/json with every file URL
// pointing back here.
func (p *PyPI) serveJSONAPI(w http.ResponseWriter, r *http.Request, c *Call, parts []string) {
	proj := pyNormalize(parts[0])
	u := p.upstream + "/pypi/" + proj
	if len(parts) == 2 {
		u += "/" + url.PathEscape(parts[1])
	}
	u += "/json"
	ev := Event{Module: proj, Artifact: "metadata", Upstream: true}
	b, st, msg, ok := p.metadata(w, r.Context(), u, "application/json", proj, pyMetaMax)
	if !ok {
		ev.Status, ev.Err = st, msg
		c.Emit(ev)
		return
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		ev.Status, ev.Err = p.fail(w, http.StatusBadGateway, "JSON API: "+err.Error())
		c.Emit(ev)
		return
	}
	fix := func(list any) []any {
		files, _ := list.([]any)
		var out []any
		for _, x := range files {
			f, ok := x.(map[string]any)
			if !ok {
				continue
			}
			name, _ := f["filename"].(string)
			if !validPyFile(name) {
				continue
			}
			f["url"] = c.Base + "/files/" + proj + "/" + name
			out = append(out, f)
		}
		return out
	}
	if l, ok := doc["urls"]; ok {
		doc["urls"] = fix(l)
	}
	if rel, ok := doc["releases"].(map[string]any); ok {
		keys := make([]string, 0, len(rel))
		for k := range rel {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			rel[k] = fix(rel[k])
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}
