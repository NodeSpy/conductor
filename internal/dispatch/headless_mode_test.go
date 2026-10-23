package dispatch

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

func headlessReq(provider string) Request {
	return Request{
		Wait:       true,
		DispatchID: "disp-1",
		Trigger: core.Trigger{Kind: "assess", Source: "github",
			Target: core.Target{Repo: "o/r", PR: 7, Number: 7}},
		Action:   config.Action{Type: "agent", Prompt: "assess the PR", Checkout: "none"},
		Step:     config.Step{ID: "assess"},
		Provider: provider, Model: "some-model",
	}
}

// Mode selection, one condition per row: the built-in no-prompt mode per
// provider; a step's own mode, a hand-off, and a detach: agent untouched; the
// runtime's headless_mode as one mode, per provider, or none; and an unknown
// provider left alone.
func TestHeadlessModeSelection(t *testing.T) {
	for name, tc := range map[string]struct {
		knob     config.HeadlessMode
		provider string
		mut      func(*Request)
		want     string // "" = no --mode injected
	}{
		"claude default":                        {provider: "claude", want: "bypassPermissions"},
		"codex default":                         {provider: "codex", want: "full-access"},
		"gemini default":                        {provider: "gemini", want: "yolo"},
		"provider id is normalized":             {provider: " Claude ", want: "bypassPermissions"},
		"explicit step mode wins":               {provider: "claude", mut: func(r *Request) { r.Step.Mode = "auto" }},
		"interactive hand-off":                  {provider: "claude", mut: func(r *Request) { r.Interactive = true }},
		"detach: step":                          {provider: "claude", mut: func(r *Request) { r.Step.Detach = true }},
		"background fixer too":                  {provider: "claude", mut: func(r *Request) { r.Wait = false }, want: "bypassPermissions"},
		"unknown provider":                      {provider: "copilot"},
		"bare launch":                           {provider: ""},
		"knob: one mode for all":                {knob: config.HeadlessMode{All: "auto"}, provider: "claude", want: "auto"},
		"knob: one mode, unknown provider":      {knob: config.HeadlessMode{All: "auto"}, provider: "copilot", want: "auto"},
		"knob: none":                            {knob: config.HeadlessMode{All: "none"}, provider: "claude"},
		"knob: map entry":                       {knob: config.HeadlessMode{Providers: map[string]string{"claude": "auto"}}, provider: "claude", want: "auto"},
		"knob: map, unlisted keeps default":     {knob: config.HeadlessMode{Providers: map[string]string{"claude": "auto"}}, provider: "codex", want: "full-access"},
		"knob: map none":                        {knob: config.HeadlessMode{Providers: map[string]string{"codex": "none"}}, provider: "codex"},
		"knob: map names a new provider":        {knob: config.HeadlessMode{Providers: map[string]string{"copilot": "allow-all"}}, provider: "copilot", want: "allow-all"},
		"knob: map entry beats the single mode": {knob: config.HeadlessMode{All: "auto", Providers: map[string]string{"claude": "plan"}}, provider: "claude", want: "plan"},
		"knob does not beat step mode":          {knob: config.HeadlessMode{All: "auto"}, provider: "claude", mut: func(r *Request) { r.Step.Mode = "plan" }},
	} {
		d := &Dispatcher{HeadlessMode: tc.knob}
		req := headlessReq(tc.provider)
		if tc.mut != nil {
			tc.mut(&req)
		}
		got := d.headlessModeArgs(req)
		var want []string
		if tc.want != "" {
			want = []string{"--mode", tc.want}
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}
}

// A provider with no known no-prompt mode is logged once — not once per
// dispatch — and a knob that decided ("none") is not a warning at all.
func TestHeadlessModeUnknownProviderLogsOnce(t *testing.T) {
	var logs []string
	d := &Dispatcher{Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }}
	for i := 0; i < 3; i++ {
		d.headlessModeArgs(headlessReq("copilot"))
		d.headlessModeArgs(headlessReq(""))
		d.headlessModeArgs(headlessReq("claude"))
	}
	if len(logs) != 2 || !strings.Contains(logs[0], "copilot") || !strings.Contains(logs[1], "bare launch") {
		t.Fatalf("want one line for copilot and one for the bare launch, got %q", logs)
	}
	quiet := &Dispatcher{HeadlessMode: config.HeadlessMode{All: "none"}, Logf: func(string, ...any) { t.Error("headless_mode: none must not warn") }}
	quiet.headlessModeArgs(headlessReq("copilot"))
}

// Through the real launch path: a headless step's `paseo run` carries the
// no-prompt mode exactly once; a step mode is the only --mode; a hand-off
// carries none.
func TestPaseoArgvHeadlessMode(t *testing.T) {
	run := func(req Request) []string {
		t.Helper()
		d := &Dispatcher{PaseoBin: "paseo", DryRun: true, repoDirs: map[string]string{}}
		ref, err := d.paseo(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		return ref.Argv
	}
	modes := func(argv []string) []string {
		var out []string
		for i := 0; i+1 < len(argv); i++ {
			if argv[i] == "--mode" {
				out = append(out, argv[i+1])
			}
		}
		return out
	}
	if got := modes(run(headlessReq("claude"))); fmt.Sprint(got) != "[bypassPermissions]" {
		t.Errorf("headless claude step: --mode %v, want [bypassPermissions]", got)
	}
	withMode := headlessReq("claude")
	withMode.Step.Mode = "auto"
	if got := modes(run(withMode)); fmt.Sprint(got) != "[auto]" {
		t.Errorf("explicit mode: --mode %v, want only [auto]", got)
	}
	handoff := headlessReq("claude")
	handoff.Interactive = true
	handoff.Trigger.Target = core.Target{}
	if got := modes(run(handoff)); len(got) != 0 {
		t.Errorf("hand-off: --mode %v, want none", got)
	}
}

// A detach: launch is handed to a person: it gets its own mode: (or none),
// never the headless one.
func TestDetachGetsNoHeadlessMode(t *testing.T) {
	owned := NewOwnedSet("")
	req, backend := detachRequest(owned)
	req.Step.Mode = ""
	req.Provider = "claude"
	d := newDetachDispatcher(backend, owned)
	if _, err := d.Dispatch(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(backend.runCalls) != 1 {
		t.Fatalf("runs = %d", len(backend.runCalls))
	}
	for _, a := range backend.runCalls[0].Args {
		if a == "--mode" {
			t.Fatalf("a detach launch must carry no injected mode: %v", backend.runCalls[0].Args)
		}
	}
}
