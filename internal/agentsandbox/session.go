package agentsandbox

import (
	"context"
	"os"
	"sync"
	"time"
)

// Sessions a home is kept for. A dispatch's agent can outlive both its
// launcher and its run: paseo restarts an agent's provider (the launcher's
// lock goes) and relaunches it with --resume; a background or hand-off agent
// stays open after its run ends. Whether such a session is still there is
// the backend's to say, never conductor's bookkeeping: each launch binds the
// workspace's registry entry to the session it started (SessionRef), and
// each sweep asks that session's backend (a SessionLister) which of its
// sessions still exist. A backend that cannot answer keeps its sessions'
// homes for SessionMaxUnknown after the launch, not forever.
//
// A launch the daemon runs itself (BackendInProcess) cannot outlive the
// daemon: its home is kept by its run and its launch's lock alone.

// SessionRef names the session a registered workspace serves.
type SessionRef struct {
	// Backend runs the session: BackendInProcess, or the name a
	// SessionLister is registered under ("" = no session bound).
	Backend string `json:"backend"`
	// ID is the session's id on its backend.
	ID string `json:"id,omitempty"`
	// Bound is when the launch bound it.
	Bound time.Time `json:"bound"`
}

// BackendInProcess is a session the daemon runs as its own child.
const BackendInProcess = "in-process"

// backendUnknown marks a recorded entry no launch has bound (one saved
// before sessions were): its backend cannot be asked.
const backendUnknown = "unknown"

// SessionLister lists the ids of a backend's sessions that still exist (not
// archived, not removed). An error: the backend cannot tell now.
type SessionLister func(ctx context.Context) (map[string]bool, error)

var sessionListers sync.Map // backend name → SessionLister

// RegisterSessionLister makes backend's sessions answerable to the sweep.
func RegisterSessionLister(backend string, l SessionLister) {
	if backend != "" && l != nil {
		sessionListers.Store(backend, l)
	}
}

// SessionMaxUnknown is how long a session whose backend cannot be asked
// keeps its home after its launch.
var SessionMaxUnknown = 7 * 24 * time.Hour

// sessionListTimeout bounds one backend's answer.
const sessionListTimeout = 15 * time.Second

// expiredOnce names the sessions whose home stopped being kept because their
// backend could not be asked: reported once.
var expiredOnce sync.Map

// BindSession records the session a launch of dispatch in ws started — every
// launch, so the entry follows the path the dispatch actually runs on now.
func (r *Registry) BindSession(ws, dispatch string, s SessionRef) {
	if dispatch == "" {
		return
	}
	if s.Bound.IsZero() {
		s.Bound = homeNow()
	}
	c := canon(ws)
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.byWS[c]; ok && e.Dispatch == dispatch {
		e.Session = s
		r.byWS[c] = e
		r.saveLocked()
	}
}

// liveSessionHomes are the per-dispatch homes of the registry's live
// out-of-process sessions: their workspace exists and their backend lists
// them (or cannot be asked, and they were bound within SessionMaxUnknown).
func (r *Resolver) liveSessionHomes() map[string]bool {
	out := map[string]bool{}
	answers := map[string]map[string]bool{}
	asked := map[string]bool{}
	for _, e := range r.registry().entries() {
		s := e.Session
		if e.Dispatch == "" || s.Backend == "" || s.Backend == BackendInProcess {
			continue
		}
		if fi, err := os.Stat(e.Workspace); err != nil || !fi.IsDir() {
			continue
		}
		if !asked[s.Backend] {
			asked[s.Backend] = true
			if l, ok := sessionListers.Load(s.Backend); ok {
				ctx, cancel := context.WithTimeout(context.Background(), sessionListTimeout)
				if live, err := l.(SessionLister)(ctx); err == nil {
					answers[s.Backend] = live
				} else {
					r.logf("agent sandbox: session backend %s cannot list its sessions: %v (their homes are kept for at most %s after their launch)", s.Backend, err, SessionMaxUnknown)
				}
				cancel()
			}
		}
		key := IdentityKey(dispatchHomeKey(e))
		if live, known := answers[s.Backend]; known {
			if live[s.ID] {
				out[key] = true
			}
			continue
		}
		if homeNow().Sub(s.Bound) < SessionMaxUnknown {
			out[key] = true
		} else if _, told := expiredOnce.LoadOrStore(s.Backend+"\x00"+s.ID+"\x00"+key, true); !told {
			r.logf("agent sandbox: session %q on %s could not be confirmed live for %s; its home %s is no longer kept for it", s.ID, s.Backend, SessionMaxUnknown, key)
		}
	}
	return out
}
