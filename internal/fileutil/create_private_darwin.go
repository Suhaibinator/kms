//go:build darwin

package fileutil

import (
	"fmt"
	"os"
)

func openPrivateExclusive(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if err := restrictOwnerOnly(file, false); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("secure private file %s: %w", path, err)
	}
	return file, nil
}

func mkdirPrivateExclusive(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil {
		return err
	}
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open private directory %s: %w", path, err)
	}
	defer dir.Close()
	if err := restrictOwnerOnly(dir, true); err != nil {
		return fmt.Errorf("secure private directory %s: %w", path, err)
	}
	return nil
}
