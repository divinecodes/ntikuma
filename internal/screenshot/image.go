// Package screenshot captures images through the desktop portal without GTK.
package screenshot

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const MaxBytes = 10 * 1024 * 1024
const maxPixels = 40_000_000

type Image struct {
	DataURL string
	Width   int
	Height  int
}

// inspect checks the size limits and format without decoding the pixels.
func inspect(data []byte) (image.Config, string, error) {
	if len(data) > MaxBytes {
		return image.Config{}, "", errors.New("screenshot exceeds 10 MiB; capture a smaller area")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") {
		return image.Config{}, "", errors.New("screenshot must be a PNG or JPEG image")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return image.Config{}, "", errors.New("screenshot exceeds 40 megapixels; capture a smaller area")
	}
	return cfg, format, nil
}

func encode(data []byte) (Image, error) {
	cfg, format, err := inspect(data)
	if err != nil {
		return Image{}, err
	}
	return Image{
		DataURL: "data:image/" + format + ";base64," + base64.StdEncoding.EncodeToString(data),
		Width:   cfg.Width, Height: cfg.Height,
	}, nil
}

// ValidateDataURL bounds images received from the frontend before storage/send.
func ValidateDataURL(content string) error {
	header, payload, ok := strings.Cut(content, ",")
	if !ok || (header != "data:image/png;base64" && header != "data:image/jpeg;base64") {
		return errors.New("screenshot must contain a PNG or JPEG data URL")
	}
	if len(payload) > base64.StdEncoding.EncodedLen(MaxBytes) {
		return errors.New("screenshot exceeds 10 MiB; capture a smaller area")
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return errors.New("invalid screenshot data")
	}
	img, err := encode(data)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(img.DataURL, header+",") {
		return errors.New("screenshot format does not match its data URL")
	}
	return nil
}

func readURI(uri string) (Image, error) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") || u.RawQuery != "" || u.Fragment != "" || !filepath.IsAbs(u.Path) {
		return Image{}, errors.New("screenshot portal returned an invalid local file URI")
	}
	path := filepath.Clean(u.Path)
	// GNOME's interactive capture can return a picture saved by the shell.
	// Preserve those user files. Remove only portal files in the temporary dir.
	f, err := os.Open(path)
	if err != nil {
		return Image{}, fmt.Errorf("read screenshot: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return Image{}, errors.New("screenshot portal did not return a regular file")
	}
	if rel, err := filepath.Rel(os.TempDir(), path); err == nil && filepath.IsLocal(rel) && rel != "." {
		defer os.Remove(path)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return Image{}, fmt.Errorf("read screenshot: %w", err)
	}
	cfg, format, err := inspect(data)
	if err != nil {
		return Image{}, err
	}
	if data, err = fitLongEdge(data, cfg, format); err != nil {
		return Image{}, err
	}
	return encode(data)
}
