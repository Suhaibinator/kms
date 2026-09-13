package grpcserver

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
	"github.com/Suhaibinator/kms/internal/watch"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func TestWatchReleaseRejectsUnavailableAcknowledgementAndKeepsStream(t *testing.T) {
	ctx := context.Background()
	st, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ns := domain.NamespaceRef{Env: "prod", App: "ack-test"}
	if _, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: ns}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIdentity(ctx, storage.CreateIdentityParams{Name: "admin", Kind: domain.IdentityKindAdmin, TokenHash: crypto.TokenHash(adminToken)}); err != nil {
		t.Fatal(err)
	}
	track := domain.ReleaseTrack{Namespace: ns, Name: "runtime"}
	activate := func() domain.ActiveConfigurationRelease {
		t.Helper()
		r, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{Namespace: ns, Name: track.Name, Digest: "empty"})
		if err != nil {
			t.Fatal(err)
		}
		a, _, err := st.ActivateConfigurationRelease(ctx, track, r.Version, nil)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	old := activate()
	activate()
	current := activate()
	if _, err := st.PruneConfigurationReleases(ctx, time.Nanosecond, 100); err != nil {
		t.Fatal(err)
	}
	svc := core.New(st, nil, "test")
	svc.SetAdminRequireClientCert(false)
	kek, err := crypto.NewKEKFromMaterial("test", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc.SetKeyring(crypto.NewKeyring(kek))
	hub := watch.NewHub(st, nil, watch.Options{HeartbeatInterval: 20 * time.Millisecond, PruneInterval: time.Hour})
	svc.SetHub(hub)
	hubCtx, stopHub := context.WithCancel(ctx)
	t.Cleanup(stopHub)
	go func() { _ = hub.Run(hubCtx) }()
	<-hub.Started()
	_, lis := serveBufconn(t, svc, hub, Config{})
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := kmsv1.NewConfigurationReleaseServiceClient(conn)
	watchCtx, cancel := context.WithTimeout(adminCtx(), 5*time.Second)
	defer cancel()
	stream, err := client.WatchRelease(watchCtx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Register{Register: registerTestReleaseSession(t, watchCtx, client, &kmsv1.ReleaseWatchRegistration{Namespace: pNS(ns.Env, ns.App), Name: track.Name, SchemaVersion: new(uint64), ClientName: "client", InstanceId: "instance"})}}); err != nil {
		t.Fatal(err)
	}
	first, err := stream.Recv()
	if err != nil || first.GetTarget().GetRelease().GetVersion() != current.Release.Version {
		t.Fatalf("snapshot: %v %v", first, err)
	}
	ack := &kmsv1.ReleaseAcknowledgement{Namespace: pNS(ns.Env, ns.App), Name: track.Name, Version: old.Release.Version, ActivationRevision: old.ActivationRevision, ClientName: "client", InstanceId: "instance", SessionId: "instance-session", TargetRevision: old.ActivationRevision, State: domain.ReleaseStateRejected, RejectionCategory: domain.ReleaseRejectPrepareFailed, Diagnostic: "must-not-be-echoed", Sequence: 42}
	sendAck := func() {
		t.Helper()
		if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Acknowledgement{Acknowledgement: ack}}); err != nil {
			t.Fatal(err)
		}
	}
	sendAck()
	for {
		event, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		rejected := event.GetAcknowledgementRejected()
		if rejected == nil {
			continue
		}
		if event.Revision != 0 || rejected.GetReason() != "target_unavailable" || rejected.GetNamespace().GetEnv() != ns.Env || rejected.GetNamespace().GetApp() != ns.App || rejected.Name != track.Name || rejected.SchemaVersion != 0 || rejected.Version != old.Release.Version || rejected.ActivationRevision != old.ActivationRevision || rejected.ClientName != "client" || rejected.InstanceId != "instance" || rejected.State != ack.State || rejected.Sequence != 42 {
			t.Fatalf("rejection identity: %v", event)
		}
		break
	}
	audits, _, err := st.ListAudit(ctx, domain.AuditFilter{EventType: "configuration_release.acknowledge"}, storage.ListPage{})
	if err != nil || len(audits) != 0 {
		t.Fatalf("rejected ACK audited as accepted: %v %v", audits, err)
	}
	rows, _, err := st.ListReleaseAcknowledgements(ctx, domain.ReleaseFilter{Namespace: ns, Name: track.Name}, storage.ListPage{})
	if err != nil || len(rows) != 1 || rows[0].State != "" {
		t.Fatalf("rejected ACK persisted: %v %v", rows, err)
	}
	// A heartbeat after the response proves the watch remains alive and its
	// configuration cursor still names the actual activation.
	heartbeat, err := stream.Recv()
	if err != nil || heartbeat.GetHeartbeat() == nil || heartbeat.Revision != current.ActivationRevision {
		t.Fatalf("post-rejection heartbeat: %v %v", heartbeat, err)
	}
	ack.Version = current.Release.Version
	ack.ActivationRevision = current.ActivationRevision
	ack.TargetRevision = current.ActivationRevision
	ack.Sequence = 43
	ack.State = domain.ReleaseStateApplied
	ack.RejectionCategory = ""
	sendAck()
	deadline := time.Now().Add(time.Second)
	for {
		rows, _, err = st.ListReleaseAcknowledgements(ctx, domain.ReleaseFilter{Namespace: ns, Name: track.Name}, storage.ListPage{})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 1 && rows[0].State == domain.ReleaseStateApplied {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("current ACK did not persist: %v", rows)
		}
		time.Sleep(time.Millisecond)
	}
	// An unavailable rejection must not turn wrong registration into a
	// recoverable condition. This is still a strict protocol error.
	ack.SchemaVersion = 1
	sendAck()
	for {
		_, err := stream.Recv()
		if err == nil {
			continue
		}
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("wrong registered track: %v", err)
		}
		break
	}

	// Queue an unavailable response and EOF together. The response must reach
	// the caller before the graceful stream close.
	stream, err = client.WatchRelease(watchCtx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Register{Register: registerTestReleaseSession(t, watchCtx, client, &kmsv1.ReleaseWatchRegistration{Namespace: pNS(ns.Env, ns.App), Name: track.Name, SchemaVersion: new(uint64), ClientName: "client", InstanceId: "second"})}}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	ack.SchemaVersion = 0
	ack.Version = old.Release.Version
	ack.ActivationRevision = old.ActivationRevision
	ack.TargetRevision = old.ActivationRevision
	ack.InstanceId = "second"
	ack.SessionId = "second-session"
	ack.Sequence = 44
	sendAck()
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	sawRejection := false
	for {
		event, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if event.GetAcknowledgementRejected() != nil {
			sawRejection = true
			if event.GetAcknowledgementRejected().Sequence != 44 {
				t.Fatal("raw client sequence changed")
			}
		}
	}
	if !sawRejection {
		t.Fatal("EOF overtook rejection")
	}
}
