package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/flow"
)

// Live run observability (#36 §17): `conductor watch [<run>]` tails the
// daemon's run-event stream over the control socket — run started/done, each
// step's start/finish, gate rounds — for one run (a history id from
// `conductor runs`, or a workflow-run id) or the whole fleet. The stream is
// the live view of exactly what the run record persists; `conductor runs
// <id>` is the after-the-fact view of the same data.

// streamRunEvents serves one watch subscription on the control socket:
// events as JSONL until the client disconnects or the daemon stops.
func streamRunEvents(ctx context.Context, conn net.Conn, events *flow.EventHub, runFilter string, log func(string, ...any)) {
	// A watch is long-lived: lift the request deadline and rely on write
	// errors (client gone) or ctx (daemon stopping) to end it.
	_ = conn.SetDeadline(time.Time{})
	ch, cancel := events.Subscribe(runFilter)
	defer cancel()
	enc := json.NewEncoder(conn)
	// Ack first so the client knows the subscription is live.
	writeControlResp(conn, controlResponse{OK: true, Msg: "watching"})
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := enc.Encode(ev); err != nil {
				return // client went away
			}
		}
	}
}

// cmdWatch is the client: dial, subscribe, pretty-print until interrupted.
func cmdWatch(args []string) error {
	cfg, rest, err := loadConfig(args)
	if err != nil {
		return err
	}
	runFilter := ""
	jsonOut := false
	for _, a := range rest {
		switch {
		case a == "--json":
			jsonOut = true
		case strings.HasPrefix(a, "--"):
			return fmt.Errorf("usage: conductor watch [<run-id>] [--json]")
		default:
			runFilter = a
		}
	}

	conn, err := dialControl(cfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(controlRequest{Cmd: "watch", RunID: runFilter}); err != nil {
		return err
	}
	dec := json.NewDecoder(conn)
	var ack controlResponse
	if err := dec.Decode(&ack); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	if ack.Error != "" {
		return fmt.Errorf("%s", ack.Error)
	}
	what := "all runs"
	if runFilter != "" {
		what = "run " + runFilter
	}
	fmt.Fprintf(os.Stderr, "watching %s (ctrl-c to stop)\n", what)

	// ^C ends the watch cleanly.
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-sigCtx.Done()
		conn.Close()
	}()

	for {
		var ev flow.RunEvent
		if err := dec.Decode(&ev); err != nil {
			if sigCtx.Err() != nil {
				return nil // interrupted by the user
			}
			return nil // daemon closed the stream
		}
		if jsonOut {
			b, _ := json.Marshal(ev)
			fmt.Println(string(b))
			continue
		}
		fmt.Println(formatRunEvent(ev))
	}
}

// formatRunEvent renders one event as a human line.
func formatRunEvent(ev flow.RunEvent) string {
	target := ev.Repo
	if ev.Number > 0 {
		target = fmt.Sprintf("%s#%d", ev.Repo, ev.Number)
	}
	head := fmt.Sprintf("%s  %-22s %-12s", ev.TS.Format("15:04:05"), ev.Run, ev.Type)
	var tail string
	switch ev.Type {
	case "run_started":
		tail = fmt.Sprintf("%s (%s)", target, ev.Kind)
	case "step_started":
		tail = ev.Step
	case "step_done":
		tail = fmt.Sprintf("%s → %s", ev.Step, ev.Status)
		if ev.DurationMS > 0 {
			tail += fmt.Sprintf(" (%s)", (time.Duration(ev.DurationMS) * time.Millisecond).Round(time.Millisecond))
		}
	case "gate":
		tail = fmt.Sprintf("%s → %s", ev.Step, ev.Status)
	case "run_done":
		tail = ev.Status
	default:
		tail = ev.Step + " " + ev.Status
	}
	if ev.Detail != "" {
		tail += "  " + clipLine(ev.Detail, 160)
	}
	return head + " " + tail
}

func clipLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// dialControl opens the daemon control socket.
func dialControl(cfg *config.Config) (net.Conn, error) {
	p := controlSockPath(cfg)
	conn, err := net.Dial("unix", p)
	if err != nil {
		return nil, fmt.Errorf("connect to daemon control socket %s — is it running? %w", p, err)
	}
	return conn, nil
}
