package pkgproxy

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func warmPaths(t *testing.T, e Ecosystem, files map[string]string) []string {
	t.Helper()
	w, ok := e.(Warmer)
	if !ok {
		t.Fatalf("%s is not a Warmer", e.Name())
	}
	in := map[string][]byte{}
	for k, v := range files {
		in[k] = []byte(v)
	}
	out := w.WarmPaths(context.Background(), in)
	sort.Strings(out)
	return out
}

func wantPaths(t *testing.T, got []string, want ...string) {
	t.Helper()
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("warm paths:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestLockfileReaders(t *testing.T) {
	up := newFakeUp(t)
	rc := RegistryConfig{Upstream: up.URL}
	mk := func(e Ecosystem, err error) Ecosystem {
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	c, _ := OpenCache(t.TempDir(), 1<<20)
	defer c.Close()
	rc.Cache = c
	rc.Fetcher, _ = NewFetcher(up.URL)

	npm := mk(NewNPM(rc))
	wantPaths(t, warmPaths(t, npm, map[string]string{
		"package-lock.json": `{"packages":{"":{"name":"app"},"node_modules/left-pad":{"version":"1.3.0","resolved":"https://registry.npmjs.org/left-pad/-/left-pad-1.3.0.tgz"},` +
			`"node_modules/a/node_modules/@acme/w":{"version":"2.0.0","resolved":"https://registry.npmjs.org/@acme/w/-/w-2.0.0.tgz"},` +
			`"node_modules/gitdep":{"version":"1.0.0","resolved":"git+ssh://git@example.test/x.git#abc"},"node_modules/local":{"link":true,"resolved":"../local"}}}`,
		"web/yarn.lock": "# yarn lockfile v1\n\n\"@babel/core@^7.0.0\", \"@babel/core@^7.1.0\":\n  version \"7.2.0\"\n  resolved \"https://registry.yarnpkg.com/@babel/core/-/core-7.2.0.tgz#abc\"\n  integrity sha512-x\n\nlodash@^4:\n  version \"4.17.21\"\n  resolved \"https://registry.yarnpkg.com/lodash/-/lodash-4.17.21.tgz\"\n",
		"b/yarn.lock":   "__metadata:\n  version: 6\n\n\"react@npm:^18.0.0\":\n  version: 18.2.0\n  resolution: \"react@npm:18.2.0\"\n  checksum: abc\n\n\"x@workspace:.\":\n  version: 0.0.0\n  resolution: \"x@workspace:.\"\n",
		"pnpm-lock.yaml": "lockfileVersion: '9.0'\npackages:\n  'is-odd@3.0.1':\n    resolution: {integrity: sha512-x}\n  '@types/node@20.1.0':\n    resolution: {integrity: sha512-y}\n  'tar@https://x.test/t.tgz':\n    resolution: {tarball: https://x.test/t.tgz}\n",
		"old/pnpm-lock.yaml": "lockfileVersion: 5.4\npackages:\n  /ms/2.1.3:\n    resolution: {integrity: sha512-z}\n  /debug@4.3.4(supports-color@8.0.0):\n    resolution: {integrity: sha512-q}\n",
		"bun.lock": "{\n  \"lockfileVersion\": 1,\n  \"packages\": {\n    \"chalk\": [\"chalk@5.3.0\", \"\", {}, \"sha512-c\"],\n    \"g\": [\"g@github:o/r#abc\", {}],\n  },\n}\n",
	}),
		"left-pad/-/left-pad-1.3.0.tgz", "@acme/w/-/w-2.0.0.tgz", "@babel/core/-/core-7.2.0.tgz", "lodash/-/lodash-4.17.21.tgz",
		"react/-/react-18.2.0.tgz", "is-odd/-/is-odd-3.0.1.tgz", "@types/node/-/node-20.1.0.tgz", "ms/-/ms-2.1.3.tgz",
		"debug/-/debug-4.3.4.tgz", "chalk/-/chalk-5.3.0.tgz")

	cargo := mk(NewCargo(rc))
	wantPaths(t, warmPaths(t, cargo, map[string]string{"Cargo.lock": "version = 3\n\n[[package]]\nname = \"serde\"\nversion = \"1.0.0\"\nsource = \"registry+https://github.com/rust-lang/crates.io-index\"\nchecksum = \"ab\"\n\n" +
		"[[package]]\nname = \"app\"\nversion = \"0.1.0\"\n\n[[package]]\nname = \"g\"\nversion = \"0.1.0\"\nsource = \"git+https://example.test/g#abc\"\n"}),
		"api/v1/crates/serde/1.0.0/download")

	gems := mk(NewRubyGems(rc))
	wantPaths(t, warmPaths(t, gems, map[string]string{"Gemfile.lock": "GIT\n  remote: https://example.test/g.git\n  specs:\n    g (1.0)\n\nGEM\n  remote: https://rubygems.org/\n  specs:\n    rack (3.0.8)\n    nokogiri (1.15.4-x86_64-linux)\n      racc (~> 1.4)\n\nPLATFORMS\n  ruby\n"}),
		"gems/rack-3.0.8.gem", "gems/nokogiri-1.15.4-x86_64-linux.gem")

	mvn := mk(NewMaven(rc))
	wantPaths(t, warmPaths(t, mvn, map[string]string{"gradle.lockfile": "# comment\ncom.google.guava:guava:32.0.0-jre=compileClasspath\nempty=\n"}),
		"com/google/guava/guava/32.0.0-jre/guava-32.0.0-jre.pom", "com/google/guava/guava/32.0.0-jre/guava-32.0.0-jre.jar")

	nuget := mk(NewNuGet(rc))
	wantPaths(t, warmPaths(t, nuget, map[string]string{"packages.lock.json": `{"version":1,"dependencies":{"net8.0":{"Newtonsoft.Json":{"type":"Direct","resolved":"13.0.1","contentHash":"x"},"lib":{"type":"Project"}}}}`}),
		"v3-flatcontainer/newtonsoft.json/13.0.1/newtonsoft.json.13.0.1.nupkg")

	comp := mk(NewComposer(rc))
	wantPaths(t, warmPaths(t, comp, map[string]string{"composer.lock": `{"packages":[{"name":"acme/w","dist":{"type":"zip","reference":"abc"}},{"name":"acme/src","source":{"type":"git"}}],"packages-dev":[{"name":"acme/t","dist":{"type":"tar","reference":"def"}}]}`}),
		"dists/acme/w/abc.zip", "dists/acme/t/def.tar")

	pub := mk(NewPub(rc))
	wantPaths(t, warmPaths(t, pub, map[string]string{"pubspec.lock": "packages:\n  http:\n    dependency: direct main\n    description:\n      name: http\n      sha256: ab\n      url: \"https://pub.dev\"\n    source: hosted\n    version: \"1.1.0\"\n  mine:\n    dependency: direct main\n    description:\n      path: ../mine\n    source: path\n    version: \"0.0.1\"\n"}),
		"packages/http/versions/1.1.0.tar.gz")

	hexEco := mk(NewHex(rc))
	wantPaths(t, warmPaths(t, hexEco, map[string]string{
		"mix.lock":   `%{"jason": {:hex, :jason, "1.4.0", "abc", [:mix], [], "hexpm", "def"}, "g": {:git, "https://example.test/g.git", "abc", []}}`,
		"rebar.lock": `{"1.2.0",[{<<"cowboy">>,{pkg,<<"cowboy">>,<<"2.10.0">>},0}]}.`,
	}), "tarballs/jason-1.4.0.tar", "tarballs/cowboy-2.10.0.tar")

	cran := mk(NewCRAN(rc))
	m := md5.Sum([]byte("x"))
	up.set("/src/contrib/PACKAGES", "Package: jsonlite\nVersion: 1.8.8\nMD5sum: "+hex.EncodeToString(m[:])+"\n\n")
	wantPaths(t, warmPaths(t, cran, map[string]string{"renv.lock": `{"Packages":{"jsonlite":{"Package":"jsonlite","Version":"1.8.8","Source":"Repository","Repository":"CRAN"},"old":{"Package":"old","Version":"0.1","Source":"Repository","Repository":"CRAN"},"gh":{"Package":"gh","Version":"1","Source":"GitHub"}}}`}),
		"src/contrib/jsonlite_1.8.8.tar.gz", "src/contrib/Archive/old/old_0.1.tar.gz")

	py := mk(NewPyPI(rc))
	plat := map[string]string{"linux": "manylinux_2_17_x86_64", "darwin": "macosx_11_0_arm64"}[runtime.GOOS]
	if runtime.GOARCH == "arm64" && runtime.GOOS == "linux" {
		plat = "manylinux_2_17_aarch64"
	}
	if runtime.GOARCH == "amd64" && runtime.GOOS == "darwin" {
		plat = "macosx_10_9_x86_64"
	}
	up.set("/simple/requests/", `{"files":[{"filename":"requests-2.31.0-py3-none-any.whl","url":"x","hashes":{"sha256":"`+sha256hex("r")+`"}},{"filename":"requests-2.31.0.tar.gz","url":"y","hashes":{"sha256":"`+sha256hex("s")+`"}}]}`)
	wantPaths(t, warmPaths(t, py, map[string]string{
		"requirements.txt": "# pinned\nrequests==2.31.0 \\\n    --hash=sha256:" + sha256hex("r") + "\nunpinned>=1\n",
		"uv.lock": "version = 1\n\n[[package]]\nname = \"numpy\"\nversion = \"1.26.0\"\nsource = { registry = \"https://pypi.org/simple\" }\nsdist = { url = \"https://files.example/numpy-1.26.0.tar.gz\", hash = \"sha256:aa\" }\nwheels = [\n  { url = \"https://files.example/numpy-1.26.0-cp312-cp312-" + plat + ".whl\", hash = \"sha256:bb\" },\n  { url = \"https://files.example/numpy-1.26.0-cp312-cp312-win_amd64.whl\", hash = \"sha256:cc\" },\n]\n\n" +
			"[[package]]\nname = \"mine\"\nversion = \"0.1.0\"\nsource = { editable = \".\" }\n",
		"poetry.lock": "[[package]]\nname = \"six\"\nversion = \"1.16.0\"\nfiles = [\n    {file = \"six-1.16.0-py2.py3-none-any.whl\", hash = \"sha256:aa\"},\n]\n\n[[package]]\nname = \"g\"\nversion = \"1\"\nfiles = []\n\n[package.source]\ntype = \"git\"\nurl = \"https://example.test/g.git\"\n\n[metadata]\nlock-version = \"2.0\"\n",
	}),
		"files/requests/requests-2.31.0-py3-none-any.whl", "files/numpy/numpy-1.26.0.tar.gz",
		"files/numpy/numpy-1.26.0-cp312-cp312-"+plat+".whl", "files/six/six-1.16.0-py2.py3-none-any.whl")
}

// A warm is served exactly like an agent's request: resolved against the
// upstream's metadata and verified, cached, audited as the prefetch; a
// lockfile's own URLs are never fetched, and a failure is reported.
func TestServerWarm(t *testing.T) {
	up := newFakeUp(t)
	r := newRegRig(t, func(c RegistryConfig) (Ecosystem, error) { return NewCargo(c) }, up.URL)
	crate := "fake crate"
	up.set("/config.json", `{"dl":"`+up.URL+`/crates/{crate}/{crate}-{version}.crate"}`)
	up.set("/se/rd/serde", `{"name":"serde","vers":"1.0.0","cksum":"`+sha256hex(crate)+`"}`+"\n"+`{"name":"serde","vers":"1.0.1","cksum":"`+sha256hex("good")+`"}`+"\n")
	up.set("/crates/serde/serde-1.0.0.crate", crate)
	up.set("/crates/serde/serde-1.0.1.crate", "tampered")
	lock := "[[package]]\nname = \"serde\"\nversion = \"1.0.0\"\nsource = \"registry+https://github.com/rust-lang/crates.io-index\"\n\n" +
		"[[package]]\nname = \"serde\"\nversion = \"1.0.1\"\nsource = \"registry+https://github.com/rust-lang/crates.io-index\"\n\n" +
		"[[package]]\nname = \"nope\"\nversion = \"9.9.9\"\nsource = \"registry+https://github.com/rust-lang/crates.io-index\"\n"
	pre := Requester{Identity: "conductor:prefetch"}
	n, err := r.srv.Warm(context.Background(), map[string][]byte{"sub/Cargo.lock": []byte(lock), "README": []byte("x")}, pre, nil)
	if n != 1 || err == nil || !strings.Contains(err.Error(), "SECURITY") || !strings.Contains(err.Error(), "not in the upstream registry") {
		t.Fatalf("warm: %d %v", n, err)
	}
	if n, _ := r.srv.Warm(context.Background(), map[string][]byte{"Cargo.lock": []byte(lock)}, pre, []string{"npm"}); n != 0 {
		t.Fatal("a warm for other ecosystems ran cargo's")
	}
	// The agent's request is now a cache hit.
	if code, got := r.get(t, "/api/v1/crates/serde/1.0.0/download"); code != 200 || got != crate {
		t.Fatalf("after warm: %d %q", code, got)
	}
	if n := up.count("/crates/serde/serde-1.0.0.crate"); n != 1 {
		t.Fatalf("the crate was fetched %d times; the warm's copy should serve the agent", n)
	}
	var warmed bool
	for _, e := range r.evs() {
		if e.Requester == pre && e.Upstream && e.Status == 200 && e.Ecosystem == "cargo" {
			warmed = true
		}
	}
	if !warmed {
		t.Fatalf("the warm's fetch is not audited as the prefetch: %+v", r.evs())
	}
	if got := r.srv.Lockfiles([]string{"cargo"}); len(got) != 1 || got[0] != "Cargo.lock" {
		t.Fatalf("lockfiles: %v", got)
	}
}
