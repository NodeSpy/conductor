package cost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

// USAGE SOURCES (`usage.sources`). Conductor observes only its own runs: the
// runs, turns and tokens its agents reported. A plan's window, limit or
// utilization is known only to whatever meters the account — so it comes
// only from a configured source, and is labelled as that source's report.
// A source type is pluggable (RegisterSourceType); "http" is built in.

// Reported is one provider/model figure a usage source reported. It is
// never summed with another provider's.
type Reported struct {
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
	// Window is the plan window the figure covers ("5h", "weekly", …), as
	// the source names it.
	Window string `json:"window,omitempty"`
	// Used / Limit are in Unit (tokens, requests, messages, …).
	Used  float64 `json:"used,omitempty"`
	Limit float64 `json:"limit,omitempty"`
	Unit  string  `json:"unit,omitempty"`
	// Utilization is Used/Limit as the source computes it, 0..1.
	Utilization *float64  `json:"utilization,omitempty"`
	ResetsAt    time.Time `json:"resets_at,omitzero"`
}

// Report is what a usage source returns: `{"providers": [Reported…]}`.
type Report struct {
	Providers []Reported `json:"providers"`
}

// Source is one configured usage source.
type Source interface {
	Name() string
	Fetch(ctx context.Context) (Report, error)
}

// SourceSpec is a source's config (config.UsageSource, without the import).
type SourceSpec struct {
	Name, Type, URL string
	Headers         map[string]string
	Timeout         time.Duration
}

var (
	sourceMu    sync.RWMutex
	sourceTypes = map[string]func(SourceSpec) (Source, error){"http": newHTTPSource}
)

// RegisterSourceType adds a usage-source implementation.
func RegisterSourceType(typ string, f func(SourceSpec) (Source, error)) {
	sourceMu.Lock()
	sourceTypes[typ] = f
	sourceMu.Unlock()
}

// NewSource builds a configured source.
func NewSource(s SourceSpec) (Source, error) {
	sourceMu.RLock()
	f := sourceTypes[s.Type]
	sourceMu.RUnlock()
	if f == nil {
		return nil, fmt.Errorf("usage source %s: unknown type %q", s.Name, s.Type)
	}
	return f(s)
}

// httpSource GETs a JSON Report.
type httpSource struct {
	spec   SourceSpec
	client *http.Client
}

func newHTTPSource(s SourceSpec) (Source, error) {
	to := s.Timeout
	if to <= 0 {
		to = 5 * time.Second
	}
	return &httpSource{spec: s, client: &http.Client{Timeout: to}}, nil
}

func (h *httpSource) Name() string { return h.spec.Name }

func (h *httpSource) Fetch(ctx context.Context) (Report, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.spec.URL, nil)
	if err != nil {
		return Report{}, err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range h.spec.Headers {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return Report{}, fmt.Errorf("usage source %s: %w", h.spec.Name, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Report{}, fmt.Errorf("usage source %s: %w", h.spec.Name, err)
	}
	if resp.StatusCode/100 != 2 {
		return Report{}, fmt.Errorf("usage source %s: HTTP %d", h.spec.Name, resp.StatusCode)
	}
	var r Report
	if err := json.Unmarshal(body, &r); err != nil {
		return Report{}, fmt.Errorf("usage source %s: not a usage report (want {\"providers\": […]}): %w", h.spec.Name, err)
	}
	// Untrusted input: clamp, drop entries that name no provider, and sort
	// for a stable display.
	out := r.Providers[:0]
	for _, p := range r.Providers {
		if p.Provider == "" {
			continue
		}
		p.Used, p.Limit = max(p.Used, 0), max(p.Limit, 0)
		if u := p.Utilization; u != nil {
			v := min(max(*u, 0), 1)
			p.Utilization = &v
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Model < out[j].Model
	})
	r.Providers = out
	return r, nil
}
