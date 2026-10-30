package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/bot"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dashboard/auth"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/store"
)

// seen is what a bot API handler would read as its operator, from each place
// one reads it.
type seen struct {
	query, json, form string
}

// botRig is a dashboard with two operators (ops runs sre, dev runs devbot)
// and the bots' operator API mounted behind the chokepoint. With record set,
// every route's handler is replaced by one that records what it saw.
type botRig struct {
	s        *Server
	svc      *bot.Service
	st       *store.Store
	auditLog string
	ops, dev *browserClient
	devKey   sshTestKey
	mu       sync.Mutex
	hits     map[string][]seen
}

func newBotRig(t *testing.T, record bool) *botRig {
	t.Helper()
	opsKey, devKey := newSSHTestKey(t, "ops"), newSSHTestKey(t, "dev")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfgText := "connectors:\n  box: { use: command }\noperators:\n  ops:\n    ssh_key: " + opsKey.pub + "\n  dev:\n    ssh_key: " + devKey.pub + "\n" +
		"dashboard:\n  enabled: true\n  bind:\n    - address: 0.0.0.0:7795\n      hostnames: [box.lan]\n      allow_plain_http: true\n"
	if err := os.WriteFile(cfgPath, []byte(cfgText), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	auditLog := filepath.Join(dir, "audit.jsonl")
	st, err := store.Open(store.Options{StatePath: filepath.Join(dir, "state.json"), AuditPath: auditLog})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	botCfg := &config.Config{
		Operators: map[string]config.OperatorConfig{"ops": {SSHKey: opsKey.pub}, "dev": {SSHKey: devKey.pub}},
		Bots: map[string]config.BotConfig{"sre": {Operators: []string{"ops"}, Description: "sre"},
			"devbot": {Operators: []string{"dev"}, Description: "devbot"}, "devbot2": {Operators: []string{"dev"}, Description: "devbot2"}},
		Teams: map[string]config.TeamConfig{"platform": {Operators: []string{"ops"}}, "devteam": {Operators: []string{"dev"}}},
	}
	svc := &bot.Service{Cfg: func() *config.Config { return botCfg }, Ledger: st, Audit: st.Audit}
	g := &botRig{svc: svc, st: st, auditLog: auditLog, hits: map[string][]seen{}, devKey: devKey}

	var routes []BotRoute
	patterns := map[string]bool{}
	for _, r := range bot.OperatorRoutes(svc) {
		routes = append(routes, BotRoute{Pattern: r.Pattern, Handler: r.Handler})
		patterns[r.Pattern] = true
	}
	// The avatar and wallet routes are added in cmd/conductor; stand-ins here
	// (cmd/conductor's test checks the real list against the rules).
	for p := range botWriteRules {
		if !patterns[p] {
			routes = append(routes, BotRoute{Pattern: p, Handler: http.NotFoundHandler()})
		}
	}
	if record {
		for i := range routes {
			p := routes[i].Pattern
			routes[i].Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var sn seen
				sn.query = r.URL.Query().Get("operator")
				ct := r.Header.Get("Content-Type")
				switch {
				case strings.HasPrefix(ct, "multipart/form-data"), strings.HasPrefix(ct, "application/x-www-form-urlencoded"):
					_ = r.ParseMultipartForm(1 << 20)
					sn.form = r.FormValue("operator")
				default:
					var body struct{ Operator string }
					_ = json.NewDecoder(r.Body).Decode(&body)
					sn.json = body.Operator
				}
				g.mu.Lock()
				g.hits[p] = append(g.hits[p], sn)
				g.mu.Unlock()
				w.WriteHeader(http.StatusOK)
			})
		}
	}

	g.s = New(Deps{
		Cfg: cfg, Store: st, Events: flow.NewEventHub(), StateDir: dir,
		ConfigPath: cfgPath, AuditPath: auditLog,
		Validate:  func(p string) error { _, err := config.Load(p); return err },
		Reload:    func() error { return nil },
		Operators: func() map[string]string { return map[string]string{"ops": opsKey.pub, "dev": devKey.pub} },
		Log:       func(string, ...any) {},
		BotRoutes: routes, BotAuthority: svc,
		AvatarStage: func(kind, name, token string) ([]string, any, bool, error) {
			if token != "plan-1" {
				return nil, nil, false, errors.New("no such plan")
			}
			return []string{kind + "s", name, "avatar"}, map[string]any{"image": "avatars/" + name + "-0123456789ab.png"}, false, nil
		},
	})
	h := g.s.bindHandler(cfg.Dashboard.Bind[0])
	signin := func(op, addr string) *browserClient {
		ck, _, err := g.s.auth.CreateSessionFor(browserRequest(cfg.Dashboard.Bind[0]), op, auth.MethodSSH, "")
		if err != nil {
			t.Fatal(err)
		}
		c := &browserClient{t: t, h: h, host: "box.lan:7795", addr: addr, cookie: ck}
		var state struct {
			CSRF string `json:"csrf"`
		}
		_ = json.Unmarshal(c.do("GET", "/api/v1/auth/state", nil).Body.Bytes(), &state)
		c.csrf = state.CSRF
		return c
	}
	g.ops, g.dev = signin("ops", "192.0.2.50:1111"), signin("dev", "192.0.2.60:2222")
	return g
}

