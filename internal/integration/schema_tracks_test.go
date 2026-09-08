package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func integrationSchemaVersion(version uint64) *uint64 { return &version }

// This test uses the real TLS/gRPC, authorization, storage, and watch stack.
// Both clients deliberately share a process identity; their schemas alone must
// keep release ownership, stream queues and acknowledgement rows separate.
func TestIndependentSchemaTracksOverRealKMS(t *testing.T) {
	env := newLoopbackTLSEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	auth := networkAuthContext(ctx, env.adminToken)
	principal := core.Principal{Identity: domain.Identity{Name: "network-root", Kind: domain.IdentityKindAdmin}, Method: domain.AuthMethodToken}
	ns := domain.NamespaceRef{Env: "prod", App: "schema-tracks"}
	wireNS := networkNS(ns.Env, ns.App)
	const name = "runtime"
	_, firstSchema, err := env.svc.CreateApplicationWithSchema(ctx, principal, domain.Application{Name: ns.App, ReleaseName: name},
		`{"type":"object","properties":{"workers":{"type":"integer"}},"required":["workers"],"additionalProperties":false}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.CreateNamespace(ctx, principal, ns, "", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
		t.Fatal(err)
	}
	secondSchema, err := env.svc.CreateConfigurationSchema(ctx, principal, ns.App,
		`{"type":"object","properties":{"workers":{"type":"string"}},"required":["workers"],"additionalProperties":false}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	releases := kmsv1.NewConfigurationReleaseServiceClient(env.adminConn)
	parameters := kmsv1.NewParameterServiceClient(env.adminConn)
	create := func(schema uint64, key, value, contentType string) *kmsv1.ConfigurationRelease {
		t.Helper()
		ref := networkRef(ns.Env, ns.App, key)
		parameter, err := parameters.PutParameter(auth, &kmsv1.PutParameterRequest{Ref: ref, Value: value, ContentType: contentType})
		if err != nil {
			t.Fatal(err)
		}
		result, err := releases.CreateRelease(auth, &kmsv1.CreateReleaseRequest{Namespace: wireNS, Name: name, SchemaVersion: schema,
			Entries: []*kmsv1.ReleaseEntrySelector{{Alias: "workers", Kind: "parameter", Ref: ref, Version: parameter.GetVersion()}}})
		if err != nil {
			t.Fatal(err)
		}
		return result.GetRelease()
	}
	activate := func(release *kmsv1.ConfigurationRelease, expected uint64) uint64 {
		t.Helper()
		result, err := releases.ActivateRelease(auth, &kmsv1.ActivateReleaseRequest{Namespace: wireNS, Name: name,
			SchemaVersion: integrationSchemaVersion(release.GetSchemaVersion()), Version: release.GetVersion(), ExpectedCurrentVersion: &expected})
		if err != nil || !result.GetChanged() {
			t.Fatalf("activation = %v, %v", result, err)
		}
		return result.GetActivationRevision()
	}
	resolved, err := releases.ResolveReleaseSchema(auth, &kmsv1.ResolveReleaseSchemaRequest{Namespace: wireNS, Name: name, SchemaSha256: secondSchema.Digest})
	if err != nil || resolved.GetSchemaVersion() != secondSchema.Version {
		t.Fatalf("resolve = %v, %v", resolved, err)
	}
	if _, err := releases.GetActiveRelease(auth, &kmsv1.GetActiveReleaseRequest{Namespace: wireNS, Name: name}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unscoped active read = %v", err)
	}
	first := create(firstSchema.Version, "workers-count", "4", "integer")
	if first.GetVersion() != 1 {
		t.Fatalf("first track starts at %d", first.GetVersion())
	}
	firstRevision := activate(first, 0)
	watchTrack := func(schema, revision uint64) (kmsv1.ConfigurationReleaseService_WatchReleaseClient, context.CancelFunc) {
		t.Helper()
		watchCtx, stop := context.WithCancel(auth)
		stream, err := releases.WatchRelease(watchCtx)
		if err != nil {
			stop()
			t.Fatal(err)
		}
		if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Register{Register: &kmsv1.ReleaseWatchRegistration{
			Namespace: wireNS, Name: name, SchemaVersion: &schema, ClientName: "same-client", InstanceId: "same-instance", LastSeenRevision: revision,
		}}}); err != nil {
			stop()
			t.Fatal(err)
		}
		return stream, stop
	}
	receive := func(stream kmsv1.ConfigurationReleaseService_WatchReleaseClient, schema, version uint64) uint64 {
		t.Helper()
		for {
			event, err := stream.Recv()
			if err != nil {
				t.Fatal(err)
			}
			release := event.GetSnapshot().GetRelease()
			if release == nil {
				release = event.GetActivation().GetRelease()
			}
			if release == nil {
				continue
			}
			if release.GetSchemaVersion() != schema || release.GetVersion() != version {
				t.Fatalf("received schema %d release %d, want schema %d release %d", release.GetSchemaVersion(), release.GetVersion(), schema, version)
			}
			return event.GetRevision()
		}
	}
	oldStream, stopOld := watchTrack(firstSchema.Version, 0)
	defer stopOld()
	if revision := receive(oldStream, firstSchema.Version, 1); revision != firstRevision {
		t.Fatalf("initial revision = %d", revision)
	}
	newStream, stopNew := watchTrack(secondSchema.Version, 0)
	defer stopNew()
	// A registered schema with no release is a live waiting subscription, never
	// a snapshot of the older schema and never an automatic stream failure.
	waiting, err := newStream.Recv()
	if err != nil || waiting.GetHeartbeat() == nil {
		t.Fatalf("waiting schema event = %v, %v", waiting, err)
	}
	if _, err := releases.GetActiveRelease(auth, &kmsv1.GetActiveReleaseRequest{Namespace: wireNS, Name: name, SchemaVersion: &secondSchema.Version}); status.Code(err) != codes.NotFound {
		t.Fatalf("unactivated schema active read = %v", err)
	}
	// Exercise the real SDK as well: resolving a known digest before its first
	// activation must register a waiting subscriber, then commit that track.
	sdk, err := kmsclient.NewClient(kmsclient.Config{
		Endpoint: env.endpoint(), Namespace: ns.Env + "/" + ns.App, Token: env.adminToken,
		TLS: env.clientTLS(nil), Timeout: 2 * time.Second, ClientName: "sdk-waiting",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sdk.Close() }()
	unknown, err := kmsclient.NewReleaseLoader(sdk, kmsclient.ReleaseLoaderConfig{
		Name: name, SchemaVersion: integrationSchemaVersion(secondSchema.Version + 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	unknownCtx, stopUnknown := context.WithTimeout(ctx, 2*time.Second)
	err = unknown.Run(unknownCtx, func(context.Context, kmsclient.ReleaseSnapshot) (kmsclient.PreparedRelease, error) {
		return nil, errors.New("unknown schema received a candidate")
	})
	stopUnknown()
	if !errors.Is(err, kmsclient.ErrNotFound) {
		t.Fatalf("unknown schema did not fail promptly: %v", err)
	}
	loader, err := kmsclient.NewReleaseLoader(sdk, kmsclient.ReleaseLoaderConfig{
		Name: name, SchemaSHA256: secondSchema.Digest, InstanceID: "sdk-waiting", ReconcileInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	loaderCtx, stopLoader := context.WithCancel(ctx)
	defer stopLoader()
	committed := make(chan uint64, 8)
	loaderDone := make(chan error, 1)
	go func() {
		loaderDone <- loader.Run(loaderCtx, func(_ context.Context, snapshot kmsclient.ReleaseSnapshot) (kmsclient.PreparedRelease, error) {
			if snapshot.SchemaVersion() != secondSchema.Version {
				return nil, errors.New("SDK crossed schema tracks")
			}
			return schemaTrackPrepared{commit: func() {
				select {
				case committed <- snapshot.Version():
				case <-loaderCtx.Done():
				}
			}}, nil
		})
	}()
	defer func() {
		stopLoader()
		select {
		case err := <-loaderDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("SDK run: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("SDK failed to stop")
		}
	}()
	waitForManagedState(t, func() bool {
		rows, _, err := env.svc.ListSubscribers(ctx, principal)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.ClientName == "sdk-waiting" && row.SchemaVersion == secondSchema.Version {
				return true
			}
		}
		return false
	}, "SDK subscribed before activation")
	second := create(secondSchema.Version, "workers-text", "four", "string")
	if second.GetVersion() != 1 {
		t.Fatalf("second track starts at %d", second.GetVersion())
	}
	secondRevision := activate(second, 0)
	select {
	case version := <-committed:
		if version != 1 {
			t.Fatalf("SDK first commit version=%d", version)
		}
	case <-ctx.Done():
		t.Fatal("SDK did not apply first activation")
	}
	if revision := receive(newStream, secondSchema.Version, 1); revision != secondRevision {
		t.Fatalf("second revision = %d", revision)
	}
	// Publishing an older contract still works after schema 2 is active.
	oldUpdate := create(firstSchema.Version, "workers-count", "5", "integer")
	if oldUpdate.GetVersion() != 2 {
		t.Fatalf("old track next version = %d", oldUpdate.GetVersion())
	}
	oldUpdateRevision := activate(oldUpdate, 1)
	if revision := receive(oldStream, firstSchema.Version, 2); revision != oldUpdateRevision {
		t.Fatalf("old update revision = %d", revision)
	}
	for _, acknowledgement := range []struct {
		stream                    kmsv1.ConfigurationReleaseService_WatchReleaseClient
		schema, version, revision uint64
	}{
		{oldStream, firstSchema.Version, 2, oldUpdateRevision}, {newStream, secondSchema.Version, 1, secondRevision},
	} {
		if err := acknowledgement.stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Acknowledgement{Acknowledgement: &kmsv1.ReleaseAcknowledgement{
			Namespace: wireNS, Name: name, SchemaVersion: acknowledgement.schema, Version: acknowledgement.version, ActivationRevision: acknowledgement.revision,
			ClientName: "same-client", InstanceId: "same-instance", State: "applied",
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	admin := kmsv1.NewAdminServiceClient(env.adminConn)
	waitForManagedState(t, func() bool {
		result, err := admin.ListReleaseSubscribers(auth, &kmsv1.ListReleaseSubscribersRequest{Namespace: wireNS, ReleaseName: name})
		if err != nil {
			return false
		}
		seen := map[uint64]bool{}
		for _, row := range result.GetSubscribers() {
			if row.GetClientName() == "same-client" && row.GetState() == "applied" && row.GetConnected() {
				seen[row.GetSchemaVersion()] = true
			}
		}
		return seen[firstSchema.Version] && seen[secondSchema.Version]
	}, "separate applied subscribers with identical process identity")
	stopOld()
	replay, stopReplay := watchTrack(firstSchema.Version, firstRevision)
	defer stopReplay()
	if revision := receive(replay, firstSchema.Version, 2); revision != oldUpdateRevision {
		t.Fatalf("replay revision = %d", revision)
	}
	newUpdate := create(secondSchema.Version, "workers-text", "five", "string")
	activate(newUpdate, 1)
	receive(newStream, secondSchema.Version, 2)
	if _, err := env.svc.RollbackConfigurationRelease(ctx, principal, domain.ReleaseTrack{Namespace: ns, Name: name, SchemaVersion: secondSchema.Version}, integrationSchemaVersion(2)); err != nil {
		t.Fatal(err)
	}
	receive(newStream, secondSchema.Version, 1)
	for _, want := range []struct{ schema, version, previous uint64 }{{firstSchema.Version, 2, 1}, {secondSchema.Version, 1, 2}} {
		current, err := releases.GetActiveRelease(auth, &kmsv1.GetActiveReleaseRequest{Namespace: wireNS, Name: name, SchemaVersion: &want.schema})
		if err != nil || current.GetRelease().GetVersion() != want.version || current.GetPreviousVersion() != want.previous {
			t.Fatalf("track schema %d current = %v, %v", want.schema, current, err)
		}
	}
	// An otherwise valid activation from schema 1 cannot be acknowledged on
	// schema 2's stream. Closing that stream must leave schema 1's identical
	// client/instance registration connected and able to receive updates.
	if err := newStream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Acknowledgement{Acknowledgement: &kmsv1.ReleaseAcknowledgement{
		Namespace: wireNS, Name: name, SchemaVersion: firstSchema.Version, Version: oldUpdate.GetVersion(), ActivationRevision: oldUpdateRevision,
		ClientName: "same-client", InstanceId: "same-instance", State: "applied",
	}}}); err != nil {
		t.Fatal(err)
	}
	for {
		_, err := newStream.Recv()
		if err == nil {
			continue
		}
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("foreign-track acknowledgement = %v, want InvalidArgument", err)
		}
		break
	}
	lastOldUpdate := create(firstSchema.Version, "workers-count", "6", "integer")
	activate(lastOldUpdate, 2)
	receive(replay, firstSchema.Version, 3)
	waitForManagedState(t, func() bool {
		result, err := admin.ListReleaseSubscribers(auth, &kmsv1.ListReleaseSubscribersRequest{Namespace: wireNS, ReleaseName: name})
		if err != nil {
			return false
		}
		connected := map[uint64]bool{}
		for _, row := range result.GetSubscribers() {
			if row.GetClientName() == "same-client" {
				connected[row.GetSchemaVersion()] = connected[row.GetSchemaVersion()] || row.GetConnected()
			}
		}
		return connected[firstSchema.Version] && !connected[secondSchema.Version]
	}, "foreign-track acknowledgement disconnects only its registered track")
}

type schemaTrackPrepared struct{ commit func() }

func (p schemaTrackPrepared) Commit() { p.commit() }
func (schemaTrackPrepared) Abort()    {}
