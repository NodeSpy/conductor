package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// paseoExec is the only thing cliBackend needs to reach the paseo CLI: a
// builder for one invocation, carrying its owner's PaseoBin/Remote config.
// Both *Dispatcher and *Reaper provide it (their paseoCmd methods), so the
// same CLI backend serves the dispatcher's launch/query path and the reaper's
// cull path without either depending on the other.
type paseoExec interface {
	paseoCmd(ctx context.Context, args ...string) *exec.Cmd
}

// cliBackend implements Backend by shelling to the `paseo` CLI through its
// owner's exec seam (paseoExec). It is the DEFAULT Backend — extracting this
// interface changed no behavior on the default path; every method body here
// is the CLI-shelling half of what used to be a Dispatcher (or Reaper) method
// directly.
type cliBackend struct {
	exec paseoExec

	// RunAgent-only policy, supplied by the Dispatcher — the only owner that
	// launches agents. A reaper-built backend leaves these zero: it never calls
	// RunAgent, and the zero values mean "no retries, no redaction", not
	// anything unsafe.
	paseoBin     string
	remote       bool
	retryMax     int
	retryBackoff time.Duration
	redact       func(string) string
}

// newDispatcherCLIBackend builds the CLI backend for a Dispatcher: its exec
// seam plus the `paseo run` retry/redaction policy. Built fresh per backend()
// call, so it always reflects the Dispatcher's current configuration.
func newDispatcherCLIBackend(d *Dispatcher) *cliBackend {
	return &cliBackend{
		exec:         d,
		paseoBin:     d.PaseoBin,
		remote:       d.remote(),
		retryMax:     d.RetryMax,
		retryBackoff: d.RetryBackoff,
		redact:       d.redactText,
	}
}

// newReaperCLIBackend builds the CLI backend for a Reaper: the same paseo
// binary and remote host its paseoCmd already used, so the reaper's argv is
// byte-identical to the pre-Backend path. No run policy — the reaper only
// lists, inspects and archives.
func newReaperCLIBackend(r *Reaper) *cliBackend {
	return &cliBackend{exec: r, paseoBin: r.PaseoBin, remote: r.Remote != nil}
}

// redactText scrubs tracked secret values from stderr-derived detail text,
// via the owner's redactor when it has one.
func (b *cliBackend) redactText(s string) string {
	if b.redact == nil {
		return s
	}
	return b.redact(s)
}