// raw sends body as-is with a content type.
func (c *browserClient) raw(method, path, ctype string, body []byte) *httptest.ResponseRecorder {
	c.t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Host, r.RemoteAddr = c.host, c.addr
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	r.AddCookie(c.cookie)
	r.Header.Set(auth.CSRFHeader, c.csrf)
	rr := httptest.NewRecorder()
	c.h.ServeHTTP(rr, r)
	return rr
}

var pathParam = regexp.MustCompile(`\{[a-z]+\}`)

// concrete fills a pattern's path with dev's own bot, team and conversation.
func concrete(pattern string) (method, path string) {
	method, path, _ = strings.Cut(pattern, " ")
	return method, "/api" + pathParam.ReplaceAllStringFunc(path, func(p string) string {
		switch p {
		case "{id}":
			if strings.Contains(path, "/conversations/") {
				return "bot:devbot"
			}
			return "T-1"
		case "{name}":
			if strings.Contains(path, "/teams/") {
				return "devteam"
			}
			return "devbot"
		case "{bot}":
			return "devbot"
		}
		return "x"
	})
}

func multipartWith(fields map[string]string) ([]byte, string) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	_ = w.Close()
	return b.Bytes(), w.FormDataContentType()
}

// THE CHOKEPOINT, every route: on every route the dashboard mounts from the
// bot API (enumerated from the router, so a new route is covered), a request
// that names another operator — in the query, a JSON key in any case, a
// urlencoded field, or a multipart field — is refused 403 before the handler
// runs. One that names the session's own operator, or none, reaches it with
// the session's operator in every place a handler reads one.
func TestBotAPIEveryRouteRefusesAnotherOperator(t *testing.T) {
	g := newBotRig(t, true)
	var writes int
	for _, rt := range g.s.deps.BotRoutes {
		method, path := concrete(rt.Pattern)
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		form, fct := multipartWith(map[string]string{"text": "hi", "operator": "ops"})
		attempts := []struct {
			name, path, ctype string
			body              []byte
		}{
			{"query", path + sep + "operator=ops", "application/json", []byte(`{"text":"hi"}`)},
			{"second query value", path + sep + "operator=dev&operator=ops", "", nil},
		}
		if !isRead(method) {
			writes++
			attempts = append(attempts,
				struct {
					name, path, ctype string
					body              []byte
				}{"json", path, "application/json", []byte(`{"text":"hi","operator":"ops"}`)},
				struct {
					name, path, ctype string
					body              []byte
				}{"json, folded key", path, "application/json", []byte(`{"text":"hi","OPERATOR":"ops","operator":"dev"}`)},
				struct {
					name, path, ctype string
					body              []byte
				}{"json under a form content type", path, "application/x-www-form-urlencoded", []byte(`{"text":"hi","operator":"ops"}`)},
				struct {
					name, path, ctype string
					body              []byte
				}{"urlencoded", path, "application/x-www-form-urlencoded", []byte("text=hi&operator=ops")},
				struct {
					name, path, ctype string
					body              []byte
				}{"multipart", path, fct, form},
				struct {
					name, path, ctype string
					body              []byte
				}{"json preamble before multipart", path, "multipart/form-data; boundary=B", []byte("{\"operator\":\"ops\"}\r\n--B--\r\n")},
			)
		}
		for _, a := range attempts {
			before := len(g.hits[rt.Pattern])
			rr := g.dev.raw(method, a.path, a.ctype, a.body)
			if rr.Code != http.StatusForbidden && rr.Code != http.StatusBadRequest {
				t.Errorf("%s (%s): dev naming ops got %d %s", rt.Pattern, a.name, rr.Code, rr.Body)
			}
			if len(g.hits[rt.Pattern]) != before {
				t.Errorf("%s (%s): the handler ran for a refused request", rt.Pattern, a.name)
			}
		}
		// Naming nobody, or itself, reaches the handler as dev everywhere.
		for _, a := range []struct{ ctype, body string }{{"application/json", `{"text":"hi"}`}, {"application/json", `{"text":"hi","operator":"dev"}`}} {
			if isRead(method) {
				break
			}
			before := len(g.hits[rt.Pattern])
			rr := g.dev.raw(method, path, a.ctype, []byte(a.body))
			if rt.Pattern == "POST /v1/tickets" || strings.HasPrefix(rt.Pattern, "PATCH /v1/tickets/") || strings.HasSuffix(rt.Pattern, "/comments") {
				continue // need a ticket dev owns: TestBotAPIWritesNeedAnOperatorOfTheTarget
			}
			if botOwnHandlers[rt.Pattern] != nil {
				continue // the dashboard serves it: TestBotAPIAvatarApplyOnlyStages
			}
			if rr.Code != http.StatusOK || len(g.hits[rt.Pattern]) != before+1 {
				t.Errorf("%s: dev as itself: %d %s", rt.Pattern, rr.Code, rr.Body)
				continue
			}
			if got := g.hits[rt.Pattern][before]; got.query != "dev" || got.json != "dev" {
				t.Errorf("%s: the handler saw %+v, want dev in the query and the body", rt.Pattern, got)
			}
		}
	}
	if writes < len(botWriteRules) {
		t.Fatalf("only %d write routes mounted, %d rules", writes, len(botWriteRules))
	}
}

