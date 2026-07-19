package grpcserver

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
	"github.com/Suhaibinator/kms/internal/watch"
)

func TestConfigurationReleaseGRPCLifecycleAndWatch(t *testing.T) {
	ctx := context.Background()
	st, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ns := domain.NamespaceRef{Env: "prod", App: "app"}
	if _, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: ns}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIdentity(ctx, storage.CreateIdentityParams{Name: "admin", Kind: domain.IdentityKindAdmin, TokenHash: crypto.TokenHash(adminToken)}); err != nil {
		t.Fatal(err)
	}
	svc := core.New(st, nil, "test")
	kek, err := crypto.NewKEKFromMaterial("kek", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc.SetKeyring(crypto.NewKeyring(kek))
	hub := watch.NewHub(st, nil, watch.Options{HeartbeatInterval: time.Second, PruneInterval: time.Hour})
	svc.SetHub(hub)
	hubCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = hub.Run(hubCtx) }()
	<-hub.Started()
	srv, err := New(svc, hub, Config{})
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()
	defer func() { srv.GracefulStop(); _ = lis.Close() }()
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	param := kmsv1.NewParameterServiceClient(conn)
	if _, err := param.PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Ref: pRef("prod", "app", "config"), Value: `{"enabled":true}`, ContentType: "json"}); err != nil {
		t.Fatal(err)
	}
	releases := kmsv1.NewConfigurationReleaseServiceClient(conn)
	created, err := releases.CreateRelease(adminCtx(), &kmsv1.CreateReleaseRequest{Namespace: pNS("prod", "app"), Name: "runtime", Entries: []*kmsv1.ReleaseEntrySelector{{Alias: "settings", Kind: "parameter", Ref: pRef("prod", "app", "config"), Label: "current"}}})
	if err != nil {
		t.Fatal(err)
	}
	if created.GetRelease().GetVersion() != 1 || created.GetRelease().GetDigest() == "" {
		t.Fatalf("created=%+v", created.GetRelease())
	}
	active, err := releases.ActivateRelease(adminCtx(), &kmsv1.ActivateReleaseRequest{Namespace: pNS("prod", "app"), Name: "runtime", Version: 1, ExpectedCurrentVersion: ptrUint64(0)})
	if err != nil {
		t.Fatal(err)
	}
	if !active.GetChanged() || active.GetActivationRevision() == 0 {
		t.Fatalf("active=%+v", active)
	}
	createdV2, err := releases.CreateRelease(adminCtx(), &kmsv1.CreateReleaseRequest{Namespace: pNS("prod", "app"), Name: "runtime", Entries: []*kmsv1.ReleaseEntrySelector{{Alias: "settings", Kind: "parameter", Ref: pRef("prod", "app", "config"), Label: "current"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := releases.ActivateRelease(adminCtx(), &kmsv1.ActivateReleaseRequest{Namespace: pNS("prod", "app"), Name: "runtime", Version: createdV2.GetRelease().GetVersion(), ExpectedCurrentVersion: ptrUint64(0)}); status.Code(err) != codes.Aborted {
		t.Fatalf("stale CAS code=%s err=%v, want Aborted", status.Code(err), err)
	}
	stream, err := releases.WatchRelease(adminCtx())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Register{Register: &kmsv1.ReleaseWatchRegistration{Namespace: pNS("prod", "app"), Name: "runtime", ClientName: "api", InstanceId: "replica-1"}}}); err != nil {
		t.Fatal(err)
	}
	event, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if event.GetSnapshot().GetRelease().GetVersion() != 1 || event.GetRevision() != active.GetActivationRevision() {
		t.Fatalf("snapshot=%+v", event)
	}
	if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Acknowledgement{Acknowledgement: &kmsv1.ReleaseAcknowledgement{Namespace: pNS("prod", "app"), Name: "runtime", Version: 1, ActivationRevision: active.GetActivationRevision(), ClientName: "api", InstanceId: "replica-1", State: "received", Diagnostic: "must-not-persist"}}}); err != nil {
		t.Fatal(err)
	}
	admin := kmsv1.NewAdminServiceClient(conn)
	deadline := time.Now().Add(time.Second)
	for {
		states, err := admin.ListReleaseSubscribers(adminCtx(), &kmsv1.ListReleaseSubscribersRequest{Namespace: pNS("prod", "app"), ReleaseName: "runtime"})
		if err != nil {
			t.Fatal(err)
		}
		for _, subscriber := range states.GetSubscribers() {
			if subscriber.GetState() != domain.ReleaseStateReceived {
				continue
			}
			if subscriber.GetDiagnostic() != "[redacted]" || !subscriber.GetConnected() {
				t.Fatalf("subscriber=%+v", subscriber)
			}
			_ = stream.CloseSend()
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("acknowledgement was not persisted")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func ptrUint64(v uint64) *uint64 { return &v }

func TestReleaseConnectionOverlapDoesNotReportPrematureDisconnect(t *testing.T) {
	h := &configurationReleaseServer{connections: make(map[releaseConnectionKey]map[uint64]struct{})}
	key := releaseConnectionKey{namespace: domain.NamespaceRef{Env: "prod", App: "app"}, name: "runtime", clientName: "api", instanceID: "replica-1"}
	first := h.addConnection(key)
	second := h.addConnection(key)
	if h.removeConnection(key, first) {
		t.Fatal("older overlapping stream was treated as the last connection")
	}
	if !h.removeConnection(key, second) {
		t.Fatal("last stream disconnect was not detected")
	}
}
