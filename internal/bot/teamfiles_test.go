package bot

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/blob"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
)

// attachTeam gives a team fixture a blob store and a state dir, as
// attachments need, and leaves the read-only message dirs removable.
func attachTeam(t *testing.T, f *teamFixture) {
	t.Helper()
	state := t.TempDir()
	config.SetStateDir(state)
	t.Cleanup(func() { config.SetStateDir("") })
	bs, err := blob.Open(filepath.Join(state, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	f.svc.Blobs = bs
	t.Cleanup(func() { chmodTree(state) })
}

// fetchFiles is a connector's file fetcher: it hands back the files, refusing
// when there are more than the bot's cap allows.
func fetchFiles(files ...connector.File) func(context.Context, int, int64) ([]connector.File, error) {
	return func(_ context.Context, maxFiles int, _ int64) ([]connector.File, error) {
		if len(files) > maxFiles {
			return nil, fmt.Errorf("%d files, over the cap of %d per message (attachments.max_files)", len(files), maxFiles)
		}
		return files, nil
	}
}

// A LEAD'S TRIAGE DOES NOT CARRY FILES: triage is asynchronous and chooses the
// handling bot later, so an event's files are not fetched or stored for the
// lead — the triage case says so, and the fetcher is never called.
func TestLeadTriageDoesNotCarryFiles(t *testing.T) {
	f := newTeamFixture(t, nil)
	attachTeam(t, f)
	ctx := context.Background()

	called := false
	fetch := func(_ context.Context, _ int, _ int64) ([]connector.File, error) {
		called = true
		return []connector.File{{Name: "shot.png", Data: pngBytes(t, 4, 4)}}, nil
	}
	// source chat.mention does not match the billing rule (pd.*), so it goes
	// to the lead's triage.
	out, err := f.svc.RouteTeam(ctx, TeamEvent{Team: "platform", Source: "chat.mention", Requester: "slack:U1",
		Title: "look at this", Text: "p99 spike", files: fetch})
	if err != nil {
		t.Fatal(err)
	}
	if out["via"] != "lead" {
		t.Fatalf("expected the lead to triage, got %v", out)
	}
	if called {
		t.Fatal("the lead's triage fetched the event's files")
	}
	lc := out["case"].(string)
	tc := f.caseOf(t, "lead", lc)
	if !strings.Contains(tc.Payload, "does not pass on") {
		t.Fatalf("the triage case does not note the dropped files: %q", tc.Payload)
	}
	if n := len(f.svc.Attachments("lead")); n != 0 {
		t.Fatalf("the lead stored %d attachments for a triage case", n)
	}
	// The triage worker's brief carries the note, not a file fence.
	prompt := f.workers.fired[len(f.workers.fired)-1].inputs["prompt"].(string)
	if !strings.Contains(prompt, "does not pass on") {
		t.Fatalf("the triage brief does not carry the dropped-files note:\n%s", prompt)
	}
	if strings.Contains(prompt, "shot.png") || strings.Contains(prompt, "FILES ") {
		t.Fatalf("the triage brief leaked the event's files:\n%s", prompt)
	}
}

// A RULE- OR FALLBACK-ROUTED EVENT CARRIES ITS FILES to the handling bot: the
// route goes straight to one bot, so its files are fetched, checked against
// that bot's caps, stored, and delivered in its inbox.
func TestTeamRoutedEventDeliversFiles(t *testing.T) {
	// Rule route: pd.* + service billing-* → bot b.
	t.Run("rule", func(t *testing.T) {
		f := newTeamFixture(t, nil)
		attachTeam(t, f)
		png := pngBytes(t, 4, 4)
		out, err := f.svc.RouteTeam(context.Background(), TeamEvent{Team: "platform", Source: "pd.incident",
			Title: "billing 5xx", Fields: map[string]any{"service": "billing-api"},
			files: fetchFiles(connector.File{Name: "trace.png", Data: png})})
		if err != nil {
			t.Fatal(err)
		}
		if out["via"] != "rule" || out["bot"] != "b" {
			t.Fatalf("expected a rule route to b, got %v", out)
		}
		atts := f.svc.Attachments("b")
		if len(atts) != 1 || atts[0].Name != "trace.png" || atts[0].MediaType != "image/png" || atts[0].Size != int64(len(png)) {
			t.Fatalf("the routed event's file did not reach b: %+v", atts)
		}
		if atts[0].CaseID != out["case"].(string) {
			t.Fatalf("the file is not on the routed case: %+v", atts[0])
		}
		// Delivered: b's inbox message carries the attachment id.
		var carried bool
		for _, m := range f.st.Bot("b").Inbox {
			for _, id := range m.Attachments {
				if id == atts[0].ID {
					carried = true
				}
			}
		}
		if !carried {
			t.Fatal("b's inbox message did not carry the routed file")
		}
		// No other bot was handed the file.
		if n := len(f.svc.Attachments("lead")) + len(f.svc.Attachments("a")); n != 0 {
			t.Fatalf("the file leaked to another bot: %d", n)
		}
	})

	// Fallback route: no lead, no rule match → the team's fallback (a).
	t.Run("fallback", func(t *testing.T) {
		f := newTeamFixture(t, func(c *config.Config) {
			tc := c.Teams["platform"]
			r := *tc.Routing
			r.Lead = "" // no lead: an unmatched event falls back
			tc.Routing = &r
			c.Teams["platform"] = tc
		})
		attachTeam(t, f)
		out, err := f.svc.RouteTeam(context.Background(), TeamEvent{Team: "platform", Source: "chat.mention",
			Title: "disk full", files: fetchFiles(connector.File{Name: "df.txt", Data: []byte("100% used")})})
		if err != nil {
			t.Fatal(err)
		}
		if out["via"] != "fallback" || out["bot"] != "a" {
			t.Fatalf("expected a fallback route to a, got %v", out)
		}
		atts := f.svc.Attachments("a")
		if len(atts) != 1 || atts[0].Name != "df.txt" || atts[0].MediaType != "text/plain" {
			t.Fatalf("the fallback did not receive the event's file: %+v", atts)
		}
		if atts[0].CaseID != out["case"].(string) {
			t.Fatalf("the file is not on the fallback case: %+v", atts[0])
		}
	})
}
