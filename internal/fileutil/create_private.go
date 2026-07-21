package fileutil

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// OpenPrivateExclusive atomically creates a new owner-only file. Unlike a
// create-then-chmod sequence, the Windows implementation supplies the DACL to
// CreateFile so another account cannot retain a handle opened in between.
func OpenPrivateExclusive(path string) (*os.File, error) {
	stablePath, err := ResolveStablePath(path)
	if err != nil {
		return nil, err
	}
	return openPrivateExclusive(stablePath)
}

// CreatePrivateTemp atomically creates a random owner-only staging file.
func CreatePrivateTemp(dir, prefix string) (*os.File, error) {
	for i := 0; i < 100; i++ {
		name, err := privateTempName(prefix)
		if err != nil {
			return nil, err
		}
		file, err := OpenPrivateExclusive(filepath.Join(dir, name))
		if os.IsExist(err) {
			continue
		}
		return file, err
	}
	return nil, fmt.Errorf("create private temporary file: too many name collisions")
}

// MkdirPrivateTemp atomically creates a random owner-only staging directory.
func MkdirPrivateTemp(dir, prefix string) (string, error) {
	stableCheck, err := ResolveStablePath(filepath.Join(dir, prefix+"parent-check"))
	if err != nil {
		return "", err
	}
	stableDir := filepath.Dir(stableCheck)
	for i := 0; i < 100; i++ {
		name, err := privateTempName(prefix)
		if err != nil {
			return "", err
		}
		path := filepath.Join(stableDir, name)
		err = mkdirPrivateExclusive(path)
		if os.IsExist(err) {
			continue
		}
		return path, err
	}
	return "", fmt.Errorf("create private temporary directory: too many name collisions")
}

func privateTempName(prefix string) (string, error) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(random[:]), nil
}
