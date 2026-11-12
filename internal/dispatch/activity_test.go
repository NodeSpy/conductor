package dispatch

import "testing"

// The last tool call in a paseo timeline tail, as a short phrase a reader of
// a status understands; the agent's own text and thoughts are not tool calls.
func TestLastToolPhrase(t *testing.T) {
	for _, tc := range []struct{ name, log, want string }{
		{"edit", "[User] fix it\n[Read] adapters/adapter.go\n[Edit] adapters/adapter.go\n", "editing adapters/adapter.go"},
		{"command", "[Bash] go test ./adapters/...\n[Thought] the test should pass now\nIt passes.\n", "running go test ./adapters/..."},
		{"unknown tool by name", "[Deploy Preview] staging\n", "using Deploy Preview staging"},
		{"raw json input dropped", `[mcp__x__tool] {"secretish":"value"}` + "\n", "using mcp__x__tool"},
		{"bare", "[TodoWrite]\n", "planning"},
		{"none", "[User] hello\n[Assistant] hi\nplain text\n", ""},
		{"multiline message after a tool", "[Grep] TODO\n[Assistant] Found three.\n- [x] item\n", "searching TODO"},
	} {
		if got := LastToolPhrase(tc.log); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
