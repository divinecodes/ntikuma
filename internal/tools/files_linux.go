package tools

import (
	"os"
	"syscall"
)

func openReadOnly(root *os.Root, relative string) (*os.File, error) {
	// A FIFO must not block opening before the regular-file check can reject it.
	return root.OpenFile(relative, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
