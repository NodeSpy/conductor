// Package api holds the dashboard's HTTP request and response types — the
// contract shared by the Go handlers and the TypeScript client. The TS types in
// web/src/api/types.ts are generated from this file with tygo (see
// web/tygo.yaml and `make web-types`); this file is the single source of truth
// for the shapes the dashboard serves.
//
// Phase 1 covers the Overview/Activity slice: status, runs and the live event
// stream. The remaining endpoints in design §5 are added here as they are built.
package api

// StatusResponse is GET /api/v1/status: a snapshot of the daemon's identity and
// health, read entirely from live in-process state.
type StatusResponse struct {
	Version   string `json:"version"`    // conductor build version ("dev" for an unstamped build)
	PID       int    `json:"pid"`        // the daemon process id
	StartedAt string `json:"started_at"` // RFC3339 time the dashboard server came up
	UptimeSec int64  `json:"uptime_sec"` // seconds since StartedAt
	Paused    bool   `json:"paused"`     // dispatch is paused (the `conductor pause` control file is present)
	ConfigSHA string `json:"config_sha"` // short hex sha256 of the loaded config file
}

// RunSummary is one row of run history for the Activity list and the Overview.
// It is the display projection of store.RunHistory — timestamps as RFC3339
// strings, step count instead of the full step records.
type RunSummary struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	Workflow   string  `json:"workflow"` // the trigger variant name, falling back to Kind
	Repo       string  `json:"repo,omitempty"`
	Number     int     `json:"number,omitempty"`
	Status     string  `json:"status"`             // running | ok | failed | retried
	Started    string  `json:"started"`            // RFC3339
	Finished   string  `json:"finished,omitempty"` // RFC3339; empty while running
	DurationMS int64   `json:"duration_ms"`        // finished runs: Finished-Started; running: elapsed so far
	Error      string  `json:"error,omitempty"`
	FailedStep string  `json:"failed_step,omitempty"`
	Steps      int     `json:"steps"`
	RetryOf    string  `json:"retry_of,omitempty"`
	Tokens     int     `json:"tokens,omitempty"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
}

// RunsResponse is GET /api/v1/runs: a page of run history, newest first, with a
// cursor for the next page ("" when there is no more).
type RunsResponse struct {
	Runs []RunSummary `json:"runs"`
	Next string       `json:"next,omitempty"`
}

// EventTopic names a stream a client subscribes to via GET /api/v1/events?topics=.
type EventTopic string

const (
	// TopicRun carries run lifecycle events (run_started, step_started,
	// step_done, run_done), sourced from flow.EventHub.
	TopicRun EventTopic = "run"
	// TopicAttention carries items that need a person. In this slice it is fed
	// by failed runs; the full attention aggregator (design §4.1) is Phase 2.
	TopicAttention EventTopic = "attention"
)

// Event is one server-sent event. On the wire the SSE `id:` field carries ID
// (so a reconnect with Last-Event-ID resumes), `event:` carries Topic, and
// `data:` is the JSON encoding of this whole envelope.
type Event struct {
	ID    int64      `json:"id"`
	Topic EventTopic `json:"topic"`
	Type  string     `json:"type"` // run_started | step_started | step_done | run_done | attention | heartbeat | reset
	TS    string     `json:"ts"`   // RFC3339
	Data  any        `json:"data,omitempty"`
}

// RunEventData is the payload of a TopicRun event — a projection of
// flow.RunEvent. (It is also the shape of a TopicAttention event's data, which
// is a failed run_done in this slice.)
type RunEventData struct {
	Type       string `json:"type"`
	Run        string `json:"run"`
	RunID      string `json:"run_id,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Repo       string `json:"repo,omitempty"`
	Number     int    `json:"number,omitempty"`
	Step       string `json:"step,omitempty"`
	Status     string `json:"status,omitempty"`
	Detail     string `json:"detail,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// Error is one API error. Reads and writes that fail return {error: Error}.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// ErrorResponse is the body of any non-2xx JSON response.
type ErrorResponse struct {
	Error Error `json:"error"`
}
