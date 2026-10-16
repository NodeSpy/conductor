package cost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTurns(t *testing.T) {
	out := `{"type":"result","num_turns":7,"usage":{"input_tokens":4000,"output_tokens":500},"total_cost_usd":0.02}`
	if u, ok := ParseReported(out, "m"); !ok || u.Turns != 7 {
		t.Fatalf("num_turns: %+v", u)
	}
	if u := FromRun("m", strings.Repeat("p", 400), out); u.Turns != 7 {
		t.Fatalf("FromRun kept: %+v", u)
	}
	// No report: at least one turn.
	if u := FromRun("m", "prompt", "plain text"); u.Turns != 1 || !u.Approximate {
		t.Fatalf("estimated: %+v", u)
	}
	// Reported usage without num_turns: one.
	if u := FromRun("m", "p", `{"usage":{"input_tokens":10,"output_tokens":10}}`); u.Turns != 1 {
		t.Fatalf("no num_turns: %+v", u)
	}
	// Floored to the estimate, the reported turns survive.
	if u := FromRun("m", strings.Repeat("p", 40000), `{"num_turns":3,"usage":{"input_tokens":1,"output_tokens":1}}`); u.Turns != 3 || !u.Approximate {
		t.Fatalf("floored: %+v", u)
	}
	if u, _ := ParseReported(`{"num_turns":-4,"usage":{"input_tokens":1}}`, "m"); u.Turns != 0 {
		t.Fatalf("negative turns: %+v", u)
	}
}

func TestDisplayModeDefaultsOff(t *testing.T) {
	defer SetDisplayMode("")
	SetDisplayMode("")
	if DisplayMode() != DisplayOff || ShowUsage() || ShowDollars() {
		t.Fatal("the default shows usage")
	}
	SetDisplayMode(DisplaySubscription)
	if !ShowUsage() || ShowDollars() {
		t.Fatal("subscription")
	}
	SetDisplayMode(DisplayAPI)
	if !ShowUsage() || !ShowDollars() {
		t.Fatal("api")
	}
}

func TestHTTPSource(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/usage":
			_, _ = w.Write([]byte(`{"providers":[
				{"provider":"zeta","model":"z1","window":"weekly","used":10,"limit":100,"unit":"messages","utilization":0.1},
				{"provider":"alpha","window":"5h","used":-3,"utilization":1.7,"resets_at":"2026-10-16T20:00:00Z"},
				{"model":"orphan","used":5}
			]}`))
		case "/bad":
			_, _ = w.Write([]byte(`not json`))
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	src, err := NewSource(SourceSpec{Name: "proxy", Type: "http", URL: srv.URL + "/usage", Headers: map[string]string{"Authorization": "Bearer t"}, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	r, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer t" {
		t.Fatalf("headers not sent: %q", gotAuth)
	}
	// Per provider, sorted; an entry naming no provider dropped; clamped.
	if len(r.Providers) != 2 || r.Providers[0].Provider != "alpha" || r.Providers[1].Provider != "zeta" {
		t.Fatalf("providers: %+v", r.Providers)
	}
	if a := r.Providers[0]; a.Used != 0 || *a.Utilization != 1 || a.ResetsAt.IsZero() {
		t.Fatalf("clamping: %+v", a)
	}
	for _, path := range []string{"/bad", "/down"} {
		s, _ := NewSource(SourceSpec{Name: "proxy", Type: "http", URL: srv.URL + path})
		if _, err := s.Fetch(context.Background()); err == nil || !strings.Contains(err.Error(), "usage source proxy") {
			t.Errorf("%s: %v", path, err)
		}
	}
	if _, err := NewSource(SourceSpec{Name: "x", Type: "vendorx"}); err == nil {
		t.Fatal("an unknown source type")
	}
	RegisterSourceType("static", func(s SourceSpec) (Source, error) { return staticSource{s.Name}, nil })
	if s, err := NewSource(SourceSpec{Name: "st", Type: "static"}); err != nil || s.Name() != "st" {
		t.Fatalf("a registered type: %v", err)
	}
}

type staticSource struct{ name string }

func (s staticSource) Name() string                          { return s.name }
func (s staticSource) Fetch(context.Context) (Report, error) { return Report{}, nil }
