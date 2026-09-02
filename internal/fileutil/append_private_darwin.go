//go:build darwin

package fileutil

import (
	"fmt"
	"os"
	"syscall"
)

func openPrivateAppendNew(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	// A new file inherits allow ACLs from its directory that the mode bits do
	// not show, so clear them the way openPrivateExclusive does.
	if err := restrictOwnerOnly(file, false); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("secure private file %s: %w", path, err)
	}
	return file, nil
}

func openPrivateAppendExisting(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0)
}