// RunAgent runs `paseo run <opts.Args...>` with bounded retries on a
// transient failure (git lock/timeout), clearing a stale git config.lock
// between attempts. Mirrors the pre-extraction retry loop in Dispatcher.paseo
// exactly.
func (b *cliBackend) RunAgent(ctx context.Context, opts RunAgentOptions) (RunAgentResult, error) {
	var out []byte
	var err error
	var detail string
	for attempt := 0; ; attempt++ {
		cmd := b.exec.paseoCmd(ctx, opts.Args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err = cmd.Output()
		if err == nil {
			return RunAgentResult{Output: string(out), AgentID: parseAgentID(out)}, nil
		}
		detail = paseoErrDetail(out, stderr.Bytes())
		if attempt >= b.retryMax || !isTransientPaseoErr(detail) {
			break
		}
		if !b.remote { // the lock file lives on the remote box; leave it to paseo
			clearStaleGitLock(ctx, b.paseoBin, opts.Cwd)
		}
		select {
		case <-ctx.Done():
			return RunAgentResult{Output: string(out)}, ctx.Err()
		case <-time.After(b.retryBackoff):
		}
	}
	if detail != "" {
		return RunAgentResult{Output: string(out)}, fmt.Errorf("paseo run: %w: %s", err, b.redactText(detail))
	}
	return RunAgentResult{Output: string(out)}, fmt.Errorf("paseo run: %w", err)
}

// AgentLog runs `paseo logs <id> --tail n` and returns its raw text. The
// output_schema SOFT path parses the agent's final message out of it (paseo
// run only yields the launch envelope). stdout is returned even on error so a
// caller can still try to salvage a partial tail.
func (b *cliBackend) AgentLog(ctx context.Context, id string, tail int) (string, error) {
	args := []string{"logs", id}
	if tail > 0 {
		args = append(args, "--tail", fmt.Sprintf("%d", tail))
	}
	out, err := b.exec.paseoCmd(ctx, args...).Output()
	if err != nil {
		return string(out), fmt.Errorf("paseo logs %s: %w", id, err)
	}
	return string(out), nil
}

// ListAgents runs `paseo ls --json`, optionally with `--label k=v` per entry
// (sorted by key for deterministic argv, no behavioral difference — paseo ls
// AND-filters exact-match labels regardless of order).
func (b *cliBackend) ListAgents(ctx context.Context, labels map[string]string) ([]AgentInfo, error) {
	args := []string{"ls", "--json"}
	args = append(args, sortedLabelArgs(labels)...)
	out, err := b.exec.paseoCmd(ctx, args...).Output()
	if err != nil {
		return nil, err
	}
	var agents []AgentInfo
	if err := json.Unmarshal(out, &agents); err != nil {
		return nil, err
	}
	return agents, nil
}

// sortedLabelArgs renders a label map as sorted `--label k=v` argv pairs.
func sortedLabelArgs(labels map[string]string) []string {
	if len(labels) == 0 {
		return nil
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	args := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		args = append(args, "--label", k+"="+labels[k])
	}
	return args
}

// Inspect runs `paseo inspect <id> --json`.
func (b *cliBackend) Inspect(ctx context.Context, id string) (AgentDetail, error) {
	out, err := b.exec.paseoCmd(ctx, "inspect", id, "--json").Output()
	if err != nil {
		return AgentDetail{}, err
	}
	var det AgentDetail
	if err := json.Unmarshal(out, &det); err != nil {
		return AgentDetail{}, err
	}
	return det, nil
}

// ArchiveAgent runs `paseo archive <id>`.
func (b *cliBackend) ArchiveAgent(ctx context.Context, id string) error {
	return b.exec.paseoCmd(ctx, "archive", id).Run()
}

// ArchiveWorkspace runs `paseo workspace archive <id>`.
func (b *cliBackend) ArchiveWorkspace(ctx context.Context, id string) error {
	return b.exec.paseoCmd(ctx, "workspace", "archive", id).Run()
}

// CreateWorktree runs `paseo workspace create` for an isolated PR/branch
// worktree. Mirrors the pre-extraction createWorktree body exactly (argv
// shape and error wording), including the belt-and-suspenders $HOME check.
func (b *cliBackend) CreateWorktree(ctx context.Context, opts CreateWorktreeOptions) (CreateWorktreeResult, error) {
	// Adopt-don't-collide (create-or-reuse). A branch-off worktree's paseo
	// workspace is named after its branch, and that branch is deterministic per
	// (PR, kind) — so a prior run's worktree for it may still exist, and
	// `workspace create` is create-or-error. Recognizing "already exists" is
	// paseo-specific knowledge that belongs in this backend: the orchestration
	// just asks for a worktree on this branch, and gets the existing one when
	// there is one (launching another agent into it), or a fresh one otherwise.
	if opts.Strategy == "branch-off" {
		if w, ok := b.worktreeOnBranch(ctx, opts.NewBranch); ok {
			return CreateWorktreeResult{WorkspaceID: w.WorkspaceID, Cwd: w.Cwd}, nil
		}
	}
	args := []string{"workspace", "create", "--isolation", opts.Isolation,
		"--path", opts.Path, "--mode", opts.Strategy, "--json"}
	switch opts.Strategy {
	case "checkout-pr":
		args = append(args, "--pr-number", itoa(opts.PRNumber), "--forge", opts.Forge)
	case "branch-off":
		args = append(args, "--new-branch", opts.NewBranch)
		if opts.BaseRef != "" {
			args = append(args, "--base", opts.BaseRef)
		}
	default:
		return CreateWorktreeResult{}, fmt.Errorf("createWorktree: unexpected strategy %q", opts.Strategy)
	}
	cmd := b.exec.paseoCmd(ctx, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return CreateWorktreeResult{}, fmt.Errorf("paseo workspace create (%s): %w%s", opts.Strategy, err, stderrTail(&stderr))
	}
	var w struct {
		WorkspaceID string `json:"workspaceId"`
		Cwd         string `json:"cwd"`
	}
	if json.Unmarshal(out, &w) != nil || w.WorkspaceID == "" {
		return CreateWorktreeResult{}, fmt.Errorf("paseo workspace create (%s): unparseable output: %s", opts.Strategy, strings.TrimSpace(string(out)))
	}
	// Belt-and-suspenders: a "successful" create that still landed in the base/home
	// is the very fallback we're guarding against — treat it as a failure.
	if w.Cwd == "" || isHomeDir(w.Cwd) {
		return CreateWorktreeResult{}, fmt.Errorf("paseo workspace create (%s) produced no worktree (cwd=%q)", opts.Strategy, w.Cwd)
	}
	return CreateWorktreeResult{WorkspaceID: w.WorkspaceID, Cwd: w.Cwd}, nil
}

// worktreeOnBranch returns the worktree workspace already on `branch`, if any —
// the create-or-reuse lookup for CreateWorktree. paseo names a branch-off
// workspace after its branch, so the match is exact on that. `branch` is always
// a conductor-owned `conductor/<kind>-<n>` slug, so it can never resolve a
// workspace made by hand. A listing failure is treated as "none" — the create
// then runs and surfaces the real error, rather than this masking it.
func (b *cliBackend) worktreeOnBranch(ctx context.Context, branch string) (WorkspaceInfo, bool) {
	if branch == "" {
		return WorkspaceInfo{}, false
	}
	wl, err := b.ListWorkspaces(ctx)
	if err != nil {
		return WorkspaceInfo{}, false
	}
	for _, w := range wl {
		if w.Isolation == "worktree" && w.Name == branch && w.WorkspaceID != "" && w.Cwd != "" {
			return w, true
		}
	}
	return WorkspaceInfo{}, false
}

// CreateWorkspace runs `paseo workspace create` for a plain (non-worktree)
// workspace, e.g. an un-pinned checkout:none run's ephemeral per-run workspace.
func (b *cliBackend) CreateWorkspace(ctx context.Context, opts CreateWorkspaceOptions) (CreateWorkspaceResult, error) {
	out, err := b.exec.paseoCmd(ctx, "workspace", "create",
		"--isolation", opts.Isolation, "--path", opts.Path, "--title", opts.Title, "--json").Output()
	if err != nil {
		return CreateWorkspaceResult{}, fmt.Errorf("paseo workspace create: %w", err)
	}
	var w struct {
		WorkspaceID string `json:"workspaceId"`
		ID          string `json:"id"`
	}
	if json.Unmarshal(out, &w) != nil {
		return CreateWorkspaceResult{}, fmt.Errorf("paseo workspace create: unparseable output")
	}
	if w.WorkspaceID != "" {
		return CreateWorkspaceResult{WorkspaceID: w.WorkspaceID}, nil
	}
	return CreateWorkspaceResult{WorkspaceID: w.ID}, nil
}

// ListWorkspaces runs `paseo workspace ls --json`.
func (b *cliBackend) ListWorkspaces(ctx context.Context) ([]WorkspaceInfo, error) {
	out, err := b.exec.paseoCmd(ctx, "workspace", "ls", "--json").Output()
	if err != nil {
		return nil, err
	}
	var wl []WorkspaceInfo
	if err := json.Unmarshal(out, &wl); err != nil {
		return nil, err
	}
	return wl, nil
}

// Clone runs `paseo clone <repo> --dir <dir> --protocol <protocol> --json`.
func (b *cliBackend) Clone(ctx context.Context, opts CloneOptions) error {
	out, err := b.exec.paseoCmd(ctx, "clone", opts.Repo, "--dir", opts.Dir, "--protocol", opts.Protocol, "--json").CombinedOutput()
	if err != nil {
		return fmt.Errorf("paseo clone %s: %w: %s", opts.Repo, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Send runs `paseo send <id> <prompt> [--json]`, always capturing stdout and
// stderr. The caller decides how to format/redact a failure — sendToAgent and
// SendCapture historically differ in whether they redact the error detail.
func (b *cliBackend) Send(ctx context.Context, opts SendOptions) (SendResult, error) {
	args := []string{"send", opts.ID, opts.Prompt}
	if opts.JSON {
		args = append(args, "--json")
	}
	cmd := b.exec.paseoCmd(ctx, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	return SendResult{Output: string(out), Stderr: stderr.String()}, err
}

// Wait runs `paseo wait <id>`, blocking until the agent goes idle (bounded by
// ctx, e.g. a caller-applied timeout).
func (b *cliBackend) Wait(ctx context.Context, id string) error {
	return b.exec.paseoCmd(ctx, "wait", id).Run()
}
