package avatar

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"
)

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), 200, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// withChunk inserts a PNG chunk right after IHDR.
func withChunk(p []byte, typ string, body []byte) []byte {
	var c bytes.Buffer
	_ = binary.Write(&c, binary.BigEndian, uint32(len(body)))
	c.WriteString(typ)
	c.Write(body)
	_ = binary.Write(&c, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(typ), body...)))
	at := 8 + 12 + 13 // signature + IHDR
	return append(append(append([]byte{}, p[:at]...), c.Bytes()...), p[at:]...)
}

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ONLY PNG, JPEG AND WEBP, by content and structure: never SVG, never a file
// whose name says image but whose content doesn't, never a polyglot (a
// second format after the image's end, or markup in its text metadata).
func TestCheck(t *testing.T) {
	good := pngOf(t, 10, 6)
	jpg, webp := read(t, "photo.jpg"), read(t, "wide.webp")
	for name, c := range map[string]struct {
		data []byte
		want string // format, or an error substring
	}{
		"png":                    {good, "png"},
		"jpeg":                   {jpg, "jpeg"},
		"webp":                   {webp, "webp"},
		"svg":                    {[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), "SVG is not accepted"},
		"svg with an xml prolog": {[]byte(`<?xml version="1.0"?><svg/>`), "SVG is not accepted"},
		"html named .png":        {[]byte("<html><body><script>alert(1)</script></body></html>"), "not a PNG, JPEG or WebP image"},
		"gif":                    {[]byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"), "not a PNG, JPEG or WebP image"},
		"empty":                  {nil, "not a PNG, JPEG or WebP image"},
		"png + html after IEND":  {append(append([]byte{}, good...), []byte("<html><script>alert(1)</script></html>")...), "bytes after the image's end"},
		"png + zip after IEND":   {append(append([]byte{}, good...), []byte("PK\x03\x04payload")...), "bytes after the image's end"},
		"png, script in tEXt":    {withChunk(good, "tEXt", []byte("Comment\x00<script>alert(1)</script>")), "markup in its text metadata"},
		"png, svg in iTXt":       {withChunk(good, "iTXt", []byte("XML\x00\x00\x00\x00\x00<svg onload=x>")), "markup in its text metadata"},
		"png, plain tEXt":        {withChunk(good, "tEXt", []byte("Comment\x00just a caption")), "png"},
		"png, truncated":         {good[:len(good)-20], "PNG: truncated chunk"},
		"jpeg + html after EOI":  {append(append([]byte{}, jpg...), []byte("<script>x</script>")...), "data after the image's end marker"},
		"webp + payload":         {append(append([]byte{}, webp...), []byte("<html>")...), "its header says"},
		"png header, garbage":    {append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 12)...), "PNG"},
	} {
		f, _, _, err := Check(c.data)
		got := f
		if err != nil {
			got = err.Error()
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
	// A JPEG whose comment carries markup.
	bad := append(append([]byte{0xff, 0xd8}, []byte{0xff, 0xfe, 0, 20}...), append([]byte("<script>x</script>"), jpg[2:]...)...)
	if _, _, _, err := Check(bad); err == nil || !strings.Contains(err.Error(), "markup in its metadata") {
		t.Fatalf("a JPEG comment with markup: %v", err)
	}
	// A canvas too large to decode.
	huge := pngOf(t, 1, 1)
	copy(huge[16:24], []byte{0, 1, 0x86, 0xa0, 0, 1, 0x86, 0xa0})
	binary.BigEndian.PutUint32(huge[29:33], crc32.ChecksumIEEE(huge[12:29]))
	if _, _, _, err := Check(huge); err == nil || !strings.Contains(err.Error(), "too large to decode") {
		t.Fatalf("a huge canvas: %v", err)
	}
}

// RE-ENCODED: an upload comes back a PNG, square (the centre, or the crop
// box), at most the size asked, with nothing of the original but pixels.
func TestNormalize(t *testing.T) {
	out, err := Normalize(pngOf(t, 900, 600), nil, Size)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil || img.Bounds().Dx() != 512 || img.Bounds().Dy() != 512 {
		t.Fatalf("normalized: %v %v", img.Bounds(), err)
	}
	// The centre square of a 900×600 image starts at x=150: its first pixel's
	// red channel is the source's x (150), averaged over the scaled block.
	if r := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA).R; r < 150 || r > 152 {
		t.Fatalf("not the centre crop: red %d", r)
	}
	// A crop box.
	box, err := Normalize(pngOf(t, 300, 200), &Crop{X: 10, Y: 20, Side: 100}, Size)
	if err != nil {
		t.Fatal(err)
	}
	bi, _ := png.Decode(bytes.NewReader(box))
	if bi.Bounds().Dx() != 100 { // never scaled up
		t.Fatalf("crop box size: %v", bi.Bounds())
	}
	if c := color.NRGBAModel.Convert(bi.At(0, 0)).(color.NRGBA); c.R != 10 || c.G != 20 {
		t.Fatalf("crop box origin: %v", c)
	}
	for _, cr := range []*Crop{{X: 250, Y: 0, Side: 100}, {X: -1, Y: 0, Side: 10}, {X: 0, Y: 0, Side: 0}} {
		if _, err := Normalize(pngOf(t, 300, 200), cr, Size); err == nil || !strings.Contains(err.Error(), "outside the") {
			t.Errorf("crop %+v: %v", cr, err)
		}
	}
	// WebP and JPEG in, PNG out; the JPEG's EXIF is gone.
	for _, f := range []string{"wide.webp", "photo.jpg"} {
		out, err := Normalize(read(t, f), nil, Size)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if Format(out) != "png" {
			t.Fatalf("%s: re-encoded as %q", f, Format(out))
		}
		if bytes.Contains(out, []byte("secret description")) || bytes.Contains(out, []byte("Photographer")) || bytes.Contains(out, []byte("eXIf")) {
			t.Fatalf("%s: metadata survived", f)
		}
	}
	if !bytes.Contains(read(t, "photo.jpg"), []byte("Photographer")) {
		t.Fatal("the fixture has no EXIF to strip")
	}
	// A polyglot is refused, not cleaned.
	if _, err := Normalize(append(pngOf(t, 4, 4), []byte("<html>")...), nil, Size); err == nil {
		t.Fatal("a polyglot upload was accepted")
	}
	// The byte cap, before anything is decoded.
	if _, err := Normalize(make([]byte, MaxUpload+1), nil, Size); err == nil || !strings.Contains(err.Error(), "over the cap of 8388608") {
		t.Fatalf("over the upload cap: %v", err)
	}
}

// A file a config names: Check, plus the byte and side caps; Serve
// re-encodes whatever it holds.
func TestCheckFileAndServe(t *testing.T) {
	if err := CheckFile(pngOf(t, MaxSide, 10)); err != nil {
		t.Fatalf("at the side cap: %v", err)
	}
	if err := CheckFile(pngOf(t, MaxSide+1, 10)); err == nil || !strings.Contains(err.Error(), "over 2048px a side") {
		t.Fatalf("over the side cap: %v", err)
	}
	big := withChunk(pngOf(t, 4, 4), "zzZz", make([]byte, MaxFile))
	if err := CheckFile(big); err == nil || !strings.Contains(err.Error(), "over the cap of 2097152") {
		t.Fatalf("over the byte cap: %v", err)
	}
	src := withChunk(pngOf(t, 40, 40), "tEXt", []byte("Author\x00someone"))
	out, err := Serve(src)
	if err != nil || bytes.Contains(out, []byte("someone")) || Format(out) != "png" {
		t.Fatalf("serve: %v", err)
	}
	if _, err := Serve([]byte("<svg/>")); err == nil {
		t.Fatal("served an SVG")
	}
}
