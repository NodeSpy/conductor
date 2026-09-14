package github

// PatternSpecificity exposes the repo-glob specificity scoring for the config
// migration (it must replicate resolve()'s most-specific-wins outcome as
// per-trigger exclusions).
func PatternSpecificity(p string) int { return patternSpecificity(p) }

// KnownKinds exposes the set of github event kinds for the migration.
func KnownKinds() map[string]bool {
	out := make(map[string]bool, len(knownKinds))
	for k, v := range knownKinds {
		out[k] = v
	}
	return out
}
