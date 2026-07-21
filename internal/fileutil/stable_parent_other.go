//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package fileutil

import "fmt"

func requireStableDirectoryChain(path string) error {
	return fmt.Errorf("entry-stability checks are unsupported on this platform for %s", path)
}

func requireStablePathSpelling(path string) error {
	return fmt.Errorf("entry-stability checks are unsupported on this platform for %s", path)
}
