package handoff

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"
)

// defaultTTL is how long a presented draft's link stays valid when no TTL is
// configured.
const defaultTTL = 30 * time.Minute

// tokenBytes is the amount of crypto-random entropy in a generated draft id
// (192 bits, base64url-encoded) — the URL is the capability, so this is sized
// like a bearer token, not a display id.
const tokenBytes = 24

// WebChannel serves each draft as a page on conductor's inbound HTTP listener and
// captures your call from it. GET /handoff?id=<id> renders the draft with a text
// box and approve/revise/discard buttons; the matching POST resolves the pending
// Await. It implements http.Handler, so production mounts it on the shared
// listener (inbound.Register) and a test mounts it on an httptest.Server — same
// code path either way.
//
// The link the caller surfaces to you is BaseURL + "/handoff?id=<id>"; BaseURL is
// the public origin conductor is reachable at (e.g. https://conductor.example.com).
// The id is a crypto-random token (see newID) — the URL itself is the capability
// — and each pending draft carries a server-side TTL deadline (see webPending).
type WebChannel struct {
	baseURL string
	ttl     time.Duration
	log     func(string, ...any)

	// tunnel and listen are set by SetTunnel (see registry.go's buildChannel). A
	// nil tunnel keeps today's behavior: the link's origin is always baseURL.
	tunnel Tunnel
	listen string

	mu      sync.Mutex
	pending map[string]*webPending
}

type webPending struct {
	draft   Draft
	done    chan Decision
	once    sync.Once
	expires time.Time // zero = never expires
}

// NewWebChannel builds a web-link channel. baseURL is the public origin the draft
// links point at (trailing slash trimmed). ttl is how long a presented draft's
// link stays valid before it's treated as expired (<=0 uses defaultTTL — tests
// that need deterministic expiry should pass a short explicit ttl rather than
// relying on the default). log may be nil.
func NewWebChannel(baseURL string, ttl time.Duration, log func(string, ...any)) *WebChannel {
	if log == nil {
		log = func(string, ...any) {}
	}
	if ttl <= 0 {
		ttl = defaultTTL
	}
	return &WebChannel{
		baseURL: strings.TrimRight(baseURL, "/"),
		ttl:     ttl,
		log:     log,
		pending: map[string]*webPending{},
	}
}

// SetTunnel wires a pluggable tunnel (see tunnel.go): Present opens
// tunnel.Open(ctx, listen) fresh for each draft to compute that draft's link
// origin, and the returned closeFn is invoked (tearing the tunnel down) when the
// presentation is closed. listen is the local address the draft page is served
// on (see Registry.WebEntries / cmd/conductor/main.go). Not calling
// SetTunnel (the zero value: tunnel == nil) keeps today's behavior — the origin
// is always baseURL. Not concurrency-safe with Present; call before the channel
// is handed out (buildChannel does this at construction).
func (c *WebChannel) SetTunnel(t Tunnel, listen string) {
	c.tunnel = t
	c.listen = listen
}

// Present registers the draft and returns a Presentation whose Ref is the page
// link. Await blocks until a POST resolves it, the ttl elapses, or ctx is
// cancelled.
func (c *WebChannel) Present(ctx context.Context, d Draft) (Presentation, error) {
	c.mu.Lock()
	if d.ID == "" {
		id, err := c.newID()
		if err != nil {
			c.mu.Unlock()
			return nil, fmt.Errorf("handoff: generate draft id: %w", err)
		}
		d.ID = id
	}
	expires := time.Now().Add(c.ttl)
	p := &webPending{draft: d, done: make(chan Decision, 1), expires: expires}
	c.pending[d.ID] = p
	c.mu.Unlock()
	// origin is computed per-Present so a wired tunnel opens a fresh public URL
	// for THIS draft (see openOrigin); lan/static return a stable origin.
	origin, tunnelClose, err := c.openOrigin(ctx, d)
	if err != nil {
		c.remove(d.ID)
		return nil, fmt.Errorf("handoff: open origin: %w", err)
	}
	ref := c.link(origin, d.ID)
	c.log("handoff: draft %s presented at %s (expires %s)", d.ID, ref, expires.Format(time.RFC3339))
	return &webPresentation{c: c, id: d.ID, ref: ref, expires: expires, tunnelClose: tunnelClose}, nil
}

