//go:build !linux || !cgo || gtk3

package platform

import (
	"context"
	"errors"

	"ntikuma/internal/selection"
)

type unavailableSelection struct{}

func NewSelectionReader(func(func())) selection.Reader { return unavailableSelection{} }
func (unavailableSelection) Support() selection.Support {
	return selection.Support{Message: "Selected-text capture requires the Linux GTK4 build. Copy the text and attach Clipboard instead."}
}
func (unavailableSelection) Read(context.Context) (string, error) {
	return "", errors.New("selected-text capture is unavailable in this build")
}
