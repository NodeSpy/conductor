package bot

import (
	"os"
	"testing"
)

// TestMain keeps every test's recall index (config.BotIndexPath and
// TeamIndexPath resolve under conductor's state dir) out of the real one:
// XDG_STATE_HOME points at a throwaway dir for the whole package.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "conductor-bot-teststate")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_STATE_HOME", tmp)
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}
