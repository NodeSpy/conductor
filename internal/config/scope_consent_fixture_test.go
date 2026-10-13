package config

// The consent rule reads declarations through ScopeConsent, which the
// connector registry sets in production. This package's tests link no
// registry, so they declare what the github connector does: a repo scope,
// with consent.
func init() {
	ScopeConsent = func(typeName string) (string, bool) {
		if typeName == "github" {
			return "repo", true
		}
		return "", false
	}
}
