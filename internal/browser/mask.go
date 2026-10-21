package browser

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
)

// maskPNG paints black over rects (CSS px; the page is cssWidth wide).
func maskPNG(shot []byte, rects [][4]float64, cssWidth float64) ([]byte, error) {
	src, err := png.Decode(bytes.NewReader(shot))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	img := image.NewRGBA(b)
	draw.Draw(img, b, src, b.Min, draw.Src)
	scale := float64(b.Dx()) / cssWidth
	const pad = 3
	for _, r := range rects {
		box := image.Rect(int(r[0]*scale)-pad, int(r[1]*scale)-pad, int((r[0]+r[2])*scale)+pad, int((r[1]+r[3])*scale)+pad).Intersect(b)
		draw.Draw(img, box, &image.Uniform{C: color.Black}, image.Point{}, draw.Src)
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
