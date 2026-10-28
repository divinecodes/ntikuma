package screenshot

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

type fakeBus struct {
	t       *testing.T
	signals chan<- *dbus.Signal
	matched bool
	removed bool
	closed  int
	version uint32
	body    []any
	callErr error
	cancel  context.CancelFunc
}

func (b *fakeBus) Names() []string                  { return []string{":1.42"} }
func (b *fakeBus) Signal(ch chan<- *dbus.Signal)    { b.signals = ch }
func (b *fakeBus) RemoveSignal(chan<- *dbus.Signal) { b.removed = true }
func (b *fakeBus) AddMatchSignalContext(context.Context, ...dbus.MatchOption) error {
	b.matched = true
	return nil
}
func (b *fakeBus) Object(_ string, path dbus.ObjectPath) dbus.BusObject {
	return fakeObject{bus: b, path: path}
}

type fakeObject struct {
	dbus.BusObject
	bus  *fakeBus
	path dbus.ObjectPath
}

func (o fakeObject) CallWithContext(_ context.Context, method string, _ dbus.Flags, args ...any) *dbus.Call {
	b := o.bus
	switch method {
	case "org.freedesktop.DBus.Properties.Get":
		return &dbus.Call{Body: []any{dbus.MakeVariant(b.version)}}
	case screenshotInterface + ".Screenshot":
		if !b.matched || b.signals == nil {
			b.t.Fatal("requested capture before subscribing")
		}
		opts := args[1].(map[string]dbus.Variant)
		if opts["interactive"].Value() != true || opts["modal"].Value() != true || args[0] != "" {
			b.t.Fatalf("wrong capture options: %+v", args)
		}
		if _, ok := opts["target"]; ok {
			b.t.Fatal("assumed a capture target")
		}
		token := opts["handle_token"].Value().(string)
		if !strings.HasPrefix(token, "ntikuma_") {
			b.t.Fatal("missing unique request token")
		}
		// Deliberately use a different handle and emit before returning it.
		handle := portalPath + "/request/1_42/older_portal_handle"
		b.signals <- &dbus.Signal{Path: portalPath + "/request/other", Name: requestInterface + ".Response", Body: []any{uint32(2), map[string]dbus.Variant{}}}
		if b.cancel != nil {
			b.cancel()
		} else if b.callErr == nil {
			b.signals <- &dbus.Signal{Path: handle, Name: requestInterface + ".Response", Body: b.body}
		}
		return &dbus.Call{Body: []any{handle}, Err: b.callErr}
	case requestInterface + ".Close":
		b.closed++
		return &dbus.Call{}
	default:
		b.t.Fatalf("unexpected method: %s", method)
		return nil
	}
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 8, 6))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func imageFile(t *testing.T, dir string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, "capture space.png")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func TestCaptureEarlyResponseAndTemporaryCleanup(t *testing.T) {
	uri := imageFile(t, t.TempDir(), pngBytes(t))
	b := &fakeBus{t: t, version: 2, body: []any{uint32(0), map[string]dbus.Variant{"uri": dbus.MakeVariant(uri)}}}
	img, err := capture(context.Background(), b)
	if err != nil || img.Width != 8 || img.Height != 6 || !strings.HasPrefix(img.DataURL, "data:image/png;base64,") {
		t.Fatalf("image: %+v, %v", img, err)
	}
	u, _ := url.Parse(uri)
	if _, err := os.Stat(u.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary capture left behind: %v", err)
	}
	if !b.removed || b.closed != 0 {
		t.Fatalf("subscription cleanup: %+v", b)
	}
}

func TestPortalCancellationDenialAndMalformedResponse(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []any
		want error
	}{
		{"cancel", []any{uint32(1), map[string]dbus.Variant{}}, ErrCancelled},
		{"denied", []any{uint32(2), map[string]dbus.Variant{}}, nil},
		{"missing uri", []any{uint32(0), map[string]dbus.Variant{}}, nil},
		{"malformed", []any{"0", map[string]dbus.Variant{}}, nil},
		{"short", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &fakeBus{t: t, version: 2, body: tc.body}
			_, err := capture(context.Background(), b)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("got %v", err)
			}
			if !b.removed {
				t.Fatal("signal channel left registered")
			}
		})
	}
}

func TestPortalContextCancellationClosesRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &fakeBus{t: t, version: 2, cancel: cancel}
	if _, err := capture(ctx, b); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if b.closed != 1 || !b.removed {
		t.Fatalf("request not cleaned up: %+v", b)
	}
}

func TestPortalUnavailableAndOldVersion(t *testing.T) {
	for _, version := range []uint32{1, 2} {
		b := &fakeBus{t: t, version: version, callErr: errors.New("permission denied")}
		if _, err := capture(context.Background(), b); err == nil {
			t.Fatal("capture unexpectedly succeeded")
		}
		if !b.removed {
			t.Fatal("signal channel left registered")
		}
	}
}

func TestImageValidationCleanupAndSavedPicturePreservation(t *testing.T) {
	data := pngBytes(t)
	img, err := encode(data)
	if err != nil || ValidateDataURL(img.DataURL) != nil {
		t.Fatalf("valid image rejected: %v", err)
	}
	for _, bad := range []string{"file:///tmp/image.png", "data:image/png;base64,!", strings.Replace(img.DataURL, "image/png", "image/jpeg", 1), "data:image/png;base64," + strings.Repeat("A", 14*1024*1024)} {
		if ValidateDataURL(bad) == nil {
			t.Fatal("invalid/oversized image accepted")
		}
	}
	for _, badURI := range []string{"https://example.com/image.png", "file://remote/tmp/image.png", "file:relative", "file:///tmp/image.png?query"} {
		if _, err := readURI(badURI); err == nil {
			t.Fatalf("accepted %q", badURI)
		}
	}
	uri := imageFile(t, t.TempDir(), []byte("not an image"))
	if _, err := readURI(uri); err == nil {
		t.Fatal("invalid image accepted")
	}
	u, _ := url.Parse(uri)
	if _, err := os.Stat(u.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid temporary capture left behind")
	}
	// Model a shell-owned Pictures file outside the portal temporary dir.
	uri = imageFile(t, t.TempDir(), data)
	t.Setenv("TMPDIR", t.TempDir())
	if _, err := readURI(uri); err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(uri)
	if _, err := os.Stat(u.Path); err != nil {
		t.Fatalf("deleted a saved picture: %v", err)
	}
}

func TestOversizedPortalImagesAreRejectedAndRemoved(t *testing.T) {
	tooManyPixels := pngBytes(t)
	// A valid PNG header with a huge canvas must fail before pixel allocation.
	binary.BigEndian.PutUint32(tooManyPixels[16:20], 100_000)
	binary.BigEndian.PutUint32(tooManyPixels[20:24], 100_000)
	binary.BigEndian.PutUint32(tooManyPixels[29:33], crc32.ChecksumIEEE(tooManyPixels[12:29]))
	for _, tc := range []struct {
		name    string
		data    []byte
		message string
	}{
		{"file size", make([]byte, MaxBytes+1), "10 MiB"},
		{"pixel count", tooManyPixels, "40 megapixels"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uri := imageFile(t, t.TempDir(), tc.data)
			if _, err := readURI(uri); err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("unexpected rejection: %v", err)
			}
			u, _ := url.Parse(uri)
			if _, err := os.Stat(u.Path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected capture left behind: %v", err)
			}
		})
	}
}
