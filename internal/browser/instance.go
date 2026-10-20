package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/browser/cdp"
)

// Instance is one running browser and what conductor tracks about it: its
// tabs, the element refs of each tab's last snapshot, and its downloads.
type Instance struct {
	proc *Process
	dir  string
	// Sites are the logged-in sites whose sessions were put in this browser
	// ("" none: the anonymous profile).
	Sites []string

	mu        sync.Mutex
	tabs      map[string]*tab // target id → tab
	order     []string        // target ids, in the order they opened
	active    string
	downloads []*Download
	byGUID    map[string]*Download
	dialogs   []string
	stopEv    func()
	// secretNodes / secrets are the fields conductor filled from a
	// credential and the values it filled (scrubbed from every reply).
	secretNodes map[int64]bool
	// submitted are the pages (host and path) a sign-in step was submitted
	// from: a test sign-in that ends on one of them did not get through.
	submitted []string
	secrets   []string
	// maskVals is every filled value AND the text the page displays for it (a
	// select's option label); used to blind the wait comparison, which the
	// agent never reads, so over-scrubbing a short value there is safe.
	maskVals []string

	// onDownload is told when a download completes (the manager moves it
	// into the workspace); it runs on its own goroutine.
	onDownload func(*Download)
}

type tab struct {
	id      string
	session string
	refs    map[string]int64 // ref → backend DOM node id (the last snapshot)
	gen     int
}

// Download is one file the browser fetched.
type Download struct {
	GUID  string `json:"-"`
	URL   string `json:"url"`
	Name  string `json:"name"`
	State string `json:"state"` // inProgress | completed | canceled | refused
	Bytes int64  `json:"bytes"`
	// Path is where it landed in the workspace (relative), once moved.
	Path string `json:"path,omitempty"`
	Err  string `json:"error,omitempty"`
}

// newInstance wires the browser's events and opens its first tab.
func newInstance(ctx context.Context, p *Process, dir string, sites []string) (*Instance, error) {
	in := &Instance{proc: p, dir: dir, Sites: sites, tabs: map[string]*tab{}, byGUID: map[string]*Download{}}
	in.stopEv = p.Conn.Listen(in.event)
	if err := p.Conn.Call(ctx, "", "Target.setDiscoverTargets", map[string]any{"discover": true}, nil); err != nil {
		return nil, err
	}
	if p.Mux == nil {
		if err := os.MkdirAll(in.downloadDir(), 0o700); err != nil {
			return nil, err
		}
	}
	if err := p.Conn.Call(ctx, "", "Browser.setDownloadBehavior", map[string]any{
		"behavior": "allowAndName", "downloadPath": in.downloadDir(), "eventsEnabled": true,
	}, nil); err != nil {
		return nil, err
	}
	var tg struct {
		TargetInfos []targetInfo `json:"targetInfos"`
	}
	if err := p.Conn.Call(ctx, "", "Target.getTargets", nil, &tg); err != nil {
		return nil, err
	}
	in.mu.Lock()
	for _, ti := range tg.TargetInfos {
		in.addTargetLocked(ti)
	}
	in.mu.Unlock()
	if len(in.order) == 0 {
		if _, err := in.newTab(ctx, "about:blank"); err != nil {
			return nil, err
		}
	}
	in.mu.Lock()
	if in.active == "" && len(in.order) > 0 {
		in.active = in.order[0]
	}
	in.mu.Unlock()
	return in, nil
}

type targetInfo struct {
	TargetID string `json:"targetId"`
	Type     string `json:"type"`
	URL      string `json:"url"`
	Title    string `json:"title"`
}

// downloadDir is where the browser saves downloads, as it sees it.
func (in *Instance) downloadDir() string {
	if in.proc.Guest != "" {
		return filepath.Join(in.proc.Guest, "xfer", "dl")
	}
	return filepath.Join(in.dir, "xfer", "dl")
}

