package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/gitpush"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// A TEACHING launch learns its repository's git fetch dependencies
// (engine/gitfetch.go). It is a launch of a step that declared
// cache: { teach: true }, in conductor's own fresh checkout of a trusted
// target's default branch head (agentsandbox.CacheBinding.Teach). Its
// credential covers any repository on the host (TeachGitAuth), the proxy
// reports each one the forge served it, and when the launch's step
// succeeds and its cache is published (TaughtGit) — and the checkout is
// still at the head it started at — that set becomes the repository's
// learned dependencies for the launch's key, replacing what the key's
// previous teaching run learned. Every other launch on the repository gets
// the repository plus those dependencies.
//
// What a dispatch's launches fetched is kept in the state dir
// (TeachStateDir) as well as in memory: a restart in the middle of a
// teaching dispatch resumes its record rather than starting a fresh one,
// whose learned set — what the launches after the restart fetched alone —
// would replace the full one.

type teaching struct {
	Dispatch string `json:"dispatch"`
	Repo     string `json:"repo"`
	Key      string `json:"key"`
	// Dir, GitDir, HeadDir locate the checkout; Head is the default
	// branch's head it started at.
	Dir     string   `json:"dir"`
	GitDir  string   `json:"git_dir,omitempty"`
	HeadDir string   `json:"head_dir,omitempty"`
	Head    string   `json:"head"`
	Fetched []string `json:"fetched,omitempty"`
}

var (
	teachMu sync.Mutex
	teachBy = map[string]*teaching{} // dispatch id → what its launches fetched

	// TeachStateDir is where teaching records are kept across a restart
	// ("" = memory only). Set at boot (SetTeachStateDir).
	TeachStateDir string
	// TeachLog logs teaching decisions (nil: silent).
	TeachLog func(format string, args ...any)
)

// teachKeep is how long a teaching record left by a dispatch that never
// ended stays in the state dir.
const teachKeep = 7 * 24 * time.Hour

// SetTeachStateDir keeps teaching records in dir, removing those a
// dispatch that never ended left there.
func SetTeachStateDir(dir string) {
	teachMu.Lock()
	TeachStateDir = dir
	teachMu.Unlock()
	if dir == "" {
		return
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > teachKeep {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func teachLog(format string, args ...any) {
	if TeachLog != nil {
		TeachLog(format, args...)
	}
}

// teachFile is a dispatch's record's path ("" when not kept on disk). The
// caller holds teachMu.
func teachFile(dispatch string) string {
	if TeachStateDir == "" {
		return ""
	}
	h := sha256.Sum256([]byte(dispatch))
	return filepath.Join(TeachStateDir, hex.EncodeToString(h[:16])+".json")
}

// loadTeaching reads a dispatch's record from the state dir. The caller
// holds teachMu.
func loadTeaching(dispatch string) *teaching {
	p := teachFile(dispatch)
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var t teaching
	if json.Unmarshal(b, &t) != nil || t.Dispatch != dispatch {
		return nil
	}
	return &t
}

// saveTeaching writes a record to the state dir. The caller holds teachMu.
func saveTeaching(t *teaching) {
	p := teachFile(t.Dispatch)
	if p == "" {
		return
	}
	b, err := json.Marshal(t)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		teachLog("git fetch: keep %s's teaching record: %v", t.Dispatch, err)
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil || os.Rename(tmp, p) != nil {
		_ = os.Remove(tmp)
		teachLog("git fetch: keep %s's teaching record: %v", t.Dispatch, err)
	}
}

func dropTeaching(dispatch string) {
	if p := teachFile(dispatch); p != "" {
		_ = os.Remove(p)
	}
}

// teachingGitAuth mints a teaching launch's credentials and records what
// they fetch under its dispatch. ok false: the launch cannot teach (no
// head recorded to check at its end, or a record of the dispatch for
// another checkout or head) and gets none.
func teachingGitAuth(e agentsandbox.Entry) ([]sandbox.GitAuth, bool) {
	if e.StartHead == "" || e.Cache == nil {
		return nil, false
	}
	teachMu.Lock()
	t := teachBy[e.Dispatch]
	if t == nil {
		t = loadTeaching(e.Dispatch)
	}
	if t == nil {
		// Where the checkout's HEAD is read at the end: the trusted git
		// dirs conductor recorded, else the layout it resolves now.
		var gitDir, headDir string
		if e.Push != nil {
			gitDir, headDir = e.Push.GitDir, e.Push.HeadDir
		} else if l, err := agentsandbox.ResolveGitLayout(e.Workspace); err == nil {
			gitDir, headDir = l.GitDir, l.HeadDir
		}
		t = &teaching{Dispatch: e.Dispatch, Repo: e.Repo, Key: e.Cache.Key.ID(), Dir: e.Workspace,
			GitDir: gitDir, HeadDir: headDir, Head: e.StartHead}
	}
	if t.Repo != e.Repo || t.Key != e.Cache.Key.ID() || t.Head != e.StartHead || t.Dir != e.Workspace {
		teachMu.Unlock()
		return nil, false
	}
	teachBy[e.Dispatch] = t
	saveTeaching(t)
	teachMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	auth := TeachGitAuth(ctx, e.Repo)
	for i := range auth {
		host := auth[i].Host
		auth[i].OnFetch = func(r string) {
			teachMu.Lock()
			t.Fetched = append(t.Fetched, host+r)
			saveTeaching(t)
			teachMu.Unlock()
		}
	}
	return auth, true
}

// TaughtGit ends a dispatch's teaching: published — its step succeeded and
// its cache was published — records what its launches fetched as the
// repository's learned dependencies for its key, provided the checkout is
// still at the default branch head it started at (work that moved it — a
// commit, a checkout of another ref — is not the default branch's, and
// teaches nothing). Otherwise it is dropped.
func TaughtGit(dispatch string, published bool) {
	teachMu.Lock()
	t := teachBy[dispatch]
	if t == nil {
		t = loadTeaching(dispatch)
	}
	delete(teachBy, dispatch)
	dropTeaching(dispatch)
	var fetched []string
	if t != nil {
		fetched = append([]string{}, t.Fetched...)
	}
	teachMu.Unlock()
	if t == nil || !published || TeachGitLearned == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	head, err := gitpush.HeadCommit(ctx, t.Dir, t.GitDir, t.HeadDir)
	cancel()
	if err != nil || !strings.EqualFold(head, t.Head) {
		teachLog("git fetch: %s: the checkout left the default branch head it started at (%s, now %s %v): nothing learned", t.Repo, short(t.Head), short(head), errOrNil(err))
		return
	}
	TeachGitLearned(t.Repo, "cache:"+t.Key, fetched)
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func errOrNil(err error) any {
	if err == nil {
		return ""
	}
	return err
}
