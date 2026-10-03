// Generic lowering shared by every plugin/contract source (pluginsource.go):
// turning a compiled trigger into the legacy config.Action identity fields
// the engine still runs on, and the engine-interpreted trigger options onto
// it.
package connector

import (
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// lowerAction builds the trigger-identity fields every connectors-model
// source lowering carries on the legacy config.Action it emits into an
// integration — nothing else. The rest of an Action's fields are legacy
// step/dispatch config that the connectors model doesn't use; only the
// identity, enable, and shadow markers (plus the FlowRef back-pointer to the
// compiled trigger) travel through.
func lowerAction(t CompiledTrigger) config.Action {
	return config.Action{
		Name:    t.Spec.Name,
		Enabled: t.Spec.Enabled,
		Shadow:  t.Spec.Shadow,
		FlowRef: t.Ref(),
	}
}

// lowerEngineOptions lowers the trigger options the ENGINE interprets onto the
// action it runs — the same for every source (plugin-contract.md §2.5): the
// per-revision attempt threshold, and the option the event's declared
// remediation names ({enabled, max}).
func lowerEngineOptions(act *config.Action, o map[string]any, sem *sdk.EventSemantics) {
	for _, k := range []string{"max_attempts_per_revision", "max_attempts_per_head"} {
		if n := toInt(o[k]); n > 0 {
			act.MaxAttemptsPerHead = n
			break
		}
	}
	if sem != nil && sem.Remediate != nil {
		if m, ok := o[sem.Remediate.Option].(map[string]any); ok {
			act.FlakyRerun = config.FlakyRerun{Enabled: truthy(m["enabled"]), Max: toInt(m["max"])}
		}
	}
}

// OptionHooks lowers a trigger's own `options.<option>` into flow hooks, per
// the event's declared `option_hooks` (plugin-contract.md §2.2): for each
// declared {option, at, verb}, when the trigger sets options.<option>, the
// host fires verb at that run phase exactly as an operator-authored `hooks:`
// entry would. The option's own value (an operator-authored block, e.g. a
// reaction/message description) becomes the hook's options; the semantic's
// own Args (verb option name → template over the event's facts) are merged
// in first, so the verb also knows what the event itself was about — the
// generic replacement for a connector's own dispatch-time/completion
// feedback (plugin-contract.md §3.8 V8). Returns nil when the event declares
// none, or the trigger sets none of the options they name.
func OptionHooks(t core.Trigger, opts map[string]any) []config.Hook {
	sem := t.Semantics()
	if sem == nil || len(sem.OptionHooks) == 0 || len(opts) == 0 {
		return nil
	}
	facts := t.Facts()
	var hooks []config.Hook
	for _, oh := range sem.OptionHooks {
		raw, ok := opts[oh.Option]
		if !ok {
			continue
		}
		merged := core.DeclaredArgs(oh.Args, facts)
		if m, ok := raw.(map[string]any); ok {
			for k, v := range m {
				merged[k] = v
			}
		}
		hooks = append(hooks, config.Hook{At: oh.At, Uses: t.Instance + "." + oh.Verb, Options: merged})
	}
	return hooks
}
