package browser

import (
	"context"
	"html"
	"net/url"
	"strings"
)

// A read-only, point-in-time view of the page's own source — the DOM as
// conductor serialises it node by node, NOT the browser's raw outerHTML. It
// is redacted structurally, at the moment each node is written: a field's
// value is never emitted, a secret's node is dropped, and the places a value
// hides (scripts, comments, templates, data-* and style) are stripped. The
// agent reads structure (tags, ids, roles, aria, link targets, visible text)
// and nothing a page could use to leak a filled value past the a11y snapshot.
//
// It needs no `evaluate` opt-in and works after a credential is filled —
// that is the point: it is how an agent reads a page it has signed into
// without ever reaching for a live script.

// DOM.Node.nodeType values conductor serialises.
const (
	nodeElement  = 1
	nodeText     = 3
	nodeComment  = 8
	nodeDocument = 9
	nodeDoctype  = 10
	nodeFragment = 11
)

// srcNode is a DOM.Node as DOM.getDocument returns it (depth -1, pierced).
type srcNode struct {
	NodeType        int       `json:"nodeType"`
	NodeName        string    `json:"nodeName"`
	LocalName       string    `json:"localName"`
	NodeValue       string    `json:"nodeValue"`
	BackendNodeID   int64     `json:"backendNodeId"`
	Attributes      []string  `json:"attributes"`
	Children        []srcNode `json:"children"`
	ShadowRoots     []srcNode `json:"shadowRoots"`
	ContentDocument *srcNode  `json:"contentDocument"`
	TemplateContent *srcNode  `json:"templateContent"`
	DocumentURL     string    `json:"documentURL"`
	PseudoType      string    `json:"pseudoType"`
}

// voidElements take no closing tag and hold no children.
var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true,
	"img": true, "input": true, "link": true, "meta": true, "param": true,
	"source": true, "track": true, "wbr": true,
}

// urlAttrs are attribute names whose value is a URL: kept, but scrubbed of
// any filled value (a page could hide one in a querystring).
var urlAttrs = map[string]bool{
	"href": true, "src": true, "srcset": true, "action": true, "formaction": true,
	"poster": true, "cite": true, "background": true, "xlink:href": true, "data": true,
}

// valueBearing reports whether an element carries a user-entered value — its
// value is never emitted, whatever its origin.
func valueBearing(local string) bool {
	return local == "input" || local == "textarea" || local == "select"
}

// source serialises the page and applies a whole-string scrub backstop. The
// structural redaction in sourceHTML is the real guarantee; the backstop is
// defence in depth for a value that reached visible text or a link.
func (in *Instance) source(ctx context.Context, t *tab, max int) (string, bool, error) {
	body, truncated, err := in.sourceHTML(ctx, t, max)
	if err != nil {
		return "", false, err
	}
	return in.scrub(body), truncated, nil
}

// sourceHTML builds the structurally-redacted source. It emits no filled
// value whatever the scrub does: a test disables the scrub and proves the
// value is still absent.
func (in *Instance) sourceHTML(ctx context.Context, t *tab, max int) (string, bool, error) {
	root, err := in.domDocument(ctx, t)
	if err != nil {
		return "", false, err
	}
	in.mu.Lock()
	secret := make(map[int64]bool, len(in.secretNodes))
	for k, v := range in.secretNodes {
		secret[k] = v
	}
	in.mu.Unlock()
	w := &sourceWriter{max: max, scrub: in.scrub, secret: secret, origin: originOf(root.DocumentURL)}
	w.walk(root)
	body := w.b.String()
	if w.cut {
		body += "\n<!-- … source truncated; pass --max to see more -->"
	}
	return body, w.cut, nil
}

// domDocument reads the whole DOM, piercing shadow roots and same-process
// frames, with each node's stable backend id.
func (in *Instance) domDocument(ctx context.Context, t *tab) (*srcNode, error) {
	var r struct {
		Root srcNode `json:"root"`
	}
	if err := in.conn().Call(ctx, t.session, "DOM.getDocument", map[string]any{"depth": -1, "pierce": true}, &r); err != nil {
		return nil, err
	}
	return &r.Root, nil
}

// sourceWriter serialises a DOM tree with structural redaction, under a byte
// cap.
type sourceWriter struct {
	b      strings.Builder
	max    int
	cut    bool
	scrub  func(string) string
	secret map[int64]bool
	origin string // the top document's origin: a frame of another is never pierced
}

