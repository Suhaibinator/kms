//go:build darwin

package fileutil

import "os"

func restrictOwnerOnly(file *os.File, directory bool) error {
	if err := clearDarwinACL(file); err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if directory {
		mode = 0o700
	}
	return file.Chmod(mode)
}

func openForOwnerRestriction(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR, 0)
}
