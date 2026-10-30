package dashboard

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// distFS holds the built SPA. `make web` builds web/ into this dist/ directory,
// which is committed so `go build` and `go install` never need Node (design §8).
// A placeholder index.html is committed so a fresh checkout builds and serves
// before the SPA has been built.
//
//go:embed all:dist
var distFS embed.FS

// spaHandler serves the embedded SPA. Hashed assets (anything under /assets/)
// are cached immutably; index.html is never cached; any unknown path falls back
// to index.html so client-side routing works on reload (design §8).
func spaHandler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "dashboard assets unavailable", http.StatusInternalServerError)
		})
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if upath == "" {
			upath = "index.html"
		}
		f, err := sub.Open(upath)
		if err != nil {
			// Unknown path → SPA fallback: serve index.html (no-store) and let
			// the client router resolve it.
			serveIndex(w, r, sub)
			return
		}
		_ = f.Close()

		switch {
		case upath == "index.html":
			w.Header().Set("Cache-Control", "no-store")
		case strings.HasPrefix(upath, "assets/"):
			// Vite fingerprints asset filenames, so they can be cached forever.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, sub fs.FS) {
	b, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "dashboard not built", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

// pid returns the daemon process id.
func pid() int { return os.Getpid() }
