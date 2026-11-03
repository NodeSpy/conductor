package pkgproxy

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
)

// Maven serves a Maven repository layout (Maven, Gradle, sbt, …). Mutable
// files (maven-metadata.xml) and checksum/signature sidecars are relayed;
// an artifact (.jar, .pom, .module, …) is verified against the upstream's
// own .sha256 (else .sha1) sidecar before it is cached. A sidecar comes
// from the same upstream as the file, so it guards against corruption, not
// against that upstream — the TLS-pinned upstream is the trust anchor.
type Maven struct{ *registry }

// DefaultMavenUpstream is Maven Central.
const DefaultMavenUpstream = "https://repo.maven.apache.org/maven2"

// NewMaven builds the Maven ecosystem.
func NewMaven(cfg RegistryConfig) (*Maven, error) {
	r, err := newRegistry("maven", DefaultMavenUpstream, cfg)
	if err != nil {
		return nil, err
	}
	return &Maven{r}, nil
}

// Name is the URL segment.
func (m *Maven) Name() string { return "maven" }

// AgentEnv carries this launch's URL to the HOME files, which read it, and
// points Maven and Gradle at the identity HOME holding them: a JVM's
// user.home comes from the password database, not $HOME.
func (m *Maven) AgentEnv(base string) []string {
	return []string{"CONDUCTOR_MAVEN_URL=" + base, "GRADLE_USER_HOME=${HOME}/.gradle", "MAVEN_OPTS=-Duser.home=${HOME}"}
}

// HomeFiles mirrors every Maven repository to the proxy (settings.xml),
// and rewrites every Maven repository a Gradle build or its settings
// declare (an init script).
func (m *Maven) HomeFiles(base string) map[string]string {
	return map[string]string{
		".m2/settings.xml": `<?xml version="1.0" encoding="UTF-8"?>
<!-- Written by conductor: every repository comes through its package proxy. -->
<settings>
  <mirrors>
    <mirror>
      <id>conductor</id>
      <mirrorOf>*</mirrorOf>
      <url>${env.CONDUCTOR_MAVEN_URL}</url>
    </mirror>
  </mirrors>
</settings>
`,
		".gradle/init.d/conductor.gradle": `// Written by conductor: every Maven repository comes through its package proxy.
def conductorRepo = System.getenv('CONDUCTOR_MAVEN_URL') ?: '` + base + `'
def conductorMirror = { repos ->
  repos.withType(MavenArtifactRepository).configureEach {
    url = conductorRepo
    allowInsecureProtocol = true
  }
}
beforeSettings { settings ->
  conductorMirror(settings.pluginManagement.repositories)
  try { conductorMirror(settings.dependencyResolutionManagement.repositories) } catch (ignored) {}
}
allprojects {
  conductorMirror(buildscript.repositories)
  conductorMirror(repositories)
}
`,
	}
}

// mavenSeg is one path segment of a repository path.
var mavenSeg = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+-]{0,200}$`)

var mavenSidecars = []string{".sha1", ".sha256", ".sha512", ".md5", ".asc"}

const mavenMetaMax = 16 << 20

// Serve answers one repository path.
func (m *Maven) Serve(w http.ResponseWriter, r *http.Request, c *Call) {
	segs := strings.Split(c.Rest, "/")
	for _, s := range segs {
		if !mavenSeg.MatchString(s) || strings.Contains(s, "..") {
			st, msg := m.fail(w, http.StatusBadRequest, "bad repository path")
			c.Emit(Event{Artifact: "request", Status: st, Err: msg})
			return
		}
	}
	file := segs[len(segs)-1]
	u := m.upstream + "/" + c.Rest
	relay := func() {
		st, msg := m.passthrough(w, r.Context(), u, "", c.Rest, mavenMetaMax, "")
		if msg != "" {
			c.Emit(Event{Module: c.Rest, Artifact: "metadata", Upstream: true, Status: st, Err: msg})
		}
	}
	if strings.HasPrefix(file, "maven-metadata.xml") {
		relay()
		return
	}
	for _, sc := range mavenSidecars {
		if strings.HasSuffix(file, sc) {
			relay()
			return
		}
	}
	if len(segs) < 4 {
		st, msg := m.fail(w, http.StatusBadRequest, "not an artifact path (group/artifact/version/file)")
		c.Emit(Event{Artifact: "request", Status: st, Err: msg})
		return
	}
	group := strings.Join(segs[:len(segs)-3], ".")
	name, version := group+":"+segs[len(segs)-3], segs[len(segs)-2]
	if strings.HasSuffix(version, "-SNAPSHOT") {
		st, msg := m.fail(w, http.StatusNotFound, "SNAPSHOT versions are not served (they change in place; Maven Central has none)")
		c.Emit(Event{Module: name, Version: version, Artifact: "artifact", Status: st, Err: msg})
		return
	}
	d, err := m.sidecar(r.Context(), u)
	if err != nil {
		st, msg := m.fail(w, http.StatusBadGateway, err.Error())
		c.Emit(Event{Module: name, Version: version, Artifact: "artifact", Status: st, Err: msg})
		return
	}
	m.serveArtifact(w, r, c, artifact{Name: name, Version: version, URL: u, Digest: d}, m.isPrivate(name))
}

// sidecar reads the upstream's published checksum for u: .sha256, else
// .sha1; none published is a zero digest (cached by URL).
func (m *Maven) sidecar(ctx context.Context, u string) (Digest, error) {
	for _, algo := range []string{"sha256", "sha1"} {
		b, err := m.meta.get(ctx, m.fetch, u+"."+algo, "", 4096)
		var nf errNotFound
		if errors.As(err, &nf) {
			continue
		}
		if err != nil {
			return Digest{}, err
		}
		f := strings.Fields(string(b))
		if len(f) == 0 {
			continue
		}
		return HexDigest(algo, f[0])
	}
	return Digest{}, nil
}
