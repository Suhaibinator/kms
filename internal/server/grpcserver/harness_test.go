package grpcserver

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/watch"
)

const (
	adminToken  = "admin-secret-token"
	clientToken = "client-secret-token"
)

type testEnv struct {
	store *memStore
	svc   *core.Service
	hub   *watch.Hub
	srv   *Server
	conn  *grpc.ClientConn
}

func (e *testEnv) param() kmsv1.ParameterServiceClient {
	return kmsv1.NewParameterServiceClient(e.conn)
}
func (e *testEnv) secret() kmsv1.SecretServiceClient     { return kmsv1.NewSecretServiceClient(e.conn) }
func (e *testEnv) admin() kmsv1.AdminServiceClient       { return kmsv1.NewAdminServiceClient(e.conn) }
func (e *testEnv) watchClient() kmsv1.WatchServiceClient { return kmsv1.NewWatchServiceClient(e.conn) }

// newTestEnv wires a real core.Service and watch.Hub over an in-memory store and
// serves them on an in-process bufconn listener. When ready is false the
// service has no keyring and reports not-ready.
func newTestEnv(t *testing.T, ready bool) *testEnv {
	t.Helper()
	store := newMemStore()
	store.addIdentity("admin", domain.IdentityKindAdmin, adminToken)
	store.addIdentity("client", domain.IdentityKindClient, clientToken)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := core.New(store, logger, "v-test")
	if ready {
		kek, err := crypto.NewKEKFromMaterial("kek-1", make([]byte, 32))
		if err != nil {
			t.Fatalf("build kek: %v", err)
		}
		svc.SetKeyring(crypto.NewKeyring(kek))
	}

	// Fast heartbeats keep the e2e tests quick, while a generous missed-heartbeat
	// count keeps the liveness window wide enough that normal test latency never
	// trips a spurious drop.
	hub := watch.NewHub(store, logger, watch.Options{
		HeartbeatInterval: 60 * time.Millisecond,
		MissedHeartbeats:  25,
		PruneInterval:     time.Hour,
	})
	svc.SetHub(hub)

	hubCtx, hubCancel := context.WithCancel(context.Background())
	go func() { _ = hub.Run(hubCtx) }()
	select {
	case <-hub.Started():
	case <-time.After(2 * time.Second):
		t.Fatal("hub did not start")
	}

	srv, err := New(svc, hub, Config{})
	if err != nil {
		t.Fatalf("New server: %v", err)
	}
	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	t.Cleanup(func() {
		_ = conn.Close()
		srv.GracefulStop()
		hubCancel()
		_ = lis.Close()
	})

	return &testEnv{store: store, svc: svc, hub: hub, srv: srv, conn: conn}
}

// authCtx returns a context carrying a bearer token (and optional secret token).
func authCtx(token string) context.Context {
	md := metadata.Pairs("authorization", "Bearer "+token)
	return metadata.NewOutgoingContext(context.Background(), md)
}

func adminCtx() context.Context  { return authCtx(adminToken) }
func clientCtx() context.Context { return authCtx(clientToken) }