// hostDownload is a finished download as a host file (fetched out of a
// container, at most max bytes).
func (in *Instance) hostDownload(guid string, max int64) (string, error) {
	host := filepath.Join(in.dir, "xfer", "dl", guid)
	if in.proc.Mux == nil {
		return host, nil
	}
	if err := os.MkdirAll(filepath.Dir(host), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(host, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	_, err = in.proc.Mux.Get(filepath.Join(in.downloadDir(), guid), max, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(host)
		return "", err
	}
	return host, nil
}

// browserPath puts a staged host file (under the instance dir) where the
// browser can open it, and returns that path as the browser sees it.
func (in *Instance) browserPath(host string) (string, error) {
	if in.proc.Mux == nil {
		return host, nil
	}
	rel, err := filepath.Rel(in.dir, host)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("browser: %s is not staged", host)
	}
	guest := filepath.Join(in.proc.Guest, rel)
	f, err := os.Open(host)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := in.proc.Mux.Put(guest, f); err != nil {
		return "", err
	}
	return guest, nil
}

// Done is closed when the browser exits.
func (in *Instance) Done() <-chan struct{} { return in.proc.Done() }

// Close ends the browser and removes its directory (the ephemeral profile).
func (in *Instance) Close() {
	if in.stopEv != nil {
		in.stopEv()
	}
	in.proc.Close()
	_ = os.RemoveAll(in.dir)
}

func (in *Instance) conn() *cdp.Conn { return in.proc.Conn }

func (in *Instance) addTargetLocked(ti targetInfo) {
	if ti.Type != "page" {
		return
	}
	if _, ok := in.tabs[ti.TargetID]; ok {
		return
	}
	in.tabs[ti.TargetID] = &tab{id: ti.TargetID}
	in.order = append(in.order, ti.TargetID)
}

// event runs on the protocol reader: it records, never calls back.
func (in *Instance) event(ev cdp.Event) {
	switch ev.Method {
	case "Target.targetCreated":
		var p struct {
			TargetInfo targetInfo `json:"targetInfo"`
		}
		if json.Unmarshal(ev.Params, &p) == nil {
			in.mu.Lock()
			in.addTargetLocked(p.TargetInfo)
			in.mu.Unlock()
		}
	case "Target.targetDestroyed", "Target.detachedFromTarget":
		var p struct {
			TargetID string `json:"targetId"`
		}
		if json.Unmarshal(ev.Params, &p) == nil && p.TargetID != "" {
			in.mu.Lock()
			if ev.Method == "Target.targetDestroyed" {
				in.removeTabLocked(p.TargetID)
			} else if t := in.tabs[p.TargetID]; t != nil {
				t.session = ""
			}
			in.mu.Unlock()
		}
	case "Browser.downloadWillBegin":
		var p struct {
			GUID              string `json:"guid"`
			URL               string `json:"url"`
			SuggestedFilename string `json:"suggestedFilename"`
		}
		if json.Unmarshal(ev.Params, &p) == nil {
			in.mu.Lock()
			d := &Download{GUID: p.GUID, URL: redactURL(p.URL), Name: p.SuggestedFilename, State: "inProgress"}
			in.downloads = append(in.downloads, d)
			in.byGUID[p.GUID] = d
			in.mu.Unlock()
		}
	case "Browser.downloadProgress":
		var p struct {
			GUID          string  `json:"guid"`
			State         string  `json:"state"`
			ReceivedBytes float64 `json:"receivedBytes"`
		}
		if json.Unmarshal(ev.Params, &p) == nil {
			in.mu.Lock()
			d := in.byGUID[p.GUID]
			if d != nil && d.State == "inProgress" {
				d.Bytes = int64(p.ReceivedBytes)
				d.State = p.State
			}
			fire := d != nil && p.State == "completed"
			in.mu.Unlock()
			if fire && in.onDownload != nil {
				go in.onDownload(d)
			}
		}
	case "Page.javascriptDialogOpening":
		var p struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(ev.Params, &p) == nil {
			// A dialog blocks its page: answered at once, never left open.
			// alert and beforeunload are accepted (OK / leave); confirm and
			// prompt are dismissed — a page never gets a "yes" conductor did
			// not ask the agent for.
			accept := p.Type == "alert" || p.Type == "beforeunload"
			verdict := "dismissed"
			if accept {
				verdict = "accepted"
			}
			in.mu.Lock()
			in.dialogs = append(in.dialogs, fmt.Sprintf("%s %q (%s)", p.Type, clip(p.Message, 200), verdict))
			in.mu.Unlock()
			go func(session string) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = in.conn().Call(ctx, session, "Page.handleJavaScriptDialog", map[string]any{"accept": accept}, nil)
			}(ev.Session)
		}
	}
}

func (in *Instance) removeTabLocked(id string) {
	delete(in.tabs, id)
	for i, x := range in.order {
		if x == id {
			in.order = append(in.order[:i], in.order[i+1:]...)
			break
		}
	}
	if in.active == id {
		in.active = ""
		if n := len(in.order); n > 0 {
			in.active = in.order[n-1]
		}
	}
}

// takeDialogs returns and clears the dialogs answered since the last call.
func (in *Instance) takeDialogs() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	d := in.dialogs
	in.dialogs = nil
	return d
}

