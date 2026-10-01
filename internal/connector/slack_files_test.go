package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeSlackThread serves conversations.replies (two pages), users.info,
// chat.getPermalink and the file downloads, all on one host.
type fakeSlackThread struct {
	srv           *httptest.Server
	userInfoCalls atomic.Int64
	fileHits      map[string]*atomic.Int64
	files         []map[string]any
	extraFiles    map[string]string // path → body
	htmlPath      string
}

func newFakeSlackThread(t *testing.T) *fakeSlackThread {
	t.Helper()
	f := &fakeSlackThread{fileHits: map[string]*atomic.Int64{}, extraFiles: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xoxb-x" {
			t.Errorf("%s: no bot token", r.URL.Path)
		}
		q := r.URL.Query()
		switch r.URL.Path {
		case "/conversations.replies":
			if q.Get("channel") != "C1" || q.Get("ts") != "100.0" {
				t.Errorf("replies params: %v", q)
			}
			if q.Get("cursor") == "" {
				json.NewEncoder(w).Encode(map[string]any{"ok": true, "has_more": true,
					"messages": []any{
						map[string]any{"user": "U1", "text": "the page 500s", "ts": "100.0", "thread_ts": "100.0", "files": f.files},
						map[string]any{"user": "U2", "text": "same here", "ts": "100.1", "thread_ts": "100.0"},
					},
					"response_metadata": map[string]any{"next_cursor": "c2"}})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true,
				"messages": []any{
					map[string]any{"user": "U1", "text": "any update?", "ts": "100.2", "thread_ts": "100.0"},
					map[string]any{"bot_id": "B1", "username": "ci-bot", "text": "build red", "ts": "100.3", "thread_ts": "100.0"},
				}})
		case "/users.info":
			f.userInfoCalls.Add(1)
			names := map[string]string{"U1": "alice", "U2": "bob"}
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "user": map[string]any{"profile": map[string]any{"display_name": names[q.Get("user")]}}})
		case "/chat.getPermalink":
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "permalink": "https://x.slack.com/archives/C1/p100"})
		default:
			if c, ok := f.fileHits[r.URL.Path]; ok {
				c.Add(1)
			}
			if r.URL.Path == f.htmlPath {
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprint(w, "<html>login</html>")
				return
			}
			if body, ok := f.extraFiles[r.URL.Path]; ok {
				w.Header().Set("Content-Type", "application/octet-stream")
				fmt.Fprint(w, body)
				return
			}
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSlackThread) addFile(name, mime, body string) {
	path := fmt.Sprintf("/files/%d", len(f.files))
	f.extraFiles[path] = body
	f.fileHits[path] = &atomic.Int64{}
	f.files = append(f.files, map[string]any{"id": fmt.Sprintf("F%d", len(f.files)), "name": name, "mimetype": mime,
		"size": len(body), "url_private_download": f.srv.URL + path})
}

func TestSlackThreadVerb(t *testing.T) {
	f := newFakeSlackThread(t)
	impl := newSlackTestImpl(t, f.srv.URL)
	f.addFile("shot.png", "image/png", "PNG")
	out, err := impl.Invoke(context.Background(), "thread", map[string]any{"channel": "C1", "ts": "100.0"})
	if err != nil {
		t.Fatal(err)
	}
	msgs := out["messages"].([]any)
	if out["count"] != 4 || len(msgs) != 4 || out["truncated"] != false {
		t.Fatalf("pagination: count=%v truncated=%v", out["count"], out["truncated"])
	}
	var order []string
	for _, m := range msgs {
		mm := m.(map[string]any)
		order = append(order, mm["ts"].(string)+"="+mm["user_name"].(string))
	}
	if strings.Join(order, ",") != "100.0=alice,100.1=bob,100.2=alice,100.3=ci-bot" {
		t.Fatalf("order/names: %v", order)
	}
	if n := f.userInfoCalls.Load(); n != 2 {
		t.Fatalf("users.info must be cached per user: %d calls", n)
	}
	if files := msgs[0].(map[string]any)["files"].([]any); len(files) != 1 || files[0].(map[string]any)["name"] != "shot.png" {
		t.Fatalf("file metadata: %+v", files)
	}
	if out["permalink"] != "https://x.slack.com/archives/C1/p100" || out["thread_ts"] != "100.0" {
		t.Fatalf("permalink/thread_ts: %v %v", out["permalink"], out["thread_ts"])
	}
	if text := out["text"].(string); !strings.Contains(text, "alice (100.0):\nthe page 500s") || !strings.Contains(text, "[files: shot.png]") {
		t.Fatalf("text: %s", text)
	}
	// limit truncates.
	out, _ = impl.Invoke(context.Background(), "thread", map[string]any{"channel": "C1", "ts": "100.0", "limit": 3})
	if out["count"] != 3 || out["truncated"] != true {
		t.Fatalf("limit: %v %v", out["count"], out["truncated"])
	}
}

func withStagingRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "slack-files")
	old := stagingRoot
	stagingRoot = func() string { return root }
	t.Cleanup(func() { stagingRoot = old })
	return root
}

