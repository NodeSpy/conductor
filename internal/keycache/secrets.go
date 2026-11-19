package keycache

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CREDENTIALS IN WHAT A RUN FILLED
//
// A learned path is a directory a tool fills with bulk cache, and many
// tools keep a token or key in the same directory (~/.cache/huggingface/
// token beside the model blobs). The protected list (paths.go) names the
// known ones, but a list is always missing entries, so a publish also
// checks what it is about to commit, from what conductor itself knows:
//
//   - the credential VALUES conductor gave the run (and the other launches
//     it started recently: a kept home holds their files too). A new file
//     — one this run wrote, not one hard-linked from the version it
//     restored, which was checked when it was published — holding any of
//     them is left out. Conductor knows these values; nothing is guessed.
//     Read in bounded chunks: memory does not grow with the file.
//   - a tree holding a git repository (a `.git` anywhere in it: a clone's
//     .git/config keeps the token it was cloned with, its hooks run code)
//     is not published at all.
//   - a file whose name says credential (id_*, *.pem, *.key, credentials*,
//     *token*, adbkey*) and whose content looks like one: a private key
//     block, a known token shape, JSON naming a token or secret, or a
//     single bare token. The content check keeps package trees whole:
//     syn's token.rs, x/oauth2's token.go and certifi's cacert.pem are
//     source and certificates, and a cache missing them breaks the build
//     it was meant to speed up.
//
// Each file or path left out is logged by path, never by value.

// secretMin is the shortest value scanned for: shorter ones match by
// chance.
const secretMin = 8

// secretKeep is how long a launch's credential values are scanned for in
// what later publishes fill (a kept home's files outlive their launch).
const secretKeep = 24 * time.Hour

// launchSecretsFile marks a launch that was given credentials: its count,
// never the values. A publish of a launch so marked whose values this
// process does not hold (it restarted) is refused.
const launchSecretsFile = "secrets"

type secretSet struct {
	at   time.Time
	vals [][]byte
}

// AddSecrets records the credential values a launch (its cache lock) was
// given, for its publish's scan. Values shorter than secretMin are
// ignored. The values stay in this process's memory only.
func (m *Manager) AddSecrets(lock string, vals []string) {
	launch := filepath.Dir(lock)
	if lock == "" || filepath.Base(lock) != launchLockFile || !within(launch, m.Store.layersRoot()) {
		return
	}
	var set [][]byte
	seen := map[string]bool{}
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if len(v) < secretMin || seen[v] {
			continue
		}
		seen[v] = true
		set = append(set, []byte(v))
	}
	if len(set) == 0 {
		return
	}
	m.mu.Lock()
	if m.secrets == nil {
		m.secrets = map[string]secretSet{}
	}
	for d, s := range m.secrets {
		if time.Since(s.at) > secretKeep {
			delete(m.secrets, d)
		}
	}
	cur := m.secrets[launch]
	cur.at = time.Now()
	cur.vals = append(cur.vals, set...)
	m.secrets[launch] = cur
	n := len(cur.vals)
	m.mu.Unlock()
	_ = writeAtomic(filepath.Join(launch, launchSecretsFile), []byte(strconv.Itoa(n)))
}

// secretsFor is what a publish of launch scans for: every value recorded
// recently, and whether the launch's own are known (false: it was given
// some, and this process no longer holds them).
func (m *Manager) secretsFor(launch string) ([][]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, known := m.secrets[launch]
	if !known {
		if _, err := os.Lstat(filepath.Join(launch, launchSecretsFile)); err != nil {
			known = true // given none
		}
	}
	var out [][]byte
	seen := map[string]bool{}
	for _, s := range m.secrets {
		for _, v := range s.vals {
			if !seen[string(v)] {
				seen[string(v)] = true
				out = append(out, v)
			}
		}
	}
	return out, known
}

// handSecrets moves launch from's recorded values (and its mark) to to:
// a resume takes over the launch it continues.
func (m *Manager) handSecrets(from, to string) {
	m.mu.Lock()
	s, ok := m.secrets[from]
	if ok {
		delete(m.secrets, from)
		cur := m.secrets[to]
		cur.at = time.Now()
		cur.vals = append(cur.vals, s.vals...)
		m.secrets[to] = cur
	}
	m.mu.Unlock()
	if b, err := os.ReadFile(filepath.Join(from, launchSecretsFile)); err == nil {
		_ = writeAtomic(filepath.Join(to, launchSecretsFile), b)
	}
}

