//go:build ignore

// Command ghfaked serves the GitHub fake on a given address
// with one scenario, for the plugin-jail canary (test/sandbox-canary/plugins.sh):
// it is copied into a checkout of the plugins repo, whose internal fake it
// imports, and built there.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/NodeSpy/conductor-plugins/internal/githubkit/ghfake"
)

func main() {
	listen := flag.String("listen", "", "host:port to serve on")
	out := flag.String("out", "", "scenario JSON to write")
	flag.Parse()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatal(err)
	}
	f := ghfake.New(ghfake.Options{Logf: log.Printf})
	f.Start()
	f.SetApp(77, "conductor-test", &key.PublicKey)
	f.AddUser("acme", "Organization")
	f.AddRepo("acme/w")
	f.Install("acme")
	f.UserToken("me", "tok-me")
	f.AddUser("rev", "User")
	f.AddCollaborator("acme/w", "rev")
	n := f.OpenPR("acme/w", "me", ghfake.PROpts{Title: "feature", Head: "feat"})
	f.SubmitReview("acme/w", n, "rev", "CHANGES_REQUESTED", "please fix", ghfake.InlineComment{Body: "this line"})
	f.Flush()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	up, _ := url.Parse(f.URL())
	go http.Serve(ln, httputil.NewSingleHostReverseProxy(up))

	keyPath := filepath.Join(filepath.Dir(*out), "app.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(keyPath, pemBytes, 0o600); err != nil {
		log.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"url": "http://" + ln.Addr().String(), "key": keyPath, "repo": "acme/w", "pr": n, "app_id": 77, "token": "tok-me"})
	if err := os.WriteFile(*out, b, 0o600); err != nil {
		log.Fatal(err)
	}
	log.Printf("ghfake serving %s", ln.Addr())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
	<-sig
	log.Printf("unhandled: %v", f.Unhandled())
}
