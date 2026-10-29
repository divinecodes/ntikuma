package screenshot

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func decodeDataURL(t *testing.T, dataURL string) (image.Image, string, int) {
	t.Helper()
	_, payload, _ := strings.Cut(dataURL, ",")
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return img, format, len(data)
}

func TestCapturesAreScaledToFitTheLongEdge(t *testing.T) {
	// Top half: one-pixel black and white stripes, vertical on the left and
	// horizontal on the right. Bottom half: white.
	src := image.NewNRGBA(image.Rect(0, 0, 4096, 2304))
	for y := range 2304 {
		for x := range 4096 {
			c := color.NRGBA{255, 255, 255, 255}
			if y < 1152 && ((x < 2048 && x%2 == 1) || (x >= 2048 && y%2 == 1)) {
				c = color.NRGBA{0, 0, 0, 255}
			}
			src.SetNRGBA(x, y, c)
		}
	}
	var original bytes.Buffer
	if err := png.Encode(&original, src); err != nil {
		t.Fatal(err)
	}
	img, err := readURI(imageFile(t, t.TempDir(), original.Bytes()))
	if err != nil || img.Width != MaxEdge || img.Height != 1152 {
		t.Fatalf("scaled to %d × %d, err %v", img.Width, img.Height, err)
	}
	scaled, format, size := decodeDataURL(t, img.DataURL)
	if format != "png" || scaled.Bounds().Dx() != img.Width || scaled.Bounds().Dy() != img.Height || ValidateDataURL(img.DataURL) != nil {
		t.Fatalf("bad scaled image: %s %v", format, scaled.Bounds())
	}
	if size >= original.Len() {
		t.Fatalf("scaled image is %d bytes, original %d", size, original.Len())
	}
	// Averaging keeps the stripes as grey; dropping pixels would leave them black or white.
	for _, at := range []image.Point{{100, 100}, {1500, 100}} {
		if stripes, _, _, _ := scaled.At(at.X, at.Y).RGBA(); stripes>>8 < 110 || stripes>>8 > 145 {
			t.Fatalf("stripes at %v: %d", at, stripes>>8)
		}
	}
	if white, _, _, _ := scaled.At(100, 1000).RGBA(); white>>8 < 250 {
		t.Fatalf("white %d", white>>8)
	}
}

func TestPortraitAndJPEGCapturesKeepShapeAndFormat(t *testing.T) {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1200, 3000)), &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	img, err := readURI(imageFile(t, t.TempDir(), b.Bytes()))
	if err != nil || img.Width != 819 || img.Height != MaxEdge || !strings.HasPrefix(img.DataURL, "data:image/jpeg;base64,") {
		t.Fatalf("%d × %d %q %v", img.Width, img.Height, img.DataURL[:24], err)
	}
	if _, format, _ := decodeDataURL(t, img.DataURL); format != "jpeg" {
		t.Fatal(format)
	}
}

func TestCapturesWithinTheLongEdgeAreSentUnchanged(t *testing.T) {
	// A JPEG with detail: re-encoding it would change its bytes.
	src := image.NewRGBA(image.Rect(0, 0, MaxEdge, 600))
	for i := range src.Pix {
		src.Pix[i] = byte(i * 31)
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, src, &jpeg.Options{Quality: 75}); err != nil {
		t.Fatal(err)
	}
	img, err := readURI(imageFile(t, t.TempDir(), b.Bytes()))
	if err != nil || img.DataURL != "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(b.Bytes()) {
		t.Fatalf("small capture was re-encoded: %v", err)
	}
}
