package github

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// appAuth mints GitHub App JWTs and caches per-installation access tokens.
// Installation tokens are used for the conductor's own reads/enrichment, on the
// App's rate pool (not your personal gh budget).
type appAuth struct {
	appID   int64
	key     *rsa.PrivateKey
	httpc   *http.Client
	apiBase string // overridable in tests
	now     func() time.Time

	mu    sync.Mutex
	cache map[int64]cachedToken
}

type cachedToken struct {
	token string
	exp   time.Time
}

func newAppAuth(appID int64, keyPath string) (*appAuth, error) {
	pem, err := os.ReadFile(expandHome(keyPath))
	if err != nil {
		return nil, fmt.Errorf("read app key: %w", err)
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM(pem)
	if err != nil {
		return nil, fmt.Errorf("parse app key: %w", err)
	}
	return &appAuth{
		appID:   appID,
		key:     key,
		httpc:   &http.Client{Timeout: 20 * time.Second},
		apiBase: "https://api.github.com",
		now:     time.Now,
		cache:   map[int64]cachedToken{},
	}, nil
}

// appJWT builds a short-lived RS256 JWT identifying the App.
func (a *appAuth) appJWT() (string, error) {
	now := a.now()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": a.appID,
	})
	return tok.SignedString(a.key)
}

// repoInstallationID resolves the App installation id covering a repository
// (used by the sweep, which has no webhook payload to read it from).
func (a *appAuth) repoInstallationID(ctx context.Context, owner, repo string) (int64, error) {
	jwtStr, err := a.appJWT()
	if err != nil {
		return 0, err
	}
	url := fmt.Sprintf("%s/repos/%s/%s/installation", a.apiBase, owner, repo)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+jwtStr)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := a.httpc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return 0, fmt.Errorf("repo installation: HTTP %d", resp.StatusCode)
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.ID, nil
}

// installationToken returns a cached (or freshly minted) installation access
// token for the given installation id.
func (a *appAuth) installationToken(ctx context.Context, instID int64) (string, error) {
	a.mu.Lock()
	if c, ok := a.cache[instID]; ok && a.now().Before(c.exp.Add(-5*time.Minute)) {
		a.mu.Unlock()
		return c.token, nil
	}
	a.mu.Unlock()

	jwtStr, err := a.appJWT()
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", a.apiBase, instID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	req.Header.Set("Authorization", "Bearer "+jwtStr)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := a.httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("installation token: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	a.mu.Lock()
	a.cache[instID] = cachedToken{token: out.Token, exp: out.ExpiresAt}
	a.mu.Unlock()
	return out.Token, nil
}
