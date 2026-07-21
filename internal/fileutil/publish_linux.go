//go:build linux

package fileutil

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func publishNoReplace(staging, dst string) error {
	err := unix.Renameat2(unix.AT_FDCWD, staging, unix.AT_FDCWD, dst, unix.RENAME_NOREPLACE)
	if err == nil {
		return nil
	}
	// Fall back for old kernels or filesystems without renameat2 support.
	if errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EINVAL) {
		return os.Link(staging, dst)
	}
	return &os.LinkError{Op: "rename-noreplace", Old: staging, New: dst, Err: err}
}
