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
	"strconv"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/configedit"
	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/dashboard/auth"
	"github.com/NodeSpy/conductor/internal/flow"
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

	// BotAPI is the bots' operator API (internal/bot.OperatorAPI plus the avatar
	// routes), which serves teams, conversations, tickets, messages, attachments
	// and avatars on `/v1/...`. The dashboard mounts it under `/api/` (design
	// §4.4), behind the same session guard as every other /api route. Nil when no
	// bots run on this daemon.
	BotAPI http.Handler

	// Browser is the browser/wallet surface (design §4.5): live sessions with a
	// screencast and takeover, personal details, payment cards and purchase
	// approvals. Nil when no browser runs on this daemon.
	Browser Browser
}

// StepupEvidence re-exports auth.StepupEvidence so a Deps caller (the daemon)
// need not import the auth subpackage to wire DecideProposal.
type StepupEvidence = authStepupEvidence

// Server is the dashboard HTTP server.
type Server struct {
	deps    Deps
	hub     *hub
	mux     *http.ServeMux
	started time.Time
	edit    *configedit.Pipeline // nil when ConfigPath is empty (read-only)
	auth    *auth.Manager        // nil until wired in New()
	tsAPI   localAPI             // tailscaled LocalAPI; nil = the real socket client
}

// New builds a dashboard server from its dependencies.
func New(deps Deps) *Server {
	if deps.Log == nil {
		deps.Log = func(string, ...any) {}
	}
	if deps.Paused == nil {
		deps.Paused = func() bool { return false }
	}
	s := &Server{deps: deps, hub: newHub(), mux: http.NewServeMux(), started: time.Now()}
	if deps.ConfigPath != "" {
		s.edit = configedit.New(configedit.Options{
			Path:     deps.ConfigPath,
			Validate: deps.Validate,
			Reload:   deps.Reload,
			Guarded:  guardedConfig,
		})
	}
	s.buildAuth() // the auth Manager + RPs (authwire.go); logs and no-ops on error
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
	if s.deps.BotAPI != nil {
		botH := s.botHandler()
		for _, p := range []string{
			"/api/v1/bots", "/api/v1/bots/",
			"/api/v1/teams", "/api/v1/teams/",
			"/api/v1/conversations", "/api/v1/conversations/",
			"/api/v1/tickets", "/api/v1/tickets/",
			"/api/v1/proposals",
		} {
			s.mux.Handle(p, botH)
		}
	}

	s.mux.Handle("/", spaHandler()) // the embedded SPA, with SPA fallback
}

// botHandler adapts the bots' operator API (which serves `/v1/...`) to the
// dashboard's `/api/v1/...` surface: it strips the `/api` prefix and, when a
// session is signed in and the caller named no operator, injects the session
// operator so writes are attributed to the signed-in human (design §6.6).
func (s *Server) botHandler() http.Handler {
	stripped := http.StripPrefix("/api", s.deps.BotAPI)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sess, ok := auth.SessionFrom(r); ok && sess.Operator != "" {
			if r.URL.Query().Get("operator") == "" {
				q := r.URL.Query()
				q.Set("operator", sess.Operator)
				r.URL.RawQuery = q.Encode()
			}
		}
		stripped.ServeHTTP(w, r)
	})
}

// Start binds the configured localhost listeners and serves until ctx is
// cancelled. It returns once the listeners are bound (serving continues in
// goroutines), or an error if no listener could be bound. In Phase 1 only the
// `localhost` keyword binds; `tailscale` is accepted by config but served in
// Phase 2.
func (s *Server) Start(ctx context.Context) error {
	// Bridge the flow event hub into our resumable hub for the lifetime of ctx.
	if s.deps.Events != nil {
		go s.hub.run(ctx, s.deps.Events)
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

// handleEvents is the SSE stream. ?topics= selects topics (default run,attention);
// the Last-Event-ID header (or ?last_event_id=) resumes a dropped stream from the
// hub's ring. Heartbeats go out every 15s so proxies and clients see liveness.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "no_stream", "streaming is not supported on this connection", "")
		return
	}

	topics := parseTopics(r.URL.Query().Get("topics"))
	lastID := int64(0)
	if h := r.Header.Get("Last-Event-ID"); h != "" {
		lastID, _ = strconv.ParseInt(h, 10, 64)
	} else if q := r.URL.Query().Get("last_event_id"); q != "" {
		lastID, _ = strconv.ParseInt(q, 10, 64)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering
	w.WriteHeader(http.StatusOK)

	ch, missed, reset, cancel := s.hub.subscribe(topics, lastID)
	defer cancel()

	if reset {
		// The client fell behind the ring: tell it to refetch its snapshots.
		writeSSE(w, api.Event{ID: s.hub.lastID(), Type: "reset", TS: time.Now().UTC().Format(time.RFC3339Nano)})
		flusher.Flush()
	}
	for _, ev := range missed {
		writeSSE(w, ev)
	}
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, ev)
			flusher.Flush()
		case <-heartbeat.C:
			// A comment line keeps the connection warm without an id or a topic.
			fmt.Fprintf(w, ": heartbeat %s\n\n", time.Now().UTC().Format(time.RFC3339))
			flusher.Flush()
		}
	}
}

func parseTopics(raw string) []api.EventTopic {
	if raw == "" {
		return []api.EventTopic{api.TopicRun, api.TopicAttention}
	}
	var out []api.EventTopic
	for _, part := range splitComma(raw) {
		switch api.EventTopic(part) {
		case api.TopicRun:
			out = append(out, api.TopicRun)
		case api.TopicAttention:
			out = append(out, api.TopicAttention)
		}
	}
	if len(out) == 0 {
		return []api.EventTopic{api.TopicRun, api.TopicAttention}
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
	if ev.ID > 0 {
		fmt.Fprintf(w, "id: %d\n", ev.ID)
	}
	if ev.Topic != "" {
		fmt.Fprintf(w, "event: %s\n", ev.Topic)
	}
	fmt.Fprintf(w, "data: %s\n\n", b)
}

// guardedConfig is the part of the loaded config that decides who can sign in
// to the dashboard and how. The config pipeline compares it before and after an
// edit, so a change that reaches it through imports or packs needs a step-up.
func guardedConfig(path string) (any, error) {
	c, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	return struct {
		Operators map[string]config.OperatorConfig
		Auth      config.DashboardAuth
		Bind      []config.DashboardBind
		Listen    []string
		Tailscale config.DashboardTailscale
		Session   config.DashboardSession
	}{c.Operators, c.Dashboard.Auth, c.Dashboard.Bind, c.Dashboard.Listen, c.Dashboard.Tailscale, c.Dashboard.Session}, nil
}
