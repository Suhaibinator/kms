package integration

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This interceptor only resets real TLS streams and observes their traffic.
// All acknowledgement validation, retention, and rejection responses come
// from the real server and SQLite store.
type retentionWatchProbe struct {
	reset         chan struct{}
	opened        atomic.Int64
	rejectedSends atomic.Int64
	rejections    chan *kmsv1.ReleaseAcknowledgementRejectedEvent
	heartbeats    chan int64
}

func (p *retentionWatchProbe) intercept(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	if method != kmsv1.ConfigurationReleaseService_WatchRelease_FullMethodName {
		return streamer(ctx, desc, cc, method, opts...)
	}
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := streamer(streamCtx, desc, cc, method, opts...)
	if err != nil {
		cancel()
		return nil, err
	}
	generation := p.opened.Add(1)
	go func() {
		select {
		case <-p.reset:
			cancel()
		case <-streamCtx.Done():
		}
	}()
	return &retentionWatchStream{ClientStream: stream, probe: p, generation: generation, cancel: cancel}, nil
}

type retentionWatchStream struct {
	grpc.ClientStream
	probe      *retentionWatchProbe
	generation int64
	cancel     context.CancelFunc
}

func (s *retentionWatchStream) SendMsg(message any) error {
	if request, ok := message.(*kmsv1.WatchReleaseRequest); ok {
		if ack := request.GetAcknowledgement(); ack != nil && ack.GetState() == kmsclient.ReleaseStateRejected && ack.GetVersion() == 2 {
			s.probe.rejectedSends.Add(1)
		}
	}
	return s.ClientStream.SendMsg(message)
}
func (s *retentionWatchStream) RecvMsg(message any) error {
	if err := s.ClientStream.RecvMsg(message); err != nil {
		s.cancel()
		return err
	}
	if event, ok := message.(*kmsv1.WatchReleaseEvent); ok {
		if rejection := event.GetAcknowledgementRejected(); rejection != nil {
			select {
			case s.probe.rejections <- rejection:
			default:
			}
		}
		if event.GetHeartbeat() != nil {
			select {
			case s.probe.heartbeats <- s.generation:
			default:
			}
		}
	}
	return nil
}

