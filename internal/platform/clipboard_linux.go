//go:build linux && cgo && !gtk3

package platform

/*
#cgo pkg-config: gtk4
#include <gtk/gtk.h>

// The MIME types offered for the clipboard's current data, or NULL without a
// display. GTK owns the array.
static const char * const *ntk_clipboard_types(gsize *count) {
  *count = 0;
  GdkDisplay *display = gdk_display_get_default();
  if (!display) return NULL;
  GdkContentFormats *formats = gdk_clipboard_get_formats(gdk_display_get_clipboard(display));
  return gdk_content_formats_get_mime_types(formats, count);
}
*/
import "C"

import "unsafe"

// clipboardTypes runs on the GTK thread and copies the types before returning.
func clipboardTypes() []string {
	var count C.gsize
	types := C.ntk_clipboard_types(&count)
	if types == nil || count == 0 {
		return nil
	}
	out := make([]string, 0, int(count))
	for _, t := range unsafe.Slice(types, int(count)) {
		out = append(out, C.GoString(t))
	}
	return out
}
