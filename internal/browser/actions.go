package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// The mechanics of each action on a browser. Policy — what is allowed,
// where files go, what is audited — is the manager's; these only drive the
// browser. Every script conductor runs in a page here is conductor's own,
// fixed text with the agent's values passed as arguments, never as code.

// node resolves a ref the agent was shown (in this tab) to a backend id.
func (in *Instance) node(t *tab, ref string) (int64, error) {
	in.mu.Lock()
	id, ok := t.refs[ref]
	in.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("unknown ref %q in this tab — take a snapshot and use a ref from it", ref)
	}
	return id, nil
}

// object resolves a backend node to a JS object id.
func (in *Instance) object(ctx context.Context, t *tab, id int64) (string, error) {
	var r struct {
		Object struct {
			ObjectID string `json:"objectId"`
		} `json:"object"`
	}
	if err := in.conn().Call(ctx, t.session, "DOM.resolveNode", map[string]any{"backendNodeId": id}, &r); err != nil {
		return "", fmt.Errorf("the element is gone (the page changed) — take a new snapshot")
	}
	return r.Object.ObjectID, nil
}

// callOn runs one of conductor's functions with an element as this.
func (in *Instance) callOn(ctx context.Context, t *tab, obj, fn string, args ...any) (json.RawMessage, error) {
	a := []map[string]any{}
	for _, v := range args {
		a = append(a, map[string]any{"value": v})
	}
	var r struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := in.conn().Call(ctx, t.session, "Runtime.callFunctionOn", map[string]any{
		"objectId": obj, "functionDeclaration": fn, "arguments": a, "returnByValue": true, "awaitPromise": false,
	}, &r); err != nil {
		return nil, err
	}
	if r.Exception != nil {
		return nil, fmt.Errorf("the page refused: %s", r.Exception.Text)
	}
	return r.Result.Value, nil
}

// eval runs one of conductor's own expressions in the page.
func (in *Instance) eval(ctx context.Context, t *tab, expr string) (json.RawMessage, error) {
	var r struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := in.conn().Call(ctx, t.session, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true}, &r); err != nil {
		return nil, err
	}
	return r.Result.Value, nil
}

