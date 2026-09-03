package controller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
)

// fakeOpencodeServer is an httptest stand-in for `opencode serve`: it creates a
// session and echoes a prompt back as an assistant text message, recording what it
// received.
type fakeOpencodeServer struct {
	mu           sync.Mutex
	createBody   map[string]any
	lastMessage  map[string]any
	lastProvider string
	lastModel    string
}

func (f *fakeOpencodeServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		f.mu.Lock()
		f.createBody = body
		f.mu.Unlock()
		writeJSON(w, map[string]any{"id": "ses_123"})
	})
	mux.HandleFunc("/session/ses_123/message", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		f.mu.Lock()
		f.lastMessage = body
		f.lastProvider, _ = body["providerID"].(string)
		f.lastModel, _ = body["modelID"].(string)
		f.mu.Unlock()
		// Reflect the prompt text back as the assistant's reply.
		var text string
		if parts, ok := body["parts"].([]any); ok && len(parts) > 0 {
			if p, ok := parts[0].(map[string]any); ok {
				text, _ = p["text"].(string)
			}
		}
		writeJSON(w, map[string]any{"parts": []map[string]any{{"type": "text", "text": "echo:" + text}}})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newOpencodeFor(t *testing.T, srvURL string, prov Provisioner) *opencodeController {
	t.Helper()
	c := newOpencodeController("oc", config.ControllerConfig{Type: "opencode"}, prov)
	c.dial = func(context.Context, string, []string) (string, func() error, error) {
		return srvURL, func() error { return nil }, nil
	}
	return c
}

func TestOpencodeCapabilities(t *testing.T) {
	c := newOpencodeController("oc", config.ControllerConfig{Type: "opencode"}, nil)
	caps, _ := c.Initialize(context.Background())
	if caps.SessionModel != ModelResumable || caps.Transport != TransportNative {
		t.Fatalf("opencode must be resumable/native, got %+v", caps)
	}
	if !caps.CheckoutPR || !caps.SendFollowup || !caps.Remote {
		t.Fatalf("opencode caps missing: %+v", caps)
	}
}

func TestOpencodeNewSessionPromptsInWorktree(t *testing.T) {
	fs := &fakeOpencodeServer{}
	srv := httptest.NewServer(fs.handler())
	defer srv.Close()

	c := newOpencodeFor(t, srv.URL, &fakeProv{})
	sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "do the thing"), Cwd: "/wt/o-r-7"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID() != "ses_123" {
		t.Fatalf("session id = %q, want ses_123", sess.ID())
	}
	waitSession(t, sess)

	fs.mu.Lock()
	defer fs.mu.Unlock()
	// The worktree is passed as the session directory, and the PR identity as title.
	if fs.createBody["directory"] != "/wt/o-r-7" {
		t.Fatalf("create session directory = %v, want the worktree", fs.createBody["directory"])
	}
	if title, _ := fs.createBody["title"].(string); title == "" {
		t.Fatal("create session should carry a PR-identity title")
	}
	// Model routing from the profile reached the message call.
	if fs.lastProvider != "anthropic" || fs.lastModel != "claude" {
		t.Fatalf("model routing = %q/%q, want anthropic/claude", fs.lastProvider, fs.lastModel)
	}
}

func TestOpencodeFollowUpTurnReturnsOutput(t *testing.T) {
	fs := &fakeOpencodeServer{}
	srv := httptest.NewServer(fs.handler())
	defer srv.Close()

	c := newOpencodeFor(t, srv.URL, &fakeProv{})
	sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "first"), Cwd: "/wt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)

	ch, err := sess.Prompt(context.Background(), Message{Text: "second"})
	if err != nil {
		t.Fatal(err)
	}
	var last Update
	for u := range ch {
		last = u
	}
	if last.Kind != UpdateDone || last.Err != nil {
		t.Fatalf("follow-up terminal update = %+v", last)
	}
	if last.Output != "echo:second" {
		t.Fatalf("follow-up output = %q, want echo:second", last.Output)
	}
}
