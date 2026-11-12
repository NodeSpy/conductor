package dashboard

import (
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// A run's link goes to the dashboard's public address: the configured one,
// else the first HTTPS listener that came up; a localhost-only dashboard
// links nothing.
func TestRunURL(t *testing.T) {
	cfg := &config.Config{}
	s := &Server{deps: Deps{Cfg: cfg}}
	if got := s.RunURL("r1"); got != "" {
		t.Fatalf("localhost only: %q", got)
	}
	s.offerPublicURL("https://box.example.ts.net:7788")
	s.offerPublicURL("https://later.example:9000") // the first one up wins
	if got := s.RunURL("r-ab/12"); got != "https://box.example.ts.net:7788/#/runs/r-ab%2F12" {
		t.Fatalf("tailnet: %q", got)
	}
	if got := s.RunURL(""); got != "" {
		t.Fatalf("no run: %q", got)
	}
	cfg.Dashboard.PublicURL = "https://dash.example/"
	if got := s.RunURL("r1"); got != "https://dash.example/#/runs/r1" {
		t.Fatalf("configured: %q", got)
	}
	var none *Server
	if got := none.RunURL("r1"); got != "" {
		t.Fatalf("no dashboard: %q", got)
	}
}
