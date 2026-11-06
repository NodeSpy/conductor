package flow

import (
	"context"
	"time"

	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/store"
)

// MaxAgentResumes bounds how many restarts one step's agent is resumed
// through without finishing; past it the step starts a fresh agent.
const MaxAgentResumes = 3

// agentSessionStore is the optional Store surface agent-step resume needs.
// *store.Store satisfies it; a Store without it never resumes an agent.
type agentSessionStore interface {
	RunAgent(runID, key string) (store.RunAgent, bool)
	PutRunAgent(runID, key string, a store.RunAgent) error
	DeleteRunAgent(runID, key string) error
}

// stepPathKey carries the path of enclosing steps (a called workflow's step,
// a parallel branch) down to the agent steps inside them.
type stepPathKey struct{}

// withStepPath enters a nested step scope named seg.
func withStepPath(ctx context.Context, seg string) context.Context {
	if p, _ := ctx.Value(stepPathKey{}).(string); p != "" {
		seg = p + "/" + seg
	}
	return context.WithValue(ctx, stepPathKey{}, seg)
}

// agentKey is a step's key within its run: the same on every execution of
// the run, distinct for every agent step of it — a for_each item's id carries
// its index, and steps in a called workflow or a branch carry its path.
func agentKey(ctx context.Context, id string) string {
	if p, _ := ctx.Value(stepPathKey{}).(string); p != "" {
		return p + "/" + id
	}
	return id
}

// agentResume prepares req to resume the agent this step had running when the
// daemon last stopped, and to record the agent it starts now. The returned
// func is called once the dispatch has returned: a step that ended (done or
// failed) drops its record; one interrupted by shutdown keeps it for the
// restart.
func (r *Runner) agentResume(ctx context.Context, t core.Trigger, id string, req *dispatch.Request) func() {
	st, ok := r.Store.(agentSessionStore)
	runID := memory.SourceFrom(ctx).Run
	if !ok || runID == "" || req.Shadow || req.Step.Background || req.Step.Detach || req.Step.DecisionLaunch != nil {
		return func() {}
	}
	key := agentKey(ctx, id)
	resumes := 0
	if prior, found := st.RunAgent(runID, key); found {
		if prior.Resumes >= MaxAgentResumes {
			r.Log("%s step %s: its agent was resumed %d times without finishing — starting a fresh agent", flowTag(t), id, prior.Resumes)
			_ = st.DeleteRunAgent(runID, key)
		} else {
			resumes = prior.Resumes + 1
			prior.Resumes = resumes
			_ = st.PutRunAgent(runID, key, prior)
			req.Resume = &dispatch.AgentSession{Runtime: prior.Runtime, SessionID: prior.SessionID,
				Workspace: prior.Workspace, Cwd: prior.Cwd, CanResume: prior.CanResume}
		}
	}
	asked := req.Resume != nil
	req.OnAgentStart = func(s dispatch.AgentStart) {
		switch {
		case s.Resumed:
			r.Log("%s resuming agent session %s for step %s", flowTag(t), s.SessionID, id)
		case asked && s.KeptWorkspace:
			r.Log("%s resume unavailable: %s, fresh agent in kept workspace (step %s)", flowTag(t), s.Fallback, id)
		case asked:
			r.Log("%s resume unavailable: %s, fresh agent (step %s)", flowTag(t), s.Fallback, id)
		}
		_ = st.PutRunAgent(runID, key, store.RunAgent{Runtime: s.Runtime, SessionID: s.SessionID,
			Workspace: s.Workspace, Cwd: s.Cwd, CanResume: s.CanResume, Resumes: resumes, Started: time.Now()})
	}
	return func() {
		if !shuttingDown(ctx) {
			_ = st.DeleteRunAgent(runID, key)
		}
	}
}
