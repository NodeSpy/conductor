package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/NodeSpy/conductor/internal/attention"
	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/journal"
)

// RecordRunEvent puts one run event on the journal's run topic and keeps the
// attention view's run items current: a failed run raises an item, and a
// later successful run of the same workflow run resolves it. The daemon calls
// it synchronously from the flow hub's tap, so no run event is lost to a slow
// reader.
func RecordRunEvent(j *journal.Journal, a *attention.Service, ev flow.RunEvent) {
	data := api.RunEventData{
		Type: ev.Type, Run: ev.Run, RunID: ev.RunID, Kind: ev.Kind,
		Repo: ev.Repo, Number: ev.Number, Step: ev.Step,
		Status: ev.Status, Detail: ev.Detail, DurationMS: ev.DurationMS,
	}
	if j != nil {
		_, _ = j.Append(string(api.TopicRun), ev.Type, ev.Run, data)
	}
	if a == nil || ev.Type != "run_done" {
		return
	}
	subject := RunSubject(ev.RunID, ev.Run)
	switch ev.Status {
	case "failed":
		a.Raise(attention.Item{
			Key: "run_failed:" + ev.Run, Kind: "run_failed", Subject: subject,
			Title: fmt.Sprintf("%s failed", runLabel(ev)), Detail: ev.Detail,
			Link: "#/runs/" + ev.Run, Severity: attention.Warn,
		})
	case "ok":
		a.ResolveSubject(subject)
		a.ResolveSubject(TargetSubject(ev.Kind, ev.Repo, ev.Number))
	}
}

// TargetSubject is the attention subject of items about a kind of work on one
// target (an escalation, a hand-off waiting on a live agent): a later
// successful run of that kind on that target resolves them.
func TargetSubject(kind, repo string, number int) string {
	return "target:" + kind + ":" + repo + "#" + strconv.Itoa(number)
}

// RunSubject is the attention subject that ties a workflow run's items
// together: its run id (kind:key) when it has one, else its history id.
func RunSubject(runID, run string) string {
	if runID != "" {
		return "run:" + runID
	}
	return "run:" + run
}

func runLabel(ev flow.RunEvent) string {
	label := ev.Kind
	if label == "" {
		label = "run"
	}
	if ev.Repo != "" {
		label += " on " + ev.Repo
		if ev.Number > 0 {
			label += "#" + strconv.Itoa(ev.Number)
		}
	}
	return label
}

// bridgeRuns feeds the flow hub into the journal for a server built without
// the daemon's journal (tests, a read-only embedding). The daemon taps the hub
// synchronously instead.
func (s *Server) bridgeRuns(ctx context.Context, fh *flow.EventHub) {
	ch, cancel := fh.Subscribe("")
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			RecordRunEvent(s.journal, s.attention, ev)
		}
	}
}

// apiEvent is a journal event as the API serves it.
func apiEvent(ev journal.Event) api.Event {
	out := api.Event{
		ID: ev.ID, Seq: ev.Seq, Topic: api.EventTopic(ev.Topic), Type: ev.Type,
		Subject: ev.Subject, TS: ev.TS.UTC().Format(time.RFC3339Nano),
	}
	if len(ev.Data) > 0 {
		out.Data = json.RawMessage(ev.Data)
	}
	return out
}

// privateJournal is a memory-only journal for a server built without the
// daemon's.
func privateJournal(log func(string, ...any)) *journal.Journal {
	j, err := journal.Open(journal.Options{})
	if err != nil {
		log("dashboard: private journal: %v", err) // only crypto/rand can fail
		panic(err)
	}
	return j
}
