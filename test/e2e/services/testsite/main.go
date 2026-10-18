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
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/browser/browsertest"
)

func main() {
	addr := flag.String("listen", "127.0.0.1:8090", "listen address")
	seed := flag.String("totp-seed", "", "base32 TOTP seed of the test account (empty: no code step)")
	imap := flag.String("imap", "", "serve a fake IMAP mailbox here (plaintext, loopback) and mail a code after the password")
	imapUser := flag.String("imap-user", "agent-inbox", "the mailbox's login")
	imapPass := flag.String("imap-pass", "", "the mailbox's (app) password")
	var shops shopAddrs
	flag.Var(&shops, "shop", "also serve a fake shop (no real merchant, no real card) at this address; repeat for several")
	flag.Parse()
	for _, a := range shops {
		shop := browsertest.NewShop()
		go func(a string) { log.Fatal(http.ListenAndServe(a, shop)) }(a)
	}
	site := browsertest.New()
	site.TOTPSeed = *seed
	if *imap != "" {
		mb := &browsertest.Mailbox{User: *imapUser, Pass: *imapPass}
		// What conductor must NOT use: an older genuine code, and a
		// lookalike sender's fresh one.
		mb.Deliver("Test Site <no-reply@testsite.example>", "Your sign-in code", "Your sign-in code is 111111.\r\n", time.Now().Add(-time.Hour))
		go func() {
			time.Sleep(time.Second)
			for {
				mb.Deliver("Test Site <no-reply@testsite.example.evil>", "Your sign-in code", "Your sign-in code is 666666.\r\n", time.Now())
				time.Sleep(20 * time.Second)
			}
		}()
		if err := mb.Serve(*imap, nil); err != nil {
			log.Fatal(err)
		}
		site.EmailCodes = mb
		http.HandleFunc("/_harness/imap", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(mb.Log())
		})
	}
	mux := http.DefaultServeMux
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

// shopAddrs is a repeatable -shop flag.
type shopAddrs []string

func (s *shopAddrs) String() string     { return strings.Join(*s, ",") }
func (s *shopAddrs) Set(v string) error { *s = append(*s, v); return nil }
