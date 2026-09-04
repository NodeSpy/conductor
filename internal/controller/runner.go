package controller

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/dispatch"
)

// Provisioner resolves the conductor-supplied worktree a controller runs an agent
// in. *dispatch.Dispatcher satisfies it via ProvisionWorktree, so every controller
// checks out through the same path as the paseo backend; tests inject a fake. A nil
// Provisioner is tolerated — the session then runs in the runtime's default dir.
type Provisioner interface {
	ProvisionWorktree(ctx context.Context, req dispatch.Request) (id, cwd string, err error)
}

// Assert the concrete dispatcher satisfies Provisioner, so a signature drift fails
// the build here rather than at a call site.
var _ Provisioner = (*dispatch.Dispatcher)(nil)

// waiter is an optional Session capability: block until the session's in-flight
// turn finishes. The generic runner's WaitForAgent uses it so a concurrency slot
// frees only once the agent's work is done, matching paseo's `wait`. Sessions that
// complete synchronously (oneshot) need not implement it.
type waiter interface {
	Wait(ctx context.Context, timeout time.Duration)
}

// controllerRunner is the engine-facing Runner for every non-paseo controller. The
// engine drives all runtimes through the same Runner surface (Dispatch / wait /
// liveness / archive); this bridges that surface onto the Controller's session
// model: Dispatch provisions the worktree, opens a session (which runs the first
// turn), and records it in a liveness table keyed both by agent id (for wait/
// archive) and by PR+kind (for the engine's live-agent dedup gate).
//
// The session broker (M2) will own long-lived session reuse; until then this table
// is the minimal liveness the engine needs, and it is deliberately process-local.
type controllerRunner struct {
	c    Controller
	prov Provisioner
	h    Handler // permission/input handler; nil → controllers apply their auto policy

	mu     sync.Mutex
	live   map[string]Session // agent id → live session
	byPR   map[string]int     // "prKey\x00kind" → live count (HasLiveAgent gate)
	bucket map[string]string  // agent id → its PR+kind bucket (for exact decrement)
}

func newControllerRunner(c Controller, prov Provisioner, h Handler) *controllerRunner {
	return &controllerRunner{
		c:      c,
		prov:   prov,
		h:      h,
		live:   map[string]Session{},
		byPR:   map[string]int{},
		bucket: map[string]string{},
	}
}

func prKey(req dispatch.Request) string {
	return req.Trigger.Key() + "\x00" + req.Trigger.Kind
}

// Dispatch provisions the worktree and opens a session for the request, returning
// a RunRef bound to the session id. A shadow/dry request is reported without
// launching anything, mirroring the paseo backend.
func (r *controllerRunner) Dispatch(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
	ref := dispatch.RunRef{Backend: string(r.c.Transport()), Kind: req.Trigger.Kind}
	if req.Shadow {
		ref.Shadowed = true
		return ref, nil
	}

	var (
		wsID, cwd string
		err       error
	)
	if r.prov != nil {
		if wsID, cwd, err = r.prov.ProvisionWorktree(ctx, req); err != nil {
			return ref, err
		}
	}

	sess, err := r.c.NewSession(ctx, Spec{Request: req, Cwd: cwd, WorkspaceID: wsID}, r.h)
	if err != nil {
		return ref, err
	}

	id := sess.ID()
	bucket := prKey(req)
	r.mu.Lock()
	r.live[id] = sess
	r.bucket[id] = bucket
	r.byPR[bucket]++
	r.mu.Unlock()

	ref.AgentID = id
	return ref, nil
}

// WaitForAgent blocks until the session's turn finishes (or ctx/timeout fires),
// then drops it from the liveness table so a slot frees and the dedup gate clears.
func (r *controllerRunner) WaitForAgent(ctx context.Context, id string, timeout time.Duration) {
	if id == "" {
		return
	}
	r.mu.Lock()
	sess := r.live[id]
	r.mu.Unlock()
	if sess == nil {
		return
	}
	if w, ok := sess.(waiter); ok {
		w.Wait(ctx, timeout)
	}
	r.forget(id)
}

// HasLiveAgent reports whether a session for this PR+kind is still open — the
// engine's re-dispatch dedup gate for live-gated kinds (reviews).
func (r *controllerRunner) HasLiveAgent(_ context.Context, prKeyStr, kind string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byPR[prKeyStr+"\x00"+kind] > 0
}

// Archive closes a finished session and drops it from the liveness table.
func (r *controllerRunner) Archive(ctx context.Context, agentID string) error {
	if agentID == "" {
		return nil
	}
	r.mu.Lock()
	sess := r.live[agentID]
	r.mu.Unlock()
	var err error
	if sess != nil {
		err = sess.Close(ctx)
	}
	r.forget(agentID)
	return err
}

// forget removes a session from the liveness indexes, decrementing exactly the
// PR+kind bucket it was dispatched under.
func (r *controllerRunner) forget(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.live[id]; !ok {
		return
	}
	delete(r.live, id)
	bucket := r.bucket[id]
	delete(r.bucket, id)
	if n := r.byPR[bucket]; n <= 1 {
		delete(r.byPR, bucket)
	} else {
		r.byPR[bucket] = n - 1
	}
}

// resolvePermission applies conductor's permission policy to an agent's request.
// With a Handler wired (the session broker + HandoffChannel land in M2), the
// decision is delegated to it — the clean hook for channel routing. Until then, or
// for an autonomous fixer with no handler, the policy auto-approves: it selects the
// first allow option the agent offered, or approves outright when it offered none.
// This is the single place the auto-approve default lives, so M2 replaces one call.
func resolvePermission(ctx context.Context, h Handler, req PermissionRequest) (PermissionOutcome, error) {
	if h != nil {
		return h.RequestPermission(ctx, req)
	}
	return autoApprove(req), nil
}

// autoApprove is the no-handler default: pick an allow option (preferring a
// one-shot allow over allow-always), else a bare approval.
func autoApprove(req PermissionRequest) PermissionOutcome {
	allowOnce, allowAny := "", ""
	for _, opt := range req.Options {
		l := strings.ToLower(opt)
		switch {
		case strings.Contains(l, "allow") && (strings.Contains(l, "once") || strings.Contains(l, "one")):
			if allowOnce == "" {
				allowOnce = opt
			}
		case strings.Contains(l, "allow") || strings.Contains(l, "approve") || strings.Contains(l, "yes"):
			if allowAny == "" {
				allowAny = opt
			}
		}
	}
	switch {
	case allowOnce != "":
		return PermissionOutcome{Selected: allowOnce, Approved: true}
	case allowAny != "":
		return PermissionOutcome{Selected: allowAny, Approved: true}
	default:
		return PermissionOutcome{Approved: true}
	}
}
