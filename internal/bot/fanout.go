package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/store"
)

// FAN-OUT / JOIN. The home session can split one case across several workers
// (`conductor bot spawn --case c4 --group recon --brief …`, once per
// worker) and collect them together (`conductor bot join --case c4
// --group recon`). The ledger tracks the group; each worker is an ordinary
// cli worker with the bot's same verbs, bound to the case. A group reports
// back into the home conversation ONCE, when its last worker finishes, with
// every worker's result side by side. worker.max_per_case caps a fan-out's
// width.

// maxOutputBytes bounds the step.done output kept per worker.
const maxOutputBytes = 16 << 10

func liveWorker(w *store.Worker) bool { return w.Status == "starting" || w.Status == "running" }

func validLabel(s string) bool { return len(s) <= 32 && validName(s) }

// validName is a short lowercase slug.
func validName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// openGroup is the case's not-yet-reported group under label (nil if none):
// a label reused after its group reported starts a new group.
func openGroup(r *store.BotRecord, caseID, label string) *store.Group {
	var ids []string
	for id, g := range r.Groups {
		if g.CaseID == caseID && g.Label == label && !g.Reported {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	return r.Groups[ids[len(ids)-1]]
}

// groupDone reports whether every worker of a group has finished.
func groupDone(r *store.BotRecord, g *store.Group) bool {
	for _, id := range g.Workers {
		if w := r.Workers[id]; w != nil && liveWorker(w) {
			return false
		}
	}
	return len(g.Workers) > 0
}

// boundedOutput keeps a worker's step.done output as JSON, capped.
func boundedOutput(output any) json.RawMessage {
	if output == nil {
		return nil
	}
	b, err := json.Marshal(output)
	if err != nil || string(b) == "null" {
		return nil
	}
	if len(b) > maxOutputBytes {
		b, _ = json.Marshal(map[string]any{"truncated": true, "head": string(b[:maxOutputBytes])})
	}
	return b
}

// GroupReport is the one message a finished group sends home.
func GroupReport(g store.Group, ws []store.Worker) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[conductor] worker group %s (%q, case %s) finished — %d workers:\n", g.ID, g.Label, g.CaseID, len(ws))
	for _, w := range ws {
		fmt.Fprintf(&b, "\n### worker %s (%s)\n%s\n", w.ID, w.Status, fence("WORKER REPORT", w.Result))
	}
	return b.String()
}

// JoinResult is what a join hands back.
type JoinResult struct {
	Group   string       `json:"group"`
	CaseID  string       `json:"case"`
	Label   string       `json:"label"`
	Done    bool         `json:"done"`
	Workers []JoinWorker `json:"workers"`
}

// JoinWorker is one worker's slice of a join.
type JoinWorker struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Brief  string          `json:"brief"`
	Result string          `json:"result,omitempty"`
	Output json.RawMessage `json:"output,omitempty"`
}

// findGroup resolves a group by id, or by case + label (the newest).
func findGroup(rec store.BotRecord, ref, caseID, label string) (*store.Group, error) {
	if ref != "" {
		if g := rec.Groups[ref]; g != nil {
			return g, nil
		}
		return nil, fmt.Errorf("no worker group %s", ref)
	}
	if caseID == "" || label == "" {
		return nil, fmt.Errorf("name a group id, or --case and --group")
	}
	var best *store.Group
	for _, g := range rec.Groups {
		if g.CaseID == caseID && g.Label == label && (best == nil || g.Created.After(best.Created) || g.Created.Equal(best.Created) && g.ID > best.ID) {
			best = g
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no worker group %q on case %s", label, caseID)
	}
	return best, nil
}

// Join waits (up to timeout) for every worker of a group and returns their
// results together. Done=false when the timeout came first; the caller
// polls again.
func (s *Service) Join(ctx context.Context, c Actor, ref, caseID, label string, timeout time.Duration) (JoinResult, error) {
	if c.Role != config.BotRoleHome {
		return JoinResult{}, fmt.Errorf("only the home session joins worker groups")
	}
	if timeout <= 0 || timeout > time.Hour {
		timeout = 30 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	for {
		rec := s.Ledger.Bot(c.Bot)
		g, err := findGroup(rec, ref, caseID, label)
		if err != nil {
			return JoinResult{}, err
		}
		out := JoinResult{Group: g.ID, CaseID: g.CaseID, Label: g.Label, Done: groupDone(&rec, g)}
		for _, id := range g.Workers {
			if w := rec.Workers[id]; w != nil {
				out.Workers = append(out.Workers, JoinWorker{ID: w.ID, Status: w.Status, Brief: oneLine(w.Brief, 200), Result: w.Result, Output: w.Output})
			}
		}
		if out.Done || time.Now().After(deadline) {
			return out, nil
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
