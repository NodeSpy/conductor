// Package dispatch executes resolved actions against a backend: the "paseo"
// backend launches a coding agent via the paseo CLI; the "local" backend runs a
// deterministic command as a direct subprocess. Reads use the App token; git
// pushes go over SSH as you; API posts use your token (see ghwrite.go).
package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"text/template"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/hosts"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// Tokens carries the two credentials dispatched work may need.
type Tokens struct {
	App  string // GitHub App installation token (reads)
	User string // your `gh auth token` (writes/posts)
}

// Author is the git identity to attribute commits to (you).
type Author struct {
	Name  string
	Email string
}

// Request is a fully-resolved unit of dispatch.
type Request struct {
	Trigger core.Trigger
	Action  config.Action
	// Step carries the dispatch's behavior — model, runtime, guidance,
	// skill, session, workspace, timeouts, isolation. It replaced the retired
	// agent PROFILE (docs/design/agents-removal.md §6): the fields always
	// described the step, so they live on it.
	Step config.Step
	// Identity is the step's stable identity (config.Step.Identity) — the key
	// memory scoping, session affinity, and outcome tracking use. Never a
	// per-run value.
	Identity string
	// Model is the RESOLVED model for this dispatch ("" = bare launch, the
	// runtime's own default). It is part of the session-affinity partition.
	Model     string
	Tokens    Tokens
	Author    Author
	Workspace string // base workspace id/path to worktree from (optional)
	Shadow    bool   // skip the terminal side effect, just log what would run
	Wait      bool   // run foreground and capture output (for workflow steps)
	CatchUp   bool   // sweep re-derivation (skip if an agent is already on the PR)
	// Interactive marks a hand-off dispatch — a background workflow step you drive
	// and close yourself. Such an agent never shares the auto scratch workspace: it
	// gets a PR/branch worktree when there's repo context (PR-centric), else its own
	// dedicated workspace.
	Interactive bool
	// AgentAuthored marks a dispatch that came out of an agent-authored plan
	// (#36 §11) rather than operator config. Conductor-launched runtimes give
	// such a dispatch deny-by-default network (#36 §15): unless the profile's
	// isolation names an explicit network policy, the launch is routed through
	// the deny-all egress proxy.
	AgentAuthored bool
	Data          map[string]any // extra template vars (e.g. prior step outputs)
}

// RunRef is the outcome of a dispatch.
type RunRef struct {
	Backend  string   `json:"backend"`
	Kind     string   `json:"kind"`
	Argv     []string `json:"argv"`
	AgentID  string   `json:"agent_id,omitempty"`
	Shadowed bool     `json:"shadowed,omitempty"`
	Skipped  bool     `json:"skipped,omitempty"` // no work dispatched (e.g. catch-up while an agent is on the PR)
	Queued   bool     `json:"queued,omitempty"`  // handed to an agent already on the PR (no new agent spawned)
	Adopted  bool     `json:"adopted,omitempty"` // queued to an open workspace you already had on this branch
	// Workdir is the LOCAL directory the agent worked in (its isolated
	// worktree, or an explicit workdir) — where quality-gate checks run and
	// the proposed diff is read (#36 §16/§17). Empty for remote runtimes,
	// checkout-less runs, and queued/adopted dispatches.
	Workdir string `json:"workdir,omitempty"`
	Output  string `json:"-"`
}

