package dashboard

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The bot page's ⋯ actions (rotate, sweep now, deliver an event) are writes
// on the bot: only its operators may, and a deliver is asked for by the
// signed-in operator. Workers is a read any operator may make.
func TestBotAPIBotOpsNeedAnOperatorOfTheBot(t *testing.T) {
	g := newBotRig(t, false)
	for _, p := range []string{"/api/v1/bots/sre/rotate", "/api/v1/bots/sre/sweep", "/api/v1/bots/sre/deliver"} {
		if rr := g.dev.do("POST", p, map[string]any{"title": "x"}); rr.Code != http.StatusForbidden ||
			!strings.Contains(rr.Body.String(), "dev is not an operator of bot:sre") {
			t.Fatalf("dev on %s: %d %s", p, rr.Code, rr.Body)
		}
	}
	rr := g.dev.do("POST", "/api/v1/bots/devbot/deliver", map[string]any{"title": "rotate logs"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("dev delivering to devbot: %d %s", rr.Code, rr.Body)
	}
	var out struct{ Ticket string }
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	tk := g.dev.do("GET", "/api/v1/tickets/"+out.Ticket, nil)
	if tk.Code != http.StatusOK || !strings.Contains(tk.Body.String(), `"created_by":"operator:dev"`) {
		t.Fatalf("the delivered ticket: %d %s", tk.Code, tk.Body)
	}
	if rr := g.ops.do("GET", "/api/v1/bots/devbot/workers", nil); rr.Code != http.StatusOK {
		t.Fatalf("ops reading devbot's workers: %d %s", rr.Code, rr.Body)
	}
}
