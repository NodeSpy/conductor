package keycache

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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
//     it started recently, and every launch in the same home: a kept home
//     holds their files too), raw and in the base64 forms tools store them
//     in (of the value, and of user:value as a Basic auth header, docker's
//     or npm's auth field keeps it). A new file — any file but the very
//     ones of the version it restored, which were checked when it was
//     published — holding one is left out, and so is an entry whose name
//     or symlink target holds one. Conductor knows these values; nothing
//     is guessed. Read in bounded chunks: memory does not grow with the
//     file. Extended attributes are stripped from what is published.
//   - a launch whose values this process no longer holds (it restarted,
//     or the launch resumed one from before a restart), or whose home an
//     earlier conductor process gave runs credentials in, is not
//     published: its files cannot be checked.
//   - a tree holding a git repository (a `.git` anywhere in it, or a bare
//     repository: HEAD, config and objects/; a clone's config keeps the
//     token it was cloned with, its hooks run code) is not published at
//     all — except a bare repository inside a known version-control cache
//     (Go's pkg/mod/cache/vcs, Cargo's git/db) whose config carries no
//     credential and whose hooks are only git's samples.
//   - a file whose name says credential (id_*, *.pem, *.key, credentials*,
//     *token*, adbkey*) and whose content looks like one: a private key
//     block, a known token shape, JSON naming a token or secret, or a
//     single bare token. The content check keeps package trees whole:
//     syn's token.rs, x/oauth2's token.go and certifi's cacert.pem are
//     source and certificates, and a cache missing them breaks the build
//     it was meant to speed up.
//
// Each file or path left out is logged by path, never by value.
//
// The screen catches a credential a tool wrote by accident. It does not
// stop an agent that means to smuggle one out: an encoding it has not
// heard of (gzip, a cipher, a byte-reversed copy) passes.

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

// launchSecretsUnknownFile marks a launch that continues one whose values
// this process never held (a resume after a restart): sticky, whatever
// values the launch is given since.
const launchSecretsUnknownFile = "secrets-unknown"

// homesDirName holds, per home, a mark for each conductor process (its
// epoch) that gave a launch in that home credentials: a publish from a
// home an earlier process did is refused (its values are gone).
const homesDirName = ".homes"

// secretUsers are the HTTP basic usernames forges pair a token with: the
// base64 of user:token is screened for each.
var secretUsers = []string{"x-access-token", "x-oauth-basic", "oauth2", "git", "token"}

// secretForms are v and the forms tools store it in: its base64 (standard
// and URL alphabets, padding dropped: a prefix of the padded form), and
// that of user:v for each of secretUsers.
func secretForms(v string) []string {
	out := []string{v}
	for _, s := range append([]string{v}, userPairs(v)...) {
		out = append(out, base64.RawStdEncoding.EncodeToString([]byte(s)), base64.RawURLEncoding.EncodeToString([]byte(s)))
	}
	return out
}

func userPairs(v string) []string {
	out := make([]string, 0, len(secretUsers))
	for _, u := range secretUsers {
		out = append(out, u+":"+v)
	}
	return out
}

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
	for _, raw := range vals {
		raw = strings.TrimSpace(raw)
		if len(raw) < secretMin {
			continue
		}
		for _, v := range secretForms(raw) {
			if !seen[v] {
				seen[v] = true
				set = append(set, []byte(v))
			}
		}
	}
	if len(set) == 0 {
		return
	}
	home := readSmall(filepath.Join(launch, launchHomeFile))
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
	if filepath.IsAbs(home) {
		// A kept home's files outlive the launch: its values are screened
		// for in the home's publishes while this process runs.
		if m.homeVals == nil {
			m.homeVals = map[string][][]byte{}
		}
		m.homeVals[home] = append(m.homeVals[home], set...)
	}
	m.mu.Unlock()
	_ = writeAtomic(filepath.Join(launch, launchSecretsFile), []byte(strconv.Itoa(n)))
	if filepath.IsAbs(home) {
		d := m.homeMarkDir(home)
		if os.MkdirAll(d, 0o700) == nil {
			_ = writeAtomic(filepath.Join(d, m.processEpoch()), []byte(home))
		}
	}
}

