package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/store"
)

// THE BOT PAGE'S ⋯ MENU on the operator API: rotate replaces the home
// session, deliver opens a ticket asked for by the operator (or a manual
// event), workers lists the bot's workers, and an unknown bot is a 404.
func TestOperatorAPIBotOps(t *testing.T) {
	f := newSvc(t)
	h := OperatorAPI(f.svc)
	ctx := context.Background()
	f.svc.Tick(ctx, "sre")
	f.home.set("agent-1", "idle")
	first := f.st.Bot("sre").Home
	if first == nil {
		t.Fatal("no home session to rotate")
	}

	w := apiDo(t, h, "POST", "/v1/bots/sre/rotate", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("rotate: %d %s", w.Code, w.Body)
	}
	if now := f.st.Bot("sre").Home; now == nil || now.ID == first.ID {
		t.Fatalf("rotate kept the old session: %+v", now)
	}

	w = apiDo(t, h, "POST", "/v1/bots/sre/deliver", strings.NewReader(`{"title":"Check the 5xx","text":"from the pager","urgent":true,"operator":"op"}`), "application/json")
	if w.Code != http.StatusCreated {
		t.Fatalf("deliver: %d %s", w.Code, w.Body)
	}
	var out struct{ Case, Ticket string }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	c := f.st.Bot("sre").Cases[out.Case]
	if c == nil || c.Title != "Check the 5xx" || c.RequesterClass != ReqOperator || c.CreatedBy != "operator:op" || out.Ticket != c.Ticket {
		t.Fatalf("delivered case: %s %+v", w.Body, c)
	}

	w = apiDo(t, h, "POST", "/v1/bots/sre/deliver", strings.NewReader(`{"title":"An alert","requester":"event"}`), "application/json")
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if c := f.st.Bot("sre").Cases[out.Case]; w.Code != http.StatusCreated || c == nil || c.RequesterClass == ReqOperator {
		t.Fatalf("event deliver: %d %s %+v", w.Code, w.Body, c)
	}
	for _, bad := range []string{`{"title":"  "}`, `{"title":"x","requester":"bot:other"}`, `not json`} {
		if w = apiDo(t, h, "POST", "/v1/bots/sre/deliver", strings.NewReader(bad), "application/json"); w.Code != http.StatusBadRequest {
			t.Fatalf("deliver %s: %d %s", bad, w.Code, w.Body)
		}
	}

	w = apiDo(t, h, "GET", "/v1/bots/sre/workers", nil, "")
	if w.Code != http.StatusOK || !strings.HasPrefix(strings.TrimSpace(w.Body.String()), `{"workers":[`) {
		t.Fatalf("workers: %d %s", w.Code, w.Body)
	}

	for _, p := range []string{"POST /v1/bots/nobody/rotate", "POST /v1/bots/nobody/sweep", "POST /v1/bots/nobody/deliver", "GET /v1/bots/nobody/workers"} {
		m, path, _ := strings.Cut(p, " ")
		if w = apiDo(t, h, m, path, strings.NewReader(`{"title":"x"}`), "application/json"); w.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}
}

// Sweep now starts a sweep for a bot with a proactive block, and says why
// not (409, in the op's words) for one without.
func TestOperatorAPIBotSweep(t *testing.T) {
	f := newSweepFixture(t, config.BotProactive{Every: config.Duration(time.Hour), Budget: config.ProactiveBudget{MaxRunsPerDay: 2}})
	h := OperatorAPI(f.svc)
	w := apiDo(t, h, "POST", "/v1/bots/sre/sweep", nil, "")
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"sweep"`) {
		t.Fatalf("sweep: %d %s", w.Code, w.Body)
	}
	if len(f.sweepFired()) != 1 {
		t.Fatalf("sweep fired %d runs", len(f.sweepFired()))
	}
	w = apiDo(t, h, "POST", "/v1/bots/other/sweep", nil, "")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "no proactive") {
		t.Fatalf("sweep without a proactive block: %d %s", w.Code, w.Body)
	}
}

// A decided proposal's row says who decided it, when and why.
func TestOperatorAPIProposalRowDecision(t *testing.T) {
	f := newSvc(t)
	when := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := f.st.UpdateBot("sre", func(r *store.BotRecord) error {
		if r.Proposals == nil {
			r.Proposals = map[string]*store.Proposal{}
		}
		r.Proposals["p9"] = &store.Proposal{ID: "p9", Kind: "verb", Uses: "box.run", Status: "denied", Approver: "operator:op", Decided: when, Reason: "not now", Created: when.Add(-time.Hour)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w := apiDo(t, OperatorAPI(f.svc), "GET", "/v1/bots/sre/proposals", nil, "")
	b := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(b, `"approver":"operator:op"`) || !strings.Contains(b, `"decided":"2026-10-01T12:00:00Z"`) || !strings.Contains(b, `"reason":"not now"`) {
		t.Fatalf("proposals: %d %s", w.Code, b)
	}
}
