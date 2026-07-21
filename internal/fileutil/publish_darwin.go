//go:build darwin

package fileutil

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func publishNoReplace(staging, dst string) error {
	err := unix.RenamexNp(staging, dst, unix.RENAME_EXCL)
	if err == nil {
		return nil
	}
	// Older or unusual filesystems may not implement exclusive rename. A hard
	// link retains the same atomic/no-replace semantics when available.
	if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EINVAL) {
		return os.Link(staging, dst)
	}
	return &os.LinkError{Op: "rename-noreplace", Old: staging, New: dst, Err: err}
}