func TestSlackDownloadVerb(t *testing.T) {
	root := withStagingRoot(t)
	f := newFakeSlackThread(t)
	impl := newSlackTestImpl(t, f.srv.URL)
	f.addFile("../../../etc/passwd", "text/plain", "root")
	f.addFile(`..\..\evil {{.gh_token}}.png`, "image/png", "PNG1")
	f.addFile(".hidden", "image/jpeg", "JPG")
	f.addFile("big.bin", "application/octet-stream", strings.Repeat("x", 100))
	// A file hosted off Slack must never receive the bot token.
	f.files = append(f.files, map[string]any{"id": "FX", "name": "x.png", "mimetype": "image/png", "size": 1,
		"url_private_download": "https://evil.example.com/x.png"})
	f.files = append(f.files, map[string]any{"id": "FE", "name": "gdoc", "mode": "external", "url_private": f.srv.URL + "/nope"})

	out, err := impl.Invoke(context.Background(), "download", map[string]any{"channel": "C1", "ts": "100.0", "max_file_bytes": 50})
	if err != nil {
		t.Fatal(err)
	}
	dir := out["dir"].(string)
	if dir != filepath.Join(root, "C1-100.0") {
		t.Fatalf("dir = %s", dir)
	}
	paths := out["paths"].([]any)
	if len(paths) != 3 || out["count"] != 3 {
		t.Fatalf("paths: %v", paths)
	}
	for _, p := range paths {
		ps := p.(string)
		if filepath.Dir(ps) != dir {
			t.Fatalf("file escaped the staging dir: %s", ps)
		}
		if strings.ContainsAny(filepath.Base(ps), "{} \\") {
			t.Fatalf("unsanitized name: %s", ps)
		}
		info, err := os.Stat(ps)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", ps, err, info)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "..", "etc")); err == nil {
		t.Fatal("traversal wrote outside the root")
	}
	images := out["images"].([]any)
	if len(images) != 2 {
		t.Fatalf("images: %v", images)
	}
	skipped := out["skipped"].([]any)
	reasons := map[string]string{}
	for _, s := range skipped {
		m := s.(map[string]any)
		reasons[m["name"].(string)] = m["reason"].(string)
	}
	if !strings.Contains(reasons["big.bin"], "max_file_bytes") || !strings.Contains(reasons["x.png"], "not on slack.com") ||
		!strings.Contains(reasons["gdoc"], "external") {
		t.Fatalf("skipped: %v", reasons)
	}

	// max_files caps the count; a re-download resets the dir.
	out, err = impl.Invoke(context.Background(), "download", map[string]any{"channel": "C1", "ts": "100.0", "max_files": 1})
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if out["count"] != 1 || len(entries) != 1 {
		t.Fatalf("max_files: count=%v entries=%d", out["count"], len(entries))
	}

	// max_total_bytes: the running total stops further downloads.
	out, _ = impl.Invoke(context.Background(), "download", map[string]any{"channel": "C1", "ts": "100.0", "max_total_bytes": 5})
	if out["count"] != 1 {
		t.Fatalf("max_total_bytes: count=%v skipped=%v", out["count"], out["skipped"])
	}
}

func TestSlackDownloadRefusesHTMLLoginPage(t *testing.T) {
	withStagingRoot(t)
	f := newFakeSlackThread(t)
	impl := newSlackTestImpl(t, f.srv.URL)
	f.addFile("a.png", "image/png", "PNG")
	f.htmlPath = "/files/0"
	out, err := impl.Invoke(context.Background(), "download", map[string]any{"channel": "C1", "ts": "100.0"})
	if err != nil {
		t.Fatal(err)
	}
	if out["count"] != 0 || !strings.Contains(fmt.Sprint(out["skipped"]), "files:read") {
		t.Fatalf("html page must be refused: %+v", out)
	}
}

func TestSanitizeFileName(t *testing.T) {
	for in, want := range map[string]string{
		"../../etc/passwd": "passwd",
		`..\..\win.ini`:    "win.ini",
		"a b{{c}}.png":     "a_b_c_.png",
		"...":              "F1",
		".bashrc":          "bashrc",
		"-rf":              "rf",
		"/":                "F1",
	} {
		if got := sanitizeFileName(in, "F1"); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("a", 300) + ".png"
	if got := sanitizeFileName(long, "F"); len(got) > 100 || !strings.HasSuffix(got, ".png") {
		t.Errorf("long name: %q", got)
	}
}

func TestFileURLAllowed(t *testing.T) {
	a := &slackAPI{base: "https://slack.com/api"}
	for raw, want := range map[string]bool{
		"https://files.slack.com/files-pri/T/x.png": true,
		"http://files.slack.com/x":                  false,
		"https://files.slack.com.evil.com/x":        false,
		"https://evil.com/files.slack.com":          false,
		"https://slack.com/x":                       true,
	} {
		if got := a.fileURLAllowed(raw); got != want {
			t.Errorf("%s: %v", raw, got)
		}
	}
}
