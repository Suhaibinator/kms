package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
	"google.golang.org/grpc"
)

// The former activation-only queued-delivery test forced session negotiation
// to fail. That fallback is retired: fail before delivering configuration and
// tell the operator which side requires an upgrade. Session target retention
// and recovery remain covered by release_ack_retention_test.go.
func TestSDKRequiresSessionCapableServer(t *testing.T) {
	env := newLoopbackTLSEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sdk, err := kmsclient.NewClient(kmsclient.Config{
		Endpoint: env.endpoint(), Namespace: "prod/compatibility", Token: env.adminToken,
		TLS: env.clientTLS(nil), ClientName: "compatibility-sdk", Timeout: time.Second,
		DialOptions: []grpc.DialOption{grpc.WithUnaryInterceptor(legacyReleaseProtocol)},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sdk.Close() }()
	version := uint64(1)
	loader, err := kmsclient.NewReleaseLoader(sdk, kmsclient.ReleaseLoaderConfig{Name: "runtime", SchemaVersion: &version})
	if err != nil {
		t.Fatal(err)
	}
	err = loader.Run(ctx, func(context.Context, kmsclient.ReleaseSnapshot) (kmsclient.PreparedRelease, error) {
		t.Error("configuration delivered despite incompatible server")
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "upgrade") || !strings.Contains(err.Error(), "server") {
		t.Fatalf("expected actionable server upgrade error, got %v", err)
	}
}
