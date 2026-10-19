package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// THE OPERATOR API (docs/wiki/Bots.md#the-operator-api) is HTTP for the
// dashboard: what the control socket's JSON lines can't carry well — a
// multipart upload, a file download, a thumbnail. The daemon serves it on a
// unix socket beside the control socket (operator-api.sock), with the same
// authentication: same-user only (0600, in the 0700 state dir). Nothing on
// it is reachable from a bot session.
//
//	POST /v1/bots/{bot}/messages                      multipart: text, case, file…  → the message's attachments
//	GET  /v1/bots/{bot}/attachments[?all=1]           → { attachments: [...] }
//	GET  /v1/bots/{bot}/attachments/{id}              → one attachment's record
//	GET  /v1/bots/{bot}/attachments/{id}/content      → its bytes (always a download)
//	GET  /v1/bots/{bot}/attachments/{id}/thumbnail[?size=N]   → a PNG thumbnail of an image
//
//	GET  /v1/teams                                    → every team: bots, routing, identity (avatar, initials)
//	GET  /v1/teams/{team}                             → one team, in full
//	GET  /v1/conversations[?operator=]                → bots' and teams' conversations: last message, unread
//	GET  /v1/conversations/{id}/messages[?after=N&operator=&mark=1]   → messages after N (mark=1: read)
//	POST /v1/conversations/{id}/messages              → send: JSON {text, operator, to, everyone}, or multipart with file parts
//	POST /v1/conversations/{id}/read                  → operator, upto: the read mark
//	GET  /v1/conversations/stream                     → live updates (server-sent events)
//	POST   /v1/conversations/{id}/pin                 → pin for an operator: JSON {operator, position} (position: 1-based; none: last)
//	DELETE /v1/conversations/{id}/pin[?operator=]     → unpin
//	PUT    /v1/conversations/pins                     → reorder: JSON {operator, order: [ids…]}, every pinned id once
//
//	GET   /v1/tickets[?assignee=&holder=&status=&priority=&label=&q=]   → tickets (default: not done or declined)
//	POST  /v1/tickets                                → create: JSON {title, description, assignee, priority, due, labels, ready, operator}
//	GET   /v1/tickets/{id}                           → one ticket: where it is, comments, history, links
//	PATCH /v1/tickets/{id}                           → JSON {title, description, priority, due, labels, assignee, status, note, operator}
//	POST  /v1/tickets/{id}/comments                  → JSON {text, operator}, or multipart with file parts
//	GET   /v1/tickets/stream                         → live updates (server-sent events: the ticket that changed)
//
// {id} is bot:<name> or team:<name>. operator names who is reading or
// writing (unread state is per operator); it may be left out when one
// operator is configured.

// multipartSlack is room for a multipart body's boundaries and headers over
// the files themselves.
const multipartSlack = 1 << 20

// OperatorAPI is the operator API's handler.
func OperatorAPI(s *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/bots/{bot}/messages", s.apiPostMessage)
	mux.HandleFunc("GET /v1/bots/{bot}/attachments", s.apiListAttachments)
	mux.HandleFunc("GET /v1/bots/{bot}/attachments/{id}", s.apiAttachment)
	mux.HandleFunc("GET /v1/bots/{bot}/attachments/{id}/content", s.apiAttachmentContent)
	mux.HandleFunc("GET /v1/bots/{bot}/attachments/{id}/thumbnail", s.apiAttachmentThumbnail)
	mux.HandleFunc("GET /v1/teams", s.apiTeams)
	mux.HandleFunc("GET /v1/teams/{team}", s.apiTeam)
	mux.HandleFunc("GET /v1/conversations", s.apiConversations)
	mux.HandleFunc("GET /v1/conversations/stream", s.apiConversationStream)
	mux.HandleFunc("GET /v1/conversations/{id}/messages", s.apiConversationMessages)
	mux.HandleFunc("POST /v1/conversations/{id}/messages", s.apiConversationSend)
	mux.HandleFunc("POST /v1/conversations/{id}/read", s.apiConversationRead)
	mux.HandleFunc("GET /v1/tickets", s.apiTickets)
	mux.HandleFunc("POST /v1/tickets", s.apiTicketCreate)
	mux.HandleFunc("GET /v1/tickets/stream", s.apiTicketStream)
	mux.HandleFunc("GET /v1/tickets/{id}", s.apiTicket)
	mux.HandleFunc("PATCH /v1/tickets/{id}", s.apiTicketPatch)
	mux.HandleFunc("POST /v1/tickets/{id}/comments", s.apiTicketComment)
	mux.HandleFunc("POST /v1/conversations/{id}/pin", s.apiConversationPin)
	mux.HandleFunc("DELETE /v1/conversations/{id}/pin", s.apiConversationUnpin)
	mux.HandleFunc("PUT /v1/conversations/pins", s.apiConversationPins)
	return mux
}

func apiJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, code int, err error) {
	apiJSON(w, code, map[string]string{"error": err.Error()})
}

// apiPostMessage takes a multipart message: `text`, an optional `case`, and
// any number of `file` parts. The body is capped at the bot's per-message
// limit as it streams, and each file at the per-file limit, before the
// service applies every cap again.
func (s *Service) apiPostMessage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("bot")
	m, err := s.bot(name)
	if err != nil {
		apiError(w, http.StatusNotFound, err)
		return
	}
	lim := m.Attachments
	r.Body = http.MaxBytesReader(w, r.Body, lim.MaxMessageOr()+multipartSlack)
	mr, err := r.MultipartReader()
	if err != nil {
		apiError(w, http.StatusBadRequest, fmt.Errorf("want multipart/form-data: %w", err))
		return
	}
	var text, caseID string
	var files []AttachmentIn
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			apiError(w, bodyErrCode(err), fmt.Errorf("reading the upload: %w", err))
			return
		}
		switch p.FormName() {
		case "text", "case":
			b, err := io.ReadAll(io.LimitReader(p, 64<<10+1))
			if err != nil || len(b) > 64<<10 {
				apiError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("%s: at most 64 KB", p.FormName()))
				return
			}
			if p.FormName() == "text" {
				text = string(b)
			} else {
				caseID = strings.TrimSpace(string(b))
			}
		case "file":
			if len(files) == lim.MaxFilesOr() {
				apiError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("more than %d files (attachments.max_files)", lim.MaxFilesOr()))
				return
			}
			b, err := io.ReadAll(io.LimitReader(p, lim.MaxFileOr()+1))
			if err != nil {
				apiError(w, bodyErrCode(err), fmt.Errorf("reading %q: %w", p.FileName(), err))
				return
			}
			if int64(len(b)) > lim.MaxFileOr() {
				apiError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("%q: over the cap of %d bytes per file (attachments.max_file)", safeAttachName(p.FileName()), lim.MaxFileOr()))
				return
			}
			files = append(files, AttachmentIn{Name: p.FileName(), Data: b})
		default:
			apiError(w, http.StatusBadRequest, fmt.Errorf("unknown form field %q (text, case, file)", p.FormName()))
			return
		}
	}
	atts, err := s.SayWith(name, text, files, caseID)
	if err != nil {
		apiError(w, http.StatusUnprocessableEntity, err)
		return
	}
	out := []any{}
	for _, a := range atts {
		out = append(out, attachmentView(*a))
	}
	apiJSON(w, http.StatusCreated, map[string]any{"queued": true, "attachments": out})
}

