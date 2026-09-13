package grpcserver

import (
	"context"
	"strings"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func registerTestReleaseSession(t *testing.T, ctx context.Context, client kmsv1.ConfigurationReleaseServiceClient, registration *kmsv1.ReleaseWatchRegistration) *kmsv1.ReleaseWatchRegistration {
	t.Helper()
	registration.SessionId = registration.InstanceId + "-session"
	_, err := client.RegisterReleaseSession(ctx, &kmsv1.RegisterReleaseSessionRequest{Session: &kmsv1.ReleaseSessionRef{Namespace: registration.Namespace, Name: registration.Name, SchemaVersion: registration.SchemaVersion, ClientName: registration.ClientName, InstanceId: registration.InstanceId, SessionId: registration.SessionId}})
	if err != nil {
		t.Fatal(err)
	}
	return registration
}

func TestLegacyReleaseWatchRequiresSDKUpgrade(t *testing.T) {
	env := newTestEnv(t, true)
	ctx, cancel := context.WithTimeout(adminCtx(), time.Second)
	defer cancel()
	stream, err := kmsv1.NewConfigurationReleaseServiceClient(env.conn).WatchRelease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Register{Register: &kmsv1.ReleaseWatchRegistration{Namespace: pNS("prod", "app"), Name: "runtime", SchemaVersion: new(uint64), ClientName: "old-sdk", InstanceId: "instance"}}}); err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv()
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(strings.ToLower(status.Convert(err).Message()), "upgrade") {
		t.Fatalf("legacy release watch must fail with an actionable upgrade error: %v", err)
	}
}