// openOrigin returns the public origin this draft's link should point at, and a
// close func releasing whatever was opened to expose it. With no tunnel wired
// (the common case — only base_url configured, SetTunnel never called) the
// origin is always baseURL and close is a no-op, matching pre-tunnel behavior
// exactly. A wired tunnel (including the explicit "static"/"lan" providers) is
// asked fresh on every call, so a spawning provider gets a new process — and
// therefore a new URL — per hand-off.
func (c *WebChannel) openOrigin(ctx context.Context, d Draft) (string, func() error, error) {
	if c.tunnel == nil {
		return c.baseURL, noopClose, nil
	}
	origin, closeFn, err := c.tunnel.Open(ctx, c.listen)
	if err != nil {
		return "", nil, err
	}
	if closeFn == nil {
		closeFn = noopClose
	}
	return origin, closeFn, nil
}

// newID returns a fresh crypto-random, base64url-encoded draft id. Caller must
// hold c.mu. Retries on the astronomically unlikely event of a collision with an
// already-pending id.
func (c *WebChannel) newID() (string, error) {
	for i := 0; i < 5; i++ {
		b := make([]byte, tokenBytes)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		id := base64.RawURLEncoding.EncodeToString(b)
		if _, exists := c.pending[id]; !exists {
			return id, nil
		}
	}
	return "", fmt.Errorf("could not generate a unique draft id")
}

func (c *WebChannel) link(origin, id string) string {
	return origin + "/handoff?id=" + id
}

// get returns the pending draft for id, or nil if it's unknown OR has expired —
// an expired entry is swept (and removed) on this access rather than by a
// background goroutine.
func (c *WebChannel) get(id string) *webPending {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.pending[id]
	if !ok {
		return nil
	}
	if !p.expires.IsZero() && !time.Now().Before(p.expires) {
		delete(c.pending, id)
		return nil
	}
	return p
}

