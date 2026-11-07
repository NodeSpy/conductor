// Package dashboard is conductor's local web surface. The HTTP server runs
// inside the daemon process (started next to serveControl in cmd/conductor) and
// shares the daemon's live in-process handles — the loaded *config.Config, the
// *store.Store and the *flow.EventHub — so every read reflects the running
// state without re-dialing the control socket.
//
// Phase 1 is the Overview/Activity vertical slice: GET /api/v1/status, GET
// /api/v1/runs and the GET /api/v1/events SSE stream, plus the embedded SPA.
// It binds localhost only and has no auth; the tailnet listener and WebAuthn
// arrive in Phase 2 (design §6).
package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/attention"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/configedit"
	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/dashboard/auth"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/journal"
	"github.com/NodeSpy/conductor/internal/store"
)

// authStepupEvidence is the type behind the exported StepupEvidence alias.
type authStepupEvidence = auth.StepupEvidence

// Deps are the live handles the dashboard reads from. They are the SAME handles
// the daemon and CLI use — nothing here opens a second store or dials a socket.
type Deps struct {
	Cfg       *config.Config
	Store     *store.Store
	Events    *flow.EventHub
	Version   string
	ConfigSHA string
	Paused    func() bool // reports whether dispatch is paused (the pause control file)
	Log       func(string, ...any)

	// Config-edit pipeline (design §5.3). ConfigPath is the live config file;
	// Validate runs the daemon's own load+semantic pass against a candidate
	// copy; Reload signals the daemon to re-read its config (SIGHUP). When
	// ConfigPath is empty the config-edit endpoints report that editing is
	// unavailable (a read-only dashboard).
	ConfigPath string
	Validate   func(path string) error
	Reload     func() error
	AuditPath  string // the audit log file (JSONL), for the Logs & audit screen

	// Auth (design §6). When Operators returns one or more operators the
	// dashboard enforces passkey sign-in on every /api call and a step-up on
	// sensitive ones; with no operators configured (a bootstrap box, the e2e
	// daemons) it serves localhost open, since nobody could sign in anyway. The
	// map is operator -> authorized_keys public key, for the enrollment SSH gate.
	Operators func() map[string]string
	// StateDir is where the auth store lives (<state>/dashboard). Defaults to a
	// "dashboard" dir beside the state file when empty.
	StateDir string
	// Lifecycle fires conductor.* events (dashboard_enrolled, dashboard_reset).
	Lifecycle func(event, operator, detail string)
	// Now is an injectable clock (tests); defaults to time.Now.
	Now func() time.Time

	// Approval bridge (design §6.6). ApprovalView returns a proposal's canonical
	// approval digest and a human summary rendered from the payload (never agent
	// text), plus the operators who may decide. DecideProposal applies a decision
	// authenticated by a verified WebAuthn step-up. Both nil = no bots on this
	// daemon (the proposal endpoints report so).
	ApprovalView   func(bot, id string) (digest, summaryJSON string, operators []string, ok bool)
	DecideProposal func(ctx context.Context, bot, id, decision, operator, reason string, ev StepupEvidence) error

	// Run controls (design §4.3). Retry re-runs a recorded execution from a step
	// (the engine's RetryRunByID); Stop cancels a live run by its history id;
	// RunActive reports whether a run is still executing in this process (the
	// history record lags). All nil on a read-only daemon — the endpoints then
	// report the control is unavailable.
	Retry     func(ctx context.Context, id, fromStep string, force bool) (string, error)
	Stop      func(ctx context.Context, id string) error
	RunActive func(id string) bool

	// BotRoutes is the bots' operator API (internal/bot.OperatorRoutes plus the
	// avatar and wallet routes), which serves teams, conversations, tickets,
	// messages, attachments and avatars on `/v1/...`. The dashboard mounts it
	// under `/api/` (design §4.4), behind the same session guard as every other
	// /api route and the bot API chokepoint (botgate.go). BotAuthority answers
	// the chokepoint's questions. Nil when no bots run on this daemon.
	BotRoutes    []BotRoute
	BotAuthority BotAuthority
	// AvatarStage takes a planned bot or team avatar change by its plan token
	// and returns it as a config edit (path, value, or a delete), having
	// written the picture under a name nothing references yet. The dashboard's
	// avatar apply stages that edit for the session, so the config pipeline
	// reviews, confirms, backs up, audits and applies it. Nil: avatar apply is
	// refused.
	AvatarStage func(kind, name, token string) (path []string, value any, del bool, err error)
	// AvatarCollect removes uploaded pictures nothing references: the config
	// pipeline's Collect, run after each apply or restore under its lock with
	// every text that is or may become the config. Optional.
	AvatarCollect func(refs [][]byte)

	// Browser is the browser/wallet surface (design §4.5): live sessions with a
	// screencast and takeover, personal details, payment cards and purchase
	// approvals. Nil when no browser runs on this daemon.
	Browser Browser

	// Journal is the daemon's durable event log and Attention its view of
	// what needs an operator (design: event-journal). The daemon records run
	// events into the journal itself (RecordRunEvent from the flow hub's tap).
	// Nil Journal: the server keeps a private one for its lifetime and bridges
	// Events into it; nil Attention: a private, unpersisted view.
	Journal   *journal.Journal
	Attention *attention.Service
}