// settle waits for the tab's document to finish loading (bounded).
func (in *Instance) settle(ctx context.Context, t *tab, d time.Duration) {
	deadline := time.Now().Add(d)
	time.Sleep(120 * time.Millisecond)
	for time.Now().Before(deadline) {
		c, cancel := context.WithTimeout(ctx, time.Second)
		v, err := in.eval(c, t, "document.readyState")
		cancel()
		if err == nil && string(v) == `"complete"` {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// navigate loads url in the tab; a network refusal (the proxy's) is an error.
func (in *Instance) navigate(ctx context.Context, t *tab, url string) error {
	var r struct {
		ErrorText string `json:"errorText"`
	}
	if err := in.conn().Call(ctx, t.session, "Page.navigate", map[string]any{"url": url}, &r); err != nil {
		return err
	}
	if r.ErrorText != "" {
		return fmt.Errorf("navigation failed: %s", r.ErrorText)
	}
	in.settle(ctx, t, 15*time.Second)
	return in.loadFailed(ctx, t)
}

// loadFailed reports a page that ended on the browser's error page (a
// redirect the proxy refused, an unreachable host).
func (in *Instance) loadFailed(ctx context.Context, t *tab) error {
	u, _ := in.pageInfo(ctx, t)
	if strings.HasPrefix(u, "chrome-error://") {
		return fmt.Errorf("navigation failed: the page could not be loaded (off this browser's allowlist, or unreachable)")
	}
	// A plain-http request the proxy refused shows its 403 as the page.
	if v, err := in.eval(ctx, t, `(document.body && document.body.innerText || "").slice(0, 300)`); err == nil {
		var text string
		_ = json.Unmarshal(v, &text)
		if strings.HasPrefix(text, "sandbox egress proxy: ") {
			return fmt.Errorf("navigation failed: %s", strings.TrimSpace(strings.TrimPrefix(text, "sandbox egress proxy: ")))
		}
	}
	return nil
}

// history moves back (-1) or forward (+1).
func (in *Instance) history(ctx context.Context, t *tab, delta int) error {
	var h struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID int `json:"id"`
		} `json:"entries"`
	}
	if err := in.conn().Call(ctx, t.session, "Page.getNavigationHistory", nil, &h); err != nil {
		return err
	}
	i := h.CurrentIndex + delta
	if i < 0 || i >= len(h.Entries) {
		return fmt.Errorf("no page to go %s to", map[bool]string{true: "back", false: "forward"}[delta < 0])
	}
	if err := in.conn().Call(ctx, t.session, "Page.navigateToHistoryEntry", map[string]any{"entryId": h.Entries[i].ID}, nil); err != nil {
		return err
	}
	in.settle(ctx, t, 10*time.Second)
	return nil
}

func (in *Instance) reload(ctx context.Context, t *tab) error {
	if err := in.conn().Call(ctx, t.session, "Page.reload", nil, nil); err != nil {
		return err
	}
	in.settle(ctx, t, 15*time.Second)
	return nil
}

// center scrolls an element into view and returns its center.
func (in *Instance) center(ctx context.Context, t *tab, id int64) (x, y float64, ok bool) {
	_ = in.conn().Call(ctx, t.session, "DOM.scrollIntoViewIfNeeded", map[string]any{"backendNodeId": id}, nil)
	var q struct {
		Quads [][]float64 `json:"quads"`
	}
	if in.conn().Call(ctx, t.session, "DOM.getContentQuads", map[string]any{"backendNodeId": id}, &q) != nil {
		return 0, 0, false
	}
	for _, quad := range q.Quads {
		if len(quad) != 8 {
			continue
		}
		cx := (quad[0] + quad[2] + quad[4] + quad[6]) / 4
		cy := (quad[1] + quad[3] + quad[5] + quad[7]) / 4
		w := math.Abs(quad[2] - quad[0])
		h := math.Abs(quad[5] - quad[1])
		if w >= 1 && h >= 1 {
			return cx, cy, true
		}
	}
	return 0, 0, false
}

func (in *Instance) mouse(ctx context.Context, t *tab, typ string, x, y float64, extra map[string]any) error {
	p := map[string]any{"type": typ, "x": x, "y": y}
	for k, v := range extra {
		p[k] = v
	}
	return in.conn().Call(ctx, t.session, "Input.dispatchMouseEvent", p, nil)
}

// click clicks an element the way a user does: a real mouse event at its
// center (an element with no box is clicked through the DOM).
func (in *Instance) click(ctx context.Context, t *tab, ref string) error {
	id, err := in.node(t, ref)
	if err != nil {
		return err
	}
	x, y, ok := in.center(ctx, t, id)
	if !ok {
		obj, err := in.object(ctx, t, id)
		if err != nil {
			return err
		}
		if _, err := in.callOn(ctx, t, obj, "function(){ this.click(); }"); err != nil {
			return err
		}
	} else {
		if err := in.mouse(ctx, t, "mouseMoved", x, y, nil); err != nil {
			return err
		}
		if err := in.mouse(ctx, t, "mousePressed", x, y, map[string]any{"button": "left", "clickCount": 1}); err != nil {
			return err
		}
		if err := in.mouse(ctx, t, "mouseReleased", x, y, map[string]any{"button": "left", "clickCount": 1}); err != nil {
			return err
		}
	}
	in.settle(ctx, t, 5*time.Second)
	return nil
}

// focus focuses an element.
func (in *Instance) focus(ctx context.Context, t *tab, ref string) (string, error) {
	id, err := in.node(t, ref)
	if err != nil {
		return "", err
	}
	_ = in.conn().Call(ctx, t.session, "DOM.scrollIntoViewIfNeeded", map[string]any{"backendNodeId": id}, nil)
	if err := in.conn().Call(ctx, t.session, "DOM.focus", map[string]any{"backendNodeId": id}, nil); err != nil {
		return "", fmt.Errorf("cannot focus %s: %v", ref, err)
	}
	return in.object(ctx, t, id)
}

// fill replaces a field's text, typed as input events.
func (in *Instance) fill(ctx context.Context, t *tab, ref, text string) error {
	obj, err := in.focus(ctx, t, ref)
	if err != nil {
		return err
	}
	if _, err := in.callOn(ctx, t, obj, `function(){
		if (typeof this.select === "function") { this.select(); return; }
		const r = document.createRange(); r.selectNodeContents(this);
		const s = window.getSelection(); s.removeAllRanges(); s.addRange(r);
	}`); err != nil {
		return err
	}
	if text == "" {
		return in.key(ctx, t, "Delete")
	}
	return in.conn().Call(ctx, t.session, "Input.insertText", map[string]any{"text": text}, nil)
}

// typeText types at an element (or wherever focus is), keeping what is there.
func (in *Instance) typeText(ctx context.Context, t *tab, ref, text string) error {
	if ref != "" {
		if _, err := in.focus(ctx, t, ref); err != nil {
			return err
		}
	}
	return in.conn().Call(ctx, t.session, "Input.insertText", map[string]any{"text": text}, nil)
}

// selectOption picks a <select>'s option by value or by its text.
func (in *Instance) selectOption(ctx context.Context, t *tab, ref, want string) (string, error) {
	id, err := in.node(t, ref)
	if err != nil {
		return "", err
	}
	obj, err := in.object(ctx, t, id)
	if err != nil {
		return "", err
	}
	v, err := in.callOn(ctx, t, obj, `function(want){
		if (!(this instanceof HTMLSelectElement)) return {err: "not a select element"};
		const o = Array.from(this.options).find(o => o.value === want) ||
		          Array.from(this.options).find(o => o.text.trim() === want.trim());
		if (!o) return {err: "no option " + JSON.stringify(want) + " (options: " + Array.from(this.options).map(o => JSON.stringify(o.text.trim())).join(", ") + ")"};
		if (o.disabled) return {err: "option " + JSON.stringify(want) + " is disabled"};
		this.value = o.value;
		this.dispatchEvent(new Event("input", {bubbles: true}));
		this.dispatchEvent(new Event("change", {bubbles: true}));
		return {ok: o.text.trim()};
	}`, want)
	if err != nil {
		return "", err
	}
	var r struct {
		OK  string `json:"ok"`
		Err string `json:"err"`
	}
	_ = json.Unmarshal(v, &r)
	if r.Err != "" {
		return "", fmt.Errorf("%s", r.Err)
	}
	return r.OK, nil
}

// key presses one key (with modifiers: "Control+a", "Shift+Tab").
func (in *Instance) key(ctx context.Context, t *tab, combo string) error {
	k, mods, err := parseKey(combo)
	if err != nil {
		return err
	}
	down := map[string]any{"type": "keyDown", "key": k.key, "code": k.code, "windowsVirtualKeyCode": k.vk, "modifiers": mods}
	if k.text != "" && mods&^8 == 0 {
		down["text"] = k.text
	}
	if err := in.conn().Call(ctx, t.session, "Input.dispatchKeyEvent", down, nil); err != nil {
		return err
	}
	return in.conn().Call(ctx, t.session, "Input.dispatchKeyEvent", map[string]any{
		"type": "keyUp", "key": k.key, "code": k.code, "windowsVirtualKeyCode": k.vk, "modifiers": mods,
	}, nil)
}

// scroll scrolls an element into view, or the page by dy pixels.
func (in *Instance) scroll(ctx context.Context, t *tab, ref string, dy float64) error {
	if ref != "" {
		id, err := in.node(t, ref)
		if err != nil {
			return err
		}
		return in.conn().Call(ctx, t.session, "DOM.scrollIntoViewIfNeeded", map[string]any{"backendNodeId": id}, nil)
	}
	if err := in.mouse(ctx, t, "mouseWheel", 640, 400, map[string]any{"deltaX": 0, "deltaY": dy}); err != nil {
		return err
	}
	time.Sleep(200 * time.Millisecond)
	return nil
}

// waitFor polls until the page shows text (or stops showing gone), or its
// URL contains url — at most d.
//
// The comparison runs here, on the page as scrubbed of filled values — a
// wait is never an oracle for a secret.
func (in *Instance) waitFor(ctx context.Context, t *tab, text, gone, url string, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		ok := true
		if url != "" {
			u, _ := in.pageInfo(ctx, t)
			ok = strings.Contains(in.scrub(u), url)
		}
		if ok && (text != "" || gone != "") {
			c, cancel := context.WithTimeout(ctx, 2*time.Second)
			v, err := in.eval(c, t, `(document.body && document.body.innerText) || ""`)
			cancel()
			var body string
			ok = err == nil && json.Unmarshal(v, &body) == nil
			body = in.scrub(body)
			ok = ok && (text == "" || strings.Contains(body, text)) && (gone == "" || !strings.Contains(body, gone))
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting", d)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// screenshot captures the tab as PNG.
func (in *Instance) screenshot(ctx context.Context, t *tab, full bool) ([]byte, error) {
	var r struct {
		Data string `json:"data"`
	}
	if err := in.conn().Call(ctx, t.session, "Page.captureScreenshot", map[string]any{"format": "png", "captureBeyondViewport": full}, &r); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(r.Data)
}

// evaluate runs an agent's expression with V8's side-effect check: any
// write — to the DOM, to a variable outside it, a network call — throws
// before it happens. Read-only by construction, not by inspection.
func (in *Instance) evaluate(ctx context.Context, t *tab, expr string) (json.RawMessage, error) {
	var r struct {
		Result struct {
			Type        string          `json:"type"`
			Value       json.RawMessage `json:"value"`
			Description string          `json:"description"`
		} `json:"result"`
		Exception *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := in.conn().Call(ctx, t.session, "Runtime.evaluate", map[string]any{
		"expression": expr, "returnByValue": true, "throwOnSideEffect": true,
		"timeout": 5000, "silent": true, "awaitPromise": false, "replMode": false,
	}, &r); err != nil {
		return nil, err
	}
	if r.Exception != nil {
		msg := r.Exception.Text
		if r.Exception.Exception != nil && r.Exception.Exception.Description != "" {
			msg = r.Exception.Exception.Description
		}
		if strings.Contains(msg, "side effect") || strings.Contains(msg, "EvalError: Possible side-effect") {
			return nil, fmt.Errorf("refused: the expression may have a side effect (browser.evaluate is read-only)")
		}
		return nil, fmt.Errorf("%s", clip(msg, 500))
	}
	if len(r.Result.Value) == 0 {
		b, _ := json.Marshal(r.Result.Description)
		if r.Result.Type == "undefined" {
			b = []byte("null")
		}
		return b, nil
	}
	return r.Result.Value, nil
}

// setFiles puts files (paths in the browser's own view) on a file input.
func (in *Instance) setFiles(ctx context.Context, t *tab, ref string, files []string) error {
	id, err := in.node(t, ref)
	if err != nil {
		return err
	}
	obj, err := in.object(ctx, t, id)
	if err != nil {
		return err
	}
	v, err := in.callOn(ctx, t, obj, `function(){ return this instanceof HTMLInputElement && this.type === "file"; }`)
	if err != nil {
		return err
	}
	if string(v) != "true" {
		return fmt.Errorf("%s is not a file input", ref)
	}
	return in.conn().Call(ctx, t.session, "DOM.setFileInputFiles", map[string]any{"files": files, "backendNodeId": id}, nil)
}

// keyDef is one key's protocol fields.
type keyDef struct {
	key, code, text string
	vk              int
}

var keys = map[string]keyDef{
	"enter":      {"Enter", "Enter", "\r", 13},
	"tab":        {"Tab", "Tab", "", 9},
	"escape":     {"Escape", "Escape", "", 27},
	"esc":        {"Escape", "Escape", "", 27},
	"backspace":  {"Backspace", "Backspace", "", 8},
	"delete":     {"Delete", "Delete", "", 46},
	"space":      {" ", "Space", " ", 32},
	"arrowup":    {"ArrowUp", "ArrowUp", "", 38},
	"arrowdown":  {"ArrowDown", "ArrowDown", "", 40},
	"arrowleft":  {"ArrowLeft", "ArrowLeft", "", 37},
	"arrowright": {"ArrowRight", "ArrowRight", "", 39},
	"up":         {"ArrowUp", "ArrowUp", "", 38},
	"down":       {"ArrowDown", "ArrowDown", "", 40},
	"left":       {"ArrowLeft", "ArrowLeft", "", 37},
	"right":      {"ArrowRight", "ArrowRight", "", 39},
	"home":       {"Home", "Home", "", 36},
	"end":        {"End", "End", "", 35},
	"pageup":     {"PageUp", "PageUp", "", 33},
	"pagedown":   {"PageDown", "PageDown", "", 34},
}

// parseKey reads "Enter", "a", "Control+a", "Shift+Tab".
func parseKey(combo string) (keyDef, int, error) {
	parts := strings.Split(combo, "+")
	if len(parts) == 0 || combo == "" {
		return keyDef{}, 0, fmt.Errorf("no key")
	}
	mods := 0
	for _, m := range parts[:len(parts)-1] {
		switch strings.ToLower(m) {
		case "alt":
			mods |= 1
		case "control", "ctrl":
			mods |= 2
		case "meta", "cmd", "command":
			mods |= 4
		case "shift":
			mods |= 8
		default:
			return keyDef{}, 0, fmt.Errorf("unknown modifier %q", m)
		}
	}
	k := parts[len(parts)-1]
	if d, ok := keys[strings.ToLower(k)]; ok {
		return d, mods, nil
	}
	if len([]rune(k)) == 1 {
		r := []rune(k)[0]
		up := strings.ToUpper(k)
		code := "Key" + up
		if r >= '0' && r <= '9' {
			code = "Digit" + k
		}
		vk := int([]rune(up)[0])
		return keyDef{key: k, code: code, text: k, vk: vk}, mods, nil
	}
	if len(k) >= 2 && (k[0] == 'F' || k[0] == 'f') {
		if n, err := strconv.Atoi(k[1:]); err == nil && n >= 1 && n <= 12 {
			return keyDef{key: "F" + k[1:], code: "F" + k[1:], vk: 111 + n}, mods, nil
		}
	}
	return keyDef{}, 0, fmt.Errorf("unknown key %q (Enter, Tab, Escape, Backspace, Delete, Space, arrows, Home, End, PageUp, PageDown, F1-F12, or one character)", k)
}
