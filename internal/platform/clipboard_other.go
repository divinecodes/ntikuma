//go:build !linux || !cgo || gtk3

package platform

func clipboardTypes() []string { return nil }
