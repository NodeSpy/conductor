package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Code-step ENGINES: what actually executes a step's work when the step is
// neither an agent dispatch nor a connector verb.
//
// A step names its engine with `use:` — the same reference grammar
// connectors and runtimes use, resolved as UseKindEngine:
//
//	use: js                 a builtin, in-binary
//	use: cli                the builtin subprocess engine (+ `command:`)
//	use: bash               a host interpreter on the box (or a path to one)
//	use: acme/wasm-engine   a plugin-backed engine (NOT wired up yet)
//
// `run:` is the ORIGINAL spelling and stays as an alias for exactly the same
// selection, because every deployed config is written in it. `run: js` and
// `use: js` are the same step; `run:` additionally keeps its historic
// permissiveness — ANY name it does not recognize is a host interpreter, no
// allowlist consulted — so no config that works today can stop working.
//
// `use:` is stricter on purpose. It is a NEW key with a resolution table
// behind it, so a name it cannot place is a typo or a misunderstanding worth
// naming, not a silent PATH lookup that fails later on a box the author is
// not looking at.
//
// A step's `use:` is an engine and NOTHING else. The workflow call is
// `call:` (see Step.Call) — a step-level `use:` that names a workflow is the
// pre-engines spelling and `conductor config migrate` rewrites it.

// EngineClass is what a resolved engine selection turns out to be —
// the one question the step dispatcher asks.
type EngineClass string

// EngineClass values.
const (
	// EngineNone means the step selected no engine (it is not a code step).
	EngineNone EngineClass = ""
	// EngineInProcess is a builtin engine that runs inside the daemon's own
	// process (js, go-embed, risor, lua). Local-only: it shares this
	// process's fate, so it can never be shipped to a `host:`.
	EngineInProcess EngineClass = "in-process"
	// EngineCLI is the builtin `cli` engine: run `command:` as a subprocess,
	// ctx on stdin, outputs from stdout.
	EngineCLI EngineClass = "cli"
	// EngineHost is a host interpreter — a program on the box, named
	// (`bash`, `python3`) or pathed (`/opt/py/bin/python`).
	EngineHost EngineClass = "host"
	// EnginePlugin is an engine that would have to be fetched. Parsed and
	// classified so the error can say so; not executable in this build.
	EnginePlugin EngineClass = "plugin"
)

// inProcessEngines are the builtin engines that run inside the daemon.
var inProcessEngines = map[string]bool{
	"js": true, "go-embed": true, "risor": true, "lua": true,
}

// hostInterpreters are the interpreter names `use:` accepts as "a program on
// the box" rather than as a plugin reference.
//
// The list exists only because `use:` has to choose between two readings of
// a bare word, and guessing wrong in the plugin direction would send an
// operator's `use: bash` to a github repo that does not exist. It is NOT a
// restriction on what conductor can run: `run:` takes any name or path with
// no list at all, and `use:` takes any PATH (`./venv/bin/python`,
// `/usr/bin/env`) the same way. Add a name here when a real interpreter is
// missing; the escape hatch in the meantime is `run:` or an explicit path.
var hostInterpreters = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true,
	"fish": true, "ash": true,
	"node": true, "deno": true, "bun": true,
	"python": true, "python2": true, "python3": true,
	"ruby": true, "perl": true, "php": true, "lua5.1": true,
	"go": true, "Rscript": true, "osascript": true,
	"pwsh": true, "powershell": true, "tclsh": true, "awk": true,
}

// EngineSelector is the step's engine reference as written: `use:` when set,
// else the `run:` alias. Empty when the step selects no engine.
func (s Step) EngineSelector() string {
	if u := strings.TrimSpace(s.Use); u != "" {
		return u
	}
	return strings.TrimSpace(s.Run)
}

// StepEngine resolves a step's engine selection to the reference and what it
// turns out to be. This is the ONE place `use:`/`run:` are collapsed, so the
// validator, the step dispatcher and `conductor validate` cannot disagree
// about which engine a step runs on.
func (s Step) StepEngine() (string, EngineClass) {
	sel := s.EngineSelector()
	if sel == "" {
		return "", EngineNone
	}
	switch {
	case sel == "cli":
		return sel, EngineCLI
	case inProcessEngines[sel]:
		return sel, EngineInProcess
	}
	// From here the two spellings part ways: `run:` treats every remaining
	// value as a host interpreter (its historic behavior, preserved
	// exactly), while `use:` only does so for a path or a known interpreter
	// name and reads anything else as a plugin reference.
	if s.Use == "" {
		return sel, EngineHost
	}
	if isLocalUsePath(sel) || hostInterpreters[sel] {
		return sel, EngineHost
	}
	return sel, EnginePlugin
}

// engineIsWorkflowErr is the message for the single most likely way a config
// lands on an engine name it did not mean: it was written before engines
// existed, when a step-level `use:` meant a WORKFLOW CALL. Checked before
// anything else, so the operator is told what they actually wrote rather than
// sent to a plugin repo for a workflow that is right there in the file.
func engineIsWorkflowErr(w, sel string) error {
	return fmt.Errorf("config: %s: `use: %s` selects a code ENGINE, but %q is a workflow — write `call: %s` (a step-level `use:` used to mean the workflow call; `conductor config migrate` rewrites it)", w, sel, sel, sel)
}

