// testsite serves the browser test site (internal/browser/browsertest) for
// the e2e browser group: a TEST account with a password and a TOTP step, a
// form, a download. It runs inside the sandbox daemon's container on
// loopback (the browser's allowlist names 127.0.0.1/localhost literally);
// /_harness/* is the harness's view of what the site saw.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"

	"github.com/NodeSpy/conductor/internal/browser/browsertest"
)

func main() {
	addr := flag.String("listen", "127.0.0.1:8090", "listen address")
	seed := flag.String("totp-seed", "", "base32 TOTP seed of the test account (empty: no code step)")
	flag.Parse()
	site := browsertest.New()
	site.TOTPSeed = *seed
	mux := http.NewServeMux()
	mux.HandleFunc("/_harness/session", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(site.Session()))
	})
	mux.HandleFunc("/_harness/state", func(w http.ResponseWriter, r *http.Request) {
		subs, acts := site.Snapshot()
		json.NewEncoder(w).Encode(map[string]any{"submissions": subs, "actions": acts, "sign_ins": site.SignIns(), "codes": site.Codes})
	})
	mux.Handle("/", site)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
