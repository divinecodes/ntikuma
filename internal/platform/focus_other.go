//go:build !linux || !cgo

// Package platform holds the small bits of native glue Wails doesn't expose.
package platform

import "unsafe"

// SetStartupID is a no-op outside Linux.
func SetStartupID(unsafe.Pointer, string) {}
