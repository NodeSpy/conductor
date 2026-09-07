package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/flow"
)

// The watch command streams the daemon's live run events over the control
// socket (#36 §17): subscribe (with an optional run filter), ack, then JSONL
// events until the client hangs up.
func TestControlSocketWatchStreams(t *testing.T) {
	cfg, _ := manualCfg(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hub := flow.NewEventHub()
	go serveControl(ctx, controlSockPath(cfg), nil, nil, nil, nil, hub, nil, func(string, ...any) {})
	waitForSock(t, controlSockPath(cfg))

	conn, err := net.Dial("unix", controlSockPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(controlRequest{Cmd: "watch", RunID: "r-one"}); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(conn)
	var ack controlResponse
	if err := dec.Decode(&ack); err != nil || !ack.OK {
		t.Fatalf("ack: %+v %v", ack, err)
	}

	// The subscription registers asynchronously after the ack write; publish
	// until the matching event lands (non-matching noise interleaved).
	got := make(chan flow.RunEvent, 1)
	go func() {
		var ev flow.RunEvent
		if err := dec.Decode(&ev); err == nil {
			got <- ev
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		hub.Publish(flow.RunEvent{Type: "step_done", Run: "r-other", Step: "noise"})
		hub.Publish(flow.RunEvent{Type: "step_done", Run: "r-one", Step: "fix", Status: "ok"})
		select {
		case ev := <-got:
			if ev.Run != "r-one" || ev.Step != "fix" || ev.Status != "ok" {
				t.Fatalf("event: %+v", ev)
			}
			return
		case <-time.After(20 * time.Millisecond):
			if time.Now().After(deadline) {
				t.Fatal("no event arrived over the watch stream")
			}
		}
	}
}

func TestControlSocketWatchWithoutHub(t *testing.T) {
	cfg, _ := manualCfg(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go serveControl(ctx, controlSockPath(cfg), nil, nil, nil, nil, nil, nil, func(string, ...any) {})
	waitForSock(t, controlSockPath(cfg))

	resp, err := sendControl(cfg, controlRequest{Cmd: "watch"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error == "" {
		t.Fatalf("watch without a flow runner must error: %+v", resp)
	}
}

// Regression (#36 iso-review round 2, item 6): the control socket now serves
// `retry --force-replay` (replays side effects) and `watch` (every run's live
// events), so any local process reaching it is a privilege boundary. Like its
// siblings (egress proxy, memory IPC) it must be 0600 — owner-only — not the
// world-reachable mode net.Listen leaves behind under a default umask.
func TestControlSocketIsOwnerOnly(t *testing.T) {
	cfg, _ := manualCfg(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go serveControl(ctx, controlSockPath(cfg), nil, nil, nil, nil, nil, nil, func(string, ...any) {})
	waitForSock(t, controlSockPath(cfg))

	fi, err := os.Stat(controlSockPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("control socket mode = %04o, want 0600 (any group/other bit lets another local user replay side effects)", perm)
	}
}

func waitForSock(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("control socket %s never appeared", path)
}

func TestFormatRunEvent(t *testing.T) {
	ev := flow.RunEvent{TS: time.Date(2026, 9, 6, 12, 30, 0, 0, time.UTC),
		Type: "step_done", Run: "rabc", Step: "fix", Status: "failed",
		Detail: "boom\nsecond line", DurationMS: 1500}
	line := formatRunEvent(ev)
	for _, want := range []string{"12:30:00", "rabc", "step_done", "fix → failed", "(1.5s)", "boom second line"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line %q missing %q", line, want)
		}
	}
}
