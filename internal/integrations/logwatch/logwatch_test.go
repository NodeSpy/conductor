package logwatch

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

func TestNamedGroups(t *testing.T) {
	re := regexp.MustCompile(`ERROR (?P<code>\d+): (?P<msg>.*)`)
	m := re.FindStringSubmatch("ERROR 500: boom happened")
	if m == nil {
		t.Fatal("pattern should match")
	}
	g := namedGroups(re, m)
	if g["code"] != "500" || g["msg"] != "boom happened" {
		t.Fatalf("named groups wrong: %v", g)
	}
	if _, ok := g[""]; ok {
		t.Error("the whole-match group (index 0 / empty name) must not be included")
	}
}

func TestValidate(t *testing.T) {
	good := &Integration{name: "t", cfg: Config{Watches: []Watch{
		{Name: "errs", Path: "/var/log/app.log", Pattern: "ERROR", Action: config.Action{Type: "command"}},
	}}}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	both := &Integration{name: "t", cfg: Config{Watches: []Watch{
		{Name: "x", Path: "/a", Command: []string{"journalctl"}, Pattern: "E", Action: config.Action{Type: "command"}},
	}}}
	if both.Validate() == nil {
		t.Error("setting both path and command must fail")
	}
	neither := &Integration{name: "t", cfg: Config{Watches: []Watch{
		{Name: "x", Pattern: "E", Action: config.Action{Type: "command"}},
	}}}
	if neither.Validate() == nil {
		t.Error("setting neither path nor command must fail")
	}
	badre := &Integration{name: "t", cfg: Config{Watches: []Watch{
		{Name: "x", Path: "/a", Pattern: "(", Action: config.Action{Type: "command"}},
	}}}
	if badre.Validate() == nil {
		t.Error("an uncompilable pattern must fail")
	}
}

func TestStreamCommandEmitsOnMatch(t *testing.T) {
	ig := &Integration{
		name: "t",
		cfg: Config{Watches: []Watch{{
			Name:     "errs",
			Command:  []string{"bash", "-c", "echo noise; echo 'ERROR boom'; sleep 30"},
			Pattern:  `ERROR (?P<msg>.*)`,
			Debounce: config.Duration(40 * time.Millisecond),
			Action:   config.Action{Type: "command"},
		}}},
	}
	emits := make(chan core.Trigger, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ig.Start(ctx, func(_ context.Context, tr core.Trigger) { emits <- tr })

	select {
	case tr := <-emits:
		if tr.Source != "logwatch" || tr.Kind != "errs" {
			t.Fatalf("unexpected trigger identity: %+v", tr)
		}
		if tr.Context["line"] != "ERROR boom" {
			t.Fatalf("expected line 'ERROR boom', got %v", tr.Context["line"])
		}
		groups, _ := tr.Context["groups"].(map[string]string)
		if groups["msg"] != "boom" {
			t.Fatalf("expected capture msg=boom, got %v", tr.Context["groups"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no trigger emitted for a matching line")
	}
}

func TestStreamFileTailEmits(t *testing.T) {
	dir := t.TempDir()
	logfile := filepath.Join(dir, "app.log")
	if err := os.WriteFile(logfile, []byte("startup ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ig := &Integration{
		name: "t",
		cfg: Config{Watches: []Watch{{
			Name:     "errs",
			Path:     logfile,
			Pattern:  "FATAL",
			Debounce: config.Duration(40 * time.Millisecond),
			Action:   config.Action{Type: "command"},
		}}},
	}
	emits := make(chan core.Trigger, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ig.Start(ctx, func(_ context.Context, tr core.Trigger) { emits <- tr })
	time.Sleep(300 * time.Millisecond) // let `tail -F` attach

	f, err := os.OpenFile(logfile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("just info\nFATAL disk gone\n")
	f.Close()

	select {
	case tr := <-emits:
		if tr.Context["line"] != "FATAL disk gone" {
			t.Fatalf("expected the FATAL line, got %v", tr.Context["line"])
		}
		if tr.Context["source"] != logfile {
			t.Fatalf("expected source=%s, got %v", logfile, tr.Context["source"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no trigger emitted for a matching appended line")
	}
}
