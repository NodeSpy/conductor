package browser

import (
	"strings"
	"testing"
)

// The source serializer is tested on hand-built DOM trees: each redaction is
// a branch whose deletion must fail a test here, and the structural rules are
// proved with the scrub backstop DISABLED (identity), so absence is by
// structure, not by the string replace.

func el(name string, attrs []string, kids ...srcNode) srcNode {
	return srcNode{NodeType: nodeElement, NodeName: strings.ToUpper(name), LocalName: name, Attributes: attrs, Children: kids}
}
func txt(s string) srcNode { return srcNode{NodeType: nodeText, NodeName: "#text", NodeValue: s} }
func comnt(s string) srcNode {
	return srcNode{NodeType: nodeComment, NodeName: "#comment", NodeValue: s}
}
func docNode(u string, kids ...srcNode) srcNode {
	return srcNode{NodeType: nodeDocument, NodeName: "#document", DocumentURL: u, Children: kids}
}

// render walks a tree with no scrub (identity) unless one is given.
func render(root srcNode, secret map[int64]bool, max int, scrub func(string) string) (string, bool) {
	if scrub == nil {
		scrub = func(s string) string { return s }
	}
	w := &sourceWriter{max: max, scrub: scrub, secret: secret, origin: originOf(root.DocumentURL)}
	w.walk(&root)
	return w.b.String(), w.cut
}

const sentinel = "S3NTINEL-do-not-leak"

// A control's value is never emitted, whatever its origin — with scrub off.
func TestSourceInputValueNeverEmitted(t *testing.T) {
	root := docNode("http://x.test/", el("html", nil, el("body", nil,
		el("input", []string{"name", "card", "value", sentinel}))))
	out, _ := render(root, nil, 0, nil)
	if strings.Contains(out, sentinel) {
		t.Fatalf("input value leaked: %s", out)
	}
	if !strings.Contains(out, `value="`+Redacted+`"`) {
		t.Fatalf("input structure/redaction marker missing: %s", out)
	}
	if !strings.Contains(out, `name="card"`) {
		t.Fatalf("other attributes should survive: %s", out)
	}
}

// A node a secret was filled into: value, checked and text dropped.
func TestSourceSecretNodeDropped(t *testing.T) {
	input := el("input", []string{"type", "checkbox", "checked", "true", "value", sentinel})
	input.BackendNodeID = 77
	div := el("div", nil, txt(sentinel))
	div.BackendNodeID = 78
	root := docNode("http://x.test/", el("html", nil, el("body", nil, input, div)))
	out, _ := render(root, map[int64]bool{77: true, 78: true}, 0, nil)
	if strings.Contains(out, sentinel) {
		t.Fatalf("secret node leaked: %s", out)
	}
	if strings.Contains(out, "checked") {
		t.Fatalf("a secret node's checked state must be dropped: %s", out)
	}
	if !strings.Contains(out, Redacted) {
		t.Fatalf("a redaction marker is expected: %s", out)
	}
}

// Script and style bodies are stripped.
func TestSourceScriptAndStyleStripped(t *testing.T) {
	root := docNode("http://x.test/", el("html", nil,
		el("head", nil,
			el("script", nil, txt(`var x="`+sentinel+`";`)),
			el("style", nil, txt(`body{background:url(`+sentinel+`)}`))),
		el("body", nil)))
	out, _ := render(root, nil, 0, nil)
	if strings.Contains(out, sentinel) {
		t.Fatalf("script/style body leaked: %s", out)
	}
	if !strings.Contains(out, "<script>") || !strings.Contains(out, "<style>") {
		t.Fatalf("the tags themselves should survive: %s", out)
	}
}

// HTML comments are stripped.
func TestSourceCommentStripped(t *testing.T) {
	root := docNode("http://x.test/", el("html", nil, el("body", nil, comnt(" "+sentinel+" "), txt("visible"))))
	out, _ := render(root, nil, 0, nil)
	if strings.Contains(out, sentinel) {
		t.Fatalf("comment leaked: %s", out)
	}
	if !strings.Contains(out, "visible") {
		t.Fatalf("text next to a comment should survive: %s", out)
	}
}

// A template's children are not serialised.
func TestSourceTemplateContentDropped(t *testing.T) {
	root := docNode("http://x.test/", el("html", nil, el("body", nil,
		el("template", nil, el("span", nil, txt(sentinel))))))
	out, _ := render(root, nil, 0, nil)
	if strings.Contains(out, sentinel) {
		t.Fatalf("template content leaked: %s", out)
	}
}

// data-* keeps its key, drops its value.
func TestSourceDataAttrStripped(t *testing.T) {
	root := docNode("http://x.test/", el("html", nil, el("body", nil,
		el("div", []string{"data-token", sentinel, "id", "acc"}))))
	out, _ := render(root, nil, 0, nil)
	if strings.Contains(out, sentinel) {
		t.Fatalf("data-* value leaked: %s", out)
	}
	if !strings.Contains(out, `data-token=""`) || !strings.Contains(out, `id="acc"`) {
		t.Fatalf("data-* key and other attrs should survive: %s", out)
	}
}