func (c *WebChannel) remove(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// resolve delivers a decision to the pending draft exactly once, reporting
// whether it was still open (a second POST, or one after Await returned, is a
// no-op that reports false).
func (p *webPending) resolve(d Decision) bool {
	delivered := false
	p.once.Do(func() {
		p.done <- d
		delivered = true
	})
	return delivered
}

// ServeHTTP renders the draft (GET) and captures the decision (POST). Unknown ids
// 404; a resolved/expired draft reports that it's already been decided.
func (c *WebChannel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	p := c.get(id)
	if p == nil {
		http.Error(w, "no such draft (already decided or expired)", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		c.renderDraft(w, p.draft)
	case http.MethodPost:
		c.handlePost(w, id, p, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (c *WebChannel) handlePost(w http.ResponseWriter, id string, p *webPending, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	action := strings.ToLower(strings.TrimSpace(r.FormValue("action")))
	text := r.FormValue("text")
	switch action {
	case ActionApprove, ActionRevise, ActionDiscard:
	default:
		http.Error(w, "action must be approve|revise|discard", http.StatusBadRequest)
		return
	}
	dec := Decision{Action: action, Text: text}
	if !p.resolve(dec) {
		c.renderDone(w, "This draft was already decided.")
		return
	}
	c.remove(id)
	c.log("handoff: draft %s decided: %s", id, action)
	c.renderDone(w, "Recorded: "+action+". You can close this tab.")
}

var draftTmpl = template.Must(template.New("draft").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}}</title>
<style>
body{font:15px/1.5 system-ui,sans-serif;max-width:760px;margin:2rem auto;padding:0 1rem;color:#111}
h1{font-size:1.15rem}
.meta{color:#666;font-size:.9rem;margin-bottom:1rem}
textarea{width:100%;min-height:16rem;font:13px/1.45 ui-monospace,monospace;padding:.6rem;box-sizing:border-box}
.row{margin-top:1rem;display:flex;gap:.6rem;flex-wrap:wrap}
button{font-size:1rem;padding:.5rem 1rem;border:1px solid #ccc;border-radius:6px;cursor:pointer}
.approve{background:#e7f6e7;border-color:#8ac68a}
.discard{background:#fbeaea;border-color:#d79a9a}
</style></head><body>
<h1>{{.Title}}</h1>
<div class="meta">{{if .Repo}}{{.Repo}}{{if .Number}}#{{.Number}}{{end}} · {{end}}handoff {{.ID}}</div>
<form method="post" action="/handoff?id={{.ID}}">
<textarea name="text">{{.Body}}</textarea>
{{if .Options}}<div class="meta">agent options: {{range .Options}}<code>{{.}}</code> {{end}}</div>{{end}}
<div class="row">
<button class="approve" name="action" value="approve" type="submit">Approve &amp; submit</button>
<button name="action" value="revise" type="submit">Send revision</button>
<button class="discard" name="action" value="discard" type="submit">Discard</button>
</div>
</form>
<p class="meta">Edit the text above and choose <b>Send revision</b> to hand it back to the agent, <b>Approve &amp; submit</b> to accept it as-is, or <b>Discard</b> to abandon.</p>
</body></html>`))

func (c *WebChannel) renderDraft(w http.ResponseWriter, d Draft) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := draftTmpl.Execute(w, d); err != nil {
		c.log("handoff: render draft %s: %v", d.ID, err)
	}
}

func (c *WebChannel) renderDone(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>done</title>`+
		`<body style="font:15px/1.5 system-ui,sans-serif;max-width:640px;margin:3rem auto;padding:0 1rem">`+
		`<p>%s</p></body>`, template.HTMLEscapeString(msg))
}

// webPresentation is one draft awaiting a decision from the web page.
type webPresentation struct {
	c       *WebChannel
	id      string
	ref     string
	expires time.Time

	// tunnelClose releases whatever openOrigin opened for this draft (a no-op for
	// lan/static). Invoked by Close, guarded by closeOnce so repeat/concurrent
	// Close calls (every caller closes unconditionally after Await returns) only
	// tear it down once.
	tunnelClose func() error
	closeOnce   sync.Once
}

func (p *webPresentation) Ref() string { return p.ref }

// Await blocks until a POST resolves the draft, the TTL deadline passes (this is
// the backstop for a client that never polls/POSTs again, so a stale link doesn't
// wait forever), or ctx is cancelled.
func (p *webPresentation) Await(ctx context.Context) (Decision, error) {
	pend := p.c.get(p.id)
	if pend == nil {
		return Decision{}, fmt.Errorf("handoff %s: no pending draft (already decided or expired)", p.id)
	}
	var expiredCh <-chan time.Time
	if !p.expires.IsZero() {
		remaining := time.Until(p.expires)
		if remaining <= 0 {
			p.c.remove(p.id)
			return Decision{}, fmt.Errorf("handoff %s: draft expired", p.id)
		}
		timer := time.NewTimer(remaining)
		defer timer.Stop()
		expiredCh = timer.C
	}
	select {
	case <-ctx.Done():
		return Decision{}, ctx.Err()
	case <-expiredCh:
		p.c.remove(p.id)
		return Decision{}, fmt.Errorf("handoff %s: draft expired", p.id)
	case d := <-pend.done:
		return d, nil
	}
}

// Close releases the presentation: tears down its tunnel (if any) then removes
// the pending draft. Safe to call more than once.
func (p *webPresentation) Close() {
	p.closeOnce.Do(func() {
		if p.tunnelClose != nil {
			if err := p.tunnelClose(); err != nil {
				p.c.log("handoff: draft %s: tunnel close: %v", p.id, err)
			}
		}
		p.c.remove(p.id)
	})
}
