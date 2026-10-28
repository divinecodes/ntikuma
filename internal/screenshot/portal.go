package screenshot

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const portalName = "org.freedesktop.portal.Desktop"
const portalPath dbus.ObjectPath = "/org/freedesktop/portal/desktop"
const screenshotInterface = "org.freedesktop.portal.Screenshot"
const requestInterface = "org.freedesktop.portal.Request"

var ErrCancelled = errors.New("screenshot cancelled")

// Capture opens the desktop's interactive capture UI. The caller must hide its
// popup first. No active-window target is assumed, including on older portals.
func Capture(ctx context.Context) (Image, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return Image{}, fmt.Errorf("connect to screenshot portal: %w", err)
	}
	defer conn.Close()
	return capture(ctx, conn)
}

// A private connection makes subscription and request cleanup independent of
// Wails and other services using the session bus.
type portalBus interface {
	Object(string, dbus.ObjectPath) dbus.BusObject
	Names() []string
	Signal(chan<- *dbus.Signal)
	RemoveSignal(chan<- *dbus.Signal)
	AddMatchSignalContext(context.Context, ...dbus.MatchOption) error
}

func capture(ctx context.Context, conn portalBus) (Image, error) {
	signals := make(chan *dbus.Signal, 32)
	conn.Signal(signals)
	defer conn.RemoveSignal(signals)
	// Subscribe before calling Screenshot. Keep the namespace broad until the
	// returned handle is known so older portals with a different handle work too.
	if err := conn.AddMatchSignalContext(ctx,
		dbus.WithMatchSender(portalName), dbus.WithMatchInterface(requestInterface),
		dbus.WithMatchMember("Response"), dbus.WithMatchPathNamespace(portalPath+"/request"),
	); err != nil {
		return Image{}, fmt.Errorf("listen for screenshot response: %w", err)
	}
	obj := conn.Object(portalName, portalPath)
	var version dbus.Variant
	if err := obj.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, screenshotInterface, "version").Store(&version); err != nil {
		return Image{}, fmt.Errorf("screenshot portal unavailable; install a desktop portal backend: %w", err)
	}
	if v, ok := version.Value().(uint32); !ok || v < 2 {
		return Image{}, errors.New("interactive screenshots require screenshot portal version 2 or newer")
	}
	token := "ntikuma_" + rand.Text()
	names := conn.Names()
	if len(names) == 0 {
		return Image{}, errors.New("screenshot portal connection has no bus name")
	}
	handle := dbus.ObjectPath(string(portalPath) + "/request/" + strings.ReplaceAll(strings.TrimPrefix(names[0], ":"), ".", "_") + "/" + token)
	completed := false
	defer func() {
		if !completed {
			closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = conn.Object(portalName, handle).CallWithContext(closeCtx, requestInterface+".Close", 0).Err
		}
	}()
	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(token), "interactive": dbus.MakeVariant(true), "modal": dbus.MakeVariant(true),
	}
	if err := obj.CallWithContext(ctx, screenshotInterface+".Screenshot", 0, "", options).Store(&handle); err != nil {
		return Image{}, fmt.Errorf("open screenshot dialog: %w", err)
	}
	if !handle.IsValid() || !strings.HasPrefix(string(handle), string(portalPath)+"/request/") {
		return Image{}, errors.New("screenshot portal returned an invalid request handle")
	}
	for {
		select {
		case <-ctx.Done():
			return Image{}, ctx.Err()
		case signal, ok := <-signals:
			if !ok {
				return Image{}, errors.New("screenshot portal disconnected")
			}
			if signal == nil || signal.Path != handle || signal.Name != requestInterface+".Response" {
				continue
			}
			completed = true
			uri, err := responseURI(signal.Body)
			if err != nil {
				return Image{}, err
			}
			return readURI(uri)
		}
	}
}

func responseURI(body []any) (string, error) {
	if len(body) != 2 {
		return "", errors.New("invalid screenshot portal response")
	}
	code, ok := body[0].(uint32)
	if !ok {
		return "", errors.New("invalid screenshot portal response code")
	}
	if code == 1 {
		return "", ErrCancelled
	}
	if code != 0 {
		return "", errors.New("desktop did not provide a screenshot; permission may have been denied or capture cancelled")
	}
	results, ok := body[1].(map[string]dbus.Variant)
	if !ok {
		return "", errors.New("invalid screenshot portal results")
	}
	uri, ok := results["uri"].Value().(string)
	if !ok || uri == "" {
		return "", errors.New("screenshot portal returned no image")
	}
	return uri, nil
}
