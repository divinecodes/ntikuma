// Package selection defines access to the primary text selection, independently
// of the normal copy/paste clipboard.
package selection

import "context"

// MaxBytes bounds transfers from another application, including when the
// configured outgoing text-context limit is unlimited.
const MaxBytes = 4 * 1024 * 1024

type Support struct {
	Available   bool   `json:"available"`
	BeforeFocus bool   `json:"beforeFocus"`
	Message     string `json:"message"`
}

type Reader interface {
	Support() Support
	Read(context.Context) (string, error)
}
