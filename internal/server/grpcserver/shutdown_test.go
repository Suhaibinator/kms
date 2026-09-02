package grpcserver

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc/test/bufconn"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/watch"
)

type blockingPingStore struct {
	*memStore
	pingStarted chan struct{}
	pingRelease chan struct{}
	startOnce   sync.Once
}

func (s *blockingPingStore) Ping(ctx context.Context) error {
	s.startOnce.Do(func() { close(s.pingStarted) })
	select {
	case <-s.pingRelease:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TestStopWaitsForHealthRefresh proves shutdown joins the readiness worker. A
// readiness probe may be inside SQLite while Stop begins; returning before it
// finishes lets the test harness remove the database directory while SQLite is
// still creating or deleting WAL sidecars.
func TestStopWaitsForHealthRefresh(t *testing.T) {
	store := &blockingPingStore{
		memStore:    newMemStore(),
		pingStarted: make(chan struct{}),
		pingRelease: make(chan struct{}),
	}
	releasePing := func() {
		select {
		case <-store.pingRelease:
		default:
			close(store.pingRelease)
		}
	}
	t.Cleanup(releasePing)

	logger := zap.NewNop()
	svc := core.New(store, logger, "shutdown-test")
	kek, err := crypto.NewKEKFromMaterial("kek-1", make([]byte, 32))
	if err != nil {
		t.Fatalf("build kek: %v", err)
	}
	svc.SetKeyring(crypto.NewKeyring(kek))
	hub := watch.NewHub(store, logger, watch.Options{})
	svc.SetHub(hub)
	srv, err := New(svc, hub, Config{HealthRefreshInterval: time.Hour})
	if err != nil {
		t.Fatalf("New server: %v", err)
	}
	lis := bufconn.Listen(1 << 20)
	t.Cleanup(func() { _ = lis.Close() })
	serveDone := make(chan struct{})
	go func() {
		_ = srv.Serve(lis)
		close(serveDone)
	}()

	select {
	case <-store.pingStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("health refresh did not begin")
	}

	stopDone := make(chan struct{})
	go func() {
		srv.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
		t.Fatal("Stop returned while the health refresh was still running")
	case <-time.After(100 * time.Millisecond):
	}

	releasePing()
	select {
	case <-stopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return after the health refresh finished")
	}
	select {
	case <-serveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after Stop")
	}
}

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
