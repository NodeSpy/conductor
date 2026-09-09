package dispatch

import "context"

// Backend is the paseo-daemon operations the Dispatcher performs: launching
// and querying agents, and creating/archiving the workspaces (including
// isolated worktrees) they run in. cliBackend implements it by shelling to
// the `paseo` CLI — the default, behavior-identical to conductor's original
// hardwired paseo path. rpcBackend implements it by calling a conductor-paseo
// plugin process over the plugin RPC (opt-in).
//
// The split follows docs/design/paseo-runtime-plugin.md: paseo's daemon-wide
// dedup/reaper/hold POLICY (when to queue vs launch, when to reap, when to
// hold) stays conductor-side, generic orchestration in this package; only the
// paseo-CLI-specific HOW of each primitive query/mutation sits behind this
// interface. Repo-generic git calls (git rev-parse, branch inspection) are
// NOT part of this interface — they apply equally regardless of which
// runtime backend is in play, so they stay free functions in this package.
type Backend interface {
	// RunAgent launches (or re-launches) a coding agent turn. Args is the
	// fully-rendered `paseo run` argument list (everything after the `run`
	// subcommand itself) — kept as a flat list rather than decomposed into
	// individual fields because it is already assembled once by the
	// orchestration layer (paseo(), which must render the IDENTICAL argv for
	// the --dry-run/shadow preview path, captured in RunRef.Argv before any
	// backend is consulted); re-deriving the same argv a second time
	// independently, inside the backend, would risk the preview and the real
	// invocation drifting apart in this behavior-critical path. Cwd carries
	// the local directory (if any) the caller resolved for this run, used
	// only for backend-local housekeeping (e.g. cliBackend's stale git-lock
	// clearing before a retry) — never re-injected into Args.
	RunAgent(ctx context.Context, opts RunAgentOptions) (RunAgentResult, error)

	// ListAgents lists non-archived agents, optionally filtered by exact-match
	// labels (`paseo ls [--label k=v ...] --json`). A nil/empty labels lists
	// every agent.
	ListAgents(ctx context.Context, labels map[string]string) ([]AgentInfo, error)

	// Inspect returns detail for one agent (`paseo inspect <id> --json`).
	Inspect(ctx context.Context, id string) (AgentDetail, error)

	// ArchiveAgent soft-deletes one agent (`paseo archive <id>`).
	ArchiveAgent(ctx context.Context, id string) error

	// ArchiveWorkspace soft-deletes a workspace, reclaiming any worktree it
	// owns (`paseo workspace archive <id>`).
	ArchiveWorkspace(ctx context.Context, id string) error

	// CreateWorktree creates an isolated PR/branch worktree workspace
	// (`paseo workspace create --isolation ... --mode ...`).
	CreateWorktree(ctx context.Context, opts CreateWorktreeOptions) (CreateWorktreeResult, error)

	// CreateWorkspace creates a plain (non-worktree) workspace, e.g. the
	// shared scratch workspace (`paseo workspace create --isolation local ...`).
	CreateWorkspace(ctx context.Context, opts CreateWorkspaceOptions) (CreateWorkspaceResult, error)

	// ListWorkspaces lists every workspace (`paseo workspace ls --json`).
	ListWorkspaces(ctx context.Context) ([]WorkspaceInfo, error)

	// Clone clones a repo and registers it with paseo (`paseo clone`).
	Clone(ctx context.Context, opts CloneOptions) error

	// Send queues a follow-up prompt to an existing live agent
	// (`paseo send <id> <prompt> [--json]`).
	Send(ctx context.Context, opts SendOptions) (SendResult, error)

	// Wait blocks until an agent goes idle (`paseo wait <id>`).
	Wait(ctx context.Context, id string) error
}

// AgentInfo is the subset of `paseo ls --json` the dispatcher needs: identity,
// location, and coarse status.
type AgentInfo struct {
	ID     string `json:"id"`
	Cwd    string `json:"cwd"`
	Status string `json:"status"`
}

// AgentDetail is the subset of `paseo inspect --json` the dispatcher needs.
type AgentDetail struct {
	Cwd       string `json:"Cwd"`
	LastUsage string `json:"LastUsage"`
	UpdatedAt string `json:"UpdatedAt"`
	CreatedAt string `json:"CreatedAt"`
}

// WorkspaceInfo is the subset of `paseo workspace ls --json` the dispatcher
// needs: which repo/project it's for, where it lives, and its isolation mode
// (worktree vs local/shared).
type WorkspaceInfo struct {
	WorkspaceID string `json:"workspaceId"`
	Project     string `json:"project"`
	Cwd         string `json:"cwd"`
	Isolation   string `json:"isolation"`
	Name        string `json:"name"`
}

// RunAgentOptions is the input to Backend.RunAgent. See the interface doc for
// why Args stays a flat argument list.
type RunAgentOptions struct {
	Args []string
	Cwd  string
}

// RunAgentResult is the output of a (successful or failed) RunAgent call.
// Output is populated even on error (the last attempt's stdout), mirroring
// the pre-refactor behavior of always recording RunRef.Output.
type RunAgentResult struct {
	Output  string
	AgentID string
}

// CreateWorktreeOptions is the input to Backend.CreateWorktree.
type CreateWorktreeOptions struct {
	Isolation string // paseo's --isolation for the new workspace (e.g. "worktree")
	Path      string // base checkout dir paseo derives the forge repo from
	Strategy  string // "checkout-pr" | "branch-off"
	PRNumber  int
	Forge     string
	NewBranch string
	BaseRef   string
}

// CreateWorktreeResult is the output of a successful CreateWorktree call.
type CreateWorktreeResult struct {
	WorkspaceID string
	Cwd         string
}

// CreateWorkspaceOptions is the input to Backend.CreateWorkspace.
type CreateWorkspaceOptions struct {
	Isolation string
	Path      string
	Title     string
}

// CreateWorkspaceResult is the output of a successful CreateWorkspace call.
type CreateWorkspaceResult struct {
	WorkspaceID string
}

// CloneOptions is the input to Backend.Clone.
type CloneOptions struct {
	Repo     string
	Dir      string
	Protocol string
}

// SendOptions is the input to Backend.Send. JSON requests `--json` (the
// supervise loop's capture-and-wait path); without it, Send is a fire-and-
// forget follow-up.
type SendOptions struct {
	ID     string
	Prompt string
	JSON   bool
}

// SendResult is the output of a Send call. Stderr is the raw captured stderr
// (untruncated, unredacted) so callers can format their own error detail —
// sendToAgent and SendCapture historically differ in whether they redact.
type SendResult struct {
	Output string
	Stderr string
}
