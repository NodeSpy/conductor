package migrate

import (
	"fmt"
	"os"
	"strings"
)

// Round 3 §MED: the identity-scope change has no upgrade path.
//
// A step's identity is the key its sessions, memory scope and outcome track
// record are all filed under. Two fixes changed how it is computed:
//
//	a `workflow:` call now runs the called workflow's steps under THAT
//	workflow's scope rather than the caller's, and
//	a compensate step is namespaced apart from the step it undoes (they used
//	to collide on one identity).
//
// Both are correct — the old identities were wrong, and the compensate case
// was two different steps sharing one record. But an operator upgrading into
// them sees affected steps start from a blank track record and unbound
// sessions, with nothing to explain why. There is no safe automatic remap:
// conductor cannot tell which of the two colliding compensate records belonged
// to which step, and inventing an answer would corrupt the history it was
// meant to preserve.
//
// So it is announced instead, and only to the configs that actually contain
// the affected constructs.
const (
	identityNoticeWorkflowCalls = "steps that call another workflow (`workflow:`)"
	identityNoticeCompensate    = "compensate steps (`compensate:`)"
)

// identityScopeNotices returns the upgrade notices for one config's contents,
// or nil when it contains neither construct.
func identityScopeNotices(files []string) []string {
	seen := map[string]bool{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		body := string(raw)
		for marker, what := range map[string]string{
			"workflow:":   identityNoticeWorkflowCalls,
			"compensate:": identityNoticeCompensate,
		} {
			if !seen[what] && containsKey(body, marker) {
				seen[what] = true
			}
		}
	}
	// Stable order regardless of map iteration.
	ordered := make([]string, 0, 2)
	for _, what := range []string{identityNoticeWorkflowCalls, identityNoticeCompensate} {
		if seen[what] {
			ordered = append(ordered, what)
		}
	}
	if len(ordered) == 0 {
		return nil
	}
	return []string{fmt.Sprintf(
		"NOTICE: step identity changed for %s. Identity keys a step's sessions, memory scope "+
			"and outcome track record, so those steps start fresh: existing session affinity will "+
			"not re-bind and their recorded outcomes will not be found. Nothing is lost — the old "+
			"records remain on disk under their old keys — and no action is required. There is no "+
			"automatic remap because the previous compensate identity was SHARED with the step it "+
			"undoes, so conductor cannot tell which record belonged to which step.",
		strings.Join(ordered, " and "))}
}

// containsKey reports whether a YAML body uses a key, ignoring occurrences
// inside a comment or as part of a longer key.
func containsKey(body, key string) bool {
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, key) || strings.HasPrefix(t, "- "+key) {
			return true
		}
	}
	return false
}
