//go:build !windows && !darwin

package fileutil

import "os"

func restrictOwnerOnly(file *os.File, directory bool) error {
	mode := os.FileMode(0o600)
	if directory {
		mode = 0o700
	}
	return file.Chmod(mode)
}

func openForOwnerRestriction(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR, 0)
}
