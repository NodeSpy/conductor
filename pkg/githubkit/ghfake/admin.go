package ghfake

import "net/http"

// admin is the HTTP face of the scenario API (admin_http.go) — stubbed until
// that file lands in this package.
func (f *Fake) admin(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 404, map[string]any{"message": "no admin route " + r.URL.Path})
}
