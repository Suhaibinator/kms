//go:build !darwin && !linux && !windows

package fileutil

import "os"

func publishNoReplace(staging, dst string) error {
	return os.Link(staging, dst)
}
