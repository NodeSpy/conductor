package bot

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// ticketSubs is the live ticket-stream subscriber count.
func ticketSubs(s *Service) int {
	s.ticketHub.mu.Lock()
	defer s.ticketHub.mu.Unlock()
	return len(s.ticketHub.subs)
}

// GET /v1/tickets/stream: the stream opens with a keep-alive, emits an event
// when a ticket changes, sends a heartbeat on its interval, and unsubscribes
// when the client disconnects.
func TestTicketStreamSSE(t *testing.T) {
	old := streamPing
	streamPing = 40 * time.Millisecond
	t.Cleanup(func() { streamPing = old })

	f, _ := attachSvc(t)
	srv := httptest.NewServer(OperatorAPI(f.svc))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/v1/tickets/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type: %q", ct)
	}

	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()

	readUntil := func(pred func(string) bool, what string) string {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("stream closed before %s", what)
				}
				if pred(l) {
					return l
				}
			case <-deadline:
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}

	// The opening keep-alive comment.
	readUntil(func(l string) bool { return strings.HasPrefix(l, ": connected") }, "the connected comment")

	// Wait until the handler has registered its subscriber, then publish.
	for i := 0; ticketSubs(f.svc) == 0 && i < 100; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	f.svc.publishTicket("T-7")
	readUntil(func(l string) bool { return l == "event: ticket" }, "the ticket event")
	data := readUntil(func(l string) bool { return strings.HasPrefix(l, "data:") }, "the event data")
	if !strings.Contains(data, `"ticket":"T-7"`) {
		t.Fatalf("event data does not name the changed ticket: %q", data)
	}

	// The heartbeat: a ": ping" comment arrives on the interval, with no
	// ticket change to trigger it.
	readUntil(func(l string) bool { return strings.HasPrefix(l, ": ping") }, "a heartbeat ping")

	// Disconnect: the handler returns and unsubscribes.
	cancel()
	deadline := time.After(3 * time.Second)
	for ticketSubs(f.svc) != 0 {
		select {
		case <-deadline:
			t.Fatalf("the subscriber was not cleaned up on disconnect: %d left", ticketSubs(f.svc))
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// POST /v1/conversations/{id}/messages, the multipart (file) branch: a message
// with a file part is accepted and its file stored against the bot; a file
// over the bot's cap is refused and nothing is stored.
func TestConversationSendMultipartFile(t *testing.T) {
	f, _ := attachSvc(t)
	h := OperatorAPI(f.svc)

	body, ct := multipartConversation(t, "see this", "op", map[string][]byte{"shot.png": pngBytes(t, 8, 8)})
	w := apiDo(t, h, "POST", "/v1/conversations/bot:sre/messages", body, ct)
	if w.Code != http.StatusCreated {
		t.Fatalf("multipart send: %d %s", w.Code, truncBody(w.Body.String()))
	}
	var m map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	atts, _ := m["attachments"].([]any)
	if len(atts) != 1 {
		t.Fatalf("the message did not record its file: %s", truncBody(w.Body.String()))
	}
	stored := f.svc.Attachments("sre")
	if len(stored) != 1 || stored[0].Name != "shot.png" || stored[0].MediaType != "image/png" {
		t.Fatalf("file not stored against the bot: %+v", stored)
	}

	// A file over the bot's per-file cap is refused (unprocessable), nothing
	// more stored.
	setLimits(f, config.AttachmentLimits{MaxFile: 100, MaxMessage: 200, MaxFiles: 2})
	body, ct = multipartConversation(t, "big one", "op", map[string][]byte{"big.txt": bytes.Repeat([]byte("x"), 101)})
	if w := apiDo(t, h, "POST", "/v1/conversations/bot:sre/messages", body, ct); w.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(w.Body.String(), "max_file") {
		t.Fatalf("an oversized file was not refused: %d %s", w.Code, truncBody(w.Body.String()))
	}
	if n := len(f.svc.Attachments("sre")); n != 1 {
		t.Fatalf("a refused multipart send stored a file: %d total", n)
	}
}

// POST /v1/tickets/{id}/comments, the multipart branch: a comment with a file
// is accepted and surfaced on the ticket; a file over the cap is refused.
func TestTicketCommentMultipart(t *testing.T) {
	f, _ := attachSvc(t)
	h := OperatorAPI(f.svc)
	v, err := f.svc.TicketCreate(context.Background(), opActor, TicketNew{Title: "needs a file", Assignee: "bot:sre", Ready: true})
	if err != nil {
		t.Fatal(err)
	}

	body, ct := multipartTicketComment(t, "here is the log", "op", map[string][]byte{"log.txt": []byte("an error line")})
	w := apiDo(t, h, "POST", "/v1/tickets/"+v.Ticket+"/comments", body, ct)
	if w.Code != http.StatusCreated {
		t.Fatalf("comment with a file: %d %s", w.Code, truncBody(w.Body.String()))
	}
	var tv map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &tv)
	// Surfaced on the ticket: the new comment names its attachment, and the
	// file is stored against the holding bot.
	stored := f.svc.Attachments("sre")
	if len(stored) != 1 || stored[0].Name != "log.txt" || stored[0].MediaType != "text/plain" || stored[0].CaseID != v.ID {
		t.Fatalf("the comment's file was not stored: %+v", stored)
	}
	if !strings.Contains(w.Body.String(), stored[0].ID) {
		t.Fatalf("the ticket view does not surface the attachment id %s: %s", stored[0].ID, truncBody(w.Body.String()))
	}

	// Over the per-file cap: refused, nothing more stored.
	setLimits(f, config.AttachmentLimits{MaxFile: 100, MaxMessage: 200, MaxFiles: 2})
	body, ct = multipartTicketComment(t, "bomb", "op", map[string][]byte{"big.txt": bytes.Repeat([]byte("x"), 101)})
	if w := apiDo(t, h, "POST", "/v1/tickets/"+v.Ticket+"/comments", body, ct); w.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(w.Body.String(), "max_file") {
		t.Fatalf("an oversized comment file was not refused: %d %s", w.Code, truncBody(w.Body.String()))
	}
	if n := len(f.svc.Attachments("sre")); n != 1 {
		t.Fatalf("a refused comment stored a file: %d total", n)
	}
}

// multipartConversation builds a conversation-send body with text, operator
// and file parts.
func multipartConversation(t *testing.T, text, operator string, files map[string][]byte) (*bytes.Buffer, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	if text != "" {
		_ = w.WriteField("text", text)
	}
	if operator != "" {
		_ = w.WriteField("operator", operator)
	}
	for name, data := range files {
		fw, err := w.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(data)
	}
	_ = w.Close()
	return &b, w.FormDataContentType()
}

// multipartTicketComment builds a ticket-comment body with text, operator and
// file parts.
func multipartTicketComment(t *testing.T, text, operator string, files map[string][]byte) (*bytes.Buffer, string) {
	t.Helper()
	return multipartConversation(t, text, operator, files)
}
