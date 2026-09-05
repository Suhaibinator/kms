package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Suhaibinator/kms/internal/fileutil"
	"github.com/Suhaibinator/kms/internal/storage"
)

// sqliteMagic is the 16-byte header every SQLite database begins with.
const sqliteMagic = "SQLite format 3\x00"

// restoreFile copies a backup database at in over dst after validating that in
// is a KMS SQLite database. It refuses to overwrite an existing dst unless
// force is set, publishes the copy atomically, and removes stale WAL/SHM files.
func restoreFile(in, dst string, force bool) error {
	if err := validateSQLiteFile(in); err != nil {
		return err
	}
	if err := storage.ValidateKMSDatabase(in); err != nil {
		return fmt.Errorf("invalid KMS backup %s: %w", in, err)
	}
	inAbs, err := filepath.Abs(in)
	if err != nil {
		return err
	}
	dstAbs, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	stableDst, err := fileutil.ResolveStablePath(dstAbs)
	if err != nil {
		return fmt.Errorf("validate restore destination %s: %w", dst, err)
	}
	inInfo, err := os.Stat(inAbs)
	if err != nil {
		return err
	}
	dstInfo, dstErr := os.Stat(stableDst)
	if inAbs == stableDst || (dstErr == nil && os.SameFile(inInfo, dstInfo)) {
		return errors.New("input and destination are the same file")
	}
	if err := copyFileAtomic(in, stableDst, force); err != nil {
		if !force && errors.Is(err, os.ErrExist) {
			return fmt.Errorf("destination %s already exists; pass --force to overwrite", dst)
		}
		return fmt.Errorf("copying backup: %w", err)
	}
	// A restored database must not inherit sidecar journals from a previous db.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(stableDst + suffix); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing stale %s: %w", stableDst+suffix, err)
		}
	}
	// Validate the current baseline through the already-resolved spelling.
	// Reopening the caller's original path here would reintroduce a parent-symlink
	// swap after the atomic publication completed.
	store, err := storage.Open(stableDst)
	if err != nil {
		return fmt.Errorf("restored database failed to open: %w", err)
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("closing restored database: %w", err)
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

func copyFileAtomic(src, dst string, force bool) error {
	stableDst, err := fileutil.ResolveStablePath(dst)
	if err != nil {
		return err
	}
	dst = stableDst
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := fileutil.CreatePrivateTemp(filepath.Dir(dst), ".kms-restore-")
	if err != nil {
		return err
	}
	tmp := out.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if force {
		return os.Rename(tmp, dst)
	}
	// Publish the completed staging file with the platform's atomic no-replace
	// primitive. Any directory entry (including a symlink) that appeared at dst
	// after validation makes publication fail closed.
	return fileutil.PublishNoReplace(tmp, dst)
}
