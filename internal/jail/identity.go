package jail

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// Per-identity tool state. A jail sees its agent CLI's own state dir
// (toolState: ~/.claude, ~/.codex, …) read-write — the agent's settings, its
// session transcripts and history. Shared across jails, one agent can read
// every other jail's transcripts. A launch with a StateKey (the paseo
// provider keys it by the paseo agent) gets that state from a directory of
// its own instead, <IdentityStateDir>/<key>/, seeded once from the
// operator's settings (never their history), with only the tool's LOGIN
// files (toolLogin) shared from the real home — so the agent is signed in
// as the operator and a token refresh is the operator's, but nothing another
// identity wrote is visible.

// toolLogin are the home-relative files holding a tool's sign-in, shared
// into a per-identity state from the real home. A tool missing here signs in
// from its environment, or not at all.
var toolLogin = map[string][]string{
	"claude-code": {".claude/.credentials.json"},
	"claude":      {".claude/.credentials.json"},
	"codex":       {".codex/auth.json"},
	"gemini":      {".gemini/oauth_creds.json", ".gemini/google_accounts.json"},
	"qwen":        {".qwen/oauth_creds.json"},
}

// toolSeed are home-relative files copied once into a fresh per-identity
// state: the operator's settings for the tool. seedFilter, when set for a
// path, rewrites the copy (drops what is history rather than settings).
var toolSeed = map[string][]string{
	"claude-code": {".claude.json", ".claude/settings.json"},
	"claude":      {".claude.json", ".claude/settings.json"},
	"codex":       {".codex/config.toml"},
	"gemini":      {".gemini/settings.json"},
	"qwen":        {".qwen/settings.json"},
}

var seedFilter = map[string]func([]byte) ([]byte, error){
	// ~/.claude.json carries the account and onboarding state the CLI needs,
	// and, under "projects", every project's history and settings — another
	// identity's, as far as this one is concerned.
	".claude.json": func(b []byte) ([]byte, error) {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, err
		}
		delete(m, "projects")
		return json.Marshal(m)
	},
}

var stateKeyClean = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// identityRoot is the per-identity state root for d ("" = the shared,
// real tool state).
func (m *Manager) identityRoot(d *Dispatch) string {
	if d.StateKey == "" || m.IdentityStateDir == "" {
		return ""
	}
	k := stateKeyClean.ReplaceAllString(d.StateKey, "_")
	if k == "" || k == "." || k == ".." {
		return ""
	}
	return filepath.Join(m.IdentityStateDir, k)
}

// prepareIdentity creates (and on first use seeds) d's per-identity state
// for its tool. It returns the root, or "" for a launch on shared state.
func (m *Manager) prepareIdentity(d *Dispatch) (string, error) {
	root := m.identityRoot(d)
	if root == "" {
		return "", nil
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("identity state: %w", err)
	}
	marker := filepath.Join(root, ".conductor-seeded-"+d.Tool)
	if _, err := os.Stat(marker); err != nil {
		for _, rel := range toolSeed[d.Tool] {
			if err := seedFile(filepath.Join(m.Home, rel), filepath.Join(root, rel), seedFilter[rel]); err != nil {
				return "", fmt.Errorf("identity state: seed %s: %w", rel, err)
			}
		}
		if err := os.WriteFile(marker, nil, 0o600); err != nil {
			return "", err
		}
	}
	// Every state path exists in the identity's copy, shaped like the real
	// one (a dir stays a dir), so it can be bound or linked in place.
	for _, rel := range toolState[d.Tool] {
		dst := filepath.Join(root, rel)
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		fi, err := os.Stat(filepath.Join(m.Home, rel))
		switch {
		case err == nil && !fi.IsDir():
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				return "", err
			}
			if err := os.WriteFile(dst, nil, 0o600); err != nil {
				return "", err
			}
		default:
			if err := os.MkdirAll(dst, 0o700); err != nil {
				return "", err
			}
		}
	}
	// A login file shared from the real home needs a file to sit on.
	for _, rel := range toolLogin[d.Tool] {
		if _, err := os.Stat(filepath.Join(m.Home, rel)); err != nil {
			continue
		}
		dst := filepath.Join(root, rel)
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(dst, nil, 0o600); err != nil {
			return "", err
		}
	}
	return root, nil
}

// seedFile copies src to dst (absent src: nothing), through filter.
func seedFile(src, dst string, filter func([]byte) ([]byte, error)) error {
	f, err := os.Open(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(f, 16<<20))
	if err != nil {
		return err
	}
	if filter != nil {
		if b, err = filter(b); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o600)
}
