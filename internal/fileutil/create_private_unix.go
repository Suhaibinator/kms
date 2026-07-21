//go:build !windows && !darwin

package fileutil

import "os"

func openPrivateExclusive(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
}

func mkdirPrivateExclusive(path string) error {
	return os.Mkdir(path, 0o700)
}
