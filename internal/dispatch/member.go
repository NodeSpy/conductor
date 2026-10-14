package dispatch

import (
	"context"
	"fmt"
	"strings"
)

// Member launches (docs/design/members.md). A member's HOME session is the
// one agent the operator talks to, so it runs on paseo through this
// dispatcher's ordinary launch path (d.paseo: labels, ownership ledger, skill
// creds). Its workers are ordinary flow runs on the cli runtime and never
// come through here — except an explicit hand-off worker, which is an
// ordinary background step.

// MemberBinding is the member a dispatch's grant is bound to: read from the
// synthesized step (config.Step.BoundMember, never decoded from YAML) and, for a
// worker, the case the member service fired the run for. Zero for every
// other dispatch.
type MemberBinding struct {
	Name, Role, CaseID string
}

// MemberContextCase is the trigger-context key the member service puts a
// worker's case id under (alongside inputs.*).
const MemberContextCase = "member_case"

// memberBinding reads a request's member binding.
func memberBinding(req Request) MemberBinding {
	if req.Step.BoundMember == "" {
		return MemberBinding{}
	}
	b := MemberBinding{Name: req.Step.BoundMember, Role: req.Step.BoundMemberRole}
	if c, ok := req.Trigger.Context[MemberContextCase].(string); ok {
		b.CaseID = c
	}
	return b
}

// CreateLocalWorkspace creates a paseo workspace over an existing directory
// and records it as conductor-owned, so archiving the agent in it reclaims it
// (Archive maps the agent's cwd back to this workspace).
func (d *Dispatcher) CreateLocalWorkspace(ctx context.Context, path, title string) (string, error) {
	release := holdProjects()
	defer release()
	res, err := d.backend().CreateWorkspace(ctx, CreateWorkspaceOptions{Isolation: "local", Path: path, Title: title})
	if err != nil {
		return "", err
	}
	if res.WorkspaceID == "" {
		return "", fmt.Errorf("paseo created no workspace for %s", path)
	}
	d.Owned.AddWorkspace(res.WorkspaceID)
	return res.WorkspaceID, nil
}

// AgentStatus reports an agent's paseo status ("running", "idle", …), or
// "gone" when paseo no longer lists it.
func (d *Dispatcher) AgentStatus(ctx context.Context, id string) (string, error) {
	agents, err := d.backend().ListAgents(ctx, nil)
	if err != nil {
		return "", err
	}
	for _, a := range agents {
		if a.ID == id || (len(id) >= 8 && strings.HasPrefix(a.ID, id)) {
			return strings.ToLower(a.Status), nil
		}
	}
	return "gone", nil
}

// ReleaseWorkspace archives a workspace conductor created whose launch then
// failed, and forgets it. It refuses a workspace the ownership ledger does
// not hold, like every archive.
func (d *Dispatcher) ReleaseWorkspace(ctx context.Context, id string) error {
	if id == "" || !d.Owned.HasWorkspace(id) {
		return fmt.Errorf("workspace %q is not conductor-owned", id)
	}
	if err := d.backend().ArchiveWorkspace(ctx, id); err != nil {
		return err
	}
	d.Owned.forget(id, "")
	return nil
}