// processEpoch names this process's run of the manager: a home mark of
// another epoch is a previous process's.
func (m *Manager) processEpoch() string {
	m.epochOnce.Do(func() { m.epoch = randHex() })
	return m.epoch
}

// homeMarkDir is home's directory of process marks.
func (m *Manager) homeMarkDir(home string) string {
	h := sha256.Sum256([]byte(filepath.Clean(home)))
	return filepath.Join(m.Store.Dir, homesDirName, hex.EncodeToString(h[:16]))
}

// homeUnknown reports a home an earlier conductor process gave a launch
// credentials in: their values are gone, so what the home holds cannot be
// checked for them.
func (m *Manager) homeUnknown(home string) bool {
	ents, _ := os.ReadDir(m.homeMarkDir(home))
	for _, e := range ents {
		if e.Name() != m.processEpoch() && !strings.HasPrefix(e.Name(), ".") {
			return true
		}
	}
	return false
}

// sweepHomes forgets the marks (and the values) of homes that are gone.
func (m *Manager) sweepHomes() {
	root := filepath.Join(m.Store.Dir, homesDirName)
	dirs, _ := os.ReadDir(root)
	for _, d := range dirs {
		dir := filepath.Join(root, d.Name())
		marks, _ := os.ReadDir(dir)
		home := ""
		for _, mk := range marks {
			if home = readSmall(filepath.Join(dir, mk.Name())); home != "" {
				break
			}
		}
		if home == "" || !filepath.IsAbs(home) {
			if len(marks) == 0 {
				_ = os.Remove(dir)
			}
			continue
		}
		if _, err := os.Lstat(home); errors.Is(err, fs.ErrNotExist) {
			if RemoveAll(dir) == nil {
				m.mu.Lock()
				delete(m.homeVals, home)
				m.mu.Unlock()
			}
		}
	}
}

// secretsFor is what a publish of launch scans for: every value recorded
// recently, and whether the launch's own are known (false: it was given
// some, and this process no longer holds them).
func (m *Manager) secretsFor(launch string) ([][]byte, bool) {
	home := readSmall(filepath.Join(launch, launchHomeFile))
	m.mu.Lock()
	defer m.mu.Unlock()
	_, known := m.secrets[launch]
	if !known {
		if _, err := os.Lstat(filepath.Join(launch, launchSecretsFile)); err != nil {
			known = true // given none
		}
	}
	if _, err := os.Lstat(filepath.Join(launch, launchSecretsUnknownFile)); err == nil {
		known = false // it continues a launch whose values are gone
	}
	var out [][]byte
	seen := map[string]bool{}
	add := func(vals [][]byte) {
		for _, v := range vals {
			if !seen[string(v)] {
				seen[string(v)] = true
				out = append(out, v)
			}
		}
	}
	for _, s := range m.secrets {
		add(s.vals)
	}
	if home != "" {
		add(m.homeVals[home])
	}
	return out, known
}

