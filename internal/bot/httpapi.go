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
