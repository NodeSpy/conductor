package flow

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

type stubRouter struct{ got []BotRoute }

func (s *stubRouter) RouteBot(_ context.Context, r BotRoute) (map[string]any, error) {
	s.got = append(s.got, r)
	return map[string]any{"case": "c1"}, nil
}

// `bot: { team: … }` routes to the team with the event's own fields for
// its rules; the source is the trigger's, never the step's.
func TestBotStepRoutesToATeam(t *testing.T) {
	r := capRunner(t)
	sr := &stubRouter{}
	r.BotRouter = sr
	tr := core.Trigger{Source: "pagerduty", Instance: "pd", Kind: "incident"}
	step := config.Step{Bot: &config.BotStep{Team: "{{.team}}", Title: "{{.title}}", Requester: "{{.user}}", Urgent: true}}
	data := map[string]any{"team": "platform", "title": "db down", "user": "chat:U9", "service": "db-main"}
	if _, _, err := r.execBot(context.Background(), tr, step, data, false); err != nil {
		t.Fatal(err)
	}
	got := sr.got[0]
	if got.Team != "platform" || got.Bot != "" || got.Title != "db down" || got.Requester != "chat:U9" || !got.Urgent || got.Source != "pd.incident" {
		t.Fatalf("route: %+v", got)
	}
	if got.Fields["service"] != "db-main" {
		t.Fatalf("the rules do not see the event's fields: %v", got.Fields)
	}
	// A bot route carries no fields.
	step = config.Step{Bot: &config.BotStep{Name: "sre", Title: "x"}}
	if _, _, err := r.execBot(context.Background(), tr, step, data, false); err != nil {
		t.Fatal(err)
	}
	if got := sr.got[1]; got.Team != "" || got.Bot != "sre" || got.Fields != nil {
		t.Fatalf("bot route: %+v", got)
	}
	// Dry run routes nothing.
	step = config.Step{Bot: &config.BotStep{Team: "platform"}}
	if out, _, err := r.execBot(context.Background(), tr, step, data, true); err != nil || out["stubbed"] != true || len(sr.got) != 2 {
		t.Fatalf("dry run routed: %v %v", out, err)
	}
}

// A trigger that carries no provenance — the zero Trigger{}, no instance and no
// kind — must not be stamped with a non-empty-but-meaningless source (the "."
// the bare instance+"."+kind would produce). It is routed provenance-less (an
// empty Source) with no requester, so the bot intake's guard drops it to least
// trust instead of handing a requester-less event full event autonomy.
func TestBotStepProvenanceLessTriggerHasEmptySource(t *testing.T) {
	r := capRunner(t)
	sr := &stubRouter{}
	r.BotRouter = sr
	step := config.Step{Bot: &config.BotStep{Name: "sre", Title: "x"}} // no requester:
	if _, _, err := r.execBot(context.Background(), core.Trigger{}, step, nil, false); err != nil {
		t.Fatal(err)
	}
	if got := sr.got[0]; got.Source != "" || got.Requester != "" {
		t.Fatalf("a provenance-less trigger stamped source %q / requester %q — want both empty", got.Source, got.Requester)
	}
	// A real event still carries its provenance unchanged.
	if _, _, err := r.execBot(context.Background(), core.Trigger{Instance: "pd", Kind: "incident"}, step, nil, false); err != nil {
		t.Fatal(err)
	}
	if got := sr.got[1]; got.Source != "pd.incident" {
		t.Fatalf("a real event lost its provenance: source %q", got.Source)
	}
}

// AN EVENT'S FILES: a connector that declares `attachments` has its files
// fetched — through its fetch verb, lazily, within the receiving bot's caps
// (a declared size over the cap is refused before any fetch) — and only for
// a target the platform assigned. An event listing no files carries no
// fetcher.
func TestBotStepFetchesAnEventsFiles(t *testing.T) {
	r := capRunner(t)
	st := newFakeState(t, "svc")
	st.outputsFn["file_get"] = func(_ int, opts map[string]any) map[string]any {
		return map[string]any{"content": base64.StdEncoding.EncodeToString([]byte("bytes of " + fmt.Sprint(opts["id"])))}
	}
	sr := &stubRouter{}
	r.BotRouter = sr
	sem := &sdk.EventSemantics{Attachments: &sdk.AttachmentsSemantics{List: "files", Name: "name", Size: "size", Fetch: "file_get",
		Args: map[string]string{"id": "{{.id}}"}, Content: "content"}}
	files := []any{map[string]any{"id": "F1", "name": "a.txt", "size": 10}, map[string]any{"id": "F2", "name": "b.png", "size": 20}}
	tr := core.Trigger{Source: "svc", Instance: "svc", Kind: "message", Sem: sem, TargetTrusted: true, Context: map[string]any{"files": files}}
	step := config.Step{Bot: &config.BotStep{Name: "sre", Title: "x"}}
	ctx := context.Background()
	if _, _, err := r.execBot(ctx, tr, step, nil, false); err != nil {
		t.Fatal(err)
	}
	got := sr.got[0]
	if got.Files == nil {
		t.Fatal("no fetcher for an event with files")
	}
	fs, err := got.Files(ctx, 10, 1000)
	if err != nil || len(fs) != 2 || fs[0].Name != "a.txt" || string(fs[0].Data) != "bytes of F1" || string(fs[1].Data) != "bytes of F2" {
		t.Fatalf("fetched: %+v %v", fs, err)
	}
	calls := st.count("file_get")
	if _, err := got.Files(ctx, 1, 1000); err == nil || !strings.Contains(err.Error(), "2 files, over the cap of 1") {
		t.Fatalf("max files: %v", err)
	}
	if _, err := got.Files(ctx, 10, 15); err == nil || !strings.Contains(err.Error(), `"b.png": 20 bytes, over the cap of 15`) {
		t.Fatalf("declared size: %v", err)
	}
	if n := st.count("file_get"); n != calls+1 {
		t.Fatalf("over-cap files were fetched anyway: %d fetches", n-calls)
	}
	// A fetched file over the cap (no declared size) is refused too.
	sem.Attachments.Size = ""
	if _, err := got.Files(ctx, 10, 5); err == nil || !strings.Contains(err.Error(), "over the cap of 5") {
		t.Fatalf("fetched size: %v", err)
	}
	// Not assigned by the platform: the list is the sender's; nothing fetched.
	tr.TargetTrusted = false
	_, _, _ = r.execBot(ctx, tr, step, nil, false)
	before := st.count("file_get")
	if _, err := sr.got[1].Files(ctx, 10, 1000); !errors.Is(err, connector.ErrFilesNotFetched) || st.count("file_get") != before {
		t.Fatalf("an unassigned target's files: %v", err)
	}
	// No files listed: no fetcher.
	tr.TargetTrusted, tr.Context = true, map[string]any{"files": []any{}}
	_, _, _ = r.execBot(ctx, tr, step, nil, false)
	if sr.got[2].Files != nil {
		t.Fatal("a fetcher for an event with no files")
	}
}
