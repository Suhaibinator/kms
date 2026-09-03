package grpcserver

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"strings"
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
	"github.com/Suhaibinator/kms/sdk/go/configstore"
)

func TestConfigurationReleaseGRPCLifecycleAndWatch(t *testing.T) {
	ctx := context.Background()
	st, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	svc := core.New(st, nil, "test")
	pinnedApp, pinnedSchema, err := svc.CreateApplicationWithSchema(ctx, core.Principal{
		Identity: domain.Identity{Name: "admin", Kind: domain.IdentityKindAdmin}, Method: domain.AuthMethodToken,
	}, domain.Application{Name: "app", ReleaseName: "runtime"}, `{"type":"object"}`, "{}")
	if err != nil || pinnedApp.SchemaVersion != pinnedSchema.Version {
		t.Fatalf("create pinned application = %+v schema=%+v err=%v", pinnedApp, pinnedSchema, err)
	}
	ns := domain.NamespaceRef{Env: "prod", App: "app"}
	if _, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: ns}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIdentity(ctx, storage.CreateIdentityParams{Name: "admin", Kind: domain.IdentityKindAdmin, TokenHash: crypto.TokenHash(adminToken)}); err != nil {
		t.Fatal(err)
	}
	kek, err := crypto.NewKEKFromMaterial("kek", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc.SetKeyring(crypto.NewKeyring(kek))
	// Token-only admin seeding; the admin client-certificate requirement has
	// its own suite (admin_mtls_test.go).
	svc.SetAdminRequireClientCert(false)
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
	defer func() { _ = conn.Close() }()
	param := kmsv1.NewParameterServiceClient(conn)
	if _, err := param.PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Ref: pRef("prod", "app", "config"), Value: `{"enabled":true}`, ContentType: "json"}); err != nil {
		t.Fatal(err)
	}
	schemas := kmsv1.NewConfigurationSchemaServiceClient(conn)
	createdSchema, err := schemas.CreateSchema(adminCtx(), &kmsv1.CreateSchemaRequest{
		Application: "app",
		SchemaJson:  `{"type":"object","properties":{"settings":{"type":"object"}},"required":["settings"]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	schema := createdSchema.GetSchema()
	if schema.GetApplication() != "app" || schema.GetReleaseName() != "runtime" || schema.GetVersion() != 2 || schema.GetDigest() == "" {
		t.Fatalf("created schema = %+v", schema)
	}
	gotSchema, err := schemas.GetSchema(adminCtx(), &kmsv1.GetSchemaRequest{Application: "app", ReleaseName: "runtime", Version: schema.GetVersion()})
	if err != nil || gotSchema.GetSchema().GetDigest() != schema.GetDigest() {
		t.Fatalf("get schema = %+v err=%v", gotSchema, err)
	}
	listedSchemas, err := schemas.ListSchemas(adminCtx(), &kmsv1.ListSchemasRequest{Application: "app", ReleaseName: "runtime"})
	if err != nil || len(listedSchemas.GetSchemas()) != 2 || listedSchemas.GetSchemas()[0].GetVersion() != schema.GetVersion() {
		t.Fatalf("list schemas = %+v err=%v", listedSchemas, err)
	}
	if _, err := schemas.CreateSchema(adminCtx(), &kmsv1.CreateSchemaRequest{Application: "app", SchemaJson: schema.GetSchemaJson()}); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("duplicate schema code = %s err=%v, want AlreadyExists", status.Code(err), err)
	}
	releases := kmsv1.NewConfigurationReleaseServiceClient(conn)
	created, err := releases.CreateRelease(adminCtx(), &kmsv1.CreateReleaseRequest{Namespace: pNS("prod", "app"), Name: "runtime", SchemaVersion: pinnedSchema.Version, Entries: []*kmsv1.ReleaseEntrySelector{{Alias: "settings", Kind: "parameter", Ref: pRef("prod", "app", "config"), Label: "current"}}})
	if err != nil {
		t.Fatal(err)
	}
	if created.GetRelease().GetVersion() != 1 || created.GetRelease().GetDigest() == "" {
		t.Fatalf("created=%+v", created.GetRelease())
	}
	active, err := releases.ActivateRelease(adminCtx(), &kmsv1.ActivateReleaseRequest{Namespace: pNS("prod", "app"), Name: "runtime", Version: 1, ExpectedCurrentVersion: new(uint64(0))})
	if err != nil {
		t.Fatal(err)
	}
	if !active.GetChanged() || active.GetActivationRevision() == 0 {
		t.Fatalf("active=%+v", active)
	}
	createdV2, err := releases.CreateRelease(adminCtx(), &kmsv1.CreateReleaseRequest{Namespace: pNS("prod", "app"), Name: "runtime", SchemaVersion: pinnedSchema.Version, Entries: []*kmsv1.ReleaseEntrySelector{{Alias: "settings", Kind: "parameter", Ref: pRef("prod", "app", "config"), Label: "current"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := releases.ActivateRelease(adminCtx(), &kmsv1.ActivateReleaseRequest{Namespace: pNS("prod", "app"), Name: "runtime", Version: createdV2.GetRelease().GetVersion(), ExpectedCurrentVersion: new(uint64(0))}); status.Code(err) != codes.Aborted {
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
		acknowledged := false
		for _, subscriber := range states.GetSubscribers() {
			if subscriber.GetState() != domain.ReleaseStateReceived {
				continue
			}
			if subscriber.GetDiagnostic() != "[redacted]" || !subscriber.GetConnected() {
				t.Fatalf("subscriber=%+v", subscriber)
			}
			acknowledged = true
			break
		}
		if acknowledged {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("acknowledgement was not persisted")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A reconnect can briefly overlap the old stream. Close the newer stream
	// first, then the older one: the final close must fence against the newest
	// persisted connection generation rather than its own older generation.
	duplicate, err := releases.WatchRelease(adminCtx())
	if err != nil {
		t.Fatal(err)
	}
	if err := duplicate.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Register{Register: &kmsv1.ReleaseWatchRegistration{Namespace: pNS("prod", "app"), Name: "runtime", ClientName: "api", InstanceId: "replica-1"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := duplicate.Recv(); err != nil {
		t.Fatal(err)
	}
	if err := duplicate.CloseSend(); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := duplicate.Recv(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("duplicate stream close err=%v, want EOF", err)
		}
	}

	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := stream.Recv(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("original stream close err=%v, want EOF", err)
		}
	}
	deadline = time.Now().Add(time.Second)
	for {
		states, err := admin.ListReleaseSubscribers(adminCtx(), &kmsv1.ListReleaseSubscribersRequest{Namespace: pNS("prod", "app"), ReleaseName: "runtime"})
		if err != nil {
			t.Fatal(err)
		}
		if len(states.GetSubscribers()) == 1 && !states.GetSubscribers()[0].GetConnected() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("last duplicate stream close left subscriber connected: %+v", states.GetSubscribers())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReleaseConnectionOverlapDoesNotReportPrematureDisconnect(t *testing.T) {
	h := &configurationReleaseServer{connections: make(map[releaseConnectionKey]*releaseConnectionState)}
	key := releaseConnectionKey{namespace: domain.NamespaceRef{Env: "prod", App: "app"}, name: "runtime", clientName: "api", instanceID: "replica-1"}
	first := h.addConnection(key)
	if err := h.persistConnection(key, first, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	second := h.addConnection(key)
	if err := h.persistConnection(key, second, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if last, _ := h.removeConnection(key, second); last {
		t.Fatal("newer overlapping stream was treated as the last connection")
	}
	last, persistedID := h.removeConnection(key, first)
	if !last {
		t.Fatal("last stream disconnect was not detected")
	}
	if persistedID != second {
		t.Fatalf("disconnect generation=%d, want most recently persisted generation %d", persistedID, second)
	}
}

func TestReleaseConnectionGenerationsAreScopedByIdentity(t *testing.T) {
	h := &configurationReleaseServer{connections: make(map[releaseConnectionKey]*releaseConnectionState)}
	base := releaseConnectionKey{
		namespace: domain.NamespaceRef{Env: "prod", App: "app"},
		name:      "runtime", clientName: "api", instanceID: "replica-1",
	}
	alice := base
	alice.identity = "alice"
	bob := base
	bob.identity = "bob"
	aliceID := h.addConnection(alice)
	bobID := h.addConnection(bob)
	if err := h.persistConnection(alice, aliceID, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := h.persistConnection(bob, bobID, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if last, persisted := h.removeConnection(alice, aliceID); !last || persisted != aliceID {
		t.Fatalf("alice removal = last %v persisted %d, want true/%d", last, persisted, aliceID)
	}
	if last, persisted := h.removeConnection(bob, bobID); !last || persisted != bobID {
		t.Fatalf("bob removal = last %v persisted %d, want true/%d", last, persisted, bobID)
	}
}

// End-to-end: the value-free verification RPC over gRPC, its ResourceExhausted
// mapping, and divergence carried on a watch acknowledgement all the way to
// the admin subscriber listing.
func TestVerifyReleaseDefaultsGRPCAndDivergentAcknowledgement(t *testing.T) {
	ctx := context.Background()
	st, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ns := domain.NamespaceRef{Env: "prod", App: "app"}
	if _, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: ns, AllowedAuthMethods: []domain.AuthMethod{domain.AuthMethodToken}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIdentity(ctx, storage.CreateIdentityParams{Name: "admin", Kind: domain.IdentityKindAdmin, TokenHash: crypto.TokenHash(adminToken)}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIdentity(ctx, storage.CreateIdentityParams{Name: "client", Kind: domain.IdentityKindClient, TokenHash: crypto.TokenHash(clientToken)}); err != nil {
		t.Fatal(err)
	}
	svc := core.New(st, nil, "test")
	kek, err := crypto.NewKEKFromMaterial("kek", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc.SetKeyring(crypto.NewKeyring(kek))
	// Token-only admin seeding; the admin client-certificate requirement has
	// its own suite (admin_mtls_test.go).
	svc.SetAdminRequireClientCert(false)
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
	defer func() { _ = conn.Close() }()

	param := kmsv1.NewParameterServiceClient(conn)
	if _, err := param.PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Ref: pRef("prod", "app", "config"), Value: "{\n  \"enabled\": true\n}", ContentType: "json"}); err != nil {
		t.Fatal(err)
	}
	if _, err := param.PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Ref: pRef("prod", "app", "greeting"), Value: "hello", ContentType: "string"}); err != nil {
		t.Fatal(err)
	}
	releases := kmsv1.NewConfigurationReleaseServiceClient(conn)
	created, err := releases.CreateRelease(adminCtx(), &kmsv1.CreateReleaseRequest{Namespace: pNS("prod", "app"), Name: "runtime", Entries: []*kmsv1.ReleaseEntrySelector{
		{Alias: "settings", Kind: "parameter", Ref: pRef("prod", "app", "config"), Label: "current"},
		{Alias: "greeting", Kind: "parameter", Ref: pRef("prod", "app", "greeting"), Label: "current"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	active, err := releases.ActivateRelease(adminCtx(), &kmsv1.ActivateReleaseRequest{Namespace: pNS("prod", "app"), Name: "runtime", Version: created.GetRelease().GetVersion()})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := configstore.ParameterHash("json", []byte(`{"enabled":true}`))
	if err != nil {
		t.Fatal(err)
	}
	wrong := strings.Repeat("f", 64)
	req := &kmsv1.VerifyReleaseDefaultsRequest{
		Namespace: pNS("prod", "app"), Profile: "dev",
		Entries: []*kmsv1.VerifyEntry{
			{Alias: "settings", ContentType: "json", Sha256: canonical},
			{Alias: "greeting", ContentType: "string", Sha256: wrong},
			{Alias: "unknown", ContentType: "string", Sha256: wrong},
		},
	}

	// A client without the operation is refused before any storage access.
	if _, err := releases.VerifyReleaseDefaults(clientCtx(), req); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("client verify code = %s err=%v, want PermissionDenied", status.Code(err), err)
	}
	// Malformed hashes are InvalidArgument.
	bad := &kmsv1.VerifyReleaseDefaultsRequest{Namespace: pNS("prod", "app"), Entries: []*kmsv1.VerifyEntry{{Alias: "settings", ContentType: "json", Sha256: strings.ToUpper(canonical)}}}
	if _, err := releases.VerifyReleaseDefaults(adminCtx(), bad); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("uppercase hash code = %s err=%v, want InvalidArgument", status.Code(err), err)
	}

	resp, err := releases.VerifyReleaseDefaults(adminCtx(), req)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if resp.GetName() != "runtime" || resp.GetVersion() != created.GetRelease().GetVersion() || resp.GetActivationRevision() != active.GetActivationRevision() {
		t.Fatalf("verify identity = %+v", resp)
	}
	if resp.GetSchemaMatches() {
		t.Fatal("no schema digest was supplied; schema_matches must be false")
	}
	verdicts := map[string]string{}
	for _, e := range resp.GetEntries() {
		verdicts[e.GetAlias()] = e.GetVerdict()
	}
	if verdicts["settings"] != domain.VerifyVerdictMatch || verdicts["greeting"] != domain.VerifyVerdictDiffers || verdicts["unknown"] != domain.VerifyVerdictUnknownAlias {
		t.Fatalf("verdicts = %v", verdicts)
	}
	if resp.GetMatchCount() != 1 || resp.GetDiffersCount() != 1 || resp.GetUnknownAliasCount() != 1 || resp.GetMissingInReleaseCount() != 0 || resp.GetSecretAliasCount() != 0 || resp.GetUnsupportedContentTypeCount() != 0 || resp.GetUnverifiedCount() != 0 {
		t.Fatalf("counts = %+v", resp)
	}

	// Exhausting the mismatch budget surfaces as ResourceExhausted.
	svc.SetVerifyDefaultsLimits(core.VerifyDefaultsLimits{RequestsPerHour: 1000, Burst: 1000, MismatchBudgetPerHour: 1})
	if _, err := releases.VerifyReleaseDefaults(adminCtx(), req); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("mismatch budget code = %s err=%v, want ResourceExhausted", status.Code(err), err)
	}
	svc.SetVerifyDefaultsLimits(core.VerifyDefaultsLimits{RequestsPerHour: 1, Burst: 1, MismatchBudgetPerHour: 1000})
	if _, err := releases.VerifyReleaseDefaults(adminCtx(), req); err != nil {
		t.Fatalf("first request within burst: %v", err)
	}
	if _, err := releases.VerifyReleaseDefaults(adminCtx(), req); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("request budget code = %s err=%v, want ResourceExhausted", status.Code(err), err)
	}

	// Divergence on an applied acknowledgement is persisted and listed.
	stream, err := releases.WatchRelease(adminCtx())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Register{Register: &kmsv1.ReleaseWatchRegistration{Namespace: pNS("prod", "app"), Name: "runtime", ClientName: "api", InstanceId: "replica-1"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	ack := &kmsv1.ReleaseAcknowledgement{Namespace: pNS("prod", "app"), Name: "runtime", Version: created.GetRelease().GetVersion(), ActivationRevision: active.GetActivationRevision(), ClientName: "api", InstanceId: "replica-1", State: domain.ReleaseStateApplied, AppliedDivergent: true, DivergentFieldCount: 2}
	if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Acknowledgement{Acknowledgement: ack}}); err != nil {
		t.Fatal(err)
	}
	admin := kmsv1.NewAdminServiceClient(conn)
	deadline := time.Now().Add(2 * time.Second)
	for {
		states, err := admin.ListReleaseSubscribers(adminCtx(), &kmsv1.ListReleaseSubscribersRequest{Namespace: pNS("prod", "app"), ReleaseName: "runtime"})
		if err != nil {
			t.Fatal(err)
		}
		var applied *kmsv1.ReleaseSubscriberState
		for _, subscriber := range states.GetSubscribers() {
			if subscriber.GetState() == domain.ReleaseStateApplied {
				applied = subscriber
			}
		}
		if applied != nil {
			if !applied.GetAppliedDivergent() || applied.GetDivergentFieldCount() != 2 {
				t.Fatalf("applied subscriber = %+v, want divergent with 2 fields", applied)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("divergent acknowledgement was not persisted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Divergence on a non-applied state is rejected and tears the stream down
	// with InvalidArgument.
	ack.State, ack.AppliedDivergent, ack.DivergentFieldCount = domain.ReleaseStatePrepared, true, 1
	if err := stream.Send(&kmsv1.WatchReleaseRequest{Request: &kmsv1.WatchReleaseRequest_Acknowledgement{Acknowledgement: ack}}); err != nil {
		t.Fatal(err)
	}
	for {
		_, err := stream.Recv()
		if err == nil {
			continue
		}
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("divergent prepared ack stream error = %v, want InvalidArgument", err)
		}
		break
	}
}
