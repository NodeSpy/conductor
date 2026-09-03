package handoff

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"sync"
)

// WebChannel serves each draft as a page on conductor's inbound HTTP listener and
// captures your call from it. GET /handoff?id=<id> renders the draft with a text
// box and approve/revise/discard buttons; the matching POST resolves the pending
// Await. It implements http.Handler, so production mounts it on the shared
// listener (inbound.Register) and a test mounts it on an httptest.Server — same
// code path either way.
//
// The link the caller surfaces to you is BaseURL + "/handoff?id=<id>"; BaseURL is
// the public origin conductor is reachable at (e.g. https://conductor.example.com).
type WebChannel struct {
	baseURL string
	log     func(string, ...any)

	mu      sync.Mutex
	seq     int
	pending map[string]*webPending
}

type webPending struct {
	draft Draft
	done  chan Decision
	once  sync.Once
}

// NewWebChannel builds a web-link channel. baseURL is the public origin the draft
// links point at (trailing slash trimmed); log may be nil.
func NewWebChannel(baseURL string, log func(string, ...any)) *WebChannel {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &WebChannel{
		baseURL: strings.TrimRight(baseURL, "/"),
		log:     log,
		pending: map[string]*webPending{},
	}
}

// Present registers the draft and returns a Presentation whose Ref is the page
// link. Await blocks until a POST resolves it (or ctx is cancelled).
func (c *WebChannel) Present(_ context.Context, d Draft) (Presentation, error) {
	c.mu.Lock()
	c.seq++
	if d.ID == "" {
		d.ID = fmt.Sprintf("h%d", c.seq)
	}
	p := &webPending{draft: d, done: make(chan Decision, 1)}
	c.pending[d.ID] = p
	c.mu.Unlock()
	c.log("handoff: draft %s presented at %s", d.ID, c.link(d.ID))
	return &webPresentation{c: c, id: d.ID, ref: c.link(d.ID)}, nil
}

func (c *WebChannel) link(id string) string {
	return c.baseURL + "/handoff?id=" + id
}

func (c *WebChannel) get(id string) *webPending {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pending[id]
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
	c   *WebChannel
	id  string
	ref string
}

func (p *webPresentation) Ref() string { return p.ref }

func (p *webPresentation) Await(ctx context.Context) (Decision, error) {
	pend := p.c.get(p.id)
	if pend == nil {
		return Decision{}, fmt.Errorf("handoff %s: no pending draft", p.id)
	}
	select {
	case <-ctx.Done():
		return Decision{}, ctx.Err()
	case d := <-pend.done:
		return d, nil
	}
}

func (p *webPresentation) Close() { p.c.remove(p.id) }