// screen checks what a publish built at data/<p> for each path: a path
// holding a git repository is removed whole, and a file that holds a
// credential is removed. It returns the paths still there.
func (m *Manager) screen(key Key, data string, paths []string, secrets [][]byte) []string {
	var kept []string
	for _, p := range paths {
		root := filepath.Join(data, filepath.FromSlash(p))
		if g := findGit(root); g != "" {
			_ = RemoveAll(root)
			m.logf("cache %s: ~/%s holds a git repository (~/%s): not published", key, p, filepath.ToSlash(filepath.Join(p, g)))
			continue
		}
		_ = filepath.WalkDir(root, func(f string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return nil
			}
			rel, _ := filepath.Rel(data, f)
			why := ""
			if suspectName(d.Name()) && looksLikeCredential(f) {
				why = "looks like a credential"
			} else if len(secrets) > 0 {
				if fi, err := d.Info(); err == nil {
					// A file hard-linked from the restored version was
					// checked when that version was published.
					if _, links, ok := idOf(fi); !ok || links == 1 {
						if holdsAny(f, secrets) {
							why = "holds a credential conductor gave a run"
						}
					}
				}
			}
			if why != "" {
				if os.Remove(f) == nil {
					m.logf("cache %s: ~/%s %s: left out", key, filepath.ToSlash(rel), why)
				}
			}
			return nil
		})
		kept = append(kept, p)
	}
	return kept
}

// findGit is the first `.git` under root (root-relative), "" when none.
func findGit(root string) string {
	found := ""
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Name() == ".git" && p != root {
			found, _ = filepath.Rel(root, p)
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// holdsAny reports whether the file at p holds any of vals, read in
// bounded chunks (each overlapping the last by the longest value, so a
// value across a chunk boundary is found).
func holdsAny(p string, vals [][]byte) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	longest := 0
	for _, v := range vals {
		longest = max(longest, len(v))
	}
	const chunk = 1 << 20
	buf := make([]byte, chunk+longest)
	keep := 0
	for {
		n, err := io.ReadFull(f, buf[keep:keep+chunk])
		win := buf[:keep+n]
		for _, v := range vals {
			if bytes.Contains(win, v) {
				return true
			}
		}
		if err != nil {
			return false
		}
		keep = min(longest-1, len(win))
		copy(buf, win[len(win)-keep:])
	}
}

var suspectNameRE = regexp.MustCompile(`(?i)^(id_.*|.*\.pem|.*\.key|credentials.*|.*token.*|adbkey.*)$`)

func suspectName(name string) bool { return suspectNameRE.MatchString(name) }

// tokenRE are credential shapes forges and clouds issue.
var tokenRE = regexp.MustCompile(`(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{20,}|hf_[A-Za-z0-9]{20,}|xox[abposr]-[A-Za-z0-9-]{10,}|sk-[A-Za-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|pul-[0-9a-f]{20,}|npm_[A-Za-z0-9]{20,}|AIza[0-9A-Za-z_-]{30,})`)

// bareTokenRE is a file that is one token and nothing else.
var bareTokenRE = regexp.MustCompile(`^[A-Za-z0-9._~+/=:-]+$`)

// credKeyRE are JSON keys that name a credential.
var credKeyRE = regexp.MustCompile(`(?i)^(access_?tokens?|refresh_?tokens?|id_?token|auth_?token|tokens?|secret|client_?secret|password|passwd|api_?key|private_?key|credentials?)$`)

// looksLikeCredential reads the head of a file whose name says credential
// and reports whether its content does too.
func looksLikeCredential(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 64<<10))
	if bytes.Contains(b, []byte("PRIVATE KEY")) || tokenRE.Match(b) {
		return true
	}
	if t := bytes.TrimSpace(b); len(t) >= 16 && len(t) <= 512 && bareTokenRE.Match(t) {
		return true // one bare token
	}
	var v any
	if json.Unmarshal(b, &v) == nil && credKeys(v, 0) {
		return true
	}
	return false
}

func credKeys(v any, depth int) bool {
	if depth > 8 {
		return false
	}
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if credKeyRE.MatchString(k) {
				return true
			}
			if credKeys(x, depth+1) {
				return true
			}
		}
	case []any:
		for _, x := range t {
			if credKeys(x, depth+1) {
				return true
			}
		}
	}
	return false
}
