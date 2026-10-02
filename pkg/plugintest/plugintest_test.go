package plugintest

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/pkg/plugin"
	"github.com/NodeSpy/conductor/pkg/sourcekit"
)

// acme is a minimal source: an HMAC-verified listener that routes each
// delivery's "kind" to the first trigger on it, and a poll that reports one
// catch-up event read from its upstream.
type acme struct{ upstream string }

func (a *acme) Describe() plugin.Decl {
	return plugin.Decl{Type: "acme", Events: []plugin.Event{{Name: "alert"}, {Name: "stale"}}}
}
func (a *acme) Invoke(plugin.InvokeRequest) (plugin.InvokeResult, error) {
	return plugin.InvokeResult{}, nil
}
func (a *acme) StartSource(ctx context.Context, req plugin.StartSourceRequest, emit func(any) error) error {
	a.upstream, _ = req.Config["upstream"].(string)
	ln := sourcekit.Listener{Addr: req.Config["listen"].(string), Path: "/webhook", Secret: "s", SigHeader: "X-Sig"}
	return ln.Serve(ctx, func(_ http.Header, body []byte) {
		var p struct{ Kind, ID string }
		_ = json.Unmarshal(body, &p)
		for _, tr := range req.Triggers {
			if tr.Event == p.Kind {
				_ = emit(plugin.SourceEvent{Event: p.Kind, Trigger: tr.ID, Instance: req.Instance,
					Target: plugin.Target{Key: "acme/" + p.ID}, Context: map[string]any{"id": p.ID}})
				return
			}
		}
	})
}
func (a *acme) Poll(context.Context, plugin.PollRequest) (plugin.PollResult, error) {
	resp, err := http.Get(a.upstream + "/stale")
	if err != nil {
		return plugin.PollResult{}, err
	}
	defer resp.Body.Close()
	var ids []string
	_ = json.NewDecoder(resp.Body).Decode(&ids)
	var out []plugin.SourceEvent
	for _, id := range ids {
		out = append(out, plugin.SourceEvent{Event: "stale", CatchUp: true, Target: plugin.Target{Key: "acme/" + id}})
	}
	return plugin.PollResult{Events: out}, nil
}

func sign(body []byte) map[string]string {
	m := hmac.New(sha256.New, []byte("s"))
	m.Write(body)
	return map[string]string{"X-Sig": "sha256=" + hex.EncodeToString(m.Sum(nil))}
}

func TestHarnessPlaysACase(t *testing.T) {
	c := Case{
		Name:       "alerts route; poll recovers stale",
		Connection: func(e Env) map[string]any { return map[string]any{"listen": e.Listen, "upstream": e.Upstream} },
		Triggers:   []Trigger{{On: "alert", Name: "a"}},
		Upstream: func(*testing.T) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`["9"]`)) })
		},
		Steps: []Step{{Deliver: &Delivery{Body: `{"Kind":"alert","ID":"1"}`}}, {Poll: true}},
		Want: []Want{
			{Event: "alert", Trigger: "a", Target: "acme/1", Context: map[string]any{"id": "1"}},
			{Event: "stale", Target: "acme/9", CatchUp: true},
		},
	}
	RunCase(t, Suite{Sign: sign}, c, HandlerStarter(&acme{}))
}

func TestDiffReportsMissingAndUnexpected(t *testing.T) {
	p := Diff([]Want{{Event: "a"}}, []Got{{Event: "b"}})
	if len(p) != 2 || !strings.Contains(p[0], "missing a") || !strings.Contains(p[1], "unexpected b") {
		t.Fatalf("diff = %q", p)
	}
	if p := Diff([]Want{{Event: "a", Context: map[string]any{"n": 1}}}, []Got{{Event: "a", Context: map[string]any{"n": 1.0}}}); len(p) != 0 {
		t.Fatalf("1 and 1.0 must agree: %q", p)
	}
}