// style and event-handler attributes are stripped.
func TestSourceStyleAndHandlerAttrsStripped(t *testing.T) {
	root := docNode("http://x.test/", el("html", nil, el("body", nil,
		el("button", []string{"onclick", "x('" + sentinel + "')", "style", "color:" + sentinel, "type", "button"}, txt("go")))))
	out, _ := render(root, nil, 0, nil)
	if strings.Contains(out, sentinel) {
		t.Fatalf("handler/style attr leaked: %s", out)
	}
	if strings.Contains(out, "onclick") || strings.Contains(out, "style=") {
		t.Fatalf("handler/style attrs must not appear: %s", out)
	}
	if !strings.Contains(out, `type="button"`) {
		t.Fatalf("other attrs should survive: %s", out)
	}
}

// A textarea's text is its value: never emitted.
func TestSourceTextareaContentDropped(t *testing.T) {
	root := docNode("http://x.test/", el("html", nil, el("body", nil,
		el("textarea", []string{"name", "notes"}, txt(sentinel)))))
	out, _ := render(root, nil, 0, nil)
	if strings.Contains(out, sentinel) {
		t.Fatalf("textarea content leaked: %s", out)
	}
	if !strings.Contains(out, "<textarea") || !strings.Contains(out, Redacted) {
		t.Fatalf("textarea structure/marker missing: %s", out)
	}
}

// A URL attribute is scrubbed (the backstop path for a link target).
func TestSourceHrefScrubbed(t *testing.T) {
	scrub := func(s string) string { return strings.ReplaceAll(s, sentinel, Redacted) }
	root := docNode("http://x.test/", el("html", nil, el("body", nil,
		el("a", []string{"href", "/go?t=" + sentinel}, txt("link")))))
	out, _ := render(root, nil, 0, scrub)
	if strings.Contains(out, sentinel) {
		t.Fatalf("href leaked: %s", out)
	}
	if !strings.Contains(out, "href=") || !strings.Contains(out, "/go?t=") {
		t.Fatalf("the link structure should survive: %s", out)
	}
}

// Visible text is scrubbed as a backstop.
func TestSourceVisibleTextScrubbed(t *testing.T) {
	scrub := func(s string) string { return strings.ReplaceAll(s, sentinel, Redacted) }
	root := docNode("http://x.test/", el("html", nil, el("body", nil, el("p", nil, txt("hello "+sentinel)))))
	out, _ := render(root, nil, 0, scrub)
	if strings.Contains(out, sentinel) {
		t.Fatalf("visible text leaked: %s", out)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("surrounding text should survive: %s", out)
	}
}

// A same-origin frame is pierced; a cross-origin frame's contents never are.
func TestSourceFrameOriginBoundary(t *testing.T) {
	same := el("iframe", nil)
	sd := docNode("about:srcdoc", el("html", nil, el("body", nil, el("p", nil, txt("SAME_OK")))))
	same.ContentDocument = &sd
	cross := el("iframe", nil)
	cd := docNode("http://evil.test/", el("html", nil, el("body", nil, el("p", nil, txt("CROSS_SECRET")))))
	cross.ContentDocument = &cd
	root := docNode("http://x.test/", el("html", nil, el("body", nil, same, cross)))
	out, _ := render(root, nil, 0, nil)
	if !strings.Contains(out, "SAME_OK") {
		t.Fatalf("a same-origin frame should be included: %s", out)
	}
	if strings.Contains(out, "CROSS_SECRET") {
		t.Fatalf("a cross-origin frame must be excluded: %s", out)
	}
}

// A shadow root is included, with the same redaction inside.
func TestSourceShadowRootIncludedAndRedacted(t *testing.T) {
	host := el("div", []string{"id", "host"})
	shadow := srcNode{NodeType: nodeFragment, Children: []srcNode{
		el("input", []string{"value", sentinel}),
		el("span", nil, txt("shadow-visible")),
	}}
	host.ShadowRoots = []srcNode{shadow}
	root := docNode("http://x.test/", el("html", nil, el("body", nil, host)))
	out, _ := render(root, nil, 0, nil)
	if strings.Contains(out, sentinel) {
		t.Fatalf("a shadow input's value leaked: %s", out)
	}
	if !strings.Contains(out, "<shadow-root>") || !strings.Contains(out, "shadow-visible") {
		t.Fatalf("shadow structure should be included: %s", out)
	}
}

// A void element gets no closing tag.
func TestSourceVoidElementNoClose(t *testing.T) {
	root := docNode("http://x.test/", el("html", nil, el("body", nil, el("input", []string{"name", "q"}), el("br", nil))))
	out, _ := render(root, nil, 0, nil)
	if strings.Contains(out, "</input>") || strings.Contains(out, "</br>") {
		t.Fatalf("void elements should not close: %s", out)
	}
}

// The byte cap truncates and reports it.
func TestSourceSizeCap(t *testing.T) {
	var kids []srcNode
	for i := 0; i < 200; i++ {
		kids = append(kids, el("p", nil, txt("some filler text here")))
	}
	root := docNode("http://x.test/", el("html", nil, el("body", nil, kids...)))
	out, cut := render(root, nil, 200, nil)
	if !cut {
		t.Fatal("a small cap should truncate")
	}
	if len(out) > 200 {
		t.Fatalf("output past the cap: %d bytes", len(out))
	}
}
