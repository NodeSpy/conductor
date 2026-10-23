package dispatch

import (
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
)

// A HEADLESS paseo agent is one conductor dispatches and owns on a step nobody
// drives: a decide step's session, a panelist, a fixer. Launched in its
// provider's default mode it raises a permission prompt for any tool use that
// mode doesn't cover (Claude's "Always Ask" covers almost nothing), and nobody
// is there to answer it: the agent waits until paseo gives up, and the step
// fails late with a misleading error — even for the agent's own
// `conductor call step.done`.
//
// So by default conductor launches a headless agent in its provider's
// NO-PROMPT mode. The trade is deliberate and documented: such an agent reads
// untrusted text (a PR, a comment) with full tool access. A step's own
// `mode:` always wins, and the paseo runtime's `headless_mode:` tightens the
// default (a mode for every provider, one per provider, or "none" for the
// provider's own default). Hand-offs and detach: steps — agents a person
// drives — never get an injected mode.

// noPromptModes is each known paseo provider's no-prompt mode id — the modes
// the paseo daemon marks "dangerous". paseo's CLI lists a provider's modes by
// label only, so the ids live here.
var noPromptModes = map[string]string{
	"claude": "bypassPermissions",
	"codex":  "full-access",
	"gemini": "yolo",
}

// headlessModeArgs returns the `--mode` a launch adds for a headless agent,
// or nil: a step that sets its own mode, a hand-off or detach: agent, a
// runtime whose headless_mode says "none", and a provider with no known
// no-prompt mode all launch exactly as before.
func (d *Dispatcher) headlessModeArgs(req Request) []string {
	if req.Interactive || req.Step.Detach || strings.TrimSpace(req.Step.Mode) != "" {
		return nil
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	mode, known := headlessModeFor(d.HeadlessMode, provider)
	if !known {
		d.warnUnknownHeadlessProvider(provider)
		return nil
	}
	if mode == "" {
		return nil
	}
	return []string{"--mode", mode}
}

// headlessModeFor resolves the knob for one provider: its own map entry, else
// the runtime-wide mode, else the built-in no-prompt mode. "" with known=true
// means "inject nothing" (headless_mode: none); known=false means no mode is
// known for this provider at all.
func headlessModeFor(h config.HeadlessMode, provider string) (mode string, known bool) {
	if m, ok := h.Providers[provider]; ok {
		return noneToEmpty(m), true
	}
	if h.All != "" {
		return noneToEmpty(h.All), true
	}
	m, ok := noPromptModes[provider]
	return m, ok
}

func noneToEmpty(m string) string {
	if m == config.HeadlessModeNone {
		return ""
	}
	return m
}

// warnUnknownHeadlessProvider logs, once per provider, that a headless agent
// launches in a default mode that may stop on a permission prompt nobody
// answers.
func (d *Dispatcher) warnUnknownHeadlessProvider(provider string) {
	if _, seen := d.headlessWarned.LoadOrStore(provider, true); seen {
		return
	}
	what := "provider " + provider
	if provider == "" {
		what = "a bare launch (no resolved provider)"
	}
	d.logf("paseo: no known no-prompt mode for %s — its headless agents launch in the provider's default mode, where a permission prompt waits for an approval nobody gives; set the step's mode: or the runtime's headless_mode: to choose one", what)
}

func (d *Dispatcher) logf(format string, args ...any) {
	if d.Logf != nil {
		d.Logf(format, args...)
	}
}
