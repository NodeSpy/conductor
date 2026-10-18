package bot

import (
	"bytes"
	"encoding/json"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

func multipartBody(t *testing.T, text string, files map[string][]byte) (*bytes.Buffer, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	if text != "" {
		_ = w.WriteField("text", text)
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

func apiDo(t *testing.T, h http.Handler, method, path string, body io.Reader, ctype string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, body)
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// THE OPERATOR API: a multipart message lands like `bot say --attach`; the
// list, the record, the bytes (always a download, never rendered) and an
// image's thumbnail come back; caps hold as the body streams.
func TestOperatorAPIAttachments(t *testing.T) {
	f, _ := attachSvc(t)
	h := OperatorAPI(f.svc)
	img := pngBytes(t, 600, 300)
	body, ct := multipartBody(t, "see attached", map[string][]byte{"shot.png": img, "page.txt": []byte("plain <b>hi</b>")})
	w := apiDo(t, h, "POST", "/v1/bots/sre/messages", body, ct)
	if w.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	var up struct {
		Attachments []map[string]any `json:"attachments"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &up)
	if len(up.Attachments) != 2 || len(f.st.Bot("sre").Inbox) != 1 {
		t.Fatalf("upload result: %s", w.Body)
	}
	var imgID, txtID string
	for _, a := range up.Attachments {
		if a["media_type"] == "image/png" {
			imgID = a["id"].(string)
		} else {
			txtID = a["id"].(string)
		}
	}
	w = apiDo(t, h, "GET", "/v1/bots/sre/attachments", nil, "")
	if w.Code != 200 || strings.Count(w.Body.String(), `"id"`) != 2 {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	w = apiDo(t, h, "GET", "/v1/bots/sre/attachments/"+txtID+"/content", nil, "")
	if w.Code != 200 || w.Body.String() != "plain <b>hi</b>" || w.Header().Get("Content-Type") != "text/plain" ||
		!strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || w.Header().Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("download: %d %v %q", w.Code, w.Header(), w.Body)
	}
	w = apiDo(t, h, "GET", "/v1/bots/sre/attachments/"+imgID+"/thumbnail?size=100", nil, "")
	th, err := png.Decode(w.Body)
	if w.Code != 200 || err != nil || th.Bounds().Dx() != 100 || th.Bounds().Dy() != 50 {
		t.Fatalf("thumbnail: %d %v", w.Code, err)
	}
	if w = apiDo(t, h, "GET", "/v1/bots/sre/attachments/"+txtID+"/thumbnail", nil, ""); w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("a text thumbnail: %d", w.Code)
	}
	if w = apiDo(t, h, "GET", "/v1/bots/sre/attachments/a999", nil, ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown attachment: %d", w.Code)
	}
	if w = apiDo(t, h, "GET", "/v1/bots/nobody/attachments", nil, ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown bot: %d", w.Code)
	}
	// Another bot's attachment is not reachable through this one.
	if w = apiDo(t, h, "GET", "/v1/bots/other/attachments/"+txtID+"/content", nil, ""); w.Code != http.StatusNotFound {
		t.Fatalf("cross-bot read: %d", w.Code)
	}
	// Removed: gone from the list and unreadable, listed with ?all=1.
	f.svc.removeAttachments("sre", map[string]string{txtID: "test"})
	if w = apiDo(t, h, "GET", "/v1/bots/sre/attachments/"+txtID+"/content", nil, ""); w.Code != http.StatusNotFound {
		t.Fatalf("a removed attachment: %d", w.Code)
	}
	if w = apiDo(t, h, "GET", "/v1/bots/sre/attachments?all=1", nil, ""); !strings.Contains(w.Body.String(), `"removed_why":"test"`) {
		t.Fatalf("?all=1: %s", w.Body)
	}
}

// Size caps hold while the upload streams: a file over max_file and a body
// over max_message are refused with 413, and nothing is queued.
func TestOperatorAPISizeChecked(t *testing.T) {
	f, _ := attachSvc(t)
	setLimits(f, config.AttachmentLimits{MaxFile: 1000, MaxMessage: 1500, MaxFiles: 2})
	h := OperatorAPI(f.svc)
	body, ct := multipartBody(t, "x", map[string][]byte{"big.txt": bytes.Repeat([]byte("a"), 1001)})
	if w := apiDo(t, h, "POST", "/v1/bots/sre/messages", body, ct); w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), "max_file") {
		t.Fatalf("over max_file: %d %s", w.Code, w.Body)
	}
	body, ct = multipartBody(t, "x", map[string][]byte{"a.txt": []byte("a"), "b.txt": []byte("b"), "c.txt": []byte("c")})
	if w := apiDo(t, h, "POST", "/v1/bots/sre/messages", body, ct); w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), "max_files") {
		t.Fatalf("over max_files: %d %s", w.Code, w.Body)
	}
	huge := strings.NewReader(strings.Repeat("z", 1500+multipartSlack+10))
	if w := apiDo(t, h, "POST", "/v1/bots/sre/messages", huge, "multipart/form-data; boundary=x"); w.Code != http.StatusRequestEntityTooLarge && w.Code != http.StatusBadRequest {
		t.Fatalf("an oversized body: %d", w.Code)
	}
	body, ct = multipartBody(t, "x", map[string][]byte{"a.zip": []byte("PK\x03\x04zz")})
	if w := apiDo(t, h, "POST", "/v1/bots/sre/messages", body, ct); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "not allowed") {
		t.Fatalf("a refused type: %d %s", w.Code, w.Body)
	}
	if w := apiDo(t, h, "POST", "/v1/bots/sre/messages", strings.NewReader("{}"), "application/json"); w.Code != http.StatusBadRequest {
		t.Fatalf("not multipart: %d", w.Code)
	}
	if n := len(f.st.Bot("sre").Inbox); n != 0 {
		t.Fatalf("a refused upload queued %d messages", n)
	}
}
