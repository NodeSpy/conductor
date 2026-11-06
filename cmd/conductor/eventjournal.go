package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/NodeSpy/conductor/internal/attention"
	"github.com/NodeSpy/conductor/internal/bot"
	"github.com/NodeSpy/conductor/internal/browser"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dashboard"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/journal"
	"github.com/NodeSpy/conductor/internal/notify"
	"github.com/NodeSpy/conductor/internal/store"
)

// The daemon's event journal and attention view (docs/design/event-journal.md):
// one durable log every client follows by cursor, and the set of things that
// need an operator kept beside it. Producers record into them synchronously;
// the dashboard serves both.

func journalDir(cfg *config.Config) string {
	return filepath.Join(filepath.Dir(historyDirPath(cfg)), "journal")
}

// openEventJournal opens the journal and the attention view under the state
// directory. A journal that can't open is logged and the daemon runs without
// one (the dashboard then keeps a private, memory-only journal).
func openEventJournal(cfg *config.Config, logf func(string, ...any)) (*journal.Journal, *attention.Service) {
	dir := journalDir(cfg)
	j, err := journal.Open(journal.Options{Dir: dir})
	if err != nil {
		logf("journal: not opened (%v); event streams are live-only this run", err)
		return nil, nil
	}
	att := attention.New(attention.Options{
		Journal:   j,
		StatePath: filepath.Join(dir, "attention.json"),
		Log:       logf,
	})
	return j, att
}

// lifecyclePublisher wraps the conductor.* lifecycle publisher so the events
// that hand something to the operator (an escalation, a run waiting on a live
// agent) also raise an attention item. A later successful run of the same
// kind on the same target resolves it (RecordRunEvent).
func lifecyclePublisher(att *attention.Service, next func(context.Context, string, core.Trigger, string, map[string]any)) func(context.Context, string, core.Trigger, string, map[string]any) {
	return func(ctx context.Context, event string, t core.Trigger, line string, extra map[string]any) {
		if att != nil {
			if it, ok := lifecycleItem(event, t, line); ok {
				att.Raise(it)
			}
		}
		next(ctx, event, t, line, extra)
	}
}

func lifecycleItem(event string, t core.Trigger, line string) (attention.Item, bool) {
	var title string
	sev := attention.Warn
	switch event {
	case notify.EventEscalate:
		title = "Escalated: " + lifecycleLabel(t)
		sev = attention.Urgent
	case notify.EventNeedsInput:
		title = "Needs your input: " + lifecycleLabel(t)
	default:
		return attention.Item{}, false
	}
	subject := dashboard.TargetSubject(t.Kind, t.Target.Repo, t.Target.Number)
	return attention.Item{
		Key: event + ":" + strings.TrimPrefix(subject, "target:"), Kind: event, Subject: subject,
		Title: title, Detail: line, Link: "#/activity", Severity: sev,
	}, true
}

func lifecycleLabel(t core.Trigger) string {
	s := t.Kind
	if t.Target.Repo != "" {
		s += " on " + t.Target.Repo
		if t.Target.Number > 0 {
			s += "#" + strconv.Itoa(t.Target.Number)
		}
	}
	return s
}

// wireEventJournal connects the producers that exist once the stack is built:
// run events (the flow hub's synchronous tap), the bots' chat and ticket
// changes, their pending proposals and notices, and the browser's purchases
// awaiting approval. It starts the attention service's reconcile loop.
func wireEventJournal(ctx context.Context, j *journal.Journal, att *attention.Service, events *flow.EventHub, bots *botStack) {
	if j == nil {
		return
	}
	if events != nil {
		events.SetTap(func(ev flow.RunEvent) { dashboard.RecordRunEvent(j, att, ev) })
	}
	if bots != nil {
		svc := bots.svc
		svc.Record = func(topic, typ, subject string, data any) { _, _ = j.Append(topic, typ, subject, data) }
		// A new proposal or a decision changes what the proposals source
		// reports: reconcile now rather than on the next interval.
		onProposal := bots.gate.OnProposal
		bots.gate.OnProposal = func(name string, p store.Proposal) {
			if onProposal != nil {
				onProposal(name, p)
			}
			att.Nudge()
		}
		if svc.Terraform != nil {
			svc.Terraform.OnPlanned = bots.gate.OnProposal
		}
		onDecision := svc.Exec.Notify
		svc.Exec.Notify = func(name string, p store.Proposal) {
			if onDecision != nil {
				onDecision(name, p)
			}
			att.Nudge()
		}
		notice := svc.Notify
		svc.Notify = func(ctx context.Context, n bot.Notice) {
			att.Raise(noticeItem(n))
			if notice != nil {
				notice(ctx, n)
			}
		}
		att.AddSource(attention.Source{Name: "proposals", List: func(context.Context) ([]attention.Item, error) {
			return proposalItems(svc), nil
		}})
	}
	att.AddSource(attention.Source{Name: "purchases", List: func(context.Context) ([]attention.Item, error) {
		m := browserMgr.Load()
		if m == nil || m.Purchases == nil {
			return nil, nil
		}
		ps, err := m.Purchases.List(browser.PurchasePending)
		if err != nil {
			return nil, err
		}
		return purchaseItems(ps), nil
	}})
	go att.Run(ctx)
}

func proposalItems(svc *bot.Service) []attention.Item {
	var out []attention.Item
	for _, name := range svc.Cfg().BotNames() {
		for _, p := range svc.Proposals(name, false) {
			if p.Status != "pending" {
				continue
			}
			what := p.Uses
			if what == "" {
				what = p.Kind
			}
			out = append(out, attention.Item{
				Key: "proposal:" + name + "/" + p.ID, Kind: "proposal",
				Title:  fmt.Sprintf("%s asks to run %s", name, what),
				Detail: p.Summary, Link: "#/bots/" + name + "/proposals/" + p.ID,
				Severity: attention.Urgent, Opened: p.Created,
			})
		}
	}
	return out
}

func purchaseItems(ps []browser.Purchase) []attention.Item {
	out := make([]attention.Item, 0, len(ps))
	for _, p := range ps {
		title := fmt.Sprintf("Approve a purchase: %s, %.2f %s", p.Merchant, p.Total, p.Currency)
		if p.Recurring {
			title += " every " + p.Interval
		}
		out = append(out, attention.Item{
			Key: "purchase:" + p.ID, Kind: "purchase", Title: title, Detail: p.Why,
			Link: "#/browser/purchases/" + p.ID, Severity: attention.Urgent, Opened: p.Created,
		})
	}
	return out
}

func noticeItem(n bot.Notice) attention.Item {
	sev := attention.Warn
	switch strings.ToLower(n.Severity) {
	case "critical", "high", "urgent":
		sev = attention.Urgent
	case "low", "info":
		sev = attention.Info
	}
	return attention.Item{
		Key: "bot_notice:" + n.Bot + ":" + n.Key, Kind: "bot_notice",
		Title: fmt.Sprintf("%s found: %s", n.Bot, n.Title), Detail: n.Text,
		Link: "#/bots/" + n.Bot, Severity: sev,
	}
}
