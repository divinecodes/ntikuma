package screenshot

import (
	"bytes"
	"errors"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
)

// MaxEdge is the longest side a captured screenshot keeps. Providers scale
// larger images down before the model sees them (Anthropic to about 1568 px,
// OpenAI to fit 2048 × 2048), so the extra pixels only cost upload time and
// count against the chat's screenshot budget.
const MaxEdge = 2048

// fitLongEdge scales an inspected capture so its longer side is at most
// MaxEdge, keeping its format. A capture that already fits is returned as is.
func fitLongEdge(data []byte, cfg image.Config, format string) ([]byte, error) {
	if max(cfg.Width, cfg.Height) <= MaxEdge {
		return data, nil
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("could not read the screenshot to scale it")
	}
	width, height := MaxEdge, max(1, cfg.Height*MaxEdge/cfg.Width)
	if cfg.Height > cfg.Width {
		width, height = max(1, cfg.Width*MaxEdge/cfg.Height), MaxEdge
	}
	scaled := shrink(src, width, height)
	var out bytes.Buffer
	if format == "jpeg" {
		err = jpeg.Encode(&out, scaled, &jpeg.Options{Quality: 90})
	} else {
		err = png.Encode(&out, scaled)
	}
	if err != nil {
		return nil, errors.New("could not encode the scaled screenshot")
	}
	return out.Bytes(), nil
}

// shrink averages every source pixel into the destination pixel it falls in
// (a box filter). Unlike dropping pixels, this keeps thin lines and small text
// as a blend instead of losing them.
func shrink(src image.Image, width, height int) *image.RGBA {
	bounds := src.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(rgba, rgba.Bounds(), src, bounds.Min, draw.Src)
	sw, sh := rgba.Bounds().Dx(), rgba.Bounds().Dy()
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	for dy := range height {
		y0, y1 := dy*sh/height, (dy+1)*sh/height
		for dx := range width {
			x0, x1 := dx*sw/width, (dx+1)*sw/width
			var r, g, b, a, n uint64
			for y := y0; y < y1; y++ {
				row := rgba.Pix[y*rgba.Stride:]
				for x := x0; x < x1; x++ {
					p := row[x*4 : x*4+4]
					r, g, b, a = r+uint64(p[0]), g+uint64(p[1]), b+uint64(p[2]), a+uint64(p[3])
					n++
				}
			}
			o := dst.PixOffset(dx, dy)
			dst.Pix[o], dst.Pix[o+1], dst.Pix[o+2], dst.Pix[o+3] = uint8(r/n), uint8(g/n), uint8(b/n), uint8(a/n)
		}
	}
	return dst
}
