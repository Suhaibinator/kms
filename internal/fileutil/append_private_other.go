//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package fileutil

import (
	"fmt"
	"os"
)

func openPrivateAppendNew(path string) (*os.File, error) {
	return nil, fmt.Errorf("private append is unsupported on this platform for %s", path)
}

func openPrivateAppendExisting(path string) (*os.File, error) {
	return nil, fmt.Errorf("private append is unsupported on this platform for %s", path)
}