// A multipart or urlencoded write with no operator field reads the session's
// from FormValue (the query, which the chokepoint sets, comes first).
func TestBotAPIFormsReadTheSessionOperator(t *testing.T) {
	g := newBotRig(t, true)
	form, fct := multipartWith(map[string]string{"text": "hi"})
	if rr := g.dev.raw("POST", "/api/v1/conversations/bot:devbot/messages", fct, form); rr.Code != 200 {
		t.Fatalf("multipart: %d %s", rr.Code, rr.Body)
	}
	if rr := g.dev.raw("POST", "/api/v1/conversations/bot:devbot/read", "application/x-www-form-urlencoded", []byte("upto=3")); rr.Code != 200 {
		t.Fatalf("form: %d %s", rr.Code, rr.Body)
	}
	for _, p := range []string{"POST /v1/conversations/{id}/messages", "POST /v1/conversations/{id}/read"} {
		if h := g.hits[p]; len(h) != 1 || h[0].form != "dev" {
			t.Fatalf("%s: the handler saw %+v", p, h)
		}
	}
}

// A write on a bot, team or ticket needs an operator of it; the operator's own
// view state (read marks, pins) needs none. The real bot API behind it: dev
// can't create a ticket for sre, message sre, or reassign its ticket to sre;
// ops can. The bot's own audit rows and the dashboard's carry who and where.
func TestBotAPIWritesNeedAnOperatorOfTheTarget(t *testing.T) {
	g := newBotRig(t, false)
	if rr := g.dev.do("POST", "/api/v1/tickets", map[string]any{"title": "buy disks", "assignee": "sre"}); rr.Code != http.StatusForbidden ||
		!strings.Contains(rr.Body.String(), "dev is not an operator of sre") {
		t.Fatalf("dev creating a ticket for sre: %d %s", rr.Code, rr.Body)
	}
	if rr := g.dev.do("POST", "/api/v1/conversations/bot:sre/messages", map[string]any{"text": "run it"}); rr.Code != http.StatusForbidden {
		t.Fatalf("dev messaging sre: %d %s", rr.Code, rr.Body)
	}
	if rr := g.dev.do("POST", "/api/v1/conversations/team:platform/pin", map[string]any{}); rr.Code == http.StatusForbidden {
		t.Fatalf("pinning is dev's own view: %d %s", rr.Code, rr.Body)
	}
	rr := g.dev.do("POST", "/api/v1/tickets", map[string]any{"title": "rotate logs", "assignee": "devbot"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("dev creating a ticket for devbot: %d %s", rr.Code, rr.Body)
	}
	var tk struct {
		Ticket    string `json:"ticket"`
		CreatedBy string `json:"created_by"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &tk)
	if tk.CreatedBy != "operator:dev" {
		t.Fatalf("created by %q", tk.CreatedBy)
	}
	if rr := g.dev.do("PATCH", "/api/v1/tickets/"+tk.Ticket, map[string]any{"assignee": "sre"}); rr.Code != http.StatusForbidden {
		t.Fatalf("dev handing its ticket to sre: %d %s", rr.Code, rr.Body)
	}
	if rr := g.ops.do("PATCH", "/api/v1/tickets/"+tk.Ticket, map[string]any{"title": "x"}); rr.Code != http.StatusForbidden {
		t.Fatalf("ops editing devbot's ticket: %d %s", rr.Code, rr.Body)
	}
	if rr := g.dev.do("POST", "/api/v1/tickets/"+tk.Ticket+"/comments", map[string]any{"text": "done"}); rr.Code != http.StatusOK && rr.Code != http.StatusCreated {
		t.Fatalf("dev commenting on its ticket: %d %s", rr.Code, rr.Body)
	}

	b, _ := os.ReadFile(g.auditLog)
	var botRow, dashRow map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e map[string]any
		_ = json.Unmarshal([]byte(line), &e)
		switch {
		case e["event"] == "bot_ticket" && e["action"] == "create":
			botRow = e
		case e["event"] == "dashboard.botapi" && e["route"] == "POST /v1/tickets" && e["status"] == "201":
			dashRow = e
		}
	}
	for name, row := range map[string]map[string]any{"bot_ticket": botRow, "dashboard.botapi": dashRow} {
		if row == nil {
			t.Fatalf("no %s row in %s", name, b)
		}
		if row["session"] == nil || row["session"] == "" || row["addr"] != "192.0.2.60:2222" || row["method"] != "ssh" || row["listener"] == nil {
			t.Fatalf("%s row lacks the session's attribution: %v", name, row)
		}
	}
	if botRow["by"] != "operator:dev" || dashRow["operator"] != "dev" || dashRow["target"] != "devbot" {
		t.Fatalf("rows: %v %v", botRow, dashRow)
	}
}

// A body the bot API would read two ways is refused rather than guessed at.
func TestBotAPIAmbiguousBodies(t *testing.T) {
	g := newBotRig(t, true)
	for name, body := range map[string]string{
		"trailing object": `{"text":"hi"}{"operator":"ops"}`,
		"non-string":      `{"operator":7}`,
	} {
		if rr := g.dev.raw("POST", "/api/v1/conversations/bot:devbot/messages", "application/json", []byte(body)); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rr.Code, rr.Body)
		}
	}
	if n := len(g.hits["POST /v1/conversations/{id}/messages"]); n != 0 {
		t.Fatalf("the handler ran %d times", n)
	}
}

// sshStepupAs confirms purpose+digest on c's session with key and returns the
// step-up token.
func sshStepupAs(t *testing.T, c *browserClient, key sshTestKey, purpose auth.Purpose, digest string) string {
	t.Helper()
	var ch auth.SSHChallengeView
	rr := c.do("POST", "/api/v1/auth/stepup/ssh/begin", map[string]string{"purpose": string(purpose), "digest": digest})
	if err := json.Unmarshal(rr.Body.Bytes(), &ch); err != nil || ch.ID == "" {
		t.Fatalf("ssh step-up begin: %d %s", rr.Code, rr.Body)
	}
	sig, err := auth.SignSSH(context.Background(), key.priv, ch.Namespace, []byte(ch.Text))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Token string `json:"token"`
	}
	rr = c.do("POST", "/api/v1/auth/stepup/ssh/finish", map[string]string{"challenge_id": ch.ID, "signature": sig})
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || out.Token == "" {
		t.Fatalf("ssh step-up finish: %d %s", rr.Code, rr.Body)
	}
	return out.Token
}

// A ticket spend limit that widens what a bot may spend — set at create,
// raised, given more merchants, or cleared — needs the browser.write step-up
// on the dashboard, bound to the request, like the wallet's own widening
// changes. Lowering it doesn't.
func TestBotAPITicketSpendWideningNeedsAStepup(t *testing.T) {
	g := newBotRig(t, false)
	create := map[string]any{"title": "buy disks", "assignee": "devbot",
		"spend_limit": map[string]any{"amount": "120", "currency": "USD", "merchants": []string{"https://shop.example.com"}}}
	rr := g.dev.do("POST", "/api/v1/tickets", create)
	var refused struct {
		Stepup struct{ Purpose, Digest, Description string } `json:"stepup"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &refused)
	if rr.Code != http.StatusUnauthorized || refused.Stepup.Purpose != string(auth.PurposeBrowserWrite) ||
		!strings.Contains(refused.Stepup.Description, "a spend limit set") || !strings.Contains(refused.Stepup.Description, "https://shop.example.com") {
		t.Fatalf("a spend limit at create: %d %s", rr.Code, rr.Body)
	}
	if n := strings.Count(mustRead(t, g.auditLog), `"action":"create"`); n != 0 {
		t.Fatal("the refused create reached the bot service")
	}
	tok := sshStepupAs(t, g.dev, g.devKey, auth.PurposeBrowserWrite, refused.Stepup.Digest)
	rr = g.dev.doWith("POST", "/api/v1/tickets", create, tok)
	if rr.Code != http.StatusCreated {
		t.Fatalf("with the step-up: %d %s", rr.Code, rr.Body)
	}
	var tk struct{ Ticket string }
	_ = json.Unmarshal(rr.Body.Bytes(), &tk)

	for _, widen := range []map[string]any{
		{"spend_limit": map[string]any{"amount": "99999", "currency": "USD", "merchants": []string{"https://shop.example.com"}}},
		{"spend_limit": map[string]any{"amount": "120", "currency": "USD", "merchants": []string{"https://shop.example.com", "https://evil.example"}}},
		{"spend_limit": nil},
	} {
		if rr := g.dev.do("PATCH", "/api/v1/tickets/"+tk.Ticket, widen); rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), "stepup_required") {
			t.Fatalf("widening %v: %d %s", widen, rr.Code, rr.Body)
		}
	}
	lower := map[string]any{"spend_limit": map[string]any{"amount": "100", "currency": "USD", "merchants": []string{"https://shop.example.com"}}}
	if rr := g.dev.do("PATCH", "/api/v1/tickets/"+tk.Ticket, lower); rr.Code != http.StatusOK {
		t.Fatalf("lowering needs no step-up: %d %s", rr.Code, rr.Body)
	}
	// A step-up for one request doesn't confirm another.
	if rr := g.dev.doWith("PATCH", "/api/v1/tickets/"+tk.Ticket, map[string]any{"spend_limit": "500"}, tok); rr.Code != http.StatusUnauthorized {
		t.Fatalf("a spent or foreign step-up: %d %s", rr.Code, rr.Body)
	}
}

