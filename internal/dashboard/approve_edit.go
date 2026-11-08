package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/NodeSpy/conductor/internal/dashboard/auth"
)

// APPROVE WITH CHANGES (a team knowledge proposal, design §4.4 "Suggested by
// bots"): the operator edits what a bot proposed for its team's knowledge and
// approves the edited text, which lands as one commit in the operator's name
// with the bot's proposal named in its trailers (Executor.Decide with Edit, as
// `conductor bot approve --edit` does). Like every decision it is behind a
// step-up, bound here to the proposal AND the exact edited text: the digest
// covers the edit's SHA-256, so a step-up for one text can't approve another.

// approvalEditDigest is the per-decision digest an approve-with-changes
// step-up commits to.
func approvalEditDigest(bot, id, actionSHA, edit string) string {
	h := sha256.Sum256([]byte(edit))
	b, _ := json.Marshal(map[string]string{
		"v": "1", "ns": "conductor-dashboard-approval-edit",
		"bot": bot, "proposal": id, "action_sha256": actionSHA, "decision": "approve",
		"edit_sha256": hex.EncodeToString(h[:]),
	})
	return auth.DigestOf(b)
}

func (s *Server) approveEditRoutes() {
	s.mux.HandleFunc("POST /api/v1/bots/{bot}/proposals/{id}/approval-edit", s.handleApprovalEditView)
	s.mux.HandleFunc("POST /api/v1/bots/{bot}/proposals/{id}/decision-edit", s.handleApprovalEditDecision)
}

// readEdit reads {"edit": "<the final text>"}.
func readEdit(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req struct {
		Edit string `json:"edit"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "malformed request", "")
		return "", false
	}
	if strings.TrimSpace(req.Edit) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "the edited text is empty", "write what should land, or deny the proposal")
		return "", false
	}
	return req.Edit, true
}

// handleApprovalEditView is the digest (and what the step-up shows) for
// approving a proposal with the given text.
func (s *Server) handleApprovalEditView(w http.ResponseWriter, r *http.Request) {
	if s.deps.DecideProposalEdit == nil || s.deps.ApprovalView == nil {
		writeError(w, http.StatusNotFound, "no_bots", "no bots on this daemon", "")
		return
	}
	edit, ok := readEdit(w, r)
	if !ok {
		return
	}
	bot, id := r.PathValue("bot"), r.PathValue("id")
	actionSHA, summary, operators, found := s.deps.ApprovalView(bot, id)
	if !found {
		writeError(w, http.StatusNotFound, "no_proposal", "no such proposal", "")
		return
	}
	digest := approvalEditDigest(bot, id, actionSHA, edit)
	desc := describeAction("approve bot "+bot+"'s proposal "+id+" with your changes", summary) +
		"\n  what lands instead (yours):\n    " + strings.Join(auth.SafeLines(edit), "\n    ")
	s.labels.note(r, auth.PurposeApprove, digest, desc)
	writeJSON(w, http.StatusOK, map[string]any{"bot": bot, "proposal": id, "digest": digest, "description": desc,
		"operators": operators, "purpose": string(auth.PurposeApprove)})
}

// handleApprovalEditDecision approves a proposal with the edited text, behind
// a step-up bound to that text.
func (s *Server) handleApprovalEditDecision(w http.ResponseWriter, r *http.Request) {
	if s.deps.DecideProposalEdit == nil || s.deps.ApprovalView == nil {
		writeError(w, http.StatusNotFound, "no_bots", "no bots on this daemon", "")
		return
	}
	if !s.authEnforced() {
		writeError(w, http.StatusForbidden, "auth_required", "approvals need a signed-in operator", "enrol an operator first")
		return
	}
	sess, ok := auth.SessionFrom(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "sign in to continue", "")
		return
	}
	edit, ok := readEdit(w, r)
	if !ok {
		return
	}
	bot, id := r.PathValue("bot"), r.PathValue("id")
	actionSHA, _, _, found := s.deps.ApprovalView(bot, id)
	if !found {
		writeError(w, http.StatusNotFound, "no_proposal", "no such proposal", "")
		return
	}
	digest := approvalEditDigest(bot, id, actionSHA, edit)
	ev, err := s.auth.RequireStepup(sess, auth.PurposeApprove, digest, r.Header.Get(auth.StepupHeader))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "stepup_required", err.Error(), "confirm it's you to approve")
		return
	}
	if err := s.deps.DecideProposalEdit(r.Context(), bot, id, sess.Operator, edit, ev); err != nil {
		writeError(w, http.StatusBadRequest, "decide_failed", err.Error(), "")
		return
	}
	s.audit(r, "dashboard.approval", digest, "bot", bot, "proposal", id, "decision", "approve", "edited", "true", "action_sha256", actionSHA)
	writeJSON(w, http.StatusOK, map[string]any{"bot": bot, "proposal": id, "decision": "approve", "edited": true, "by": sess.Operator})
}