// StepupEvidence re-exports auth.StepupEvidence so a Deps caller (the daemon)
// need not import the auth subpackage to wire DecideProposal.
type StepupEvidence = authStepupEvidence

// Server is the dashboard HTTP server.
type Server struct {
	deps      Deps
	journal   *journal.Journal
	attention *attention.Service
	bridge    bool // journal is private: feed it from deps.Events
	mux       *http.ServeMux
	started   time.Time
	edit      *configedit.Pipeline // nil when ConfigPath is empty (read-only)
	auth      *auth.Manager        // nil until wired in New()
	authErr   error                // why auth could not be wired; the API is refused while set
	tsAPI     localAPI             // tailscaled LocalAPI; nil = the real socket client
	labels    *stepupLabels        // what each handed-out step-up digest confirms, for approve-login
	quiet     *applyBudget         // config applies that needed no step-up, rate-limited
}

// New builds a dashboard server from its dependencies.
func New(deps Deps) *Server {
	if deps.Log == nil {
		deps.Log = func(string, ...any) {}
	}
	if deps.Paused == nil {
		deps.Paused = func() bool { return false }
	}
	s := &Server{deps: deps, journal: deps.Journal, attention: deps.Attention, mux: http.NewServeMux(), started: time.Now(), labels: newStepupLabels(deps.Now),
		quiet: newApplyBudget(deps.Now, quietBudgetPath(deps))}
	if deps.ConfigPath != "" {
		s.edit = configedit.New(configedit.Options{
			Path:     deps.ConfigPath,
			Validate: deps.Validate,
			Reload:   deps.Reload,
			Guarded:  guardedConfig,
			Collect:  deps.AvatarCollect,
			// A full table of staged sets reclaims ended sessions' (with
			// sign-in off there is only the unowned set).
			Live: func(owner string) bool { return s.auth == nil || s.auth.SessionLive(owner) },
		})
	}
	if s.journal == nil {
		s.journal, s.bridge = privateJournal(deps.Log), true
	}
	if s.attention == nil {
		s.attention = attention.New(attention.Options{Journal: s.journal, Log: deps.Log})
	}
	s.buildAuth() // the auth Manager + RPs (authwire.go); on error the API is refused (authErr)
	s.routes()
	s.authRoutes()    // the proposal-approval endpoints (behind the session guard)
	s.browserRoutes() // browsers, the wallet and purchase approvals (§4.5)
	return s
}

