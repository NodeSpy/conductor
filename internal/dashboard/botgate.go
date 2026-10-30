package dashboard

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/NodeSpy/conductor/internal/auditctx"
	"github.com/NodeSpy/conductor/internal/dashboard/auth"
)

// THE BOT API CHOKEPOINT (design §4.4). The bots' operator API names its
// operator in the request — a query parameter, a JSON key, a form field —
// because on its unix socket the caller is the local user, who may be any
// operator. Mounted on the dashboard, the signed-in session is the operator,
// on every route:
//
//   - a request that names another operator (query, JSON body, urlencoded or
//     multipart form) is refused 403, never rewritten;
//   - the session's operator is then set in the query and the body, so every
//     handler sees it whichever place it reads;
//   - a write needs an operator of the bot or team it acts on (botWriteRules);
//     a write route with no rule is refused, so a new route stays closed until
//     it has one;
//   - the session's attribution rides the request's context into the bot
//     service's audit rows, and a targeted write is audited here too.
//
// Reads follow the bot API's own rules: any operator reads any conversation.

// BotRoute is one route of the bots' operator API: its ServeMux pattern
// ("METHOD /v1/...") and handler.
type BotRoute struct {
	Pattern string
	Handler http.Handler
}

// BotAuthority is what the chokepoint asks the bot service.
type BotAuthority interface {
	// Operates reports whether operator op operates target: "bot:<name>",
	// "team:<name>", or a bare bot or team name; "" (the triage queue) is
	// every configured operator's.
	Operates(op, target string) bool
	// TicketOwner is who a ticket's writes answer to ("" in the triage queue).
	TicketOwner(id string) (string, error)
}

// botTargets names what a write acts on, given the request and its JSON body
// (nil when it has none). nil targets: the operator's own view state (read
// marks, pins), which needs no operator of anything.
type botTargets func(a BotAuthority, r *http.Request, body map[string]json.RawMessage) ([]string, error)

func pathTarget(kind, param string) botTargets {
	return func(_ BotAuthority, r *http.Request, _ map[string]json.RawMessage) ([]string, error) {
		return []string{kind + ":" + r.PathValue(param)}, nil
	}
}

func conversationTarget(_ BotAuthority, r *http.Request, _ map[string]json.RawMessage) ([]string, error) {
	return []string{r.PathValue("id")}, nil
}

func ownState(BotAuthority, *http.Request, map[string]json.RawMessage) ([]string, error) {
	return nil, nil
}

// jsonStr is a body key as the bot API's handlers read it (a string; anything
// else is "").
func jsonStr(body map[string]json.RawMessage, k string) (string, bool) {
	raw, ok := body[k]
	if !ok {
		return "", false
	}
	var v string
	_ = json.Unmarshal(raw, &v)
	return v, true
}

func ticketNewTarget(_ BotAuthority, _ *http.Request, body map[string]json.RawMessage) ([]string, error) {
	a, _ := jsonStr(body, "assignee")
	return []string{a}, nil
}

