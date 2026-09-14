package controller

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// launchCall records one cli launch.
type launchCall struct {
	dir  string
	env  []string
	argv []string
}

// fakeLauncher scripts cli process launches.
type fakeLauncher struct {
	mu    sync.Mutex
	calls []launchCall
	out   func(argv []string) (string, error)
}

func (l *fakeLauncher) launch(_ context.Context, dir string, env []string, argv []string) (cliProc, error) {
	l.mu.Lock()
	l.calls = append(l.calls, launchCall{dir: dir, env: env, argv: argv})
	l.mu.Unlock()
	out, err := "", error(nil)
	if l.out != nil {
		out, err = l.out(argv)
	}
	return &fakeProc{out: out, err: err}, nil
}

func (l *fakeLauncher) call(i int) launchCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls[i]
}

func (l *fakeLauncher) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.calls)
}

type fakeProc struct {
	out string
	err error
}

func (p *fakeProc) Wait() (string, error) { return p.out, p.err }
func (p *fakeProc) Kill() error           { return nil }

// TestBoundedBufferCaps (#57 M10): the combined-output buffer discards writes
// past its cap (so a flooding tool can't grow the heap without bound), never
// reports a short write to the exec copier, and marks the output as truncated.
func TestBoundedBufferCaps(t *testing.T) {
	b := &boundedBuffer{max: 64}

	// A short write always reports its full length, even once the cap is hit.
	chunk := strings.Repeat("x", 40)
	for i := 0; i < 5; i++ {
		if n, err := b.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("write %d: n=%d err=%v (must swallow overflow, never short-write)", i, n, err)
		}
	}

	s := b.String()
	body, marker, found := strings.Cut(s, "\n[conductor:")
	if !found {
		t.Fatalf("truncated output must carry a marker: %q", s)
	}
	if len(body) != 64 {
		t.Fatalf("retained body = %d bytes, want the 64-byte cap", len(body))
	}
	if !strings.Contains(marker, "64 bytes") {
		t.Fatalf("marker should name the cap: %q", marker)
	}

	// Under the cap: verbatim, no marker.
	u := &boundedBuffer{max: 64}
	_, _ = u.Write([]byte("hello"))
	if u.String() != "hello" {
		t.Fatalf("under-cap output must be verbatim: %q", u.String())
	}
}

// TestBoundedBufferConcurrentWrites: os/exec copies stdout and stderr with
// independent goroutines onto the shared writer, so Write must be race-free.
// (Run under -race.)
func TestBoundedBufferConcurrentWrites(t *testing.T) {
	b := &boundedBuffer{max: 1 << 16}
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_, _ = b.Write([]byte("stream-chunk-of-bytes\n"))
			}
		}()
	}
	wg.Wait()
	if got := len(b.String()); got > (1<<16)+64 {
		t.Fatalf("concurrent writes overflowed the cap: %d bytes", got)
	}
}

// TestStartCLIProcTruncatesFloodingOutput drives the real subprocess path: a
// tool emitting far more than the cap yields bounded captured output with the
// truncation marker.
func TestStartCLIProcTruncatesFloodingOutput(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh unavailable")
	}
	// ~3 MiB of a single byte, well past the 1 MiB cap.
	argv := []string{"sh", "-c", "head -c 3145728 /dev/zero | tr '\\0' a"}
	proc, err := startCLIProc(context.Background(), "", nil, argv)
	if err != nil {
		t.Fatal(err)
	}
	out, err := proc.Wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !strings.Contains(out, "output truncated at") {
		t.Fatalf("flooding output must be marked truncated (len=%d)", len(out))
	}
	if len(out) > cliOutputCap+128 {
		t.Fatalf("captured output not bounded: %d bytes (cap %d)", len(out), cliOutputCap)
	}
}

func TestCLIOneshotRunInWorktree(t *testing.T) {
	l := &fakeLauncher{}
	c := newCLIController("cx", config.ControllerConfig{Transport: "cli", Tool: "codex"}, nil)
	c.launch = l.launch

	if c.Model() != ModelOneshot {
		t.Fatalf("codex recipe should be oneshot, got %q", c.Model())
	}

	sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "fix the bug"), Cwd: "/wt/o-r-7"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)

	call := l.call(0)
	if call.dir != "/wt/o-r-7" {
		t.Fatalf("cli cwd = %q, want the worktree", call.dir)
	}
	// The RESOLVED model rides the argv — a `use: cli` runtime used to
	// drop it, so an exact pin silently launched the tool's default.
	if joinArgs(call.argv) != "codex exec fix the bug --model anthropic/claude" {
		t.Fatalf("codex argv = %v", call.argv)
	}
	if !envHas(call.env, "GH_TOKEN", "utok") {
		t.Fatalf("cli env missing the user token: %v", call.env)
	}

	// A oneshot recipe refuses a follow-up.
	if _, err := sess.Prompt(context.Background(), Message{Text: "again"}); err != ErrNoFollowup {
		t.Fatalf("oneshot follow-up should be ErrNoFollowup, got %v", err)
	}
}

