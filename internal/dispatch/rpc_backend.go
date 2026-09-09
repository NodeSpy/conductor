package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/NodeSpy/conductor/internal/plugin"
)

// Invoker is the subset of *internal/plugin.Client rpcBackend needs, narrowed
// so a test can drive rpcBackend against an in-process fake with no real
// plugin subprocess.
type Invoker interface {
	Invoke(ctx context.Context, req plugin.InvokeRequest) (map[string]any, error)
}

// rpcBackend implements Backend by calling a conductor-paseo plugin's verbs
// over the plugin RPC (Invoker.Invoke — the same path a real *plugin.Client
// uses) instead of shelling to the `paseo` CLI directly. It is opt-in: the
// Dispatcher defaults to cliBackend; configure this via Dispatcher.SetBackend
// to route paseo through a plugin instead.
//
// Verb mapping mirrors Backend 1:1 — see plugins/conductor-paseo/main.go's
// verb constants: RunAgent -> "run", ListAgents -> "list_agents",
// Inspect -> "inspect", ArchiveAgent -> "archive_agent",
// ArchiveWorkspace -> "archive_workspace", CreateWorktree -> "create_worktree",
// CreateWorkspace -> "create_workspace", ListWorkspaces -> "list_workspaces",
// Clone -> "clone", Send -> "send", Wait -> "wait".
type rpcBackend struct {
	client   Invoker
	instance string         // InvokeRequest.Instance (the plugin instance name)
	conn     map[string]any // InvokeRequest.Connection (e.g. {"paseo_bin": "..."})

	// RetryMax/RetryBackoff mirror cliBackend's RunAgent retry policy — bounded
	// retries on a transient paseo error (git lock/timeout), detected the same
	// way (isTransientPaseoErr against the returned error's message, which the
	// plugin formats identically to cliBackend's paseoErrDetail).
	RetryMax     int
	RetryBackoff time.Duration
}

// NewRPCBackend builds a Backend that drives paseo through a plugin's verbs.
// client is usually a *internal/plugin.Client (which satisfies Invoker);
// instance is the plugin instance name to put in every InvokeRequest; conn is
// the connection payload forwarded on every call (e.g. {"paseo_bin": "..."}
// to point a test plugin at a stub binary).
func NewRPCBackend(client Invoker, instance string, conn map[string]any, retryMax int, retryBackoff time.Duration) Backend {
	return &rpcBackend{client: client, instance: instance, conn: conn, RetryMax: retryMax, RetryBackoff: retryBackoff}
}

// invoke calls one verb, forwarding the configured instance/connection.
func (b *rpcBackend) invoke(ctx context.Context, verb string, options map[string]any) (map[string]any, error) {
	return b.client.Invoke(ctx, plugin.InvokeRequest{
		Instance: b.instance, Verb: verb, Options: options, Connection: b.conn,
	})
}

// RunAgent calls the "run" verb with bounded retries on a transient failure,
// mirroring cliBackend.RunAgent's retry loop (minus the local git-lock
// clearing, which is a filesystem-local concern cliBackend owns directly —
// see the Backend.RunAgent doc comment).
func (b *rpcBackend) RunAgent(ctx context.Context, opts RunAgentOptions) (RunAgentResult, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		out, err := b.invoke(ctx, "run", map[string]any{"args": opts.Args})
		if err == nil {
			return RunAgentResult{Output: rpcStr(out["output"]), AgentID: rpcStr(out["agentId"])}, nil
		}
		lastErr = err
		if attempt >= b.RetryMax || !isTransientPaseoErr(err.Error()) {
			break
		}
		select {
		case <-ctx.Done():
			return RunAgentResult{}, ctx.Err()
		case <-time.After(b.RetryBackoff):
		}
	}
	return RunAgentResult{}, fmt.Errorf("paseo run (plugin): %w", lastErr)
}

func (b *rpcBackend) ListAgents(ctx context.Context, labels map[string]string) ([]AgentInfo, error) {
	out, err := b.invoke(ctx, "list_agents", map[string]any{"labels": stringMapToAny(labels)})
	if err != nil {
		return nil, err
	}
	var agents []AgentInfo
	if err := decodeInto(out["agents"], &agents); err != nil {
		return nil, err
	}
	return agents, nil
}

