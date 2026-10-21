//go:build linux || darwin

package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// boxOf reads an element's viewport rect (CSS px) via the unguarded internal
// eval, so the test can find where a filled field landed in a screenshot.
func boxOf(t *testing.T, in *Instance, tb *tab, sel string) (x, y, w, h, iw float64) {
	t.Helper()
	ctx := context.Background()
	v, err := in.eval(ctx, tb, `(()=>{const e=document.querySelector(`+strconv.Quote(sel)+`);const r=e.getBoundingClientRect();return [r.left,r.top,r.width,r.height,innerWidth];})()`)
	if err != nil {
		t.Fatalf("box eval: %v", err)
	}
	var a []float64
	if err := json.Unmarshal(v, &a); err != nil || len(a) != 5 {
		t.Fatalf("box: %s", v)
	}
	return a[0], a[1], a[2], a[3], a[4]
}

// masked reports whether a box in a screenshot is painted solid black (an
// unmasked field shows its white background and dark glyphs instead).
func masked(img image.Image, x, y, w, h, scale float64) bool {
	bx, by := int(x*scale), int(y*scale)
	bw, bh := int(w*scale), int(h*scale)
	seen := false
	for yy := by; yy < by+bh; yy++ {
		for xx := bx; xx < bx+bw; xx++ {
			if !image.Pt(xx, yy).In(img.Bounds()) {
				continue
			}
			seen = true
			if r, g, b, _ := img.At(xx, yy).RGBA(); r != 0 || g != 0 || b != 0 {
				return false
			}
		}
	}
	return seen
}

func decodeShot(t *testing.T, w *walletRig, res map[string]any) image.Image {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(w.ws, res["path"].(string)))
	if err != nil {
		t.Fatalf("read shot: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return img
}

// TestRedteamShortValueScreenshotMasked: a filled value under 4 characters (a
// region code — and, by the same path, a card CVC) is hidden in a snapshot by
// its node, but the screenshot masking used to search only for values >= 4
// characters, so the short value stayed visible to OCR. It must now be masked
// by its field, the same as a snapshot hides it.
func TestRedteamShortValueScreenshotMasked(t *testing.T) {
	w := newWalletRig(t)
	op := w.caller("k-op", RequesterOperator)
	w.do(op, "open", map[string]any{"url": w.a + "/signup"})
	w.do(op, "fill-form", map[string]any{"profile": "personal"})
	s, _ := w.m.session(op)
	in := s.active
	tb, _ := in.current(context.Background())

	img := decodeShot(t, w, w.do(op, "screenshot", map[string]any{"path": "signup.png"}))
	_, _, _, _, iw := boxOf(t, in, tb, `[name="email"]`)
	scale := float64(img.Bounds().Dx()) / iw

	ex, ey, ew, eh, _ := boxOf(t, in, tb, `[name="email"]`)
	if !masked(img, ex, ey, ew, eh, scale) {
		t.Errorf("the email field (>=4) is not masked")
	}
	rx, ry, rw, rh, _ := boxOf(t, in, tb, `[name="state"]`)
	if !masked(img, rx, ry, rw, rh, scale) {
		t.Errorf("LEAK: the filled region field (< 4 chars) is not masked — visible to OCR")
	}

	// A full-page capture must mask the same field (the node box is placed in
	// document coordinates, scroll included).
	full := decodeShot(t, w, w.do(op, "screenshot", map[string]any{"path": "signup-full.png", "full": true}))
	fv, err := in.eval(context.Background(), tb, `(()=>{const r=document.querySelector('[name="state"]').getBoundingClientRect();return [r.left+scrollX,r.top+scrollY,r.width,r.height,document.documentElement.scrollWidth];})()`)
	if err != nil {
		t.Fatalf("full box: %v", err)
	}
	var fb []float64
	if err := json.Unmarshal(fv, &fb); err != nil {
		t.Fatalf("full box: %s", fv)
	}
	fscale := float64(full.Bounds().Dx()) / fb[4]
	if !masked(full, fb[0], fb[1], fb[2], fb[3], fscale) {
		t.Errorf("LEAK: the filled region field is not masked in a full-page screenshot")
	}
	w.noLeak()
}

// TestRedteamOnlyShortValueScreenshotMasked: when the ONLY value filled in a
// browser is under 4 characters, secretValues() is empty — the masking used to
// return the screenshot untouched. The field must still be masked.
func TestRedteamOnlyShortValueScreenshotMasked(t *testing.T) {
	w := newWalletRig(t)
	w.st.Evaluate = []string{"agent:test@*"}
	op := w.caller("k-op", RequesterOperator)
	w.do(op, "open", map[string]any{"url": w.a + "/signup"})
	snap := w.do(op, "snapshot", nil)["snapshot"].(string)
	w.do(op, "fill-profile", map[string]any{"profile": "personal", "field": "address.region", "ref": refOf(t, snap, "State")})
	s, _ := w.m.session(op)
	in := s.active
	if len(in.secretValues()) != 0 {
		t.Fatalf("expected no scrubbable values, got %v", in.secretValues())
	}
	tb, _ := in.current(context.Background())
	img := decodeShot(t, w, w.do(op, "screenshot", map[string]any{"path": "region.png"}))
	rx, ry, rw, rh, iw := boxOf(t, in, tb, `[name="state"]`)
	scale := float64(img.Bounds().Dx()) / iw
	if !masked(img, rx, ry, rw, rh, scale) {
		t.Errorf("LEAK: a browser with only a short filled value gave an unmasked screenshot")
	}
	// A profile fill must also turn evaluate off (the agent cannot read the
	// field back after any fill).
	if err := w.fail(op, "evaluate", map[string]any{"expr": "document.querySelector('[name=state]').value"}); err == nil || !strings.Contains(err.Error(), "credential was filled") {
		t.Fatalf("evaluate after a profile fill: %v", err)
	}
	w.noLeak()
}
