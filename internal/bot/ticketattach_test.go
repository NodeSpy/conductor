package bot

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/store"
)

// findAttach is the bot's stored attachment with this name, or a failure.
func findAttach(t *testing.T, f *svcFixture, bot, name string) store.Attachment {
	t.Helper()
	for _, a := range f.svc.Attachments(bot) {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("bot %s has no attachment named %q (%d stored)", bot, name, len(f.svc.Attachments(bot)))
	return store.Attachment{}
}

// commentByText is the ticket comment whose text contains sub.
func commentByText(t *testing.T, v TicketView, sub string) store.TicketComment {
	t.Helper()
	for _, c := range v.Comments {
		if strings.Contains(c.Text, sub) {
			return c
		}
	}
	t.Fatalf("ticket %s has no comment containing %q (%d comments)", v.Ticket, sub, len(v.Comments))
	return store.TicketComment{}
}

// TICKET ATTACHMENTS: a ticket carries files on create and on a comment. They
// are detected by content, checked against the holding bot's caps, stored
// read-only in conductor's state, surfaced as a comment's attachments, and —
// on an active ticket — delivered into the holding bot's inbox.
func TestTicketAttachmentsCreateAndComment(t *testing.T) {
	f, _ := attachSvc(t)
	ctx := context.Background()
	png := pngBytes(t, 4, 4)

	// `ticket new --attach`: a ready ticket for bot:sre, carrying two files.
	v, err := f.svc.TicketCreate(ctx, opActor, TicketNew{Title: "look at these", Assignee: "bot:sre", Ready: true,
		Files: []AttachmentIn{{Name: "shot.png", Data: png}, {Name: "notes.txt", Data: []byte("plain text here")}}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Holder != "sre" || v.Status != store.TicketInProgress {
		t.Fatalf("ready ticket did not land on sre: %+v", v)
	}
	// Stored, with the type DETECTED (never taken from the name) and the size.
	img := findAttach(t, f, "sre", "shot.png")
	txt := findAttach(t, f, "sre", "notes.txt")
	if img.MediaType != "image/png" || img.Size != int64(len(png)) || img.CaseID != v.ID {
		t.Fatalf("image attachment record: %+v", img)
	}
	if txt.MediaType != "text/plain" || txt.Size != int64(len("plain text here")) || txt.CaseID != v.ID {
		t.Fatalf("text attachment record: %+v", txt)
	}
	// Surfaced on the ticket: the create carries them as one comment.
	fc := commentByText(t, v, "files with the ticket")
	if len(fc.Attachments) != 2 {
		t.Fatalf("the create comment does not list both files: %+v", fc)
	}
	ids := map[string]bool{img.ID: true, txt.ID: true}
	for _, id := range fc.Attachments {
		if !ids[id] {
			t.Fatalf("the comment lists an attachment that is not stored: %s", id)
		}
	}

	// `ticket comment --attach`: a comment on the active ticket carries a file.
	v2, err := f.svc.TicketComment(ctx, opActor, v.Ticket, "and one more", []AttachmentIn{{Name: "extra.csv", Data: []byte("a,b\n1,2\n")}})
	if err != nil {
		t.Fatal(err)
	}
	csv := findAttach(t, f, "sre", "extra.csv")
	if csv.MediaType != "text/csv" || csv.CaseID != v.ID {
		t.Fatalf("csv attachment record: %+v", csv)
	}
	cc := commentByText(t, v2, "and one more")
	if len(cc.Attachments) != 1 || cc.Attachments[0] != csv.ID {
		t.Fatalf("the comment does not carry its file: %+v", cc)
	}
	if n := len(f.svc.Attachments("sre")); n != 3 {
		t.Fatalf("expected 3 stored attachments, got %d", n)
	}
	// The operator's comment on an active ticket reaches the holding bot's
	// inbox, carrying the attachment.
	var delivered bool
	for _, m := range f.st.Bot("sre").Inbox {
		if strings.Contains(m.Text, "commented on ticket "+v.Ticket) {
			if len(m.Attachments) == 1 && m.Attachments[0] == csv.ID {
				delivered = true
			} else {
				t.Fatalf("the comment's inbox message did not carry the file: %+v", m.Attachments)
			}
		}
	}
	if !delivered {
		t.Fatal("the operator's comment never reached bot sre's inbox")
	}
}

// Caps and placement hold, all or nothing: a file over the holding bot's
// per-file cap refuses the whole op and stores nothing; files only go on a
// ticket a bot holds.
func TestTicketAttachmentCapsAndPlacement(t *testing.T) {
	f, _ := attachSvc(t)
	ctx := context.Background()
	setLimits(f, config.AttachmentLimits{MaxFile: 100, MaxMessage: 150, MaxFiles: 2})

	// Over the per-file cap: refused, nothing stored.
	big := AttachmentIn{Name: "big.txt", Data: bytes.Repeat([]byte("x"), 101)}
	if _, err := f.svc.TicketCreate(ctx, opActor, TicketNew{Title: "too big", Assignee: "bot:sre", Files: []AttachmentIn{big}}); err == nil ||
		!strings.Contains(err.Error(), "max_file") {
		t.Fatalf("an oversized file was not refused: %v", err)
	}
	if n := len(f.svc.Attachments("sre")); n != 0 {
		t.Fatalf("a refused create stored %d attachments", n)
	}

	// A refused type refuses the whole op, nothing stored.
	if _, err := f.svc.TicketCreate(ctx, opActor, TicketNew{Title: "zip", Assignee: "bot:sre",
		Files: []AttachmentIn{{Name: "a.zip", Data: []byte("PK\x03\x04rest")}}}); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("a refused type was not refused: %v", err)
	}
	if n := len(f.svc.Attachments("sre")); n != 0 {
		t.Fatalf("a refused type stored %d attachments", n)
	}

	// Files need a bot to hold them: a team (or triage) assignee is refused
	// before any ticket is created.
	if _, err := f.svc.TicketCreate(ctx, opActor, TicketNew{Title: "team", Assignee: "", // triage queue
		Files: []AttachmentIn{{Name: "ok.txt", Data: []byte("hi")}}}); err == nil || !strings.Contains(err.Error(), "files go on a ticket a bot holds") {
		t.Fatalf("files on an unassigned ticket were not refused: %v", err)
	}

	// Comment caps hold too: a comment with an oversized file is refused and
	// stores nothing.
	v, err := f.svc.TicketCreate(ctx, opActor, TicketNew{Title: "host", Assignee: "bot:sre", Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.TicketComment(ctx, opActor, v.Ticket, "with a bomb", []AttachmentIn{big}); err == nil || !strings.Contains(err.Error(), "max_file") {
		t.Fatalf("an oversized comment file was not refused: %v", err)
	}
	if n := len(f.svc.Attachments("sre")); n != 0 {
		t.Fatalf("a refused comment stored %d attachments", n)
	}
}