func TestCLIResumableClaudeCode(t *testing.T) {
	l := &fakeLauncher{out: func(argv []string) (string, error) {
		// First run emits a session id; a resume run echoes back.
		if argIndex(argv, "--resume") < 0 {
			return `{"session_id":"claude-abc","result":"done"}`, nil
		}
		return `{"result":"resumed"}`, nil
	}}
	c := newCLIController("cc", config.ControllerConfig{Transport: "cli", Tool: "claude-code"}, nil)
	c.launch = l.launch

	if c.Model() != ModelResumable {
		t.Fatalf("claude-code recipe should be resumable, got %q", c.Model())
	}
	caps, _ := c.Initialize(context.Background())
	if !caps.SendFollowup {
		t.Fatal("a resumable recipe advertises SendFollowup")
	}

	sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "start"), Cwd: "/wt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)

	first := l.call(0)
	if argIndex(first.argv, "-p") < 0 || argIndex(first.argv, "start") < 0 {
		t.Fatalf("first claude argv = %v", first.argv)
	}

	// The follow-up resumes the captured tool session id.
	ch, err := sess.Prompt(context.Background(), Message{Text: "more"})
	if err != nil {
		t.Fatal(err)
	}
	var last Update
	for u := range ch {
		last = u
	}
	if last.Kind != UpdateDone {
		t.Fatalf("follow-up terminal update = %+v", last)
	}
	resume := l.call(l.count() - 1)
	ri := argIndex(resume.argv, "--resume")
	if ri < 0 || resume.argv[ri+1] != "claude-abc" {
		t.Fatalf("follow-up should --resume the captured session id, got %v", resume.argv)
	}
}

func TestCLICustomCommandRecipe(t *testing.T) {
	l := &fakeLauncher{}
	c := newCLIController("x", config.ControllerConfig{Transport: "cli", Command: []string{"mytool", "--flag"}}, nil)
	c.launch = l.launch

	sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "go"), Cwd: "/wt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)
	if joinArgs(l.call(0).argv) != "mytool --flag go" {
		t.Fatalf("custom recipe argv = %v", l.call(0).argv)
	}
}

func TestParseClaudeResult(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"type":"result","result":"the answer","session_id":"x"}`, "the answer"},
		{`{"result":"{\"decision\":\"approve\"}"}`, `{"decision":"approve"}`},
		{"not json at all", "not json at all"},     // non-envelope → raw
		{`{"type":"result"}`, `{"type":"result"}`}, // no result field → raw
	}
	for _, c := range cases {
		if got := parseClaudeResult(c.in); got != c.want {
			t.Errorf("parseClaudeResult(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A foreground claude-code turn's reply is captured via OutputCapturer, decoded
// out of the --output-format json envelope to the bare result text.
func TestCLISessionOutputCapturesResult(t *testing.T) {
	l := &fakeLauncher{out: func([]string) (string, error) {
		return `{"session_id":"s1","result":"the verdict"}`, nil
	}}
	c := newCLIController("cc", config.ControllerConfig{Transport: "cli", Tool: "claude-code"}, nil)
	c.launch = l.launch

	sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("review", "go"), Cwd: "/wt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)
	oc, ok := sess.(OutputCapturer)
	if !ok {
		t.Fatal("cliSession must implement OutputCapturer")
	}
	if got := oc.Output(); got != "the verdict" {
		t.Fatalf("Output() = %q, want the decoded result text", got)
	}
}

// End to end on the cli runtime: a foreground output_schema step gets the
// directive injected into its prompt, and its reply is extracted + validated +
// canonicalized into RunRef.Output — the conductor-owned contract on a runtime
// with no native --output-schema.
func TestControllerRunnerAppliesOutputSchema(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"decision"},
		"properties": map[string]any{"decision": map[string]any{"type": "string"}}}
	l := &fakeLauncher{out: func(argv []string) (string, error) {
		// The prompt must carry the injected schema directive.
		pi := argIndex(argv, "-p")
		if pi < 0 || !strings.Contains(argv[pi+1], "JSON") {
			t.Errorf("prompt should carry the schema directive: %v", argv)
		}
		return `{"result":"my call is {\"decision\":\"approve\"}"}`, nil
	}}
	c := newCLIController("cc", config.ControllerConfig{Transport: "cli", Tool: "claude-code"}, nil)
	c.launch = l.launch
	r := newControllerRunner(c, nil, nil)

	req := makeReq("review", "judge this")
	req.Wait = true
	req.Action.OutputSchema = schema
	req.Step.OutputSchema = schema

	ref, err := r.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !strings.Contains(ref.Output, `"decision"`) || !strings.Contains(ref.Output, "approve") {
		t.Fatalf("RunRef.Output should be the canonical validated object, got %q", ref.Output)
	}
	if strings.Contains(ref.Output, "my call is") {
		t.Fatalf("Output should be the object alone, not the surrounding prose: %q", ref.Output)
	}
}

// When the first reply misses, the runner opens ONE fresh corrective turn; a
// valid reply there is accepted.
func TestControllerRunnerSchemaCorrectiveRetry(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"decision"}}
	var n int
	l := &fakeLauncher{out: func([]string) (string, error) {
		n++
		if n == 1 {
			return `{"result":"no structured answer here"}`, nil
		}
		return `{"result":"{\"decision\":\"reject\"}"}`, nil
	}}
	c := newCLIController("cc", config.ControllerConfig{Transport: "cli", Tool: "claude-code"}, nil)
	c.launch = l.launch
	r := newControllerRunner(c, nil, nil)

	req := makeReq("review", "judge this")
	req.Wait = true
	req.Action.OutputSchema = schema
	req.Step.OutputSchema = schema

	ref, err := r.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected exactly one corrective turn (2 launches), got %d", n)
	}
	if !strings.Contains(ref.Output, "reject") {
		t.Fatalf("expected the corrective turn's object, got %q", ref.Output)
	}
}
