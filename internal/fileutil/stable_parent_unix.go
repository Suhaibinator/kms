//go:build aix || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package fileutil

import (
	"fmt"
	"os"
	"syscall"
)

func requireStableDirectoryChain(path string) error {
	euid := uint32(os.Geteuid())
	for _, current := range stablePathChain(path) {
		info, err := os.Stat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", current)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("cannot inspect owner of %s", current)
		}
		if stat.Uid != 0 && stat.Uid != euid {
			return fmt.Errorf("%s is owned by uid %d", current, stat.Uid)
		}
		// Write+execute on a directory permits entry replacement. A sticky
		// directory owned by this account or root (for example /tmp) still
		// prevents other accounts from renaming our private staging entry.
		if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return fmt.Errorf("%s is group- or other-writable without the sticky bit", current)
		}
	}
	return nil
}

func requireStablePathSpelling(path string) error {
	euid := uint32(os.Geteuid())
	for _, current := range stablePathChain(path) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("cannot inspect owner of %s", current)
		}
		if stat.Uid != 0 && stat.Uid != euid {
			return fmt.Errorf("%s is owned by uid %d", current, stat.Uid)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory or trusted symlink", current)
		}
		if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return fmt.Errorf("%s is group- or other-writable without the sticky bit", current)
		}
	}
	return nil
}
