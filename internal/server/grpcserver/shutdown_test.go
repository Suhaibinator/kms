package grpcserver

import (
	"context"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
)

// TestForcedStopUnblocksActiveWatchStream locks in the escape hatch that
// serve.go's bounded shutdown relies on: a long-lived Subscribe stream keeps
// GracefulStop blocked (it waits for in-flight RPCs), and Stop force-closes the
// stream so the graceful stop can complete. This is the opposite of the normal
// test pattern (which closes the client first); it reproduces the SIGTERM path
// with a still-connected hot-reload client.
func TestForcedStopUnblocksActiveWatchStream(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "app"})

	ctx, cancel := context.WithCancel(adminCtx())
	defer cancel()

	stream, err := env.watchClient().Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := stream.Send(&kmsv1.SubscribeRequest{ClientName: "shutdown-app", Namespaces: []*kmsv1.NamespaceRef{pNS("prod", "app")}}); err != nil {
		t.Fatalf("send registration: %v", err)
	}
	// Wait for the initial snapshot so the stream is definitely established and
	// registered before we shut down.
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("recv snapshot: %v", err)
	}

	gracefulDone := make(chan struct{})
	go func() {
		env.srv.GracefulStop()
		close(gracefulDone)
	}()

	// With the stream open, GracefulStop must NOT complete on its own.
	select {
	case <-gracefulDone:
		t.Fatal("GracefulStop returned while a watch stream was still open; it should block until Stop()")
	case <-time.After(300 * time.Millisecond):
	}

	// The forced Stop cancels the stream's context, so GracefulStop unwinds.
	env.srv.Stop()
	select {
	case <-gracefulDone:
	case <-time.After(5 * time.Second):
		t.Fatal("GracefulStop did not return after Stop(); forced shutdown is not unblocking active streams")
	}

	// The client observes the stream terminate rather than hanging. Drain any
	// heartbeat events already buffered before the terminal error arrives.
	recvErr := make(chan error, 1)
	go func() {
		for {
			if _, err := stream.Recv(); err != nil {
				recvErr <- err
				return
			}
		}
	}()
	select {
	case err := <-recvErr:
		if err == nil {
			t.Fatal("expected the watch stream to error after forced shutdown")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watch stream did not terminate after forced shutdown")
	}
}
