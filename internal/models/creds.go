package models

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Credential handling for the LIVE-API discovery path.
//
// The rule, and it is absolute: a credential file is read ONLY to mint an
// Authorization header for one request. The token value is never logged, never
// echoed, never written to the catalog cache, and never placed in an error
// string. redactToken exists so a diagnostic can still say WHICH credential
// was used without saying what it is.
//
// The live API is an OPTIONAL entitlement filter (§3.3): where credentials
// allow, it narrows the public catalog to what the account can actually run.
// It is never required — every path here degrades to the catalog.

// homeDir is the home-directory lookup, a seam so tests never touch a real
// credential file.
var homeDir = os.UserHomeDir

// claudeCredsPath is where the claude CLI stores its OAuth credentials.
func claudeCredsPath() (string, error) {
	h, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".claude", ".credentials.json"), nil
}

// claudeOAuthToken reads the claude CLI's stored OAuth access token. The
// returned string is a SECRET: pass it straight into a header and drop it.
func claudeOAuthToken() (string, error) {
	path, err := claudeCredsPath()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		// Deliberately reports the path, never the contents.
		return "", fmt.Errorf("read claude credentials: %w", err)
	}
	var doc struct {
		ClaudeAIOAuth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("parse %s: malformed credentials file", path)
	}
	tok := strings.TrimSpace(doc.ClaudeAIOAuth.AccessToken)
	if tok == "" {
		return "", fmt.Errorf("%s carries no claudeAiOauth.accessToken", path)
	}
	return tok, nil
}

// redactToken renders a credential for a diagnostic: enough to tell two
// credentials apart, never enough to use one. Short values redact entirely.
func redactToken(tok string) string {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return "(none)"
	}
	if len(tok) <= 12 {
		return "(redacted)"
	}
	return tok[:6] + "…(redacted)"
}
