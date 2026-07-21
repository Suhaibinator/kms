//go:build aix || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package fileutil

import (
	"fmt"
	"os"
)

func requireAlreadyPrivate(file *os.File, _ string) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("mode %04o grants group or other access", info.Mode().Perm())
	}
	return nil
}
