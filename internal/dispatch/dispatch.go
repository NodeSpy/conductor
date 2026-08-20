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
	"text/template"

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
	Workspace string // base workspace id/path to worktree from (optional)
	Shadow    bool   // skip the terminal side effect, just log what would run
}

// RunRef is the outcome of a dispatch.
type RunRef struct {
	Backend  string   `json:"backend"`
	Kind     string   `json:"kind"`
	Argv     []string `json:"argv"`
	AgentID  string   `json:"agent_id,omitempty"`
	Shadowed bool     `json:"shadowed,omitempty"`
	Output   string   `json:"-"`
}

// Dispatcher routes requests to a backend.
type Dispatcher struct {
	PaseoBin        string
	DefaultBackends map[string]string
	DryRun          bool
}

// New builds a Dispatcher from dispatch config.
func New(d config.Dispatch, dryRun bool) *Dispatcher {
	bin := "paseo"
	if b, ok := d.Backends["paseo"]; ok && b.Bin != "" {
		bin = b.Bin
	}
	return &Dispatcher{PaseoBin: bin, DefaultBackends: d.DefaultBackends, DryRun: dryRun}
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
