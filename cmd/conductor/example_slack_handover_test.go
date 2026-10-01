package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/store"
)

// TestExampleSlackHandoverWiring runs the shipped example's slack hand-over
// trigger end to end against a fake Slack API, with dispatch faked: it reads
// the thread, downloads its screenshot, launches a DETACHED agent with the
// form's repo/mode and the screenshot attached, and reports back by DM to
// the invoking user — never to the connector's default channel, and with no
// reaction on the source message.
func TestExampleSlackHandoverWiring(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	var dmText string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		calls = append(calls, fmt.Sprintf("%s channel=%v users=%v", r.URL.Path, firstOf(body["channel"], r.URL.Query().Get("channel")), body["users"]))
		mu.Unlock()
		switch r.URL.Path {
		case "/conversations.replies":
			fmt.Fprintf(w, `{"ok":true,"messages":[{"user":"U0123456789","ts":"50.0","text":"checkout button does nothing","files":[
				{"id":"F1","name":"shot.png","mimetype":"image/png","size":3,"url_private_download":"%s/files/shot.png"}]}]}`, srv.URL)
		case "/users.info":
			fmt.Fprint(w, `{"ok":true,"user":{"profile":{"display_name":"pat"}}}`)
		case "/chat.getPermalink":
			fmt.Fprint(w, `{"ok":true,"permalink":"https://example.slack.com/archives/C9/p50"}`)
		case "/files/shot.png":
			w.Header().Set("Content-Type", "image/png")
			fmt.Fprint(w, "PNG")
		case "/conversations.open":
			fmt.Fprint(w, `{"ok":true,"channel":{"id":"D42"}}`)
		case "/chat.postMessage":
			mu.Lock()
			dmText, _ = body["text"].(string)
			mu.Unlock()
			fmt.Fprint(w, `{"ok":true,"ts":"60.0"}`)
		default:
			fmt.Fprint(w, `{"ok":true}`)
		}
	}))
	defer srv.Close()
	t.Setenv("PC_SLACK_API_URL", srv.URL)
	config.SetStateDir(t.TempDir())
	defer config.SetStateDir("")

	raw, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.Replace(string(raw), "~/.config/conductor/github-app.pem", writeTempRSAKey(t), 1)
	for _, v := range []string{"GH_WEBHOOK_SECRET", "GH_SMEE_URL", "SLACK_APP_TOKEN", "SLACK_BOT_TOKEN", "CW_SECRET", "GH_PAT",
		"CONDUCTOR_INVOKE_TOKEN", "CONDUCTOR_INVOKE_HMAC"} {
		t.Setenv(v, "dummy-"+v)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := buildFlowStack(cfg, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	var got dispatch.Request
	stack.Runner.Agents.Dispatch = func(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		got = req
		return dispatch.RunRef{AgentID: "ag-7", WorkspaceID: "ws-7", Branch: "handover/slack-your-org-api-abc123", Workdir: "/wt/7", Detached: true}, nil
	}
	stack.Runner.Agents.Guidance = func(string, config.Step, config.Policy) string { return "GUIDANCE-MARKER" }

	idx := -1
	for i, tr := range cfg.Triggers {
		if tr.On == "slack-ops.message_shortcut" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("example has no slack-ops.message_shortcut trigger")
	}
	spec := cfg.Triggers[idx]
	if spec.Options["form"] == nil {
		t.Fatal("the shortcut trigger must inherit the base's form")
	}
	trig := core.Trigger{
		Source: "slack", Instance: "slack-ops", Kind: "message_shortcut", TargetTrusted: true,
		Target: core.Target{Repo: "slack:C9", Number: 1},
		Context: map[string]any{"slack": map[string]any{
			"channel": "C9", "user": "U0123456789", "ts": "50.0", "thread_ts": "50.0", "via": "shortcut",
			"callback_id": "conductor_handover", "text": "checkout button does nothing", "files": []any{},
			"form": map[string]any{"repo": "your-org/api", "mode": "plan", "notes": "start with the cart"},
		}},
		Action: config.Action{Checkout: "none", FlowRef: fmt.Sprintf("%d:%s", idx, spec.On)},
	}
	stack.Runner.Run(context.Background(), store.WorkflowRun{Outputs: map[string]map[string]any{}}, trig, spec, idx, nil, false)

	if !got.Step.Detach || got.Step.Repo != "your-org/api" || got.Step.Mode != "plan" {
		t.Fatalf("detached launch: detach=%v repo=%q mode=%q", got.Step.Detach, got.Step.Repo, got.Step.Mode)
	}
	if len(got.Step.Images) != 1 || !strings.HasSuffix(got.Step.Images[0], "shot.png") {
		t.Fatalf("images: %v", got.Step.Images)
	}
	p := got.Action.Prompt
	if !strings.Contains(p, "BEGIN SLACK THREAD") || !strings.Contains(p, "{{.thread.text}}") || strings.Contains(p, "GUIDANCE-MARKER") {
		t.Fatalf("prompt (pre-render, no guidance appended): %s", p)
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(calls, "\n")
	if strings.Contains(joined, "C0123456789") || strings.Contains(joined, "reactions.add") {
		t.Fatalf("must not post to the default channel or react:\n%s", joined)
	}
	if !strings.Contains(joined, "/conversations.open channel=<nil> users=U0123456789") || !strings.Contains(joined, "/chat.postMessage channel=D42") {
		t.Fatalf("want a DM to the invoking user:\n%s", joined)
	}
	if !strings.Contains(dmText, "branch: handover/slack-your-org-api-abc123") || !strings.Contains(dmText, "workspace: ws-7") ||
		!strings.Contains(dmText, "https://example.slack.com/archives/C9/p50") {
		t.Fatalf("DM text: %s", dmText)
	}
}

func firstOf(v any, alt string) any {
	if v == nil && alt != "" {
		return alt
	}
	return v
}
