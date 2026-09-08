package storage

import (
	"bytes"
	"errors"
	"github.com/Suhaibinator/kms/internal/domain"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type baselineArtifact struct {
	info os.FileInfo
	data []byte
}

func captureBaselineArtifacts(t *testing.T, path string) map[string]baselineArtifact {
	t.Helper()
	out := map[string]baselineArtifact{}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		info, err := os.Stat(path + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		out[suffix] = baselineArtifact{info, data}
	}
	return out
}
func assertBaselineArtifactsUnchanged(t *testing.T, path string, before map[string]baselineArtifact) {
	t.Helper()
	after := captureBaselineArtifacts(t, path)
	if len(before) != len(after) {
		t.Errorf("sidecar set changed: before %v after %v", artifactNames(before), artifactNames(after))
	}
	for suffix, want := range before {
		got, ok := after[suffix]
		if !ok || !sameBaselineFile(want.info, got.info) || !bytes.Equal(want.data, got.data) {
			t.Errorf("operator artifact %q changed", suffix)
		}
	}
}
func artifactNames(files map[string]baselineArtifact) []string {
	out := []string{}
	for suffix := range files {
		out = append(out, suffix)
	}
	return out
}
func rawBaselineDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(sqliteFileURI(filepath.ToSlash(path))+"?_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(0)"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	return db
}
func execBaselineSQL(t *testing.T, db *gorm.DB, sql string) {
	t.Helper()
	if err := db.Exec(sql).Error; err != nil {
		t.Fatal(err)
	}
}
func TestBaselineInspectionRejectsClosedWALWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unsupported.db")
	db := rawBaselineDB(t, path)
	execBaselineSQL(t, db, "CREATE TABLE operator_data (id INTEGER)")
	sqlDB, _ := db.DB()
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	before := captureBaselineArtifacts(t, path)
	if len(before) != 1 {
		t.Fatal("closed WAL fixture has sidecars")
	}
	if st, err := Open(path); err == nil {
		st.Close()
		t.Fatal("unsupported database accepted")
	}
	assertBaselineArtifactsUnchanged(t, path, before)
}
func TestBaselineInspectionReadsUncheckpointedWALWithoutMutation(t *testing.T) {
	for _, change := range []string{"UPDATE schema_migrations SET version=2", "CREATE TABLE operator_data (id INTEGER)"} {
		t.Run(change, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "old.db")
			st, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = st.Close(); err != nil {
				t.Fatal(err)
			}
			db := rawBaselineDB(t, path)
			execBaselineSQL(t, db, change)
			before := captureBaselineArtifacts(t, path)
			if len(before["-wal"].data) == 0 {
				t.Fatal("fixture has no WAL")
			}
			// Prove the incompatible update has not reached the main database.
			mainCopy := filepath.Join(t.TempDir(), "main.db")
			if err = os.WriteFile(mainCopy, before[""].data, 0600); err != nil {
				t.Fatal(err)
			}
			if err = ValidateKMSDatabase(mainCopy); err != nil {
				t.Fatalf("main file should still be supported: %v", err)
			}
			if err = ValidateKMSDatabase(path); err == nil {
				t.Fatal("uncheckpointed incompatible baseline accepted")
			}
			if reopened, err := Open(path); err == nil {
				reopened.Close()
				t.Fatal("Open accepted incompatible WAL")
			}
			assertBaselineArtifactsUnchanged(t, path, before)
		})
	}
}
func TestBaselineInspectionSupportedReopenAndSafeNames(t *testing.T) {
	names := []string{"kms.db", "spaces # %.db"}
	if runtime.GOOS != "windows" {
		names = append(names, "question ?.db")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			st, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			st.Close()
			// A supported uncheckpointed write must also remain visible in inspection.
			db := rawBaselineDB(t, path)
			execBaselineSQL(t, db, "UPDATE schema_migrations SET applied_at='from-wal'")
			before := captureBaselineArtifacts(t, path)
			if err := ValidateKMSDatabase(path); err != nil {
				t.Fatal(err)
			}
			assertBaselineArtifactsUnchanged(t, path, before)
			copyPath, cleanup, err := copyBaselineSnapshot(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cleanup)
			info, err := os.Stat(filepath.Dir(copyPath))
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0700 {
				t.Errorf("snapshot directory permissions: %v", info.Mode())
			}
			info, err = os.Stat(copyPath)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
				t.Errorf("snapshot file permissions: %v", info.Mode())
			}
			snapshot := rawBaselineDB(t, copyPath)
			var applied string
			if err = snapshot.Raw("SELECT applied_at FROM schema_migrations").Scan(&applied).Error; err != nil {
				t.Fatal(err)
			}
			if applied != "from-wal" {
				t.Fatalf("WAL value lost: %q", applied)
			}
			snapDB, _ := snapshot.DB()
			snapDB.Close()
			cleanup()
			if _, err = os.Stat(filepath.Dir(copyPath)); !os.IsNotExist(err) {
				t.Errorf("snapshot directory not cleaned: %v", err)
			}
			liveDB, _ := db.DB()
			liveDB.Close()
			reopened, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			reopened.Close()
		})
	}
}

func TestBaselineSnapshotDetectsSidecarReplacementAndAppearance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database-wal")
	if err := os.WriteFile(path, []byte("wal bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	original, err := baselineSnapshotInfo(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, changed, err := readBaselineSnapshotFile(path, nil, io.Discard); err != nil || !changed {
		t.Fatalf("new WAL: changed=%v err=%v", changed, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := readBaselineSnapshotFile(path, original, io.Discard); err != nil || !changed {
		t.Fatalf("removed WAL: changed=%v err=%v", changed, err)
	}
	// Same bytes/size/mtime still do not make a replacement file the same WAL.
	other := filepath.Join(t.TempDir(), "replacement")
	if err := os.WriteFile(other, []byte("wal bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, original.ModTime(), original.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, path); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := readBaselineSnapshotFile(path, original, io.Discard); err != nil || !changed {
		t.Fatalf("replaced WAL: changed=%v err=%v", changed, err)
	}
}

func TestBaselineSnapshotCleansTemporaryFilesAfterFailure(t *testing.T) {
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	t.Setenv("TMP", tempRoot)
	t.Setenv("TEMP", tempRoot)
	source := filepath.Join(t.TempDir(), "database")
	if err := os.WriteFile(source, []byte("operator data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(source+"-wal", 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := copyBaselineSnapshot(source); err == nil {
		t.Fatal("nonregular WAL accepted")
	}
	entries, err := os.ReadDir(tempRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "kms-baseline-") {
			t.Errorf("temporary snapshot leaked: %s", entry.Name())
		}
	}
}

func TestBaselineInspectionRejectsNonemptyRollbackJournalWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if err = os.WriteFile(path+"-journal", []byte("journal with possible external references"), 0600); err != nil {
		t.Fatal(err)
	}
	before := captureBaselineArtifacts(t, path)
	if err = ValidateKMSDatabase(path); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("expected recovery precondition, got %v", err)
	}
	assertBaselineArtifactsUnchanged(t, path, before)
}