// ticketTarget is a ticket's owner, and on a reassignment the new assignee too.
func ticketTarget(a BotAuthority, r *http.Request, body map[string]json.RawMessage) ([]string, error) {
	owner, err := a.TicketOwner(r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	out := []string{owner}
	if to, ok := jsonStr(body, "assignee"); ok && r.Method == http.MethodPatch {
		out = append(out, to)
	}
	return out, nil
}

// botWriteRules is every write route the dashboard lets through, by pattern.
var botWriteRules = map[string]botTargets{
	"POST /v1/bots/{bot}/messages":         pathTarget("bot", "bot"),
	"POST /v1/conversations/{id}/messages": conversationTarget,
	"POST /v1/conversations/{id}/read":     ownState,
	"POST /v1/conversations/{id}/pin":      ownState,
	"DELETE /v1/conversations/{id}/pin":    ownState,
	"PUT /v1/conversations/pins":           ownState,
	"POST /v1/tickets":                     ticketNewTarget,
	"PATCH /v1/tickets/{id}":               ticketTarget,
	"POST /v1/tickets/{id}/comments":       ticketTarget,
	"POST /v1/bots/{name}/avatar/plan":     pathTarget("bot", "name"),
	"POST /v1/bots/{name}/avatar/apply":    pathTarget("bot", "name"),
	"POST /v1/teams/{name}/avatar/plan":    pathTarget("team", "name"),
	"POST /v1/teams/{name}/avatar/apply":   pathTarget("team", "name"),
}

// BotWriteRuled reports whether the chokepoint has a rule for a write route.
func BotWriteRuled(pattern string) bool { _, ok := botWriteRules[pattern]; return ok }

const (
	// botGateSmallBody caps a JSON or urlencoded body (the bot API's own caps
	// are lower).
	botGateSmallBody = 4 << 20
	// botGateUpload caps a multipart body, spooled to disk while it is read.
	botGateUpload = 128 << 20
)

func isRead(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// mountBotAPI puts every bot API route behind the chokepoint and mounts the
// result under /api.
func (s *Server) mountBotAPI() {
	inner := http.NewServeMux()
	for _, rt := range s.deps.BotRoutes {
		inner.Handle(rt.Pattern, s.botGate(rt))
	}
	h := http.StripPrefix("/api", inner)
	for _, p := range []string{
		"/api/v1/bots", "/api/v1/bots/",
		"/api/v1/teams", "/api/v1/teams/",
		"/api/v1/conversations", "/api/v1/conversations/",
		"/api/v1/tickets", "/api/v1/tickets/",
		"/api/v1/proposals",
	} {
		s.mux.Handle(p, h)
	}
}

func botRefuse(w http.ResponseWriter, code int, msg string) {
	slug := map[int]string{http.StatusForbidden: "forbidden", http.StatusBadRequest: "bad_request",
		http.StatusRequestEntityTooLarge: "too_large", http.StatusNotFound: "not_found", http.StatusUnauthorized: "unauthenticated"}[code]
	writeError(w, code, slug, msg, "")
}

// botGate is the chokepoint in front of one route.
func (s *Server) botGate(rt BotRoute) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := auth.SessionFrom(r)
		if !ok {
			if s.authEnforced() {
				botRefuse(w, http.StatusUnauthorized, "sign in to continue")
				return
			}
			rt.Handler.ServeHTTP(w, r) // sign-in off: no session to speak for
			return
		}
		op := sess.Operator
		if op == "" {
			botRefuse(w, http.StatusForbidden, "this session has no operator")
			return
		}
		mismatch := func(v string) bool { return v != "" && v != op }

		u := *r.URL
		q := u.Query()
		for _, v := range q["operator"] {
			if mismatch(v) {
				botRefuse(w, http.StatusForbidden, "this session is operator "+op+": it can't act as "+v)
				return
			}
		}
		q.Set("operator", op)
		u.RawQuery = q.Encode()
		r2 := r.WithContext(auditctx.With(r.Context(), sess.Attribution()))
		r2.URL = &u

		var body map[string]json.RawMessage
		if !isRead(r.Method) {
			nb, cleanup, b, code, msg := s.botBody(r, op)
			defer cleanup()
			if code != 0 {
				botRefuse(w, code, msg)
				return
			}
			body = b
			if nb != nil {
				r2.Body, r2.ContentLength = nb.rc, nb.n
				r2.Header = r.Header.Clone()
				r2.Header.Set("Content-Length", strconv.FormatInt(nb.n, 10))
			}
		}
		if isRead(r.Method) {
			rt.Handler.ServeHTTP(w, r2)
			return
		}

		rule, ok := botWriteRules[rt.Pattern]
		if !ok {
			botRefuse(w, http.StatusForbidden, "the dashboard has no rule for "+rt.Pattern)
			return
		}
		targets, err := rule(s.deps.BotAuthority, r2, body)
		if err != nil {
			botRefuse(w, http.StatusNotFound, err.Error())
			return
		}
		for _, t := range targets {
			if s.deps.BotAuthority == nil || !s.deps.BotAuthority.Operates(op, t) {
				what := t
				if what == "" {
					what = "the triage queue"
				}
				botRefuse(w, http.StatusForbidden, op+" is not an operator of "+what)
				return
			}
		}
		if targets == nil {
			rt.Handler.ServeHTTP(w, r2)
			return
		}
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		rt.Handler.ServeHTTP(sw, r2)
		s.audit(r2, "dashboard.botapi", "", "route", rt.Pattern, "target", strings.Join(targets, ","), "status", strconv.Itoa(sw.code))
	})
}

