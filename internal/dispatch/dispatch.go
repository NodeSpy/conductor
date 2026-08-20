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
	"os/exec"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
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
	Trigger   core.Trigger
	Action    config.Action
	Profile   config.AgentProfile // populated for agent actions
	Tokens    Tokens
	Author    Author
	Workspace string         // base workspace id/path to worktree from (optional)
	Shadow    bool           // skip the terminal side effect, just log what would run
	Wait      bool           // run foreground and capture output (for workflow steps)
	CatchUp   bool           // sweep re-derivation (skip if an agent is already on the PR)
	Data      map[string]any // extra template vars (e.g. prior step outputs)
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
	Output   string   `json:"-"`
}

// Dispatcher routes requests to a backend.
type Dispatcher struct {
	PaseoBin        string
	DefaultBackends map[string]string
	DryRun          bool

	// CheckoutDir resolves a local checkout path for a repo (owner/name) that
	// paseo can derive the forge repo from when creating a PR/branch worktree.
	// nil uses the built-in resolver (reuse an existing workspace, else clone).
	// Injectable for tests.
	CheckoutDir func(ctx context.Context, repo string) (string, error)

	// ScratchWorkspace resolves a single reusable workspace id for checkout:none
	// agents (so triage agents don't each leak a throwaway workspace). nil uses
	// the built-in resolver (find-by-title, else create). Injectable for tests.
	ScratchWorkspace func(ctx context.Context) (string, error)

	// Retry policy for transient `paseo run` failures (git lock/timeout).
	RetryMax     int
	RetryBackoff time.Duration

	mu        sync.Mutex
	repoDirs  map[string]string // repo -> resolved checkout cwd (memoized)
	scratchWS string            // memoized scratch workspace id
}

// New builds a Dispatcher from dispatch config.
func New(d config.Dispatch, dryRun bool) *Dispatcher {
	bin := "paseo"
	if b, ok := d.Backends["paseo"]; ok && b.Bin != "" {
		bin = b.Bin
	}
	return &Dispatcher{PaseoBin: bin, DefaultBackends: d.DefaultBackends, DryRun: dryRun,
		RetryMax: d.Retry.Attempts(), RetryBackoff: d.Retry.BackoffDur(),
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
	_ = exec.CommandContext(ctx, d.PaseoBin, "wait", id).Run()
}

// Dispatch selects the backend for the action and runs it.
func (d *Dispatcher) Dispatch(ctx context.Context, req Request) (RunRef, error) {
	backend := req.Action.Backend
	if backend == "" {
		backend = d.DefaultBackends[req.Action.Type]
	}
	switch backend {
	case "paseo":
		return d.paseo(ctx, req)
	case "local", "":
		return d.local(ctx, req)
	default:
		return RunRef{}, fmt.Errorf("unknown backend %q", backend)
	}
}

// templateData assembles the variables available to prompt/command/env
// templates: trigger fields plus the two tokens.
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
	return data
}

func render(s string, data map[string]any) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	tmpl, err := template.New("t").Option("missingkey=zero").Parse(s)
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
