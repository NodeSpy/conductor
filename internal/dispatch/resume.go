package dispatch

// Agent-step resume across restarts. When a step's agent starts, its runtime
// reports where it runs (AgentSession) through Request.OnAgentStart; the flow
// keeps that with the run. A restart that interrupts the step re-dispatches it
// with Request.Resume set, and the runtime puts the agent back: the same
// workspace, the same session when the runtime can resume one. Core knows only
// a session id, a workspace and whether the runtime can resume — how a session
// is resumed is the runtime's business.

// AgentSession is where a step's agent runs, as its runtime reports it once the
// agent has started.
type AgentSession struct {
	// Runtime is the runtime (controller) the agent runs on. A resume on a
	// different runtime never reuses the session.
	Runtime string
	// SessionID is the runtime's own id for the agent's session ("" = none it
	// could resume).
	SessionID string
	// Workspace is the id of the workspace conductor provisioned for the agent
	// ("" = none); Cwd is the directory the agent works in.
	Workspace string
	Cwd       string
	// CanResume: the runtime declared it can resume SessionID.
	CanResume bool
}

// AgentStart is what a runtime reports through Request.OnAgentStart.
type AgentStart struct {
	AgentSession
	// Resumed: the runtime resumed Request.Resume's session.
	Resumed bool
	// KeptWorkspace: the agent runs in Request.Resume's workspace.
	KeptWorkspace bool
	// Fallback says why a requested resume did not happen ("" when it did, or
	// when none was requested).
	Fallback string
}

// ResumeNote is the turn a resumed agent session receives: it already holds
// the step's prompt and its own work so far.
const ResumeNote = "Conductor restarted while you were working on this step, which interrupted you. " +
	"Your workspace is as you left it. Continue the task from where you stopped."

// RestartNote leads the prompt of a fresh agent started in a step's kept
// workspace, when the previous agent's session could not be resumed.
const RestartNote = "Note: conductor restarted while an earlier agent was working on this step. " +
	"Its work so far, including uncommitted changes, is still in this workspace. " +
	"Check it (git status, git diff) and continue from there rather than starting over.\n\n"
