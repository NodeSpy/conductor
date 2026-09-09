package controller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/acp"
)

// TestSpawnACPScrubEnv proves the #54 §8.1 fix: an env-scrubbed ACP launch (an
// EXTERNAL runtime plugin) does NOT receive the daemon's credential-bearing
// environment, while a normal (bundled) ACP launch still does.
func TestSpawnACPScrubEnv(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}
	// A stand-in daemon secret sitting in the process environment.
	t.Setenv("CONDUCTOR_TEST_SECRET", "leaky-daemon-token")

	run := func(scrub bool) string {
		dir := t.TempDir()
		out := filepath.Join(dir, "env.txt")
		script := filepath.Join(dir, "dump.sh")
		if err := os.WriteFile(script, []byte("#!/bin/sh\n/usr/bin/env > "+out+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, cleanup, err := spawnACP(context.Background(), []string{script}, "", nil, acp.DelegateFuncs{}, "", launchOpts{}, scrub)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		// Wait for the child to dump its env and exit.
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if b, err := os.ReadFile(out); err == nil && len(b) > 0 {
				return string(b)
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("child never wrote its environment")
		return ""
	}

	// Scrubbed: the daemon secret is gone, but PATH (operational baseline) stays.
	env := run(true)
	if strings.Contains(env, "leaky-daemon-token") {
		t.Fatalf("scrubbed external runtime received the daemon secret:\n%s", env)
	}
	if !strings.Contains(env, "PATH=") {
		t.Fatalf("scrubbed env dropped PATH (too aggressive):\n%s", env)
	}

	// Not scrubbed (bundled ACP runtimes): behavior unchanged — secret present.
	if env := run(false); !strings.Contains(env, "leaky-daemon-token") {
		t.Fatalf("non-scrubbed launch should still inherit the full env:\n%s", env)
	}
}