// Dispatcher routes requests to a backend.
type Dispatcher struct {
	PaseoBin string
	DryRun   bool

	// Secrets redacts tracked secret values from the error details this
	// package builds out of paseo stderr/output before they leave the
	// package (nil = passthrough).
	Secrets *secrets.Resolver

	// Remote runs every paseo CLI invocation on an SSH host — a paseo runtime
	// with `host:`. nil = the local binary. See remote.go for what changes.
	Remote *hosts.Target

	// HostClient is the SSH client used for out-of-band remote checks a paseo
	// CLI invocation can't do itself (targetIsGitRepo's remote half). nil uses
	// a real hosts.Client (actual ssh). Injectable for tests, mirroring
	// hosts.Client's own Run seam.
	HostClient *hosts.Client

	// CheckoutDir resolves a local checkout path for a repo (owner/name) that
	// paseo can derive the forge repo from when creating a PR/branch worktree.
	// nil uses the built-in resolver (reuse an existing workspace, else clone).
	// Injectable for tests.
	CheckoutDir func(ctx context.Context, repo string) (string, error)

	// ScratchWorkspace resolves a single reusable workspace id for checkout:none
	// agents (so triage agents don't each leak a throwaway workspace). nil uses
	// the built-in resolver (find-by-title, else create). Injectable for tests.
	ScratchWorkspace func(ctx context.Context) (string, error)

	// WorktreeCreator creates an isolated PR/branch worktree workspace up front and
	// returns its id and cwd. We create the worktree with `paseo workspace create`
	// (which creates-or-errors) and then run the agent pinned into it with
	// `--workspace <id>`, rather than `paseo run --new-workspace worktree` — that
	// path can silently drop the agent in $HOME (exit 0, no worktree) on a transient
	// worktree-creation hiccup. nil uses the built-in `paseo workspace create`.
	// Injectable for tests.
	WorktreeCreator func(ctx context.Context, req Request, baseDir string) (id, cwd string, err error)

	// Retry policy for transient `paseo run` failures (git lock/timeout).
	RetryMax     int
	RetryBackoff time.Duration

	// CloneProtocol is the protocol paseo uses to clone a base checkout for a repo
	// with no existing workspace ("ssh" or "https"). paseo requires it for an
	// owner/repo shorthand. Empty defaults to "ssh" (matches the push identity).
	CloneProtocol string

	// AdoptOpenWorkspaces routes PR feedback to an agent already checked out on the
	// PR's head branch (e.g. a workspace you opened yourself) instead of spawning a
	// fresh worktree. Opt-in; set from top-level config.
	AdoptOpenWorkspaces bool

	// backendImpl is the paseo-daemon Backend this Dispatcher drives. nil (the
	// default, and every existing construction path) uses cliBackend — the
	// CLI-shelling implementation that is behavior-identical to the
	// pre-Backend-interface Dispatcher. Set to an rpcBackend to run paseo
	// through a conductor-paseo plugin instead. Use SetBackend to configure it;
	// the field stays unexported so every call site goes through backend(),
	// which supplies the cliBackend default.
	backendImpl Backend

	mu        sync.Mutex
	repoDirs  map[string]string // repo -> resolved checkout cwd (memoized)
	scratchWS string            // memoized scratch workspace id
}

// SetBackend configures the Backend this Dispatcher drives paseo through. nil
// (or never calling SetBackend) keeps the default cliBackend — the bundled,
// CLI-shelling path. Not safe to call concurrently with dispatch in progress.
func (d *Dispatcher) SetBackend(b Backend) { d.backendImpl = b }

// backend returns the configured Backend, defaulting to cliBackend (the
// CLI-shelling implementation wrapping this Dispatcher's own PaseoBin/Remote/
// Retry/Secrets config) when none was set.
func (d *Dispatcher) backend() Backend {
	if d.backendImpl != nil {
		return d.backendImpl
	}
	return newDispatcherCLIBackend(d)
}

// redactText scrubs tracked secret values from stderr-derived detail text.
func (d *Dispatcher) redactText(s string) string {
	if d.Secrets == nil {
		return s
	}
	return d.Secrets.Redact(s)
}

// New builds a Dispatcher. paseoBin defaults to "paseo"; retry tunes transient
// `paseo run` re-attempts (git lock/timeout under a sweep fan-out).
func New(paseoBin string, retry config.Retry, dryRun bool) *Dispatcher {
	if paseoBin == "" {
		paseoBin = "paseo"
	}
	return &Dispatcher{PaseoBin: paseoBin, DryRun: dryRun,
		RetryMax: retry.Attempts(), RetryBackoff: retry.BackoffDur(),
		repoDirs: map[string]string{}}
}