// A spend limit is confirmed for one bot: moving a ticket that has one to
// another bot, or reopening it, confirms it again — the browser.write
// step-up, its description naming the bot that will hold the budget. A
// change that confirms nothing new needs none.
func TestBotAPITicketSpendMoveNeedsAStepup(t *testing.T) {
	g := newBotRig(t, false)
	stepup := func(rr *httptest.ResponseRecorder) (string, string) {
		t.Helper()
		var refused struct {
			Stepup struct{ Purpose, Digest, Description string } `json:"stepup"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &refused)
		if rr.Code != http.StatusUnauthorized || refused.Stepup.Purpose != string(auth.PurposeBrowserWrite) {
			t.Fatalf("want a browser.write step-up: %d %s", rr.Code, rr.Body)
		}
		return refused.Stepup.Digest, refused.Stepup.Description
	}
	create := map[string]any{"title": "buy disks", "assignee": "devbot", "spend_limit": map[string]any{"amount": "120", "currency": "USD"}}
	d, desc := stepup(g.dev.do("POST", "/api/v1/tickets", create))
	if !strings.Contains(desc, "confirmed for bot:devbot") {
		t.Fatalf("the create's step-up doesn't name the bot: %s", desc)
	}
	rr := g.dev.doWith("POST", "/api/v1/tickets", create, sshStepupAs(t, g.dev, g.devKey, auth.PurposeBrowserWrite, d))
	var tk struct{ Ticket string }
	if _ = json.Unmarshal(rr.Body.Bytes(), &tk); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body)
	}
	path := "/api/v1/tickets/" + tk.Ticket

	if rr := g.dev.do("PATCH", path, map[string]any{"title": "buy two disks"}); rr.Code != http.StatusOK {
		t.Fatalf("a title edit needs no step-up: %d %s", rr.Code, rr.Body)
	}
	move := map[string]any{"assignee": "devbot2"}
	d, desc = stepup(g.dev.do("PATCH", path, move))
	if !strings.Contains(desc, "moves from bot:devbot to bot:devbot2") {
		t.Fatalf("the reassignment's step-up doesn't name the new holder: %s", desc)
	}
	if rr := g.dev.doWith("PATCH", path, move, sshStepupAs(t, g.dev, g.devKey, auth.PurposeBrowserWrite, d)); rr.Code != http.StatusOK {
		t.Fatalf("reassign with the step-up: %d %s", rr.Code, rr.Body)
	}
	if !strings.Contains(mustRead(t, g.auditLog), `"spend_for":"devbot2"`) {
		t.Fatal("the audit doesn't record whom the limit moved to")
	}

	if rr := g.dev.do("PATCH", path, map[string]any{"status": "declined", "note": "not now"}); rr.Code != http.StatusOK {
		t.Fatalf("declining needs no step-up: %d %s", rr.Code, rr.Body)
	}
	reopen := map[string]any{"status": "reopen"}
	d, desc = stepup(g.dev.do("PATCH", path, reopen))
	if !strings.Contains(desc, "reopened") || !strings.Contains(desc, "bot:devbot2") {
		t.Fatalf("the reopen's step-up: %s", desc)
	}
	if rr := g.dev.doWith("PATCH", path, reopen, sshStepupAs(t, g.dev, g.devKey, auth.PurposeBrowserWrite, d)); rr.Code != http.StatusOK {
		t.Fatalf("reopen with the step-up: %d %s", rr.Code, rr.Body)
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, _ := os.ReadFile(p)
	return string(b)
}

// doWith is do with a step-up token.
func (c *browserClient) doWith(method, path string, body any, stepup string) *httptest.ResponseRecorder {
	c.t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Host, r.RemoteAddr = c.host, c.addr
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(c.cookie)
	r.Header.Set(auth.CSRFHeader, c.csrf)
	r.Header.Set(auth.StepupHeader, stepup)
	rr := httptest.NewRecorder()
	c.h.ServeHTTP(rr, r)
	return rr
}

// An avatar apply from the dashboard never writes the config: it stages the
// change in the session's config edits, which then need a review and (per
// config_stepup) a confirmation like any other config change. Only an
// operator of the bot or team may stage it.
func TestBotAPIAvatarApplyOnlyStages(t *testing.T) {
	g := newBotRig(t, false)
	before := mustRead(t, g.s.deps.ConfigPath)
	if rr := g.dev.raw("POST", "/api/v1/bots/sre/avatar/apply", "application/x-www-form-urlencoded", []byte("token=plan-1")); rr.Code != http.StatusForbidden {
		t.Fatalf("dev staging sre's picture: %d %s", rr.Code, rr.Body)
	}
	rr := g.dev.raw("POST", "/api/v1/bots/devbot/avatar/apply", "application/x-www-form-urlencoded", []byte("token=plan-1"))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"staged":true`) {
		t.Fatalf("apply: %d %s", rr.Code, rr.Body)
	}
	if mustRead(t, g.s.deps.ConfigPath) != before {
		t.Fatal("an avatar apply wrote the config")
	}
	var cfg struct {
		Staged []struct{ Path []string } `json:"staged"`
	}
	_ = json.Unmarshal(g.dev.do("GET", "/api/v1/config", nil).Body.Bytes(), &cfg)
	if len(cfg.Staged) != 1 || strings.Join(cfg.Staged[0].Path, ".") != "bots.devbot.avatar" {
		t.Fatalf("staged: %+v", cfg.Staged)
	}
	var rev struct {
		Stepup      bool
		Description string
	}
	_ = json.Unmarshal(g.dev.do("GET", "/api/v1/config/review", nil).Body.Bytes(), &rev)
	if !rev.Stepup || !strings.Contains(rev.Description, `bots.devbot.avatar.image: (none) → "avatars/devbot-0123456789ab.png"`) {
		t.Fatalf("the review must ask for a confirmation and name the picture: %+v", rev)
	}
	_ = json.Unmarshal(g.ops.do("GET", "/api/v1/config", nil).Body.Bytes(), &cfg)
	if len(cfg.Staged) != 0 {
		t.Fatal("another session sees dev's staged picture")
	}
}

// A write route the chokepoint has no rule for is refused before its handler
// runs, so a route added to the bot API later can't skip the target check.
func TestBotAPIUnruledWriteRouteIsRefused(t *testing.T) {
	g := newBotRig(t, false)
	hit := false
	deps := g.s.deps
	deps.BotRoutes = append(append([]BotRoute(nil), deps.BotRoutes...), BotRoute{
		Pattern: "POST /v1/bots/{bot}/unruled",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }),
	})
	s := New(deps)
	c := *g.ops
	c.h = s.bindHandler(deps.Cfg.Dashboard.Bind[0])
	rr := c.raw("POST", "/api/v1/bots/sre/unruled", "application/json", []byte(`{"text":"hi"}`))
	if rr.Code != http.StatusForbidden || hit {
		t.Fatalf("an unruled write route: %d %s (handler ran: %v)", rr.Code, rr.Body, hit)
	}
	if rr := c.raw("GET", "/api/v1/bots/sre/unruled", "", nil); rr.Code == http.StatusOK {
		t.Fatalf("the route is POST only: %d", rr.Code)
	}
}