func bodyErrCode(err error) int {
	var mb *http.MaxBytesError
	if errors.As(err, &mb) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

func (s *Service) apiListAttachments(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("bot")
	if _, err := s.bot(name); err != nil {
		apiError(w, http.StatusNotFound, err)
		return
	}
	all := r.URL.Query().Get("all") == "1"
	out := []any{}
	for _, a := range s.Attachments(name) {
		if a.Removed.IsZero() || all {
			out = append(out, attachmentView(a))
		}
	}
	apiJSON(w, http.StatusOK, map[string]any{"attachments": out})
}

func (s *Service) apiAttachment(w http.ResponseWriter, r *http.Request) {
	a, err := s.Attachment(r.PathValue("bot"), r.PathValue("id"))
	if err != nil {
		apiError(w, http.StatusNotFound, err)
		return
	}
	apiJSON(w, http.StatusOK, attachmentView(a))
}

// apiAttachmentContent serves an attachment's bytes as a download, never
// rendered: a sender's HTML or SVG must not run in the dashboard's origin.
func (s *Service) apiAttachmentContent(w http.ResponseWriter, r *http.Request) {
	a, data, err := s.AttachmentBytes(r.PathValue("bot"), r.PathValue("id"))
	if err != nil {
		apiError(w, http.StatusNotFound, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", a.MediaType)
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": a.Name}))
	if h.Get("Content-Disposition") == "" {
		h.Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(a.Name))
	}
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func (s *Service) apiAttachmentThumbnail(w http.ResponseWriter, r *http.Request) {
	a, data, err := s.AttachmentBytes(r.PathValue("bot"), r.PathValue("id"))
	if err != nil {
		apiError(w, http.StatusNotFound, err)
		return
	}
	if !strings.HasPrefix(a.MediaType, "image/") {
		apiError(w, http.StatusUnsupportedMediaType, fmt.Errorf("%s is not an image", a.ID))
		return
	}
	size := 256
	if v, err := strconv.Atoi(r.URL.Query().Get("size")); err == nil {
		size = min(max(v, 32), 512)
	}
	th, err := Thumbnail(data, size)
	if err != nil {
		apiError(w, http.StatusUnsupportedMediaType, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(th)
}

func (s *Service) apiConversations(w http.ResponseWriter, r *http.Request) {
	cs, err := s.Conversations(r.URL.Query().Get("operator"))
	if err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	apiJSON(w, http.StatusOK, map[string]any{"conversations": cs})
}

func (s *Service) apiConversationMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	after, _ := strconv.Atoi(q.Get("after"))
	ms, err := s.ChatMessages(r.PathValue("id"), q.Get("operator"), after, q.Get("mark") == "1")
	if err != nil {
		apiError(w, http.StatusNotFound, err)
		return
	}
	apiJSON(w, http.StatusOK, map[string]any{"conversation": r.PathValue("id"), "messages": ms})
}

// apiConversationSend takes a message as JSON, or as multipart (text,
// operator, to…, everyone, file…) when it carries files — capped as it
// streams, like the bot messages endpoint.
func (s *Service) apiConversationSend(w http.ResponseWriter, r *http.Request) {
	req := ChatSend{Conversation: r.PathValue("id")}
	kind, name, err := s.ParseConversation(req.Conversation)
	if err != nil {
		apiError(w, http.StatusNotFound, err)
		return
	}
	limit := int64(MaxChatMessage) + multipartSlack
	if kind == "bot" {
		limit += s.Cfg().Bots[name].Attachments.MaxMessageOr()
	} else {
		limit += 25 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct == "multipart/form-data" {
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			apiError(w, bodyErrCode(err), fmt.Errorf("reading the upload: %w", err))
			return
		}
		defer r.MultipartForm.RemoveAll()
		req.Text, req.Operator = r.FormValue("text"), r.FormValue("operator")
		req.To, req.Everyone = r.MultipartForm.Value["to"], r.FormValue("everyone") == "true" || r.FormValue("everyone") == "1"
		for _, fh := range r.MultipartForm.File["file"] {
			f, err := fh.Open()
			if err != nil {
				apiError(w, http.StatusBadRequest, err)
				return
			}
			b, err := io.ReadAll(io.LimitReader(f, config.MaxAttachFile+1))
			f.Close()
			if err != nil {
				apiError(w, bodyErrCode(err), err)
				return
			}
			req.Files = append(req.Files, AttachmentIn{Name: fh.Filename, Data: b})
		}
	} else {
		var body struct {
			Text     string   `json:"text"`
			Operator string   `json:"operator"`
			To       []string `json:"to"`
			Everyone bool     `json:"everyone"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			apiError(w, bodyErrCode(err), fmt.Errorf("want JSON {text, operator, to, everyone} or multipart: %w", err))
			return
		}
		req.Text, req.Operator, req.To, req.Everyone = body.Text, body.Operator, body.To, body.Everyone
	}
	m, err := s.Chat(r.Context(), req)
	if err != nil {
		apiError(w, http.StatusUnprocessableEntity, err)
		return
	}
	apiJSON(w, http.StatusCreated, m)
}

func (s *Service) apiConversationRead(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	_ = r.ParseForm()
	upto, _ := strconv.Atoi(r.FormValue("upto"))
	if err := s.MarkRead(r.PathValue("id"), r.FormValue("operator"), upto); err != nil {
		apiError(w, http.StatusNotFound, err)
		return
	}
	apiJSON(w, http.StatusOK, map[string]any{"read": true})
}

// apiConversationStream sends every recorded message as a server-sent
// event until the client goes away; a comment every 25s keeps it open.
func (s *Service) apiConversationStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		apiError(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	ch, cancel := s.SubscribeChat()
	defer cancel()
	_, _ = fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			b, _ := json.Marshal(ev)
			_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
			fl.Flush()
		}
	}
}

// ticketAPIError maps a ticket op's error to a status: an unknown ticket is
// 404, anything else the request's fault.
func ticketAPIError(w http.ResponseWriter, err error) {
	code := http.StatusUnprocessableEntity
	if strings.HasPrefix(err.Error(), "no ticket ") {
		code = http.StatusNotFound
	}
	apiError(w, code, err)
}

func (s *Service) apiTickets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	args := map[string]any{"assignee": q.Get("assignee"), "holder": q.Get("holder"), "priority": q.Get("priority"),
		"label": q.Get("label"), "q": q.Get("q")}
	if st := q.Get("status"); st != "" {
		args["status"] = strings.Split(st, ",")
	}
	a, err := s.operatorActor(q.Get("operator"))
	if err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	out, err := s.ticketOp(r.Context(), a, "ticket_list", args)
	if err != nil {
		ticketAPIError(w, err)
		return
	}
	if out["tickets"] == nil {
		out["tickets"] = []any{}
	}
	apiJSON(w, http.StatusOK, out)
}

// jsonArgs decodes a small JSON body into op arguments.
func jsonArgs(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxTicketDescription+64<<10)
	args := map[string]any{}
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		apiError(w, bodyErrCode(err), fmt.Errorf("want a JSON object: %w", err))
		return nil, false
	}
	return args, true
}

func (s *Service) apiTicketCreate(w http.ResponseWriter, r *http.Request) {
	args, ok := jsonArgs(w, r)
	if !ok {
		return
	}
	a, err := s.operatorActor(str(args, "operator"))
	if err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	delete(args, "attachments") // files go on a comment
	out, err := s.ticketOp(r.Context(), a, "ticket_new", args)
	if err != nil {
		ticketAPIError(w, err)
		return
	}
	apiJSON(w, http.StatusCreated, out)
}

func (s *Service) apiTicket(w http.ResponseWriter, r *http.Request) {
	v, err := s.TicketShow(r.PathValue("id"))
	if err != nil {
		ticketAPIError(w, err)
		return
	}
	apiJSON(w, http.StatusOK, v)
}

// apiTicketPatch applies, in order, an edit (title, description, priority,
// due, labels), an assignment, and a status change — each through the same
// op the CLI uses.
func (s *Service) apiTicketPatch(w http.ResponseWriter, r *http.Request) {
	args, ok := jsonArgs(w, r)
	if !ok {
		return
	}
	tid := r.PathValue("id")
	a, err := s.operatorActor(str(args, "operator"))
	if err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	edit := map[string]any{"ticket": tid}
	for _, k := range []string{"title", "description", "priority", "due", "labels"} {
		if v, ok := args[k]; ok {
			edit[k] = v
		}
	}
	var out map[string]any
	if len(edit) > 1 {
		if out, err = s.ticketOp(r.Context(), a, "ticket_edit", edit); err != nil {
			ticketAPIError(w, err)
			return
		}
	}
	if _, ok := args["assignee"]; ok {
		if out, err = s.ticketOp(r.Context(), a, "ticket_assign", map[string]any{"ticket": tid, "to": str(args, "assignee")}); err != nil {
			ticketAPIError(w, err)
			return
		}
	}
	if st := str(args, "status"); st != "" {
		if out, err = s.ticketOp(r.Context(), a, "ticket_status", map[string]any{"ticket": tid, "to": st, "note": str(args, "note")}); err != nil {
			ticketAPIError(w, err)
			return
		}
	}
	if out == nil {
		apiError(w, http.StatusBadRequest, errors.New("nothing to change"))
		return
	}
	apiJSON(w, http.StatusOK, out)
}

func (s *Service) apiTicketComment(w http.ResponseWriter, r *http.Request) {
	tid := r.PathValue("id")
	args := map[string]any{"ticket": tid}
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var files []AttachmentIn
	if ct == "multipart/form-data" {
		r.Body = http.MaxBytesReader(w, r.Body, MaxTicketComment+25<<20+multipartSlack)
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			apiError(w, bodyErrCode(err), err)
			return
		}
		defer r.MultipartForm.RemoveAll()
		args["text"], args["operator"] = r.FormValue("text"), r.FormValue("operator")
		for _, fh := range r.MultipartForm.File["file"] {
			f, err := fh.Open()
			if err != nil {
				apiError(w, http.StatusBadRequest, err)
				return
			}
			b, err := io.ReadAll(io.LimitReader(f, config.MaxAttachFile+1))
			f.Close()
			if err != nil {
				apiError(w, bodyErrCode(err), err)
				return
			}
			files = append(files, AttachmentIn{Name: fh.Filename, Data: b})
		}
	} else {
		body, ok := jsonArgs(w, r)
		if !ok {
			return
		}
		args["text"], args["operator"] = str(body, "text"), str(body, "operator")
	}
	a, err := s.operatorActor(str(args, "operator"))
	if err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	v, err := s.TicketComment(r.Context(), a, tid, str(args, "text"), files)
	if err != nil {
		ticketAPIError(w, err)
		return
	}
	apiJSON(w, http.StatusCreated, v)
}

func (s *Service) apiTicketStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		apiError(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch, cancel := s.SubscribeTickets()
	defer cancel()
	_, _ = fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			b, _ := json.Marshal(ev)
			_, _ = fmt.Fprintf(w, "event: ticket\ndata: %s\n\n", b)
			fl.Flush()
		}
	}
}

func (s *Service) apiConversationPin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Operator string `json:"operator"`
		Position int    `json:"position"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	pins, err := s.ChatPin(body.Operator, r.PathValue("id"), body.Position)
	if err != nil {
		apiError(w, http.StatusUnprocessableEntity, err)
		return
	}
	apiJSON(w, http.StatusOK, map[string]any{"pins": pins})
}

func (s *Service) apiConversationUnpin(w http.ResponseWriter, r *http.Request) {
	pins, err := s.ChatUnpin(r.URL.Query().Get("operator"), r.PathValue("id"))
	if err != nil {
		apiError(w, http.StatusUnprocessableEntity, err)
		return
	}
	apiJSON(w, http.StatusOK, map[string]any{"pins": pins})
}

func (s *Service) apiConversationPins(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Operator string   `json:"operator"`
		Order    []string `json:"order"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	pins, err := s.ChatReorder(body.Operator, body.Order)
	if err != nil {
		apiError(w, http.StatusUnprocessableEntity, err)
		return
	}
	apiJSON(w, http.StatusOK, map[string]any{"pins": pins})
}

func (s *Service) apiTeams(w http.ResponseWriter, r *http.Request) {
	out := []any{}
	for _, t := range s.Cfg().TeamNames() {
		out = append(out, s.teamStatus(r.Context(), t, false))
	}
	apiJSON(w, http.StatusOK, map[string]any{"teams": out})
}

func (s *Service) apiTeam(w http.ResponseWriter, r *http.Request) {
	t := r.PathValue("team")
	if _, ok := s.Cfg().Teams[t]; !ok {
		apiError(w, http.StatusNotFound, fmt.Errorf("unknown team %q", t))
		return
	}
	apiJSON(w, http.StatusOK, s.teamStatus(r.Context(), t, true))
}
