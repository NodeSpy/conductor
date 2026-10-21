package main

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The operator API socket is same-user only (0600), like the control
// socket, and serves HTTP.
func TestOperatorAPISocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "opapi")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "operator-api.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	go serveOperatorAPI(ctx, path, h, func(string, ...any) {})
	var st os.FileInfo
	for i := 0; i < 100; i++ {
		if st, err = os.Stat(path); err == nil && st.Mode().Perm() == 0o600 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || st.Mode().Perm() != 0o600 || st.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket: %v %v", st, err)
	}
	c := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}}
	resp, err := c.Get("http://operator/v1/x")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("GET over the socket: %v", err)
	}
	resp.Body.Close()
	cancel()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the socket outlived the daemon")
}

// `--attach` reads regular files into the request (repeatable); anything
// else is refused before it reaches the daemon.
func TestAttachFiles(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.png")
	_ = os.WriteFile(a, []byte("alpha"), 0o600)
	_ = os.WriteFile(b, []byte("beta"), 0o600)
	req := map[string]any{"attach": a + "\x00" + b}
	if err := attachFiles(req, a+"\x00"+b); err != nil {
		t.Fatal(err)
	}
	files := req["attachments"].([]any)
	if _, left := req["attach"]; left || len(files) != 2 {
		t.Fatalf("request: %v", req)
	}
	f0 := files[0].(map[string]any)
	if f0["name"] != "a.txt" || f0["data"] != base64.StdEncoding.EncodeToString([]byte("alpha")) {
		t.Fatalf("file 0: %v", f0)
	}
	if err := attachFiles(map[string]any{}, dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("a directory: %v", err)
	}
	if err := attachFiles(map[string]any{}, filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a missing file")
	}
}
