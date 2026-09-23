package models

import (
	"path/filepath"
	"testing"
	"time"
)

func TestUnsupportedSignature(t *testing.T) {
	yes := []string{
		"API Error: 400 Claude Code 2.1.220 does not support this model; version 2.1.280 or newer is required.",
		"error: unknown model 'foo-9'",
		"model not found: foo",
		"Invalid model: bar",
		"this model has been deprecated",
	}
	for _, s := range yes {
		if !UnsupportedSignature(s) {
			t.Fatalf("should classify as unsupported: %q", s)
		}
	}
	no := []string{
		"", "rate limit exceeded", `{"decision":"approve"}`,
		"network timeout talking to the api",
	}
	for _, s := range no {
		if UnsupportedSignature(s) {
			t.Fatalf("must NOT classify as unsupported: %q", s)
		}
	}
}

func TestUnsupportedCacheTTLAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unsup.json")
	c := NewUnsupportedCache(path)
	c.Mark("", "claude-opus-5-5")
	if !c.Has("claude", "claude-opus-5-5") {
		t.Fatal("an empty-runtime mark applies to every runtime")
	}
	if c.Has("claude", "claude-opus-5") {
		t.Fatal("unmarked model must not be excluded")
	}
	// Survives a reload.
	c2 := NewUnsupportedCache(path)
	if !c2.Has("claude", "claude-opus-5-5") {
		t.Fatal("marks must persist across a restart")
	}
	// Expires after the TTL — the fleet climbs back to the newest model.
	c2.now = func() time.Time { return time.Now().Add(UnsupportedTTL + time.Minute) }
	if c2.Has("claude", "claude-opus-5-5") {
		t.Fatal("an expired mark must not exclude")
	}
}