// newTab opens a tab at url and makes it the active one.
func (in *Instance) newTab(ctx context.Context, url string) (*tab, error) {
	var r struct {
		TargetID string `json:"targetId"`
	}
	if err := in.conn().Call(ctx, "", "Target.createTarget", map[string]any{"url": url}, &r); err != nil {
		return nil, err
	}
	in.mu.Lock()
	in.addTargetLocked(targetInfo{TargetID: r.TargetID, Type: "page"})
	in.active = r.TargetID
	t := in.tabs[r.TargetID]
	in.mu.Unlock()
	return t, in.attach(ctx, t)
}

// current is the active tab, attached.
func (in *Instance) current(ctx context.Context) (*tab, error) {
	in.mu.Lock()
	t := in.tabs[in.active]
	if t == nil && len(in.order) > 0 {
		in.active = in.order[len(in.order)-1]
		t = in.tabs[in.active]
	}
	in.mu.Unlock()
	if t == nil {
		return in.newTab(ctx, "about:blank")
	}
	return t, in.attach(ctx, t)
}

// attach opens a protocol session on a tab (once) and turns on what
// conductor reads from it.
func (in *Instance) attach(ctx context.Context, t *tab) error {
	in.mu.Lock()
	sess := t.session
	in.mu.Unlock()
	if sess != "" {
		return nil
	}
	var r struct {
		SessionID string `json:"sessionId"`
	}
	if err := in.conn().Call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": t.id, "flatten": true}, &r); err != nil {
		return err
	}
	for _, m := range []string{"Page.enable", "DOM.enable", "Accessibility.enable"} {
		if err := in.conn().Call(ctx, r.SessionID, m, nil, nil); err != nil {
			return err
		}
	}
	in.mu.Lock()
	t.session = r.SessionID
	in.mu.Unlock()
	return nil
}

// TabInfo describes one tab.
type TabInfo struct {
	Index  int    `json:"index"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	Active bool   `json:"active,omitempty"`
}

// tabList is the tabs in opening order (index 1-based).
func (in *Instance) tabList(ctx context.Context) ([]TabInfo, error) {
	var tg struct {
		TargetInfos []targetInfo `json:"targetInfos"`
	}
	if err := in.conn().Call(ctx, "", "Target.getTargets", nil, &tg); err != nil {
		return nil, err
	}
	info := map[string]targetInfo{}
	for _, ti := range tg.TargetInfos {
		info[ti.TargetID] = ti
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	var out []TabInfo
	for i, id := range in.order {
		ti := info[id]
		out = append(out, TabInfo{Index: i + 1, URL: redactURL(ti.URL), Title: clip(ti.Title, 120), Active: id == in.active})
	}
	return out, nil
}

// tabAt is the tab at a 1-based index.
func (in *Instance) tabAt(i int) (*tab, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if i < 1 || i > len(in.order) {
		return nil, fmt.Errorf("no tab %d (there are %d)", i, len(in.order))
	}
	return in.tabs[in.order[i-1]], nil
}

// pageInfo is the tab's URL and title, read from the browser (not the page).
func (in *Instance) pageInfo(ctx context.Context, t *tab) (url, title string) {
	var r struct {
		TargetInfo targetInfo `json:"targetInfo"`
	}
	if in.conn().Call(ctx, "", "Target.getTargetInfo", map[string]any{"targetId": t.id}, &r) == nil {
		return r.TargetInfo.URL, r.TargetInfo.Title
	}
	return "", ""
}

// downloadList is every download, oldest first.
func (in *Instance) downloadList() []Download {
	in.mu.Lock()
	defer in.mu.Unlock()
	out := make([]Download, 0, len(in.downloads))
	for _, d := range in.downloads {
		out = append(out, *d)
	}
	return out
}

// setDownload updates a download under the lock.
func (in *Instance) setDownload(d *Download, fn func(*Download)) {
	in.mu.Lock()
	fn(d)
	in.mu.Unlock()
}

// cookies reads every cookie in the browser.
func (in *Instance) cookies(ctx context.Context) ([]Cookie, error) {
	var r struct {
		Cookies []Cookie `json:"cookies"`
	}
	if err := in.conn().Call(ctx, "", "Storage.getCookies", map[string]any{}, &r); err != nil {
		return nil, err
	}
	sort.SliceStable(r.Cookies, func(i, j int) bool { return r.Cookies[i].Domain < r.Cookies[j].Domain })
	return r.Cookies, nil
}

// setCookies puts cookies in the browser.
func (in *Instance) setCookies(ctx context.Context, cs []Cookie) error {
	if len(cs) == 0 {
		return nil
	}
	return in.conn().Call(ctx, "", "Storage.setCookies", map[string]any{"cookies": cs}, nil)
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
