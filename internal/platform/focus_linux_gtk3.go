//go:build linux && cgo && gtk3

// Package platform holds the small bits of native glue Wails doesn't expose.
package platform

/*
#cgo pkg-config: gtk+-3.0
#include <stdlib.h>
#include <gtk/gtk.h>

static void ntk_set_startup_id(void *win, const char *token) {
	if (win == NULL || token == NULL || token[0] == '\0') return;
	gtk_window_set_startup_id(GTK_WINDOW(win), token);
}
*/
import "C"

import "unsafe"

// SetStartupID is the GTK3 build of the same helper; see focus_linux.go.
func SetStartupID(nativeWindow unsafe.Pointer, token string) {
	if nativeWindow == nil || token == "" {
		return
	}
	ct := C.CString(token)
	defer C.free(unsafe.Pointer(ct))
	C.ntk_set_startup_id(nativeWindow, ct)
}
