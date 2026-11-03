package pkgproxy

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"regexp"
	"strings"
)

// CRAN serves a CRAN-like repository (install.packages, renv, pak). Index
// files (PACKAGES*) are relayed unchanged; a package archive is cached by
// URL — CRAN publishes only an MD5 (checked when the index lists one, as a
// corruption guard, not as a content address), so TLS to the fixed
// upstream is the guarantee.
type CRAN struct{ *registry }

// DefaultCRANUpstream is the CRAN cloud mirror.
const DefaultCRANUpstream = "https://cloud.r-project.org"

// NewCRAN builds the CRAN ecosystem.
func NewCRAN(cfg RegistryConfig) (*CRAN, error) {
	r, err := newRegistry("cran", DefaultCRANUpstream, cfg)
	if err != nil {
		return nil, err
	}
	return &CRAN{r}, nil
}

// Name is the URL segment.
func (c *CRAN) Name() string { return "cran" }

// AgentEnv overrides renv's repositories (it ignores ~/.Rprofile when a
// project has one).
func (c *CRAN) AgentEnv(base string) []string {
	return []string{"CONDUCTOR_CRAN_URL=" + base, "RENV_CONFIG_REPOS_OVERRIDE=" + base, "RENV_CONFIG_PAK_ENABLED=FALSE"}
}

// HomeFiles sets the CRAN repository for R sessions without a project
// .Rprofile.
func (c *CRAN) HomeFiles(base string) map[string]string {
	return map[string]string{".Rprofile": "# Written by conductor: packages come through its package proxy.\n" +
		"local(options(repos = c(CRAN = Sys.getenv(\"CONDUCTOR_CRAN_URL\", \"" + base + "\"))))\n"}
}

var (
	cranSeg  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	cranFile = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9.]{0,99})_([0-9][0-9.-]{0,40})\.(tar\.gz|tgz|zip)$`)
)

const cranMetaMax = 128 << 20

// Serve routes src/contrib/… and bin/<platform>/contrib/<r-version>/….
func (c *CRAN) Serve(w http.ResponseWriter, r *http.Request, call *Call) {
	segs := strings.Split(call.Rest, "/")
	okPath := len(segs) >= 3 && ((segs[0] == "src" && segs[1] == "contrib") || segs[0] == "bin")
	for _, s := range segs {
		if !cranSeg.MatchString(s) || strings.Contains(s, "..") {
			okPath = false
		}
	}
	if !okPath {
		st, m := c.fail(w, http.StatusBadRequest, "not a src/contrib or bin path")
		call.Emit(Event{Artifact: "request", Status: st, Err: m})
		return
	}
	file := segs[len(segs)-1]
	u := c.upstream + "/" + call.Rest
	if strings.HasPrefix(file, "PACKAGES") {
		st, msg := c.passthrough(w, r.Context(), u, "", call.Rest, cranMetaMax, "")
		if msg != "" {
			call.Emit(Event{Module: call.Rest, Artifact: "metadata", Upstream: true, Status: st, Err: msg})
		}
		return
	}
	m := cranFile.FindStringSubmatch(file)
	if m == nil {
		st, msg := c.fail(w, http.StatusBadRequest, "not a package archive")
		call.Emit(Event{Artifact: "request", Status: st, Err: msg})
		return
	}
	a := artifact{Name: m[1], Version: m[2], URL: u}
	dir := c.upstream + "/" + strings.Join(segs[:len(segs)-1], "/")
	if d, ok := c.md5(r.Context(), dir+"/PACKAGES", m[1], m[2]); ok {
		a.Digest = d
	}
	c.serveArtifact(w, r, call, a, c.isPrivate(m[1]))
}

// md5 reads name@ver's MD5sum from a PACKAGES index (archived versions are
// not listed: those are cached by URL unchecked).
func (c *CRAN) md5(ctx context.Context, index, name, ver string) (Digest, bool) {
	b, err := c.meta.get(ctx, c.fetch, index, "", cranMetaMax)
	if err != nil {
		return Digest{}, false
	}
	var pkg, v, sum string
	flush := func() bool { return pkg == name && v == ver && sum != "" }
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			if flush() {
				break
			}
			pkg, v, sum = "", "", ""
			continue
		}
		k, val, _ := strings.Cut(line, ":")
		switch k {
		case "Package":
			pkg = strings.TrimSpace(val)
		case "Version":
			v = strings.TrimSpace(val)
		case "MD5sum":
			sum = strings.TrimSpace(val)
		}
	}
	if !flush() {
		return Digest{}, false
	}
	d, err := HexDigest("md5", sum)
	return d, err == nil
}