// routes registers the API and SPA handlers.
func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/v1/status", s.handleStatus)
	s.mux.HandleFunc("GET /api/v1/runs", s.handleRuns)
	s.mux.HandleFunc("GET /api/v1/runs/{id}", s.handleRunDetail)
	s.mux.HandleFunc("POST /api/v1/runs/{id}/retry", s.handleRetry)
	s.mux.HandleFunc("POST /api/v1/runs/{id}/stop", s.handleStop)
	s.mux.HandleFunc("GET /api/v1/events", s.handleEvents)
	s.mux.HandleFunc("GET /api/v1/events/since", s.handleEventsSince)
	s.mux.HandleFunc("GET /api/v1/attention", s.handleAttention)
	s.mux.HandleFunc("POST /api/v1/attention/{key}/dismiss", s.handleAttentionDismiss)

	// Workflows (design §4.4).
	s.mux.HandleFunc("GET /api/v1/workflows", s.handleWorkflows)
	s.mux.HandleFunc("GET /api/v1/workflows/{name}", s.handleWorkflow)
	s.mux.HandleFunc("POST /api/v1/workflows/{name}/test", s.handleWorkflowTest)

	// Connector-published filters (design §4.2).
	s.mux.HandleFunc("GET /api/v1/connectors/attrs", s.handleConnectorAttrs)
	s.mux.HandleFunc("GET /api/v1/connectors/attrs/values", s.handleAttrValues)

	// Config-edit pipeline.
	s.mux.HandleFunc("GET /api/v1/config", s.handleConfig)
	s.mux.HandleFunc("POST /api/v1/config/stage", s.handleStage)
	s.mux.HandleFunc("POST /api/v1/config/unstage", s.handleUnstage)
	s.mux.HandleFunc("POST /api/v1/config/clear", s.handleClear)
	s.mux.HandleFunc("GET /api/v1/config/review", s.handleReview)
	s.mux.HandleFunc("GET /api/v1/config/validate", s.handleValidate)
	s.mux.HandleFunc("POST /api/v1/config/apply", s.handleApply)
	s.mux.HandleFunc("GET /api/v1/config/history", s.handleHistory)
	s.mux.HandleFunc("POST /api/v1/config/restore/review", s.handleRestoreReview)
	s.mux.HandleFunc("POST /api/v1/config/restore", s.handleRestore)

	// Section read models.
	s.mux.HandleFunc("GET /api/v1/secrets", s.handleSecrets)
	s.mux.HandleFunc("GET /api/v1/packs", s.handlePacks)
	s.mux.HandleFunc("GET /api/v1/plugins", s.handlePlugins)
	s.mux.HandleFunc("GET /api/v1/connections", s.handleConnections)
	s.mux.HandleFunc("GET /api/v1/system", s.handleSystem)
	s.mux.HandleFunc("GET /api/v1/audit", s.handleAudit)

	// The bots' operator API (design §4.4): teams, conversations (chat), tickets,
	// messages, attachments, avatars and the bot read models. It serves `/v1/...`,
	// so we mount it under `/api/` (strip the prefix) on the paths it owns. The
	// approval view/decision endpoints stay on the dashboard's own auth mux
	// (authRoutes), which carries the step-up; the more-specific pattern wins, so
	// the `/api/v1/bots/` subtree below never shadows them.
	if s.deps.BotRoutes != nil {
		s.mountBotAPI()
	} else {
		s.mountNoBots()
	}

	// An API path nothing serves is a JSON 404, never the SPA's HTML.
	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "No such API endpoint: "+r.URL.Path, "")
	})
	s.mux.Handle("/", spaHandler()) // the embedded SPA, with SPA fallback
}

// Start binds the configured localhost listeners and serves until ctx is
// cancelled. It returns once the listeners are bound (serving continues in
// goroutines), or an error if no listener could be bound. In Phase 1 only the
// `localhost` keyword binds; `tailscale` is accepted by config but served in
// Phase 2.
func (s *Server) Start(ctx context.Context) error {
	// Without the daemon's journal, bridge the flow hub into the private one.
	if s.bridge && s.deps.Events != nil {
		go s.bridgeRuns(ctx, s.deps.Events)
	}

	port := s.deps.Cfg.Dashboard.EffectivePort()
	// The localhost listener serves plain HTTP (a potentially-trustworthy origin,
	// so WebAuthn works) with every request tagged as arriving on localhost.
	srv := &http.Server{
		Handler:           s.listenerHandler(auth.ListenerLocalhost),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	var bound int
	// Bind 127.0.0.1 and ::1 explicitly (a bare "localhost" would resolve to
	// only one). The same handler serves both.
	for _, addr := range []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), net.JoinHostPort("::1", strconv.Itoa(port))} {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			s.deps.Log("dashboard: could not bind %s: %v", addr, err)
			continue
		}
		bound++
		go func(ln net.Listener, addr string) {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.deps.Log("dashboard: serve %s stopped: %v", addr, err)
			}
		}(ln, addr)
	}
	if bound == 0 {
		return fmt.Errorf("dashboard: no localhost listener could be bound on port %d", port)
	}

	// The tailnet listener (design §6.2): bind the node's tailnet addresses with
	// the MagicDNS certificate. It degrades to localhost-only (not fail-closed) if
	// tailscaled is absent.
	if s.deps.Cfg.Dashboard.BindsTailscale() {
		if err := s.startTailnet(ctx, port); err != nil {
			s.deps.Log("dashboard: tailnet listener not started (%v); serving localhost only", err)
		}
	}
	// Explicit listeners (dashboard.bind). A failure is logged, not fatal: the
	// localhost listener still serves, as with the tailnet one.
	for _, b := range s.deps.Cfg.Dashboard.Bind {
		if err := s.startBind(ctx, b); err != nil {
			s.deps.Log("dashboard: %v", err)
		}
	}

	// Close the server when ctx is cancelled (the daemon re-execs on reload).
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	s.deps.Log("dashboard: serving on http://localhost:%d (auth %s)", port, s.authState())
	return nil
}

// Handler exposes the mux for in-process testing (httptest).
func (s *Server) Handler() http.Handler { return s.mux }

