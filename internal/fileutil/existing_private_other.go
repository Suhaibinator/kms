//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package fileutil

import (
	"fmt"
	"os"
)

func requireCurrentUserOwner(*os.File) error {
	return fmt.Errorf("file ownership checks are unsupported on this platform")
}
