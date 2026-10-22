// Package avatar checks and normalizes a bot's avatar image
// (docs/wiki/Bots.md#identity): PNG, JPEG or WebP only, never SVG; checked
// by content and structure, not by name; and re-encoded server-side, so what
// conductor stores and serves is pixels only, with no metadata, no trailing
// payload and a capped size.
package avatar

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg" // decoder
	"image/png"
	"strings"

	_ "golang.org/x/image/webp" // decoder
)

// Limits.
const (
	// MaxUpload caps an uploaded image before it is re-encoded.
	MaxUpload = 8 << 20
	// MaxFile caps an image a config names (a hand-placed one, or one a
	// pack ships); an uploaded one is re-encoded far below it.
	MaxFile = 2 << 20
	// MaxSide caps the sides of an image a config names.
	MaxSide = 2048
	// MaxPixels refuses to decode anything larger (width × height): a small
	// file can declare an enormous canvas.
	MaxPixels = 40_000_000
	// Size is the side of a normalized avatar: a square.
	Size = 512
)

// Formats are the accepted image formats.
var Formats = []string{"png", "jpeg", "webp"}

// Format is an image's format by its signature: "png", "jpeg", "webp", or
// "" for anything else.
func Format(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		return "jpeg"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "webp"
	}
	return ""
}

