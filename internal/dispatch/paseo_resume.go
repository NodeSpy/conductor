package dispatch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// Agent-step resume on the paseo runtime (resume.go has the contract). A paseo
// agent lives in the paseo daemon, not in conductor, so a conductor restart
// leaves it where it was: still working, or idle with its turn done. Resuming
// is finding it again and sending it the restart note as its next turn.
//
// A foreground `paseo run` reports the agent's id only when its turn ends — a
// restart in the middle of the turn never learns it. So the session id
// conductor records is its own: a fresh token, put on the agent as the
// stepSessionLabel label at launch, by which `paseo ls` finds it again.

// stepSessionLabel is the label naming a step's agent for a restart.
const stepSessionLabel = "conductor-session"

// paseoKept is where a step's fresh agent starts after its old agent could not
// be resumed: the kept workspace (ws/cwd, or the dispatch's own directory when
// the old agent had no workspace of its own), told so by the restart note.
type paseoKept struct {
	kept     bool
	ws, cwd  string
	fallback string // why the old agent was not resumed
}

// resumeStepAgent continues the agent req.Resume recorded, when it is still
// there. handled=false: it is not, and the step starts a fresh agent — in
// kept's workspace when that survived.
func (d *Dispatcher) resumeStepAgent(ctx context.Context, req Request) (ref RunRef, handled bool, kept paseoKept, err error) {
	prior := req.Resume
	id, fallback := d.findStepAgent(ctx, prior)
	if id != "" {
		var sent bool
		ref, sent, err = d.continueStepAgent(ctx, req, id)
		if err == nil || sent || ctx.Err() != nil {
			return ref, true, paseoKept{}, err
		}
		fallback = "resumed agent " + id + " failed: " + err.Error()
		// The old agent stays out of the way of the fresh one: it alone is
		// archived, never its workspace (the fresh agent works there next).
		if d.Owned.HasAgent(id) && d.backend().ArchiveAgent(context.WithoutCancel(ctx), id) == nil {
			d.Owned.forget("", id)
		}
	}
	return RunRef{}, false, d.keptWorkspace(ctx, prior, fallback), nil
}

// findStepAgent returns the live agent prior's session token names, or why
// there is none.
func (d *Dispatcher) findStepAgent(ctx context.Context, prior *AgentSession) (id, fallback string) {
	if prior.SessionID == "" || !prior.CanResume {
		return "", "the runtime reported no session it can resume"
	}
	agents, err := d.backend().ListAgents(ctx, map[string]string{"conductor": "1", stepSessionLabel: prior.SessionID})
	if err != nil {
		return "", "could not list paseo agents: " + err.Error()
	}
	for _, a := range agents {
		if a.ID != "" {
			return a.ID, ""
		}
	}
	return "", "its paseo agent is gone"
}

// continueStepAgent puts the step's live agent back to work: once its
// interrupted turn is over, it gets the restart note, and that turn's output
// is the step's. sent: the agent took the note, so an error after it is the
// step's own, not a failed resume.
func (d *Dispatcher) continueStepAgent(ctx context.Context, req Request, id string) (ref RunRef, sent bool, err error) {
	ref = RunRef{Backend: "paseo", Kind: req.Trigger.Kind, AgentID: id}
	if !d.remote() {
		ref.Workdir = req.Resume.Cwd
	}
	// The agent was found by the label this conductor put on it, so it is
	// conductor's to archive, though the interrupted launch never got to
	// record it. Its done call carries the dispatch id it was launched
	// under: that dispatch is in flight again, delivering to this one.
	d.Owned.AddAgent(id)
	prev := d.Owned.DispatchesForAgent(id)
	d.Owned.BindDispatch(req.DispatchID, id)
	for _, did := range prev {
		if did != req.DispatchID {
			d.inflight.Store(did, true)
			defer d.inflight.Delete(did)
		}
	}
	req.OnAgentStart(AgentStart{AgentSession: *req.Resume, Resumed: true, KeptWorkspace: true})

	// A turn still running finishes first; the note comes after it.
	_ = d.backend().Wait(ctx, id)
	if err := ctx.Err(); err != nil {
		return ref, false, err
	}
	schema := req.Action.OutputSchema
	note := ResumeNote
	if len(schema) > 0 {
		// The reply carries the answer: a done call made with the token
		// the agent was launched with may not reach this conductor.
		note += schemaDirective(schema)
		for _, did := range append(prev, req.DispatchID) {
			if did != "" {
				d.registerOutputSlot(did, schema)
				defer d.dropOutputSlot(did)
			}
		}
	}
	if ref.Output, err = d.SendCapture(ctx, id, note); err != nil || len(schema) == 0 {
		return ref, err == nil, err
	}
	for corrected := false; ; corrected = true {
		for _, did := range append(prev, req.DispatchID) {
			if v, ok := d.takeDeliveredOutput(did); ok {
				ref.Output = marshalCanonicalValue(v)
				return ref, true, nil
			}
		}
		if obj, ok := d.captureSchemaAnswer(ctx, RunAgentResult{AgentID: id}, schema); ok {
			ref.Output = marshalCanonical(obj)
			return ref, true, nil
		}
		if corrected {
			break
		}
		// One corrective turn, as a fresh launch gets.
		if _, err := d.SendCapture(ctx, id, "Your previous reply was not valid JSON matching the schema. Return ONLY the JSON object."+schemaDirective(schema)); err != nil {
			return ref, true, err
		}
	}
	return ref, true, fmt.Errorf("output_schema: the resumed agent's reply did not match the schema")
}

// keptWorkspace resolves where the fresh agent replacing prior's starts.
func (d *Dispatcher) keptWorkspace(ctx context.Context, prior *AgentSession, fallback string) paseoKept {
	k := paseoKept{fallback: fallback}
	if prior.Workspace == "" {
		// No workspace of its own: the dispatch resolves the same directory
		// again (a pin, a work_dir).
		k.kept = true
		return k
	}
	list, err := d.backend().ListWorkspaces(ctx)
	if err != nil {
		k.fallback += "; its workspace could not be checked: " + err.Error()
		return k
	}
	for _, w := range list {
		if w.WorkspaceID == prior.Workspace {
			k.kept, k.ws, k.cwd = true, w.WorkspaceID, prior.Cwd
			if k.cwd == "" || d.remote() {
				k.cwd = w.Cwd
			}
			return k
		}
	}
	k.fallback += "; its workspace is gone"
	return k
}

// newStepSessionToken mints the token a step's agent is labelled with.
func newStepSessionToken() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
