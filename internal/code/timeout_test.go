package code

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Regression: the flow-level `timeout:` is a context deadline, and the
// in-process engines used to ignore ctx entirely — a while(1)/for{} step
// wedged the runner goroutine forever. Each engine must now return a
// context error once the deadline passes, well before the watchdog below.
func TestInfiniteLoopCutByTimeout(t *testing.T) {
	cases := []struct {
		run, code string
	}{
		{"js", "while (1) {}"},
		{"go-embed", "func run(ctx map[string]any) any { for {} }"},
		{"lua", "while true do end"},
		{"risor", "x := 0\nfor { x = x + 1 }"},
	}
	for _, tc := range cases {
		t.Run(tc.run, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()

			e := &Executor{}
			type result struct {
				out map[string]any
				err error
			}
			done := make(chan result, 1)
			go func() {
				out, err := e.Exec(ctx, Spec{Run: tc.run, Code: tc.code}, map[string]any{"n": 1})
				done <- result{out, err}
			}()
			select {
			case r := <-done:
				if r.err == nil {
					t.Fatalf("%s: infinite loop returned without error: %v", tc.run, r.out)
				}
				if !strings.Contains(r.err.Error(), "context deadline exceeded") &&
					!strings.Contains(strings.ToLower(r.err.Error()), "context") &&
					!strings.Contains(strings.ToLower(r.err.Error()), "cancel") {
					t.Fatalf("%s: expected a context error, got: %v", tc.run, r.err)
				}
			case <-time.After(30 * time.Second):
				t.Fatalf("%s: engine did not honor the step timeout — still running", tc.run)
			}
		})
	}
}

// A go-embed global initializer (not just run()) is also bound to the step
// ctx — declarations can execute arbitrary code at eval time.
func TestGoEmbedInitializerLoopCutByTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	e := &Executor{}
	done := make(chan error, 1)
	go func() {
		_, err := e.Exec(ctx, Spec{Run: "go-embed", Code: `
var boom = func() int { for {}; return 0 }()

func run(ctx map[string]any) any { return nil }
`}, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("initializer loop returned without error")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("go-embed initializer loop was not cut by the timeout")
	}
}
