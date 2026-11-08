package main

import (
	"context"
	"fmt"

	"github.com/NodeSpy/conductor/internal/dashboard"
)

// dashboardDecideEdit is the dashboard's DecideProposalEdit: an approval of a
// team knowledge proposal with the operator's edited text, through the same
// Executor.Decide (Decision.Edit) `conductor bot approve --edit` uses.
func dashboardDecideEdit(ctx context.Context, botName, id, operator, edit string, ev dashboard.StepupEvidence) error {
	if botCtl == nil || botCtl.Exec == nil {
		return fmt.Errorf("no bots on this daemon")
	}
	d := dashboardDecision(botName, id, "approve", operator, "", ev)
	d.Edit = edit
	_, err := botCtl.Exec.Decide(ctx, d)
	return err
}
