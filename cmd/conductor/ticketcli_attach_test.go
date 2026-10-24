package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

// `ticket new --attach` and `ticket comment --attach` read each file off disk
// into the control request: its base name and its bytes, base64-encoded,
// repeatable. A path that is not a readable regular file fails the whole call
// before anything is sent.
func TestTicketRequestAttach(t *testing.T) {
	dir := t.TempDir()
	one := filepath.Join(dir, "shot.png")
	two := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(one, []byte("PNGDATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(two, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	// new --attach, repeatable.
	pos, fl, _, _ := botFlags([]string{"a", "ticket", "--attach", one, "--attach", two})
	op, a, err := ticketRequest("new", pos, fl)
	if err != nil {
		t.Fatal(err)
	}
	if op != "ticket_new" {
		t.Fatalf("op: %s", op)
	}
	atts, ok := a["attachments"].([]any)
	if !ok || len(atts) != 2 {
		t.Fatalf("attachments not carried: %#v", a["attachments"])
	}
	want := map[string]string{"shot.png": "PNGDATA", "notes.txt": "hello"}
	for _, x := range atts {
		m := x.(map[string]any)
		name := m["name"].(string)
		data, err := base64.StdEncoding.DecodeString(m["data"].(string))
		if err != nil {
			t.Fatalf("%s: data not base64: %v", name, err)
		}
		if string(data) != want[name] {
			t.Fatalf("%s: got %q, want %q", name, data, want[name])
		}
		delete(want, name)
	}
	if len(want) != 0 {
		t.Fatalf("files missing from the request: %v", want)
	}

	// comment --attach carries the file too.
	pos, fl, _, _ = botFlags([]string{"T-5", "here is the log", "--attach", two})
	op, a, err = ticketRequest("comment", pos, fl)
	if err != nil {
		t.Fatal(err)
	}
	if op != "ticket_comment" || a["ticket"] != "T-5" || a["text"] != "here is the log" {
		t.Fatalf("comment: %s %v", op, a)
	}
	if atts, ok := a["attachments"].([]any); !ok || len(atts) != 1 || atts[0].(map[string]any)["name"] != "notes.txt" {
		t.Fatalf("comment attachment not carried: %#v", a["attachments"])
	}

	// A missing path fails the whole call — nothing is sent.
	pos, fl, _, _ = botFlags([]string{"a", "ticket", "--attach", filepath.Join(dir, "nope.txt")})
	if _, _, err := ticketRequest("new", pos, fl); err == nil {
		t.Fatal("a missing --attach path was accepted")
	}

	// A directory is not a regular file: refused.
	pos, fl, _, _ = botFlags([]string{"a", "ticket", "--attach", dir})
	if _, _, err := ticketRequest("new", pos, fl); err == nil {
		t.Fatal("a directory --attach path was accepted")
	}
}