func (w *sourceWriter) write(s string) {
	if w.cut {
		return
	}
	if w.max > 0 && w.b.Len()+len(s) > w.max {
		w.cut = true
		return
	}
	w.b.WriteString(s)
}

// walk serialises one node and its (redacted) subtree.
func (w *sourceWriter) walk(n *srcNode) {
	if n == nil || w.cut {
		return
	}
	switch n.NodeType {
	case nodeDocument, nodeFragment:
		for i := range n.Children {
			w.walk(&n.Children[i])
		}
		return
	case nodeDoctype:
		w.write("<!DOCTYPE html>\n")
		return
	case nodeComment:
		// HTML comments are stripped: a value hides there and they are
		// useless to the agent.
		return
	case nodeText:
		// Visible text is kept, run through scrub as a backstop only — the
		// structural rules above are what keep a value out.
		w.write(html.EscapeString(w.scrub(n.NodeValue)))
		return
	case nodeElement:
		// below
	default:
		return
	}

	local := n.LocalName
	if local == "" {
		local = strings.ToLower(n.NodeName)
	}
	secret := w.secret[n.BackendNodeID]

	w.write("<" + local)
	w.writeAttrs(n, local, secret)
	w.write(">")
	if voidElements[local] {
		return
	}

	switch {
	case secret:
		// A node a secret was filled into: its value, checked state and text
		// are dropped; a placeholder stands for them.
		w.write(Redacted)
	case local == "script" || local == "style":
		// Script and style bodies are stripped: structure, never content.
	case local == "template":
		// A template's content is a document fragment a value can hide in.
	case local == "textarea":
		// A textarea's text IS its value: never emitted.
		if hasText(n) {
			w.write(Redacted)
		}
	default:
		for i := range n.ShadowRoots {
			w.write("<shadow-root>")
			w.walk(&n.ShadowRoots[i])
			w.write("</shadow-root>")
		}
		if local == "iframe" && w.includeFrame(n.ContentDocument) {
			w.walk(n.ContentDocument)
		}
		for i := range n.Children {
			w.walk(&n.Children[i])
		}
	}
	w.write("</" + local + ">")
}

// writeAttrs emits an element's attributes, redacting structurally: a
// control's value, a secret node's value/checked, event handlers and style
// bodies never appear; data-* keeps its key but drops its value; a URL is
// scrubbed; every other value is scrubbed as a backstop.
func (w *sourceWriter) writeAttrs(n *srcNode, local string, secret bool) {
	vb := valueBearing(local)
	hadValue := false
	for i := 0; i+1 < len(n.Attributes); i += 2 {
		k := n.Attributes[i]
		v := n.Attributes[i+1]
		lk := strings.ToLower(k)
		switch {
		case strings.HasPrefix(lk, "on"):
			// an event handler: script, where a value hides.
			continue
		case lk == "style":
			// a style body, stripped.
			continue
		case vb && (lk == "value" || lk == "defaultvalue"):
			hadValue = true
			continue
		case secret && (lk == "value" || lk == "checked" || lk == "defaultvalue"):
			continue
		case strings.HasPrefix(lk, "data-"):
			w.write(" " + k + `=""`)
			continue
		case urlAttrs[lk]:
			w.write(" " + k + `="` + html.EscapeString(w.scrub(v)) + `"`)
		default:
			w.write(" " + k + `="` + html.EscapeString(w.scrub(v)) + `"`)
		}
	}
	if (vb && hadValue) || secret {
		w.write(` value="` + Redacted + `"`)
	}
}

// includeFrame reports whether a pierced frame's document is the top
// document's own origin (or an about: frame, which inherits it). A
// cross-origin frame's contents are never serialised.
func (w *sourceWriter) includeFrame(doc *srcNode) bool {
	if doc == nil {
		return false
	}
	if strings.HasPrefix(doc.DocumentURL, "about:") {
		return true
	}
	o := originOf(doc.DocumentURL)
	return o != "" && o == w.origin
}

// hasText reports whether a node has any non-empty text descendant.
func hasText(n *srcNode) bool {
	for i := range n.Children {
		c := &n.Children[i]
		if c.NodeType == nodeText && strings.TrimSpace(c.NodeValue) != "" {
			return true
		}
		if hasText(c) {
			return true
		}
	}
	return false
}

// originOf is scheme://host[:port], or "" for a URL without one.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