func TestSDKReconnectDiscardsExpiredAcknowledgementAndKeepsWatching(t *testing.T) {
	env := newLoopbackTLSEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	principal := core.Principal{Identity: domain.Identity{Name: "network-root", Kind: domain.IdentityKindAdmin}, Method: domain.AuthMethodToken}
	ns := domain.NamespaceRef{Env: "prod", App: "ack-retention"}
	_, schema, err := env.svc.CreateApplicationWithSchema(ctx, principal, domain.Application{Name: ns.App, ReleaseName: "runtime", Contract: []domain.ApplicationContractField{{Alias: "setting", Kind: domain.ReleaseEntryParameter, ContentType: "integer"}}}, `{"type":"object","properties":{"setting":{"type":"integer"}},"required":["setting"],"additionalProperties":false}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.CreateNamespace(ctx, principal, ns, "", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
		t.Fatal(err)
	}
	track := domain.ReleaseTrack{Namespace: ns, Name: "runtime", SchemaVersion: schema.Version}
	parameter := domain.Ref{NS: ns, Key: "setting"}
	if _, _, err := env.svc.PutParameter(ctx, principal, parameter, "1", "integer", "{}"); err != nil {
		t.Fatal(err)
	}
	activate := func() domain.ActiveConfigurationRelease {
		t.Helper()
		rel, err := env.svc.CreateConfigurationRelease(ctx, principal, domain.CreateConfigurationReleaseInput{Namespace: ns, Name: track.Name, SchemaVersion: track.SchemaVersion, Entries: []domain.ReleaseEntrySelector{{Alias: "setting", Kind: domain.ReleaseEntryParameter, Ref: parameter, Version: 1}}})
		if err != nil {
			t.Fatal(err)
		}
		active, _, err := env.svc.ActivateConfigurationRelease(ctx, principal, track, rel.Version, nil)
		if err != nil {
			t.Fatal(err)
		}
		return active
	}
	activate()
	probe := &retentionWatchProbe{reset: make(chan struct{}), rejections: make(chan *kmsv1.ReleaseAcknowledgementRejectedEvent, 8), heartbeats: make(chan int64, 64)}
	sdk, err := kmsclient.NewClient(kmsclient.Config{Endpoint: env.endpoint(), Namespace: ns.Env + "/" + ns.App, Token: env.adminToken, TLS: env.clientTLS(nil), ClientName: "retention-sdk", Timeout: 2 * time.Second, DialOptions: []grpc.DialOption{grpc.WithStreamInterceptor(probe.intercept)}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sdk.Close() }()
	loader, err := kmsclient.NewReleaseLoader(sdk, kmsclient.ReleaseLoaderConfig{Name: track.Name, SchemaSHA256: schema.Digest, InstanceID: "retention-instance", ReconcileInterval: 25 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	var applied atomic.Uint64
	go func() {
		done <- loader.Run(runCtx, func(_ context.Context, snapshot kmsclient.ReleaseSnapshot) (kmsclient.PreparedRelease, error) {
			if snapshot.Version() == 2 {
				return nil, errors.New("test rejects second candidate")
			}
			return schemaTrackPrepared{commit: func() { applied.Store(snapshot.Version()) }}, nil
		})
	}()
	defer func() {
		stop()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("loader stopped unexpectedly: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("loader did not stop")
		}
	}()
	waitForManagedState(t, func() bool { return applied.Load() == 1 }, "initial SDK release")
	rejected := activate()
	admin := kmsv1.NewAdminServiceClient(env.adminConn)
	waitForManagedState(t, func() bool {
		result, err := admin.ListReleaseSubscribers(networkAuthContext(ctx, env.adminToken), &kmsv1.ListReleaseSubscribersRequest{Namespace: networkNS(ns.Env, ns.App), ReleaseName: track.Name, SchemaVersion: &schema.Version})
		if err != nil {
			return false
		}
		for _, row := range result.GetSubscribers() {
			if row.GetClientName() == "retention-sdk" && row.GetState() == "rejected" && row.GetReleaseVersion() == 2 {
				return true
			}
		}
		return false
	}, "server persisted the original valid rejection")
	activate()
	waitForManagedState(t, func() bool { return applied.Load() == 3 }, "third release")
	activate()
	waitForManagedState(t, func() bool { return applied.Load() == 4 }, "fourth release")
	waitForManagedState(t, func() bool {
		rows, err := admin.ListReleaseSubscribers(networkAuthContext(ctx, env.adminToken), &kmsv1.ListReleaseSubscribersRequest{Namespace: networkNS(ns.Env, ns.App), ReleaseName: track.Name, SchemaVersion: &schema.Version})
		if err != nil {
			return false
		}
		for _, row := range rows.Subscribers {
			if row.ClientName == "retention-sdk" && row.State == "applied" && row.ReleaseVersion == 4 {
				return true
			}
		}
		return false
	}, "server acknowledged fourth release before pruning older delivery evidence")
	beforeReconnect := probe.rejectedSends.Load()
	if _, err := env.store.PruneConfigurationReleases(ctx, time.Nanosecond, 100); err != nil {
		t.Fatal(err)
	}
	exists, err := env.store.ConfigurationReleaseActivationExists(ctx, track, 2, rejected.ActivationRevision)
	if err != nil || exists {
		t.Fatalf("old activation survived retention: %v %v", exists, err)
	}
	select {
	case probe.reset <- struct{}{}:
	case <-ctx.Done():
		t.Fatal("no live stream to reset")
	}
	select {
	case event := <-probe.rejections:
		if event.GetReason() != "target_unavailable" || event.GetVersion() != 2 || event.GetSchemaVersion() != schema.Version || event.GetSequence() == 0 {
			t.Fatalf("invalid ACK rejection: %v", event)
		}
	case <-ctx.Done():
		t.Fatal("server did not reject expired replay without closing stream")
	}
	activate()
	waitForManagedState(t, func() bool { return applied.Load() == 5 }, "release after retention/reconnect")
	// A second real stream reset must not replay the discarded rejection again.
	select {
	case probe.reset <- struct{}{}:
	case <-ctx.Done():
		t.Fatal("no recovered stream to reset")
	}
	for {
		select {
		case generation := <-probe.heartbeats:
			if generation >= 3 {
				if got := probe.rejectedSends.Load(); got != beforeReconnect+1 {
					t.Fatalf("expired ACK replayed again: %d sends, want %d initial sends + one replay", got, beforeReconnect)
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("SDK did not continue after its second reconnect")
		}
	}
}

// Retain coverage of old SDK activation delivery after session support is added.
func legacyReleaseProtocol(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	if method == "/kms.v1.ConfigurationReleaseService/RegisterReleaseSession" {
		return status.Error(codes.Unimplemented, "legacy release protocol")
	}
	return invoker(ctx, method, req, reply, cc, opts...)
}
