//go:build !linux

package tools

import (
	"context"
	"errors"
	"time"
)

func runCommand(context.Context, string, time.Duration) (string, error) {
	return "", errors.New("shell commands are supported only on Linux")
}
