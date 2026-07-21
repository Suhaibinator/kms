//go:build darwin || linux

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestCopyFileAtomicRefusesDestinationCreatedDuringCopy deterministically puts
// a competing destination between staging-file creation and publication. A
// check-then-rename implementation would overwrite the canary; the atomic
// no-replace primitive must instead return os.ErrExist.
func TestCopyFileAtomicRefusesDestinationCreatedDuringCopy(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "blocked-source")
	destination := filepath.Join(dir, "destination")
	if err := unix.Mkfifo(source, 0o600); err != nil {
		t.Fatalf("create source FIFO: %v", err)
	}

	result := make(chan error, 1)
	go func() { result <- copyFileAtomic(source, destination, false) }()

	var writer *os.File
	deadline := time.Now().Add(5 * time.Second)
	for writer == nil && time.Now().Before(deadline) {
		fd, err := unix.Open(source, unix.O_WRONLY|unix.O_NONBLOCK, 0)
		switch {
		case err == nil:
			writer = os.NewFile(uintptr(fd), source)
		case errors.Is(err, syscall.ENXIO):
			select {
			case err := <-result:
				t.Fatalf("copy stopped before opening the FIFO: %v", err)
			case <-time.After(5 * time.Millisecond):
			}
		default:
			t.Fatalf("open source FIFO writer: %v", err)
		}
	}
	if writer == nil {
		t.Fatal("copy did not open the source FIFO")
	}
	defer func() { _ = writer.Close() }()

	// The visible private staging file proves validation is complete and the
	// copy is blocked in io.Copy, before the final publication operation.
	for time.Now().Before(deadline) {
		matches, err := filepath.Glob(filepath.Join(dir, ".kms-restore-*"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) == 1 {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("copy stopped before creating its staging file: %v", err)
		case <-time.After(5 * time.Millisecond):
		}
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, ".kms-restore-*")); len(matches) != 1 {
		t.Fatalf("restore staging file was not observable: %v", matches)
	}

	const canary = "concurrent destination"
	if err := os.WriteFile(destination, []byte(canary), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("restored data")); err != nil {
		t.Fatalf("release blocked copy: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close source FIFO: %v", err)
	}

	select {
	case err := <-result:
		if !errors.Is(err, os.ErrExist) {
			t.Fatalf("copy error = %v, want os.ErrExist", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("copy did not finish after releasing the FIFO")
	}
	if got, err := os.ReadFile(destination); err != nil || string(got) != canary {
		t.Fatalf("concurrent destination = %q, %v; want preserved canary", got, err)
	}
	if matches, err := filepath.Glob(filepath.Join(dir, ".kms-restore-*")); err != nil || len(matches) != 0 {
		t.Fatalf("restore staging files remain: %v (glob err %v)", matches, err)
	}
}
