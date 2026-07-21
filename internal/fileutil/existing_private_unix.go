//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package fileutil

import (
	"fmt"
	"os"
	"syscall"
)

func requireCurrentUserOwner(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot inspect file owner")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("file is owned by uid %d, current uid is %d", stat.Uid, os.Geteuid())
	}
	return nil
}
