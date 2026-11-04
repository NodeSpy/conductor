//go:build linux || darwin

package agentsandbox

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// TestSandboxInspectChild is the client a sandboxed agent runs: it trusts
// what $SSL_CERT_FILE names (as curl, node, Python and git do when told)
// and goes through the launch's proxy.
func TestSandboxInspectChild(t *testing.T) {
	if os.Getenv("AGENTSANDBOX_INSPECT") != "1" {
		t.Skip("runs only inside the sandbox")
	}
	say := func(ok bool, what string) {
		if ok {
			fmt.Println("ok " + what)
		} else {
			fmt.Println("FAIL " + what)
		}
	}
	bundle := os.Getenv("SSL_CERT_FILE")
	same := bundle != ""
	for _, k := range sandbox.InspectCAEnv {
		same = same && os.Getenv(k) == bundle
	}
	say(same, "every CA variable names the bundle")
	for _, kv := range os.Environ() {
		if strings.Contains(kv, "PRIVATE KEY") {
			say(false, "no key in the environment")
		}
	}
	b, err := os.ReadFile(bundle)
	say(err == nil && strings.Contains(string(b), "BEGIN CERTIFICATE") && !strings.Contains(string(b), "PRIVATE KEY"), "the bundle is readable and holds certificates only")
	say(os.WriteFile(bundle, []byte("x"), 0o600) != nil, "the bundle is read-only")
	proxyURL, _ := url.Parse(os.Getenv("HTTPS_PROXY"))
	client := func(roots []byte) *http.Client {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(roots)
		return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: pool}, ForceAttemptHTTP2: true}}
	}
	fetch := func(c *http.Client, u string) (int, string) {
		resp, err := c.Get(u)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	inspected := client(b)
	code, body := fetch(inspected, os.Getenv("INSPECT_MIRROR")+"/@myorg/pkg?token=s3cret")
	say(code == 200 && body == "mirror /@myorg/pkg", fmt.Sprintf("an allowed path passes (%d %q)", code, body))
	code, body = fetch(inspected, os.Getenv("INSPECT_MIRROR")+"/@other/pkg")
	say(code == 403 && !strings.Contains(body, "mirror"), fmt.Sprintf("another path is refused (%d)", code))
	own, _ := os.ReadFile(os.Getenv("INSPECT_BLIND_CA"))
	code, body = fetch(client(own), os.Getenv("INSPECT_BLIND")+"/anything")
	say(code == 200 && body == "blind /anything", fmt.Sprintf("an uninspected host is a blind tunnel with its own certificate (%d %q)", code, body))
	code, _ = fetch(client(own), os.Getenv("INSPECT_MIRROR")+"/@myorg/pkg")
	say(code == 0, "the inspected host does not show its own certificate")
}

// localhostCA is a private CA and a localhost leaf it signed: an inspected
// mirror's upstream, verified through inspect's ca:.
func localhostCA(t *testing.T) (caPEM []byte, leaf tls.Certificate) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caT := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mirror ca"}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	caDER, err := x509.CreateCertificate(rand.Reader, caT, caT, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	lt := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, lt, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// TestSandboxInspectsARealClient: a real sandboxed client reaches an
// inspected mirror at an allowed path only, an uninspected host blind, and
// every inspected request is audited with its path, never the query.
func TestSandboxInspectsARealClient(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	if why := Probe(); why != "" {
		if os.Getenv("CONDUCTOR_REQUIRE_SANDBOX") == "1" {
			t.Fatalf("the agent sandbox cannot be built here: %s", why)
		}
		t.Skipf("no sandbox here: %s", why)
	}
	root, err := SocketDir("csn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	ws, state := filepath.Join(root, "ws"), filepath.Join(root, "st")
	for _, d := range []string{ws, state} {
		os.MkdirAll(d, 0o700)
	}
	caPEM, leaf := localhostCA(t)
	caFile := filepath.Join(root, "mirror-ca.pem")
	os.WriteFile(caFile, caPEM, 0o600)
	mirror := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "mirror "+r.URL.Path)
	}))
	mirror.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}}
	mirror.StartTLS()
	defer mirror.Close()
	blind := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "blind "+r.URL.Path)
	}))
	defer blind.Close()
	blindCA := filepath.Join(ws, "blind.pem")
	os.WriteFile(blindCA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: blind.Certificate().Raw}), 0o600)
	_, mport, _ := net.SplitHostPort(mirror.Listener.Addr().String())

	home, logins, err := PrepareHome(HomeSpec{Root: filepath.Join(state, "agent-state"), Key: "agent:inspect", Tool: "claude-code", GOOS: runtime.GOOS})
	if err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	if r, err := filepath.EvalSymlinks(self); err == nil {
		self = r
	}
	pol := Policy{
		Identity: "agent:inspect", Tool: "claude-code", Workspace: ws, Home: home, Logins: logins, Run: "run-inspect",
		Egress:   []string{"https://localhost:" + mport + "/@myorg/", blind.Listener.Addr().String()},
		Inspect:  []sandbox.InspectHost{{Host: "localhost", CA: caFile}},
		ReadOnly: []string{filepath.Dir(self)},
	}
	pm := sandbox.NewProxyManager(nil)
	var mu sync.Mutex
	var recs []sandbox.ConnRecord
	pm.OnConn = func(r sandbox.ConnRecord) { mu.Lock(); recs = append(recs, r); mu.Unlock() }
	defer pm.Close()
	rel, err := NewRelayer()
	if err != nil {
		t.Fatal(err)
	}
	defer rel.Close()
	l, err := Wrap(pol, []string{self, "-test.run=^TestSandboxInspectChild$", "-test.v=false"}, Deps{
		SelfExe: selfConductor(t), EgressUnix: pm.UnixEndpoint, EgressAddr: pm.Endpoint,
		EgressUnixSession: pm.UnixEndpointSession, EgressAddrSession: pm.EndpointSession, Relay: rel.Relay,
		LookPath: exec.LookPath, RealHome: root, Inherited: os.Environ(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Cleanup()
	cmd := exec.Command(l.Argv[0], l.Argv[1:]...)
	cmd.Dir = l.Dir
	cmd.Env = append(l.Env, "AGENTSANDBOX_INSPECT=1", "INSPECT_MIRROR=https://localhost:"+mport,
		"INSPECT_BLIND=https://"+blind.Listener.Addr().String(), "INSPECT_BLIND_CA="+blindCA)
	out, err := cmd.CombinedOutput()
	t.Logf("inspect probe (%s):\n%s", runtime.GOOS, out)
	if err != nil || strings.Contains(string(out), "FAIL") || strings.Count(string(out), "ok ") < 7 {
		t.Fatalf("inspect probe: %v\n%s", err, out)
	}
	mu.Lock()
	defer mu.Unlock()
	var allowed, denied bool
	for _, r := range recs {
		if strings.Contains(r.Path, "s3cret") || strings.Contains(r.Reason, "s3cret") {
			t.Fatalf("the query was recorded: %+v", r)
		}
		allowed = allowed || (r.Inspected && r.Method == "GET" && r.Path == "/@myorg/pkg" && r.Outcome == "closed")
		denied = denied || (r.Inspected && r.Path == "/@other/pkg" && r.Outcome == "denied")
	}
	if !allowed || !denied {
		t.Fatalf("inspected requests not audited: %+v", recs)
	}
}
