package storage

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/fileutil"
)

// A baseline check must not open operator files with SQLite: even read-only
// WAL connections can create or update sidecars. Copying the database costs a
// full sequential read plus a verification read at startup, but keeps this
// inspection portable and leaves rejected databases untouched. WAL pages
// accompany the main file; SQLite recovers them only in our private directory.
// SHM is a disposable index and is rebuilt there. Nonempty rollback journals
// require operator recovery because they may reference external super-journals.
func copyBaselineSnapshot(path string) (string, func(), error) {
	for attempt := 0; attempt < 3; attempt++ {
		snapshot, cleanup, changed, err := copyBaselineSnapshotAttempt(path)
		if err != nil || !changed {
			return snapshot, cleanup, err
		}
	}
	return "", nil, domain.Errorf(domain.ErrAborted, "database changed during baseline inspection; stop writers and retry")
}

type baselineSnapshotFile struct {
	suffix string
	info   os.FileInfo
	digest [sha256.Size]byte
}

func baselineSnapshotInfo(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Do not follow sidecar symlinks or consume special files supplied beside
	// an otherwise ordinary database. The main path is checked the same way.
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("baseline inspection requires regular file %q", path)
	}
	// On Windows Lstat defers loading the file ID until SameFile is called.
	// Resolve it now, while capturing the snapshot, rather than letting a later
	// comparison load the ID of a replacement at the same path. SameFile uses
	// a no-follow metadata handle there; it does not read a symlink's target.
	if !os.SameFile(info, info) {
		return nil, fmt.Errorf("cannot capture baseline file identity for %q", path)
	}
	return info, nil
}

func sameBaselineFile(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return os.SameFile(a, b) && a.Size() == b.Size() && a.Mode() == b.Mode() && a.ModTime().Equal(b.ModTime())
}

func copyBaselineSnapshotAttempt(path string) (snapshot string, cleanup func(), changed bool, err error) {
	dir, err := fileutil.MkdirPrivateTemp(os.TempDir(), "kms-baseline-")
	if err != nil {
		return "", nil, false, fmt.Errorf("create baseline inspection directory: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	defer func() {
		if err != nil || changed {
			cleanup()
			snapshot = ""
		}
	}()
	snapshot = filepath.Join(dir, "database.sqlite")
	files := []baselineSnapshotFile{{suffix: ""}, {suffix: "-wal"}, {suffix: "-journal"}}
	for i := range files {
		files[i].info, err = baselineSnapshotInfo(path + files[i].suffix)
		if err != nil {
			return
		}
	}
	// A rollback journal can name a super-journal outside this directory.
	// Never allow recovery of a copied journal to follow operator-side paths.
	if journal := files[2].info; journal != nil && journal.Size() != 0 {
		err = domain.Errorf(domain.ErrAborted, "database has a nonempty rollback journal; resolve pending recovery before retrying baseline inspection")
		return
	}
	if files[0].info == nil {
		err = fmt.Errorf("database disappeared during baseline inspection")
		return
	}
	for i := range files {
		f := &files[i]
		if f.info == nil {
			continue
		}
		output, createErr := fileutil.OpenPrivateExclusive(snapshot + f.suffix)
		if createErr != nil {
			err = createErr
			return
		}
		f.digest, changed, err = readBaselineSnapshotFile(path+f.suffix, f.info, output)
		closeErr := output.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil || changed {
			return
		}
	}
	// Verify every byte after all files have been copied. Metadata alone is
	// insufficient on filesystems with coarse timestamp resolution. Concurrent
	// writers/checkpoints cause a retry rather than a false compatibility result.
	for _, f := range files {
		var digest [sha256.Size]byte
		digest, changed, err = readBaselineSnapshotFile(path+f.suffix, f.info, io.Discard)
		if err != nil || changed {
			return
		}
		if f.info != nil && digest != f.digest {
			changed = true
			return
		}
	}
	for _, f := range files {
		var info os.FileInfo
		info, err = baselineSnapshotInfo(path + f.suffix)
		if err != nil {
			return
		}
		if !sameBaselineFile(f.info, info) {
			changed = true
			return
		}
	}
	return
}

// readBaselineSnapshotFile also checks absent sidecars, since a WAL appearing
// during the copy is just as significant as a preexisting WAL changing.
func readBaselineSnapshotFile(path string, expected os.FileInfo, output io.Writer) (digest [sha256.Size]byte, changed bool, err error) {
	info, err := baselineSnapshotInfo(path)
	if err != nil || !sameBaselineFile(expected, info) {
		return digest, err == nil, err
	}
	if info == nil {
		return digest, false, nil
	}
	input, err := os.Open(path)
	if os.IsNotExist(err) {
		return digest, true, nil
	}
	if err != nil {
		return digest, false, err
	}
	defer func() {
		if closeErr := input.Close(); err == nil {
			err = closeErr
		}
	}()
	opened, err := input.Stat()
	if err != nil || !sameBaselineFile(expected, opened) {
		return digest, err == nil, err
	}
	h := sha256.New()
	// Bound the read to the captured size even if a writer keeps appending.
	if _, err = io.CopyN(io.MultiWriter(output, h), input, expected.Size()); err == io.EOF {
		return digest, true, nil
	} else if err != nil {
		return digest, false, err
	}
	copy(digest[:], h.Sum(nil))
	return digest, false, nil
}
