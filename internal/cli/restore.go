package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// sqliteMagic is the 16-byte header every SQLite database begins with.
const sqliteMagic = "SQLite format 3\x00"

// restoreFile copies a backup database at in over dst after validating that in
// is a SQLite file. It refuses to overwrite an existing dst unless force is
// set, and removes any stale WAL/SHM sidecar files so the restored database is
// self-consistent. It is a pure file operation with no storage dependency so
// it can be exercised directly in tests.
func restoreFile(in, dst string, force bool) error {
	if err := validateSQLiteFile(in); err != nil {
		return err
	}
	inAbs, err := filepath.Abs(in)
	if err != nil {
		return err
	}
	dstAbs, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	if inAbs == dstAbs {
		return errors.New("input and destination are the same file")
	}
	if fileExists(dst) && !force {
		return fmt.Errorf("destination %s already exists; pass --force to overwrite", dst)
	}

	if err := copyFile(in, dst); err != nil {
		return fmt.Errorf("copying backup: %w", err)
	}
	// A restored database must not inherit sidecar journals from a previous db.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(dst + suffix); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing stale %s: %w", dst+suffix, err)
		}
	}
	return nil
}

// validateSQLiteFile confirms path exists and starts with the SQLite magic.
func validateSQLiteFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening input %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var header [16]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return fmt.Errorf("input %s is not a valid SQLite database", path)
	}
	if string(header[:]) != sqliteMagic {
		return fmt.Errorf("input %s is not a valid SQLite database", path)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