// engineUnknownErr is the message for a step `use:` that names no engine
// conductor can run — a reference that is not a builtin, not an interpreter,
// not a path, and does not parse as a plugin reference either.
func engineUnknownErr(w, sel string, c *Config) error {
	if c != nil {
		if _, ok := c.Workflows[sel]; ok {
			return engineIsWorkflowErr(w, sel)
		}
	}
	base := fmt.Errorf("config: %s: `use: %s` names no engine conductor can run — the builtins are %s; or name a host interpreter (bash, node, python3, …) or a path to one, or an engine PLUGIN (a bare name, owner/repo/engine, or ./path/to/binary). To CALL a workflow, use `call:`", w, sel, strings.Join(BuiltinNames(UseKindEngine), ", "))
	if _, err := ParseUse(UseKindEngine, sel); err != nil {
		return fmt.Errorf("%w (%v)", base, err)
	}
	return base
}

// validateStepEngine checks a code step's engine selection and the body it
// needs. A step that selects no engine is not this function's business.
func validateStepEngine(w string, s Step, c *Config) error {
	if s.Use != "" && s.Run != "" {
		return fmt.Errorf("config: %s: set `use: %s` or `run: %s`, not both — they select the same engine (`use:` is the current spelling)", w, s.Use, s.Run)
	}
	sel, class := s.StepEngine()
	if class == EngineNone {
		return nil
	}
	if class == EnginePlugin {
		// A plugin-backed engine: an out-of-process binary conductor fetches
		// and drives over the plugin protocol (pkg/plugin's plugin.run). It
		// is a legitimate reference now, so the only things left to check are
		// that it is not a workflow the author meant to `call:`, and that it
		// parses as a reference at all.
		if c != nil {
			if _, ok := c.Workflows[sel]; ok {
				return engineIsWorkflowErr(w, sel)
			}
		}
		if _, err := ParseUse(UseKindEngine, sel); err != nil {
			return engineUnknownErr(w, sel, c)
		}
		if len(s.Command) > 0 {
			return fmt.Errorf("config: %s: `command:` is the `cli` engine's argv, but this step selects the plugin engine `%s` — write `use: cli` to run a command, or drop `command:`", w, sel)
		}
		// NO `code:` requirement, unlike the builtin engines. An engine plugin
		// declares its own contract: one may take the step's `code:` as a
		// script, another may be entirely driven by `inputs:` and `args:`, and
		// conductor cannot tell which from here. The engine says so itself —
		// on the wire, at run time — rather than the loader guessing.
		return nil
	}
	// An in-process engine shares the daemon's process, so there is nothing
	// meaningful to ship to another box.
	if class == EngineInProcess && (s.Host != "" || s.SSH != nil) {
		return fmt.Errorf("config: %s: `use: %s` executes inside conductor's own process and is local-only — use a host interpreter (e.g. `use: node`/`use: sh`) for remote code", w, sel)
	}
	if class == EngineCLI {
		if len(s.Command) == 0 {
			return fmt.Errorf("config: %s: `use: cli` needs `command:` — the argv it runs (add `code:` to hand it a script as well)", w)
		}
		return nil
	}
	// Every other engine takes its work as `code:`, and has no argv to put a
	// `command:` on — a step carrying both has almost certainly reached for
	// the wrong engine.
	if len(s.Command) > 0 {
		return fmt.Errorf("config: %s: `command:` is the `cli` engine's argv, but this step selects `%s` — write `use: cli` to run a command, or drop `command:`", w, sel)
	}
	if strings.TrimSpace(s.Code) == "" {
		return fmt.Errorf("config: %s: `%s: %s` needs `code:`", w, engineKey(s), sel)
	}
	return nil
}

// engineKey names the key the step actually wrote, so an error quotes the
// config back rather than the spelling the message author preferred.
func engineKey(s Step) string {
	if s.Use != "" {
		return "use"
	}
	return "run"
}

// Argv is a command line: the `command:` on a step, and what the `cli`
// engine executes.
//
//	command: [git, log, --oneline, -5]    exact argv, one word per entry
//	command: "git log --oneline -5"       split on whitespace, quotes honored
//
// The list form is canonical and is what anything generated (a migration, a
// pack instantiation) emits. The string form exists because a one-liner
// reads better as one, and it is split HERE rather than handed to a shell:
// there is no shell in the cli engine's execution path, so `command: "rm -rf
// $HOME"` cannot expand a variable, glob, or chain a `;` — it is three
// literal words. Write `command: [sh, -c, "…"]` when a shell is what you
// actually want.
type Argv []string

// UnmarshalYAML accepts the list form and the string form.
func (a *Argv) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var s string
		if err := n.Decode(&s); err != nil {
			return fmt.Errorf("command: must be a string or a list of words: %w", err)
		}
		words, err := splitArgv(s)
		if err != nil {
			return fmt.Errorf("command: %w", err)
		}
		*a = words
		return nil
	}
	var list []string
	if err := n.Decode(&list); err != nil {
		return fmt.Errorf("command: must be a string or a list of words: %w", err)
	}
	*a = list
	return nil
}

// splitArgv splits a command string into words on unquoted whitespace,
// honoring single and double quotes (which group and are removed) and a
// backslash (which escapes the next byte). It performs NO expansion — see
// Argv: this is word-splitting so the string form is usable, not a shell.
func splitArgv(s string) ([]string, error) {
	var (
		out   []string
		cur   strings.Builder
		word  bool // a word is open (so "" can be a deliberate empty arg)
		quote byte // 0, '\'' or '"'
	)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && quote != '\'' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			word = true
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
			cur.WriteByte(c)
		case c == '\'' || c == '"':
			quote, word = c, true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if word {
				out = append(out, cur.String())
				cur.Reset()
				word = false
			}
		default:
			cur.WriteByte(c)
			word = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c quote in %q", quote, s)
	}
	if word {
		out = append(out, cur.String())
	}
	return out, nil
}
