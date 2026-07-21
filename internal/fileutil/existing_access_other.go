//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package fileutil

import (
	"fmt"
	"os"
)

func requireAlreadyPrivate(*os.File, string) error {
	return fmt.Errorf("existing access checks are unsupported on this platform")
}
