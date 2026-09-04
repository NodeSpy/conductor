package github

import "context"

// AppTokens mints GitHub App installation tokens outside an Integration
// instance — the connectors model's action face needs one for `as: bot`
// verb calls (and for read fallbacks) without owning the webhook source.
// It wraps the same appAuth the integration uses, so caching, the JWT flow,
// and the PC_GITHUB_API_BASE test override behave identically.
type AppTokens struct {
	auth *appAuth
}

// NewAppTokens builds a minter from GitHub App credentials.
func NewAppTokens(appID int64, privateKeyPath string) (*AppTokens, error) {
	a, err := newAppAuth(appID, privateKeyPath)
	if err != nil {
		return nil, err
	}
	return &AppTokens{auth: a}, nil
}

// TokenForRepo returns a (cached) installation access token for the
// installation covering owner/repo.
func (t *AppTokens) TokenForRepo(ctx context.Context, owner, repo string) (string, error) {
	id, err := t.auth.repoInstallationID(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	return t.auth.installationToken(ctx, id)
}

// APIBaseURL exposes the GitHub API base (honoring the PC_GITHUB_API_BASE
// test override) for the connector verb implementations.
func APIBaseURL() string { return apiBaseURL() }
