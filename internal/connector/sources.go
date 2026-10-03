// Generic lowering shared by every plugin/contract source (pluginsource.go):
// turning a compiled trigger into the legacy config.Action identity fields
// the engine still runs on, and the engine-interpreted trigger options onto
// it.
package connector

import (
	"github.com/NodeSpy/conductor/internal/config"
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