func (b *rpcBackend) Inspect(ctx context.Context, id string) (AgentDetail, error) {
	out, err := b.invoke(ctx, "inspect", map[string]any{"id": id})
	if err != nil {
		return AgentDetail{}, err
	}
	det := AgentDetail{
		Cwd:       rpcStr(out["cwd"]),
		LastUsage: rpcStr(out["lastUsage"]),
		UpdatedAt: rpcStr(out["updatedAt"]),
		CreatedAt: rpcStr(out["createdAt"]),
	}
	// A malformed pending-permissions payload must not fail the whole inspect:
	// the reaper's other signals (hold marker, HoldSet) still apply, and an
	// error here would read as "couldn't inspect" and skip the startup grace.
	_ = decodeInto(out["pendingPermissions"], &det.PendingPermissions)
	return det, nil
}

func (b *rpcBackend) ArchiveAgent(ctx context.Context, id string) error {
	_, err := b.invoke(ctx, "archive_agent", map[string]any{"id": id})
	return err
}

func (b *rpcBackend) ArchiveWorkspace(ctx context.Context, id string) error {
	_, err := b.invoke(ctx, "archive_workspace", map[string]any{"id": id})
	return err
}

func (b *rpcBackend) CreateWorktree(ctx context.Context, opts CreateWorktreeOptions) (CreateWorktreeResult, error) {
	out, err := b.invoke(ctx, "create_worktree", map[string]any{
		"isolation": opts.Isolation, "path": opts.Path, "strategy": opts.Strategy,
		"prNumber": opts.PRNumber, "forge": opts.Forge, "newBranch": opts.NewBranch, "baseRef": opts.BaseRef,
	})
	if err != nil {
		return CreateWorktreeResult{}, err
	}
	return CreateWorktreeResult{WorkspaceID: rpcStr(out["workspaceId"]), Cwd: rpcStr(out["cwd"])}, nil
}

func (b *rpcBackend) CreateWorkspace(ctx context.Context, opts CreateWorkspaceOptions) (CreateWorkspaceResult, error) {
	out, err := b.invoke(ctx, "create_workspace", map[string]any{
		"isolation": opts.Isolation, "path": opts.Path, "title": opts.Title,
	})
	if err != nil {
		return CreateWorkspaceResult{}, err
	}
	return CreateWorkspaceResult{WorkspaceID: rpcStr(out["workspaceId"])}, nil
}

func (b *rpcBackend) ListWorkspaces(ctx context.Context) ([]WorkspaceInfo, error) {
	out, err := b.invoke(ctx, "list_workspaces", nil)
	if err != nil {
		return nil, err
	}
	var wl []WorkspaceInfo
	if err := decodeInto(out["workspaces"], &wl); err != nil {
		return nil, err
	}
	return wl, nil
}

func (b *rpcBackend) Clone(ctx context.Context, opts CloneOptions) error {
	_, err := b.invoke(ctx, "clone", map[string]any{"repo": opts.Repo, "dir": opts.Dir, "protocol": opts.Protocol})
	return err
}

func (b *rpcBackend) Send(ctx context.Context, opts SendOptions) (SendResult, error) {
	out, err := b.invoke(ctx, "send", map[string]any{"id": opts.ID, "prompt": opts.Prompt, "json": opts.JSON})
	if err != nil {
		// Stderr is unavailable over RPC — the plugin's error message already
		// carries the same stdout-JSON-else-stderr detail cliBackend surfaces.
		return SendResult{}, err
	}
	return SendResult{Output: rpcStr(out["output"])}, nil
}

func (b *rpcBackend) Wait(ctx context.Context, id string) error {
	_, err := b.invoke(ctx, "wait", map[string]any{"id": id})
	return err
}

// rpcStr extracts a string output field, tolerating a missing/non-string/nil
// value (a plugin's zero-value output for that field).
func rpcStr(v any) string {
	s, _ := v.(string)
	return s
}

// stringMapToAny widens a label map to map[string]any for an InvokeRequest's
// Options (the wire type), or nil when empty (so the plugin sees no "labels"
// key at all rather than an empty map — matches cliBackend's "no --label
// flags at all" behavior for a nil/empty filter).
func stringMapToAny(m map[string]string) map[string]any {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// decodeInto round-trips a decoded JSON-ish value (as it comes back from an
// InvokeResult's Outputs — []any of map[string]any, or a real plugin.Client's
// json.Unmarshal'd result, same shape either way) into dst via JSON, so
// Backend's typed results stay a single source of truth (AgentInfo /
// WorkspaceInfo's json tags already match the plugin's wire field names).
func decodeInto(v any, dst any) error {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}
