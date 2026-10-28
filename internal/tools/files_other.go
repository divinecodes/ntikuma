//go:build !linux

package tools

import (
	"errors"
	"os"
)

func openReadOnly(_ *os.Root, _ string) (*os.File, error) {
	return nil, errors.New("local file tools currently require Linux")
}