// --- handlers ---

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	resp := api.StatusResponse{
		Version:   s.deps.Version,
		PID:       pid(),
		StartedAt: s.started.UTC().Format(time.RFC3339),
		UptimeSec: int64(time.Since(s.started).Seconds()),
		Paused:    s.deps.Paused(),
		ConfigSHA: s.deps.ConfigSHA,
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleRuns serves a page of run history, newest first. Filters: ?since (a Go
// duration like "12h" or an RFC3339 instant) and ?limit (default 50, max 200);
// ?cursor pages backwards in time (the Next from the previous page).
func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 200 {
		limit = 200
	}

	recs := s.deps.Store.ListHistory(0) // newest first (by Started)

	var sinceCut time.Time
	if v := r.URL.Query().Get("since"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			sinceCut = time.Now().Add(-d)
		} else if t, err := time.Parse(time.RFC3339, v); err == nil {
			sinceCut = t
		}
	}
	var cursorBefore time.Time
	if v := r.URL.Query().Get("cursor"); v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			cursorBefore = t
		}
	}

	q := r.URL.Query()
	out := make([]api.RunSummary, 0, limit)
	for _, rec := range recs {
		if !sinceCut.IsZero() && rec.Started.Before(sinceCut) {
			continue
		}
		if !cursorBefore.IsZero() && !rec.Started.Before(cursorBefore) {
			continue // already returned on a previous page
		}
		// Connector-qualifier filters (?github.repo=owner/name). conductor's own
		// concepts (status/workflow) are filtered client-side in the list.
		if !matchesAttrs(rec, q) {
			continue
		}
		out = append(out, toSummary(rec))
		if len(out) >= limit {
			break
		}
	}

	resp := api.RunsResponse{Runs: out}
	// A full page implies there may be more: hand back a cursor at the oldest
	// row's start time.
	if len(out) == limit && len(out) > 0 {
		resp.Next = out[len(out)-1].Started
	}
	writeJSON(w, http.StatusOK, resp)
}

// toSummary projects a durable run record onto the display shape.
func toSummary(rec store.RunHistory) api.RunSummary {
	workflow := rec.Variant
	if workflow == "" {
		workflow = rec.Kind
	}
	// A manual run carries conductor's internal "manual:<name>#<hash>" key in
	// place of a connector target; it is not a repo to display, so drop it (the
	// workflow name already identifies the run).
	repo, number := rec.Repo, rec.Number
	if strings.HasPrefix(repo, "manual:") {
		repo, number = "", 0
	}
	var dur int64
	switch {
	case !rec.Finished.IsZero():
		dur = rec.Finished.Sub(rec.Started).Milliseconds()
	case rec.Status == "running":
		dur = time.Since(rec.Started).Milliseconds()
	}
	s := api.RunSummary{
		ID: rec.ID, Kind: rec.Kind, Workflow: workflow,
		Repo: repo, Number: number, Status: rec.Status,
		Started: rec.Started.UTC().Format(time.RFC3339Nano),
		Error:   rec.Error, FailedStep: rec.FailedStep, RetryOf: rec.RetryOf,
		Steps: len(rec.Steps), Tokens: rec.Tokens, CostUSD: rec.CostUSD,
		DurationMS: dur,
	}
	if !rec.Finished.IsZero() {
		s.Finished = rec.Finished.UTC().Format(time.RFC3339Nano)
	}
	return s
}

// handleEvents is the SSE stream over the journal. ?topics= selects topics
// (default run,attention); the Last-Event-ID header (or ?cursor=) resumes
// after that event, replaying from the journal however long the client was
// away. A cursor that can't be continued (another epoch, aged out) gets one
// reset event first. Heartbeats go out every 15s so proxies and clients see
// liveness.
//
// The stream never skips an event for a slow client: it reads the journal at
// the client's pace.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "no_stream", "streaming is not supported on this connection", "")
		return
	}
	topics := parseTopics(r.URL.Query().Get("topics"))
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = r.URL.Query().Get("cursor")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	ctx := r.Context()
	for {
		changed := s.journal.Changed()
		evs, next, reset, err := s.journal.Read(cursor, topics, 256)
		if err != nil {
			s.deps.Log("dashboard: events: %v", err)
			return
		}
		if reset {
			writeSSE(w, api.Event{ID: next, Topic: api.TopicReset, Type: "reset", TS: time.Now().UTC().Format(time.RFC3339Nano)})
		}
		for _, ev := range evs {
			writeSSE(w, apiEvent(ev))
		}
		if reset || len(evs) > 0 {
			flusher.Flush()
		}
		cursor = next
		if len(evs) == 256 {
			continue // more to catch up on
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-heartbeat.C:
			// A comment line keeps the connection warm without an id or a topic.
			fmt.Fprintf(w, ": heartbeat %s\n\n", time.Now().UTC().Format(time.RFC3339))
			flusher.Flush()
		}
	}
}

