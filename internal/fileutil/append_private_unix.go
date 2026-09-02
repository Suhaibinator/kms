//go:build aix || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package fileutil

import (
	"os"
	"syscall"
)

func openPrivateAppendNew(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
}

func openPrivateAppendExisting(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0)
}
