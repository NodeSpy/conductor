package agentsandbox

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// Per-identity tool state. A sandboxed agent's HOME is a directory of its
// identity's own, <root>/<key>/: its CLI's settings, sessions and history
// live there, so a resumed session finds its memory and no identity sees
// another's transcripts. It is created — and seeded once from the operator's
// settings for the tool, never their history — before the launch, because a
// bind source that does not exist would leave the agent writing into an
// ephemeral mount whose contents are lost when it exits. Only the tool's
// login files are shared from the operator's real home.

// HomeSpec describes one identity's home.
type HomeSpec struct {
	Root     string // <state>/agent-state
	Key      string // the identity (IdentityKey cleans it)
	Tool     string // canonical tool (CanonicalTool)
	RealHome string // the operator's home: seed and login source
	// GitName / GitEmail are the identity the agent's commits carry (the
	// operator's own); written into the home's .gitconfig with signing off —
	// conductor signs on the way out (internal/gitpush).
	GitName  string
	GitEmail string
	// GOOS picks how login files are shared: a bind over a placeholder
	// (linux), or a symlink the Seatbelt profile allows (darwin).
	GOOS string
}

var keyClean = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// IdentityKey turns an identity name into a directory name: safe characters
// only, never "." or "..", and — when cleaning lost information or the name
// is long — a hash suffix so two identities never share a home.
func IdentityKey(name string) string {
	k := strings.Trim(keyClean.ReplaceAllString(name, "_"), "._-")
	if k != name || len(k) > 64 || k == "" {
		sum := sha256.Sum256([]byte(name))
		if len(k) > 48 {
			k = k[:48]
		}
		if k == "" {
			k = "id"
		}
		k += "-" + hex.EncodeToString(sum[:])[:12]
	}
	return k
}

// Login is one login file shared into an identity home: Path in the home,
// Src in the operator's real home.
type Login struct {
	Path string `json:"path"`
	Src  string `json:"src"`
}

// PrepareHome creates (and on first use seeds) the identity's home and
// returns it with the login files to share into it.
func PrepareHome(s HomeSpec) (string, []Login, error) {
	if s.Root == "" {
		return "", nil, fmt.Errorf("agentsandbox: no tool-state root")
	}
	home := filepath.Join(s.Root, IdentityKey(s.Key))
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", nil, fmt.Errorf("agentsandbox: identity home: %w", err)
	}
	t := tools[s.Tool]
	marker := filepath.Join(home, ".conductor-seeded-"+s.Tool)
	if s.Tool != "" && s.RealHome != "" {
		if _, err := os.Stat(marker); err != nil {
			for _, rel := range t.seed {
				if err := seedFile(filepath.Join(s.RealHome, rel), filepath.Join(home, rel), seedFilter[rel]); err != nil {
					return "", nil, fmt.Errorf("agentsandbox: seed %s: %w", rel, err)
				}
			}
			if err := os.WriteFile(marker, nil, 0o600); err != nil {
				return "", nil, err
			}
		}
	}
	// Every state path exists, so the CLI writes into the identity home,
	// never a missing one: a file (~/.claude.json) as an empty one — `{}`
	// for JSON, which a CLI reads as corrupt when it has no bytes at all
	// (Claude Code exits) — a dir as a dir.
	for _, rel := range t.state {
		dst := filepath.Join(home, rel)
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if stateIsFile(rel) {
			var empty []byte
			if strings.HasSuffix(rel, ".json") {
				empty = []byte("{}\n")
			}
			if err := writeIfAbsent(dst, empty); err != nil {
				return "", nil, err
			}
			continue
		}
		if err := os.MkdirAll(dst, 0o700); err != nil {
			return "", nil, err
		}
	}
	var logins []Login
	if s.RealHome != "" {
		for _, rel := range t.login {
			src := filepath.Join(s.RealHome, rel)
			if fi, err := os.Stat(src); err != nil || !fi.Mode().IsRegular() {
				continue
			}
			dst := filepath.Join(home, rel)
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				return "", nil, err
			}
			if s.GOOS == "darwin" {
				// Seatbelt cannot mount one path over another: the identity
				// home points at the operator's file, which the profile
				// allows by its own path.
				if cur, err := os.Readlink(dst); err != nil || cur != src {
					_ = os.Remove(dst)
					if err := os.Symlink(src, dst); err != nil {
						return "", nil, err
					}
				}
			} else {
				if fi, err := os.Lstat(dst); err == nil && !fi.Mode().IsRegular() {
					// The placeholder a bind sits on must be a plain file.
					_ = os.RemoveAll(dst)
				}
				if err := writeIfAbsent(dst, nil); err != nil {
					return "", nil, err
				}
			}
			logins = append(logins, Login{Path: dst, Src: src})
		}
	}
	if err := writeGitconfig(home, s.GitName, s.GitEmail); err != nil {
		return "", nil, err
	}
	return home, logins, nil
}

