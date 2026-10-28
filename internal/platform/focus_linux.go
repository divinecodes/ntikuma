//go:build linux && cgo && !gtk3

package platform

/*
#cgo pkg-config: gtk4
#include <stdlib.h>
#include <gtk/gtk.h>

static void ntk_set_startup_id(void *win, const char *token){
  if (win == NULL || token == NULL || token[0] == '\0') return;
  gtk_window_set_startup_id(GTK_WINDOW(win), token);
}
*/
import "C"

import "unsafe"

// SetStartupID hands an xdg-activation token to the window before it is
// shown, so GNOME treats the show as user-initiated and gives it focus instead
// of posting an "is ready" notification. nativeWindow is the GtkWindow pointer
// from (*application.WebviewWindow).NativeWindow().
//
// Must run on the GTK main thread (wrap it in application.InvokeSync).
func SetStartupID(nativeWindow unsafe.Pointer, token string) {
	if nativeWindow == nil || token == "" {
		return
	}

	ct := C.CString(token)
	defer C.free(unsafe.Pointer(ct))
	C.ntk_set_startup_id(nativeWindow, ct)
}