// WaitForAgent blocks until the given background agent goes idle (or ctx/timeout
// fires), so a concurrency slot frees only once the agent's work is done. A
// non-positive timeout means wait indefinitely (bounded only by ctx).
func (d *Dispatcher) WaitForAgent(ctx context.Context, id string, timeout time.Duration) {
	if id == "" {
		return
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	_ = d.backend().Wait(ctx, id)
}

// Send queues a follow-up prompt to an existing live agent (paseo's native
// session follow-up, `paseo send`). It's the concrete surface behind the paseo
// controller's session Prompt; the engine's autonomous per-PR queuing uses the
// same primitive internally (see liveAgentForPR).
func (d *Dispatcher) Send(ctx context.Context, id, prompt string) error {
	return d.sendToAgent(ctx, id, prompt)
}

// SendCapture delivers a follow-up prompt and waits for the turn to finish
// (`paseo send` waits by default), returning the completed turn's JSON
// output — the same capture shape as `paseo run --json`. The supervise loop
// (#36 §11) reads the agent's revised plan out of it.
func (d *Dispatcher) SendCapture(ctx context.Context, id, prompt string) (string, error) {
	res, err := d.backend().Send(ctx, SendOptions{ID: id, Prompt: prompt, JSON: true})
	if err != nil {
		if s := strings.TrimSpace(res.Stderr); s != "" {
			return "", fmt.Errorf("%w: %s", err, d.redactText(truncate(s, 300)))
		}
		return "", err
	}
	return res.Output, nil
}

// Dispatch selects the backend for the action and runs it.
func (d *Dispatcher) Dispatch(ctx context.Context, req Request) (RunRef, error) {
	// Routing is fixed: agents run via paseo (the only agent runner), commands run
	// as a local subprocess. A per-action `backend:` may still override (e.g. run a
	// command inside a paseo worktree). Agents without an override always use paseo.
	backend := req.Action.Backend
	if backend == "" {
		if req.Action.Type == "agent" {
			backend = "paseo"
		} else {
			backend = "local"
		}
	}
	switch backend {
	case "paseo":
		return d.paseo(ctx, req)
	case "local":
		return d.local(ctx, req)
	default:
		return RunRef{}, fmt.Errorf("unknown backend %q", backend)
	}
}

// scrubber is the resolver templateData uses to REDACT tracked secret values
// out of the scope handed to an external runtime's prompt/env templates
// (#122 R3): a step output that echoed a resolved secret (curl -v printing
// an auth header) must not surface it in a later agent's prompt. Package
// level because the controller-facing helpers (AgentEnv, RenderPrompt) have
// no Dispatcher. Set once at boot beside the other redaction choke points.
var scrubber atomic.Pointer[secrets.Resolver]

// SetScrubber installs the tracked-secret scrubber for template data.
func SetScrubber(r *secrets.Resolver) { scrubber.Store(r) }

// templateData assembles the variables available to prompt/command/env
// templates: trigger fields plus the two tokens. Everything EXCEPT the
// intentional credential channels — the named secrets/vaults scopes (the
// deprecated-but-supported env templating) and the dispatch tokens — is
// scrubbed of tracked secret values before an external runtime renders
// against it; ordinary data flows through untouched.
func templateData(req Request) map[string]any {
	t := req.Trigger.Target
	data := map[string]any{
		"repo":      t.Repo,
		"owner":     t.Owner,
		"name":      t.Name,
		"pr":        t.PR,
		"issue":     t.Issue,
		"number":    t.Number,
		"head":      t.HeadSHA,
		"base":      t.BaseRef,
		"url":       t.HTMLURL,
		"kind":      req.Trigger.Kind,
		"title":     req.Trigger.Title,
		"app_token": req.Tokens.App,
		"gh_token":  req.Tokens.User,
	}
	for k, v := range req.Trigger.Context {
		if _, exists := data[k]; !exists {
			data[k] = v
		}
	}
	for k, v := range req.Data { // step outputs etc. win over context
		data[k] = v
	}
	if r := scrubber.Load(); r != nil {
		for k, v := range data {
			switch k {
			case "secrets", "vaults", "app_token", "gh_token":
				// The explicit credential channels: {{.secrets.x}} /
				// {{.vaults.v.k}} (deprecated env templating, still
				// supported) and the dispatch tokens ({{.gh_token}}).
				continue
			}
			data[k] = r.RedactValue(v)
		}
	}
	return data
}

// dispatchFuncs: {{secret "name"}} renders the OPAQUE boundary handle here
// too — a dispatched agent's prompt and env carry the handle, never the
// value (the flow runner resolves handles only at conductor's own egress;
// an agent that needs the value goes through the secret broker).
var dispatchFuncs = template.FuncMap{"secret": secrets.SecretTemplateFunc}

func render(s string, data map[string]any) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	tmpl, err := template.New("t").Option("missingkey=zero").Funcs(dispatchFuncs).Parse(s)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

// expandTilde expands a leading ~/ to the user's home directory.
func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return h
			}
			return h + p[1:]
		}
	}
	return p
}

func renderAll(in []string, data map[string]any) ([]string, error) {
	out := make([]string, len(in))
	for i, s := range in {
		r, err := render(s, data)
		if err != nil {
			return nil, err
		}
		out[i] = r
	}
	return out, nil
}
