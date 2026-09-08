//go:build darwin || linux

package cli

import (
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/watch"
)

// Gate the first listener factory so the signal lands before serve enters its
// event loop. Readiness must never precede signal registration, even when a
// listener starts accepting requests while the remaining startup work blocks.
func TestServeCapturesSIGHUPBeforeListenerStartup(t *testing.T) {
	entered := make(chan bool, 1)
	resume := make(chan struct{})
	previous := GRPCFactory
	GRPCFactory = func(_ *core.Service, _ *watch.Hub, _ GRPCConfig) (GRPCServer, error) {
		entered <- signal.Ignored(syscall.SIGHUP)
		<-resume
		return startupSignalGRPCServer{}, nil
	}
	t.Cleanup(func() { GRPCFactory = previous })
	s := startServe(t, false)
	// Release the gate before startServe's cleanup waits for shutdown, including
	// when an assertion fails while the factory is blocked.
	defer close(resume)
	select {
	case ignored := <-entered:
		if ignored {
			t.Fatal("SIGHUP is still ignored when the first listener can open")
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("listener factory was not reached; log:\n%s", s.logs.String())
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("send startup SIGHUP: %v", err)
	}
	// The factory continues only after the signal has been sent. The buffered
	// registration must retain the reload until the event loop can process it.
	resume <- struct{}{}
	s.health(t)
	s.awaitLog(t, configReloadedMsg)
	if exit := s.stopAndWait(t); exit != 0 {
		t.Fatalf("serve exit = %d, want 0; log:\n%s", exit, s.logs.String())
	}
}

type startupSignalGRPCServer struct{}

func (startupSignalGRPCServer) Serve() error  { return nil }
func (startupSignalGRPCServer) GracefulStop() {}
func (startupSignalGRPCServer) Stop()         {}

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
