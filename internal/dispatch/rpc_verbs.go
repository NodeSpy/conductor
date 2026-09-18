package dispatch

import "github.com/NodeSpy/conductor/internal/plugin"

// RequiredVerbs is the plugin verb-name set a runtime plugin must declare to be
// driven as a Backend-RPC runtime via rpcBackend. It mirrors the verb→method
// mapping in rpc_backend.go and equals Backend's method set MINUS AgentLog:
// `logs` is a soft-path-only feature both backends already tolerate a plugin
// not having, so it must not gate classification.
//
// This verb set is what distinguishes a Backend-RPC "verb" runtime plugin (e.g.
// the conductor-paseo plugin, which speaks plugin.invoke) from an ACP runtime
// plugin: Decl.Kind is "runtime" for BOTH, so kind cannot discriminate — the
// declared verbs are the only unambiguous signal, and they are already required
// of every plugin at install time (Describe/checkDeclKind).
var RequiredVerbs = []string{
	"run", "list_agents", "inspect", "archive_agent", "archive_workspace",
	"create_worktree", "create_workspace", "list_workspaces", "clone", "send", "wait",
}

// SpeaksBackendRPC reports whether decl declares every verb in RequiredVerbs —
// i.e. the plugin can be driven as a dispatch.Backend. A runtime plugin missing
// any required verb is treated as a non-Backend-RPC (ACP-dialect) runtime and
// left to the ACP controller path. nil decl → false.
func SpeaksBackendRPC(decl *plugin.Decl) bool {
	if decl == nil {
		return false
	}
	have := make(map[string]bool, len(decl.Verbs))
	for _, v := range decl.Verbs {
		have[v.Name] = true
	}
	for _, want := range RequiredVerbs {
		if !have[want] {
			return false
		}
	}
	return true
}