// Check accepts a PNG, JPEG or WebP image and nothing else: by its
// signature (never its name), whole (nothing after the image's own end — a
// polyglot carries its second format there), with no markup in its text
// metadata, decodable, and within MaxPixels. It returns the format and size.
func Check(data []byte) (format string, w, h int, err error) {
	format = Format(data)
	if format == "" {
		return "", 0, 0, notAnImage(data)
	}
	switch format {
	case "png":
		err = checkPNG(data)
	case "jpeg":
		err = checkJPEG(data)
	case "webp":
		err = checkWebP(data)
	}
	if err != nil {
		return "", 0, 0, err
	}
	cfg, decoded, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || decoded != format {
		return "", 0, 0, fmt.Errorf("not a valid %s image: %v", format, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return "", 0, 0, fmt.Errorf("%dx%d is too large to decode (at most %d pixels)", cfg.Width, cfg.Height, MaxPixels)
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return "", 0, 0, fmt.Errorf("not a valid %s image: %v", format, err)
	}
	return format, cfg.Width, cfg.Height, nil
}

func notAnImage(data []byte) error {
	head := strings.ToLower(string(data[:min(len(data), 512)]))
	if strings.Contains(head, "<svg") || strings.Contains(head, "<?xml") {
		return errors.New("SVG is not accepted (it can carry script): use a PNG, JPEG or WebP")
	}
	return errors.New("not a PNG, JPEG or WebP image (the content decides, not the file name)")
}

// markup is what a polyglot hides in an image's text metadata for a browser
// to find.
var markup = []string{"<script", "<svg", "<html", "<!doctype", "<?xml", "<iframe", "javascript:"}

func hasMarkup(b []byte) bool {
	s := strings.ToLower(string(b))
	for _, m := range markup {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// checkPNG walks the chunks: each whole, IEND last and at the very end, and
// no markup in a text chunk.
func checkPNG(data []byte) error {
	off := 8
	for {
		if off+8 > len(data) {
			return errors.New("PNG: truncated chunk")
		}
		n := int(binary.BigEndian.Uint32(data[off:]))
		typ := string(data[off+4 : off+8])
		end := off + 12 + n
		if n < 0 || end > len(data) {
			return errors.New("PNG: truncated chunk")
		}
		body := data[off+8 : off+8+n]
		if (typ == "tEXt" || typ == "iTXt") && hasMarkup(body) {
			return errors.New("PNG: markup in its text metadata (a polyglot)")
		}
		if typ == "IEND" {
			if end != len(data) {
				return fmt.Errorf("PNG: %d bytes after the image's end (a polyglot carries its second format there)", len(data)-end)
			}
			return nil
		}
		off = end
	}
}

// checkJPEG walks the segments to the scan (no markup in a comment or an
// application segment) and requires the end marker as the last bytes.
func checkJPEG(data []byte) error {
	if !bytes.HasSuffix(data, []byte{0xff, 0xd9}) {
		return errors.New("JPEG: data after the image's end marker, or no end marker (a polyglot carries its second format there)")
	}
	off := 2
	for off+4 <= len(data) {
		if data[off] != 0xff {
			return errors.New("JPEG: malformed segment")
		}
		marker := data[off+1]
		if marker == 0xda { // start of scan: entropy-coded data follows
			return nil
		}
		n := int(binary.BigEndian.Uint16(data[off+2:]))
		if n < 2 || off+2+n > len(data) {
			return errors.New("JPEG: truncated segment")
		}
		if (marker == 0xfe || (marker >= 0xe0 && marker <= 0xef)) && hasMarkup(data[off+4:off+2+n]) {
			return errors.New("JPEG: markup in its metadata (a polyglot)")
		}
		off += 2 + n
	}
	return errors.New("JPEG: no image data")
}

// checkWebP requires the RIFF size to cover the file exactly.
func checkWebP(data []byte) error {
	if n := int64(binary.LittleEndian.Uint32(data[4:8])) + 8; n != int64(len(data)) {
		return fmt.Errorf("WebP: the file is %d bytes, its header says %d (a polyglot carries its second format after it)", len(data), n)
	}
	return nil
}

// Crop is a square crop box in the source image's pixels (the dashboard's
// crop tool); nil crops the centre square.
type Crop struct{ X, Y, Side int }

// Normalize checks an uploaded image (at most MaxUpload bytes) and
// re-encodes it: cropped to a square (the crop box, else the centre),
// scaled down to at most size×size, and written as a PNG — pixels only, no
// metadata.
func Normalize(data []byte, crop *Crop, size int) ([]byte, error) {
	if len(data) > MaxUpload {
		return nil, fmt.Errorf("the image is %d bytes, over the cap of %d", len(data), MaxUpload)
	}
	if _, _, _, err := Check(data); err != nil {
		return nil, err
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	var r image.Rectangle
	if crop == nil {
		side := min(b.Dx(), b.Dy())
		x0, y0 := b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2
		r = image.Rect(x0, y0, x0+side, y0+side)
	} else {
		r = image.Rect(b.Min.X+crop.X, b.Min.Y+crop.Y, b.Min.X+crop.X+crop.Side, b.Min.Y+crop.Y+crop.Side)
		if crop.Side <= 0 || crop.X < 0 || crop.Y < 0 || !r.In(b) {
			return nil, fmt.Errorf("crop %d,%d %dpx is outside the %dx%d image", crop.X, crop.Y, crop.Side, b.Dx(), b.Dy())
		}
	}
	out := scale(src, r, min(size, r.Dx()))
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// scale renders the square r of src at side×side, averaging the source
// pixels each output pixel covers.
func scale(src image.Image, r image.Rectangle, side int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, side, side))
	n := r.Dx()
	for y := 0; y < side; y++ {
		sy0, sy1 := r.Min.Y+y*n/side, r.Min.Y+(y+1)*n/side
		sy1 = max(sy1, sy0+1)
		for x := 0; x < side; x++ {
			sx0, sx1 := r.Min.X+x*n/side, r.Min.X+(x+1)*n/side
			sx1 = max(sx1, sx0+1)
			var rr, gg, bb, aa, cnt uint64
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					c := color.NRGBAModel.Convert(src.At(sx, sy)).(color.NRGBA)
					rr += uint64(c.R)
					gg += uint64(c.G)
					bb += uint64(c.B)
					aa += uint64(c.A)
					cnt++
				}
			}
			dst.SetNRGBA(x, y, color.NRGBA{uint8(rr / cnt), uint8(gg / cnt), uint8(bb / cnt), uint8(aa / cnt)})
		}
	}
	return dst
}

// CheckFile checks an image a config names: Check, plus MaxFile and
// MaxSide.
func CheckFile(data []byte) error {
	if len(data) > MaxFile {
		return fmt.Errorf("%d bytes, over the cap of %d (upload it with `conductor bot avatar set` to have it re-encoded)", len(data), MaxFile)
	}
	_, w, h, err := Check(data)
	if err != nil {
		return err
	}
	if w > MaxSide || h > MaxSide {
		return fmt.Errorf("%dx%d, over %dpx a side (upload it with `conductor bot avatar set` to have it re-encoded)", w, h, MaxSide)
	}
	return nil
}

// Serve re-encodes an image a config names for serving: whatever the file
// holds, what leaves conductor is a fresh PNG of its pixels.
func Serve(data []byte) ([]byte, error) {
	if err := CheckFile(data); err != nil {
		return nil, err
	}
	return Normalize(data, nil, Size)
}