// handSecrets moves launch from's recorded values (and its mark) to to:
// a resume takes over the launch it continues.
func (m *Manager) handSecrets(from, to string) {
	_, marked := os.Lstat(filepath.Join(from, launchSecretsFile))
	_, unknown := os.Lstat(filepath.Join(from, launchSecretsUnknownFile))
	m.mu.Lock()
	s, ok := m.secrets[from]
	if unknown == nil || (marked == nil && !ok) {
		// from was given values this process does not hold (it ran before
		// a restart): what it wrote cannot be checked, whatever to is given.
		_ = writeAtomic(filepath.Join(to, launchSecretsUnknownFile), []byte("1"))
	}
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
// holding a git repository is removed whole; an entry whose name or
// symlink target holds a credential, and a file whose content does, is
// removed; extended attributes are stripped. restored are the files of
// the versions the launch restored (hard-linked in, checked when they
// were published): their content is not read again. It returns the paths
// still there.
func (m *Manager) screen(key Key, data string, paths []string, secrets [][]byte, restored map[fileID]bool) []string {
	var kept []string
	for _, p := range paths {
		root := filepath.Join(data, filepath.FromSlash(p))
		if g := findGit(root); g != "" {
			_ = RemoveAll(root)
			m.logf("cache %s: ~/%s holds a git repository (~/%s): not published", key, p, filepath.ToSlash(filepath.Join(p, g)))
			continue
		}
		_ = filepath.WalkDir(root, func(f string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(data, f)
			why := ""
			switch {
			case f != root && holdsIn([]byte(d.Name()), secrets):
				why = "is named with a credential conductor gave a run"
			case d.Type()&fs.ModeSymlink != 0:
				if l, err := os.Readlink(f); err == nil && holdsIn([]byte(l), secrets) {
					why = "links to a path holding a credential conductor gave a run"
				}
			case d.Type().IsRegular():
				fi, err := d.Info()
				if err != nil {
					return nil
				}
				id, _, ok := idOf(fi)
				if ok && restored[id] {
					return nil // the restored version's own file
				}
				stripXattrs(f)
				if suspectName(d.Name()) && looksLikeCredential(f) {
					why = "looks like a credential"
				} else if len(secrets) > 0 && holdsAny(f, secrets) {
					why = "holds a credential conductor gave a run"
				}
			}
			if why == "" {
				return nil
			}
			if RemoveAll(f) == nil {
				m.logf("cache %s: ~/%s %s: left out", key, filepath.ToSlash(rel), why)
			}
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		})
		kept = append(kept, p)
	}
	return kept
}

// holdsIn reports b holding any of vals.
func holdsIn(b []byte, vals [][]byte) bool {
	for _, v := range vals {
		if bytes.Contains(b, v) {
			return true
		}
	}
	return false
}

// findGit is the first git repository under root (root-relative): a
// `.git`, or a bare repository other than an allowed version-control
// cache's (vcsCacheRepo); "" when none.
func findGit(root string) string {
	found := ""
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root || !d.IsDir() && d.Name() != ".git" {
			return nil
		}
		if d.Name() == ".git" || (bareRepo(p) && !vcsCacheRepo(root, p)) {
			found, _ = filepath.Rel(root, p)
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// bareRepo reports a directory laid out as a bare git repository: HEAD
// and config files, and an objects/, refs/ or hooks/ directory.
func bareRepo(dir string) bool {
	h, err1 := os.Lstat(filepath.Join(dir, "HEAD"))
	c, err2 := os.Lstat(filepath.Join(dir, "config"))
	if err1 != nil || err2 != nil || !h.Mode().IsRegular() || !c.Mode().IsRegular() {
		return false
	}
	for _, sub := range []string{"objects", "refs", "hooks"} {
		if fi, err := os.Lstat(filepath.Join(dir, sub)); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

// vcsCacheDirs are where tools keep bare clones of the modules they fetch.
var vcsCacheDirs = []string{"/pkg/mod/cache/vcs/", "/.cargo/git/db/", "/cargo/git/db/"}

// credConfigRE are git config lines that carry a credential: a URL with
// userinfo, an extra header, a credential helper or store.
var credConfigRE = regexp.MustCompile(`(?i)(://[^/\s]*@|extraheader|^\s*\[credential|helper\s*=|password|token)`)

// vcsCacheRepo reports a bare repository a version-control cache keeps
// (vcsCacheDirs) holding no credential in its config and no hook but
// git's samples.
func vcsCacheRepo(root, dir string) bool {
	slash := filepath.ToSlash(dir) + "/"
	in := false
	for _, v := range vcsCacheDirs {
		in = in || strings.Contains(slash, v)
	}
	if !in {
		return false
	}
	b, err := os.ReadFile(filepath.Join(dir, "config"))
	if err != nil || len(b) > 64<<10 {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if credConfigRE.MatchString(line) {
			return false
		}
	}
	hooks, _ := os.ReadDir(filepath.Join(dir, "hooks"))
	for _, h := range hooks {
		if !strings.HasSuffix(h.Name(), ".sample") {
			return false
		}
	}
	return true
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
