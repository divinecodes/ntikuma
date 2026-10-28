//go:build linux && cgo && !gtk3

package platform

/*
#cgo pkg-config: gtk4
#include <stdint.h>
#include <gtk/gtk.h>
#ifdef GDK_WINDOWING_WAYLAND
#include <gdk/wayland/gdkwayland.h>
#endif
#ifdef GDK_WINDOWING_X11
#include <gdk/x11/gdkx.h>
#endif

extern void ntkSelectionResult(uintptr_t handle, void *data, int length, int status);

// 0 unsupported, 1 X11 before focus, 2 Wayland after focus.
static int ntk_selection_support(void) {
  GdkDisplay *display = gdk_display_get_default();
  if (!display) return 0;
#ifdef GDK_WINDOWING_X11
  if (GDK_IS_X11_DISPLAY(display)) return 1;
#endif
#ifdef GDK_WINDOWING_WAYLAND
  if (GDK_IS_WAYLAND_DISPLAY(display) &&
      gdk_wayland_display_query_registry(display, "zwp_primary_selection_device_manager_v1")) return 2;
#endif
  return 0;
}

typedef struct {
  uintptr_t handle;
  GCancellable *cancel;
  GInputStream *stream;
  GByteArray *bytes;
  gsize limit;
} NtkSelection;

static void ntk_selection_finish(NtkSelection *read, int status) {
  ntkSelectionResult(read->handle, read->bytes->data, read->bytes->len, status);
  g_clear_object(&read->stream);
  g_object_unref(read->cancel);
  g_byte_array_unref(read->bytes);
  g_free(read);
}

static void ntk_selection_next(NtkSelection *read);
static void ntk_selection_chunk(GObject *source, GAsyncResult *result, gpointer data) {
  NtkSelection *read = data;
  GError *error = NULL;
  GBytes *chunk = g_input_stream_read_bytes_finish(G_INPUT_STREAM(source), result, &error);
  if (error) {
    g_error_free(error);
    ntk_selection_finish(read, 1);
    return;
  }
  gsize length;
  const guint8 *bytes = g_bytes_get_data(chunk, &length);
  if (!length) {
    g_bytes_unref(chunk);
    ntk_selection_finish(read, 0);
    return;
  }
  g_byte_array_append(read->bytes, bytes, length);
  g_bytes_unref(chunk);
  if (read->bytes->len > read->limit) {
    ntk_selection_finish(read, 2);
    return;
  }
  ntk_selection_next(read);
}

static void ntk_selection_next(NtkSelection *read) {
  gsize size = MIN((gsize)65536, read->limit + 1 - read->bytes->len);
  g_input_stream_read_bytes_async(read->stream, size, G_PRIORITY_DEFAULT,
                                read->cancel, ntk_selection_chunk, read);
}

static void ntk_selection_stream(GObject *source, GAsyncResult *result, gpointer data) {
  NtkSelection *read = data;
  GError *error = NULL;
  read->stream = gdk_clipboard_read_finish(GDK_CLIPBOARD(source), result, NULL, &error);
  if (error) {
    int status = g_error_matches(error, G_IO_ERROR, G_IO_ERROR_NOT_SUPPORTED) ? 0 : 1;
    g_error_free(error);
    ntk_selection_finish(read, status);
    return;
  }
  if (!read->stream) { ntk_selection_finish(read, 0); return; }
  ntk_selection_next(read);
}

static GCancellable *ntk_selection_start(uintptr_t handle, gsize limit) {
  NtkSelection *read = g_new0(NtkSelection, 1);
  read->handle = handle;
  read->limit = limit;
  read->cancel = g_cancellable_new();
  read->bytes = g_byte_array_new();
  GCancellable *cancel = g_object_ref(read->cancel);
  GdkDisplay *display = gdk_display_get_default();
  if (!display || !ntk_selection_support()) { ntk_selection_finish(read, 3); return cancel; }
  GdkClipboard *clipboard = gdk_display_get_primary_clipboard(display);
  // Do not attach text selected within Ntikuma itself.
  if (gdk_clipboard_is_local(clipboard)) { ntk_selection_finish(read, 0); return cancel; }
  static const char *types[] = {"text/plain;charset=utf-8", "UTF8_STRING", "text/plain", NULL};
  gdk_clipboard_read_async(clipboard, types, G_PRIORITY_DEFAULT, read->cancel,
                           ntk_selection_stream, read);
  return cancel;
}

static void ntk_selection_cancel(GCancellable *cancel) { g_cancellable_cancel(cancel); }
static void ntk_selection_unref(GCancellable *cancel) { g_object_unref(cancel); }
*/
import "C"

import (
	"context"
	"errors"
	"runtime/cgo"
	"unicode/utf8"
	"unsafe"

	"ntikuma/internal/selection"
)

type primaryReader struct{ invoke func(func()) }

// NewSelectionReader uses the application's GTK thread without creating another
// display connection or requesting remote-desktop permissions.
func NewSelectionReader(invoke func(func())) selection.Reader { return primaryReader{invoke: invoke} }

func (r primaryReader) Support() selection.Support {
	var code C.int
	r.invoke(func() { code = C.ntk_selection_support() })
	switch code {
	case 1:
		return selection.Support{Available: true, BeforeFocus: true, Message: "Reads highlighted text before the popup opens. The source application must publish its selection."}
	case 2:
		return selection.Support{Available: true, Message: "Reads highlighted text after Ntikuma receives focus. The source application must keep its selection available."}
	default:
		return selection.Support{Message: "Selected text is unavailable in this desktop session. Copy the text and attach Clipboard instead."}
	}
}

type selectionResult struct {
	text string
	err  error
}

func (r primaryReader) Read(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	result := make(chan selectionResult, 1)
	handle := cgo.NewHandle(result)
	var cancel *C.GCancellable
	r.invoke(func() { cancel = C.ntk_selection_start(C.uintptr_t(handle), C.gsize(selection.MaxBytes)) })
	defer C.ntk_selection_unref(cancel)
	select {
	case value := <-result:
		return value.text, value.err
	case <-ctx.Done():
		C.ntk_selection_cancel(cancel)
		return "", ctx.Err()
	}
}

//export ntkSelectionResult
func ntkSelectionResult(id C.uintptr_t, data unsafe.Pointer, length C.int, status C.int) {
	handle := cgo.Handle(id)
	result := handle.Value().(chan selectionResult)
	defer handle.Delete()
	var value selectionResult
	switch status {
	case 0:
		value.text = string(C.GoBytes(data, length))
		if !utf8.ValidString(value.text) {
			value = selectionResult{err: errors.New("selected text was not valid UTF-8")}
		}
	case 2:
		value.err = errors.New("selected text exceeds 4 MiB; select a smaller passage")
	case 3:
		value.err = errors.New("selected text is unavailable in this desktop session")
	default:
		value.err = errors.New("couldn't read selected text; the source application may have cleared it")
	}
	result <- value
}