// handleEventsSince is the journal as JSON pages: the events after ?cursor=
// (?topics=, ?limit= up to 1000), for a client catching up without holding a
// stream. An empty cursor returns no events and the current position.
func (s *Server) handleEventsSince(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 256
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 {
		limit = min(v, 1000)
	}
	evs, next, reset, err := s.journal.Read(q.Get("cursor"), parseTopics(q.Get("topics")), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "journal", err.Error(), "")
		return
	}
	page := api.EventsPage{Events: make([]api.Event, 0, len(evs)), Cursor: next, Reset: reset}
	for _, ev := range evs {
		page.Events = append(page.Events, apiEvent(ev))
	}
	writeJSON(w, http.StatusOK, page)
}

// handleAttention is the attention view: the open items and the journal
// cursor they reflect.
func (s *Server) handleAttention(w http.ResponseWriter, r *http.Request) {
	items, cursor := s.attention.Snapshot()
	resp := api.AttentionResponse{Items: make([]api.AttentionItem, 0, len(items)), Cursor: cursor}
	for _, it := range items {
		resp.Items = append(resp.Items, api.AttentionItem{
			Key: it.Key, Kind: it.Kind, Title: it.Title, Detail: it.Detail, Link: it.Link,
			Subject: it.Subject, Severity: it.Severity, Opened: it.Opened.UTC().Format(time.RFC3339Nano),
			Source: it.Source,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAttentionDismiss closes a raised attention item. Items a source
// reports close when they are dealt with, not by dismissing them.
func (s *Server) handleAttentionDismiss(w http.ResponseWriter, r *http.Request) {
	switch err := s.attention.Dismiss(r.PathValue("key")); {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"dismissed": true})
	case errors.Is(err, attention.ErrUnknown):
		writeError(w, http.StatusNotFound, "not_found", err.Error(), "")
	default:
		writeError(w, http.StatusConflict, "not_dismissable", err.Error(), "")
	}
}

// parseTopics reads ?topics=: a comma list of journal topics, default run and
// attention.
func parseTopics(raw string) []string {
	out := splitComma(raw)
	if len(out) == 0 {
		return []string{string(api.TopicRun), string(api.TopicAttention)}
	}
	return out
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// --- wire helpers ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, errCode, msg, hint string) {
	writeJSON(w, code, api.ErrorResponse{Error: api.Error{Code: errCode, Message: msg, Hint: hint}})
}

// writeSSE writes one event in the SSE wire format: an `id:` (so Last-Event-ID
// works), an `event:` topic, and the envelope JSON as `data:`.
func writeSSE(w http.ResponseWriter, ev api.Event) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if ev.ID != "" {
		fmt.Fprintf(w, "id: %s\n", ev.ID)
	}
	if ev.Topic != "" {
		fmt.Fprintf(w, "event: %s\n", ev.Topic)
	}
	fmt.Fprintf(w, "data: %s\n\n", b)
}

// dashboardStateDir is where the dashboard keeps its state: Deps.StateDir, or
// a "dashboard" dir beside the state file. "" when there is neither.
func dashboardStateDir(deps Deps) string {
	if deps.StateDir != "" {
		return deps.StateDir
	}
	if deps.Cfg != nil && deps.Cfg.Store.StateFile != "" {
		return filepath.Join(filepath.Dir(deps.Cfg.Store.StateFile), "dashboard")
	}
	return ""
}

// quietBudgetPath is where the quiet-apply window is kept, so the restart
// each apply causes doesn't reset it.
func quietBudgetPath(deps Deps) string {
	if dir := dashboardStateDir(deps); dir != "" {
		return filepath.Join(dir, "quiet-applies.json")
	}
	return ""
}

// guardedConfig is the loaded config minus its inert parts (pricing and
// usage.mode, the same paths configedit treats as inert; configedit judges a
// pricing change on the text, where lowering a price is a widening). The config pipeline
// compares it before and after an edit, so a change that reaches anything else
// through imports, packs or YAML references needs a step-up too.
func guardedConfig(path string) (any, error) {
	c, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	c.Pricing = nil
	if c.Usage != nil {
		u := *c.Usage
		u.Mode = ""
		c.Usage = &u
		if len(u.Sources) == 0 {
			c.Usage = nil // a usage block with only a mode is no usage block
		}
	}
	return c, nil
}