type statusWriter struct {
	http.ResponseWriter
	code  int
	wrote bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.code, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type newBody struct {
	rc io.ReadCloser
	n  int64
}

// botBody reads a write's body and checks every place a handler could read an
// operator from it. Which handler reads what doesn't depend on the
// Content-Type alone — the JSON handlers decode whatever arrives — so:
//
//   - multipart: every non-file `operator` part must be the session's (the
//     handlers' FormValue reads the query first, which is set to it); a
//     preamble that a JSON decoder would read is refused;
//   - urlencoded: `operator` must be the session's, and is set to it; a body
//     a JSON decoder would read is refused;
//   - anything else that starts as a JSON object is decoded (one object,
//     nothing after it), every key that folds to "operator" — as
//     encoding/json matches it — must be the session's, and the body is
//     re-encoded with only `operator: <session>`.
//
// It returns the replacement body (nil: unchanged), a cleanup, the JSON body
// when there was one, and a refusal (code 0: none).
func (s *Server) botBody(r *http.Request, op string) (*newBody, func(), map[string]json.RawMessage, int, string) {
	none := func() {}
	ct, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct == "multipart/form-data" {
		f, err := os.CreateTemp("", "conductor-botgate-*")
		if err != nil {
			return nil, none, nil, http.StatusInternalServerError, "can't spool the upload"
		}
		cleanup := func() { f.Close(); os.Remove(f.Name()) }
		n, err := io.Copy(f, io.LimitReader(r.Body, botGateUpload+1))
		if err != nil {
			return nil, cleanup, nil, http.StatusBadRequest, "reading the upload: " + err.Error()
		}
		if n > botGateUpload {
			return nil, cleanup, nil, http.StatusRequestEntityTooLarge, "the upload is too large"
		}
		_, _ = f.Seek(0, io.SeekStart)
		if startsJSONObject(bufio.NewReader(f)) {
			return nil, cleanup, nil, http.StatusBadRequest, "a multipart body can't start with a JSON object"
		}
		_, _ = f.Seek(0, io.SeekStart)
		mr := multipart.NewReader(f, params["boundary"])
		for {
			p, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, cleanup, nil, http.StatusBadRequest, "reading the upload: " + err.Error()
			}
			if p.FormName() == "operator" && p.FileName() == "" {
				v, _ := io.ReadAll(io.LimitReader(p, 4096))
				if string(v) != "" && string(v) != op {
					return nil, cleanup, nil, http.StatusForbidden, "this session is operator " + op + ": it can't act as " + string(v)
				}
			}
		}
		_, _ = f.Seek(0, io.SeekStart)
		return &newBody{rc: io.NopCloser(f), n: n}, cleanup, nil, 0, ""
	}

	b, err := io.ReadAll(io.LimitReader(r.Body, botGateSmallBody+1))
	if err != nil {
		return nil, none, nil, http.StatusBadRequest, "reading the body: " + err.Error()
	}
	if len(b) > botGateSmallBody {
		return nil, none, nil, http.StatusRequestEntityTooLarge, "the body is too large"
	}
	refuse := func(v string) (*newBody, func(), map[string]json.RawMessage, int, string) {
		return nil, none, nil, http.StatusForbidden, "this session is operator " + op + ": it can't act as " + v
	}
	if ct == "application/x-www-form-urlencoded" {
		if jsonish(b) {
			return nil, none, nil, http.StatusBadRequest, "a form body can't be a JSON object"
		}
		vals, err := url.ParseQuery(string(b))
		if err != nil {
			return nil, none, nil, http.StatusBadRequest, "malformed form body"
		}
		for _, v := range vals["operator"] {
			if v != "" && v != op {
				return refuse(v)
			}
		}
		vals.Set("operator", op)
		enc := vals.Encode()
		return &newBody{rc: io.NopCloser(strings.NewReader(enc)), n: int64(len(enc))}, none, nil, 0, ""
	}
	if !jsonish(b) {
		return &newBody{rc: io.NopCloser(bytes.NewReader(b)), n: int64(len(b))}, none, nil, 0, ""
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	var body map[string]json.RawMessage
	if err := dec.Decode(&body); err != nil {
		return nil, none, nil, http.StatusBadRequest, "malformed JSON body"
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, none, nil, http.StatusBadRequest, "one JSON object, and nothing after it"
	}
	for k, raw := range body {
		if !strings.EqualFold(k, "operator") {
			continue
		}
		var v *string
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, none, nil, http.StatusBadRequest, "operator must be a string"
		}
		if v != nil && *v != "" && *v != op {
			return refuse(*v)
		}
		delete(body, k)
	}
	body["operator"], _ = json.Marshal(op)
	enc, err := json.Marshal(body)
	if err != nil {
		return nil, none, nil, http.StatusBadRequest, "malformed JSON body"
	}
	return &newBody{rc: io.NopCloser(bytes.NewReader(enc)), n: int64(len(enc))}, none, body, 0, ""
}

// startsJSONObject is jsonish for a reader.
func startsJSONObject(br *bufio.Reader) bool {
	for {
		c, err := br.ReadByte()
		if err != nil {
			return false
		}
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		}
		return c == '{'
	}
}

// jsonish reports whether a JSON decoder would read b as an object: its first
// byte past JSON whitespace is '{'.
func jsonish(b []byte) bool {
	b = bytes.TrimLeft(b, " \t\r\n")
	return len(b) > 0 && b[0] == '{'
}

// BotWriteRulePatterns is every pattern the chokepoint has a write rule for.
func BotWriteRulePatterns() []string {
	out := make([]string, 0, len(botWriteRules))
	for p := range botWriteRules {
		out = append(out, p)
	}
	return out
}
