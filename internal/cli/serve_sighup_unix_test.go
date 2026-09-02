//go:build darwin || linux

package cli

import (
	"os"
	"syscall"
	"testing"
)

// TestServeReloadsOnRealSIGHUP delivers the actual signal to this process, the
// one thing the reloadSignal seam cannot prove: that `serve` treats a hangup as
// a reload request rather than dying from it, exactly as `systemctl reload`
// will.
//
// Deliberately not parallel. The signal reaches the whole process, and it is
// only safe because cmdServe calls signal.Ignore(SIGHUP) before it opens
// anything — the default disposition (terminate) is gone from the moment the
// first serve test runs, so a hangup can never kill the test binary.
func TestServeReloadsOnRealSIGHUP(t *testing.T) {
	s := startServe(t, false)
	s.health(t)

	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}
	s.awaitLog(t, configReloadedMsg)

	// The process survived the hangup and is still serving; a foreground
	// `serve` outliving its terminal is the point.
	s.health(t)
	if exit := s.stopAndWait(t); exit != 0 {
		t.Fatalf("serve exit = %d, want 0; log:\n%s", exit, s.logs.String())
	}
}