// stateIsFile reports a tool-state path that is a file (~/.claude.json)
// rather than a directory (~/.claude, ~/.codex): a name with an extension
// past its leading dot.
func stateIsFile(rel string) bool {
	return strings.Contains(strings.TrimPrefix(filepath.Base(rel), "."), ".")
}

// writeGitconfig gives the agent's own git the operator's identity and no
// signing: the agent holds no key, and conductor signs what it pushes. It is
// rewritten every launch (the agent may edit it; conductor never reads it).
func writeGitconfig(home, name, email string) error {
	var b strings.Builder
	b.WriteString("# Written by conductor for a sandboxed agent. conductor pushes and signs.\n[user]\n")
	if name != "" {
		fmt.Fprintf(&b, "\tname = %s\n", gitQuote(name))
	}
	if email != "" {
		fmt.Fprintf(&b, "\temail = %s\n", gitQuote(email))
	}
	b.WriteString("[commit]\n\tgpgsign = false\n[tag]\n\tgpgsign = false\n[gc]\n\tauto = 0\n[maintenance]\n\tauto = false\n")
	p := filepath.Join(home, ".gitconfig")
	_ = os.Remove(p) // never write through a symlink the agent left here
	return os.WriteFile(p, []byte(b.String()), 0o600)
}

// gitQuote quotes a git config value.
func gitQuote(s string) string {
	s = strings.NewReplacer("\n", " ", "\r", " ").Replace(s)
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func writeIfAbsent(p string, b []byte) error {
	if _, err := os.Lstat(p); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

var seedFilter = map[string]func([]byte) ([]byte, error){
	// ~/.claude.json carries the account and onboarding state the CLI needs
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

// trustClaudeWorkspace records in a claude-code identity home that dir is
// trusted, as the operator's own ~/.claude.json does for their projects
// (the seed drops "projects": another identity's, as far as this one is
// concerned). A workspace conductor created for this identity's dispatch is
// trusted by construction; without it, claude-code's interactive UI (an
// agent-deck session) stops at its folder-trust question, which no one is
// there to answer. The agent may have replaced the file: it is read without
// following a symlink and replaced by rename, never written through.
func trustClaudeWorkspace(home, dir string) error {
	p := filepath.Join(home, ".claude.json")
	m := map[string]json.RawMessage{}
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	switch {
	case err == nil:
		b, rerr := io.ReadAll(io.LimitReader(f, 16<<20))
		f.Close()
		if rerr != nil {
			return rerr
		}
		if len(bytes.TrimSpace(b)) > 0 && json.Unmarshal(b, &m) != nil {
			return nil // not a state file claude-code could read either
		}
	case os.IsNotExist(err):
	default:
		return nil // a symlink, or not a file: not conductor's to rewrite
	}
	projects := map[string]map[string]any{}
	if raw, ok := m["projects"]; ok && json.Unmarshal(raw, &projects) != nil {
		return nil
	}
	e := projects[dir]
	if e == nil {
		e = map[string]any{}
	}
	if v, _ := e["hasTrustDialogAccepted"].(bool); v {
		return nil
	}
	e["hasTrustDialogAccepted"] = true
	projects[dir] = e
	raw, err := json.Marshal(projects)
	if err != nil {
		return err
	}
	m["projects"] = raw
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(home, ".claude.json.conductor-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), p)
}
