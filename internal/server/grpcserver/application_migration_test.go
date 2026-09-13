package grpcserver

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
	"github.com/Suhaibinator/kms/internal/watch"
)

const migrationTestSchema = `{"type":"object","properties":{"database":{"type":"object"},"rate_limits":{"type":"integer","minimum":0}},"required":["database","rate_limits"],"additionalProperties":false}`

type migrationTestEnv struct {
	svc   *core.Service
	store *storage.SQLStore
	conn  *grpc.ClientConn
	// app is the seeded application; schema is the registered target track.
	app    domain.Application
	schema domain.ConfigurationSchema
}

func (e *migrationTestEnv) admin() kmsv1.AdminServiceClient {
	return kmsv1.NewAdminServiceClient(e.conn)
}

// request returns the reviewed migration of the dev environment onto the target
// schema: database is renamed to db, rate_limits is edited, db_password is
// carried unchanged.
func (e *migrationTestEnv) request(env string) *kmsv1.MigrateApplicationReleaseRequest {
	return &kmsv1.MigrateApplicationReleaseRequest{
		Namespace: pNS(env, e.app.Name), SourceSchemaVersion: e.app.SchemaVersion, SchemaVersion: e.schema.Version,
		Contract: []*kmsv1.ApplicationContractField{
			{Alias: "db", Kind: domain.ReleaseEntryParameter, ContentType: "json"},
			{Alias: "db_password", Kind: domain.ReleaseEntrySecret},
			{Alias: "rate_limits", Kind: domain.ReleaseEntryParameter, ContentType: "integer"},
		},
		Changes: []*kmsv1.ApplicationMigrationChange{
			{Alias: "db", FromAlias: "database"},
			{Alias: "rate_limits", Value: proto.String("12")},
		},
	}
}

// newMigrationTestEnv serves the admin transport over a real SQL store because
// the shared memStore deliberately lacks application and release management.
// It seeds application gradethis with active releases in dev and prod, then
// registers a second schema (db/rate_limits, minimum 10) as the migration
// target.
func newMigrationTestEnv(t *testing.T) *migrationTestEnv {
	t.Helper()
	ctx := context.Background()
	st, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for _, identity := range []struct{ name, kind, token string }{
		{"admin", domain.IdentityKindAdmin, adminToken},
		{"client", domain.IdentityKindClient, clientToken},
	} {
		if _, err := st.CreateIdentity(ctx, storage.CreateIdentityParams{Name: identity.name, Kind: identity.kind, TokenHash: crypto.TokenHash(identity.token)}); err != nil {
			t.Fatal(err)
		}
	}
	kek, err := crypto.NewKEKFromMaterial("kek-test", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	keyCheck, err := crypto.NewKeyCheck(kek)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertKeyMetadata(ctx, domain.KeyMetadata{ID: "kek-test", Source: domain.KeySourceFile, KeyCheck: keyCheck, State: domain.KeyStateActive, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	svc := core.New(st, nil, "test")
	svc.SetKeyring(crypto.NewKeyring(kek))
	// Token-only admin seeding; the admin client-certificate requirement has
	// its own suite (admin_mtls_test.go).
	svc.SetAdminRequireClientCert(false)
	hub := watch.NewHub(st, nil, watch.Options{HeartbeatInterval: time.Second, PruneInterval: time.Hour})
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

	pr := core.Principal{Identity: domain.Identity{Name: "admin", Kind: domain.IdentityKindAdmin}, Method: domain.AuthMethodToken}
	app, _, err := svc.CreateApplicationWithSchema(ctx, pr, domain.Application{Name: "gradethis", ReleaseName: "runtime", Contract: []domain.ApplicationContractField{
		{Alias: "database", Kind: domain.ReleaseEntryParameter, ContentType: "json"},
		{Alias: "rate_limits", Kind: domain.ReleaseEntryParameter, ContentType: "integer"},
		{Alias: "db_password", Kind: domain.ReleaseEntrySecret},
	}}, migrationTestSchema, "{}")
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"dev", "prod"} {
		ns := domain.NamespaceRef{Env: env, App: app.Name}
		if _, err := svc.CreateNamespace(ctx, pr, ns, env+" environment", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := svc.PutParameter(ctx, pr, domain.Ref{NS: ns, Key: "database"}, `{"host":"db.internal"}`, "json", "{}"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := svc.PutParameter(ctx, pr, domain.Ref{NS: ns, Key: "rate_limits"}, "5", "integer", "{}"); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.PutSecret(ctx, pr, core.PutSecretInput{Ref: domain.Ref{NS: ns, Key: "db_password"}, Value: []byte("hunter2"), ContentType: "text/plain", Metadata: "{}"}); err != nil {
			t.Fatal(err)
		}
		shipped, err := svc.ShipApplicationChange(ctx, pr, domain.ShipInput{SchemaVersion: &app.SchemaVersion, Application: app.Name, Environment: env})
		if err != nil || shipped.Status != domain.ShipStatusActivated {
			t.Fatalf("seed ship %s: %+v %v", env, shipped, err)
		}
	}
	schema, err := svc.CreateConfigurationSchema(ctx, pr, app.Name, `{"type":"object","properties":{"db":{"type":"object"},"rate_limits":{"type":"integer","minimum":10}},"required":["db","rate_limits"],"additionalProperties":false}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	return &migrationTestEnv{svc: svc, store: st, conn: conn, app: app, schema: schema}
}

func migrationEntry(t *testing.T, entries []*kmsv1.ApplicationReleasePlanEntry, alias string) *kmsv1.ApplicationReleasePlanEntry {
	t.Helper()
	for _, entry := range entries {
		if entry.GetAlias() == alias {
			return entry
		}
	}
	t.Fatalf("plan has no entry %q: %+v", alias, entries)
	return nil
}

func TestMigrateApplicationReleaseGRPCPreviewThenExecute(t *testing.T) {
	env := newMigrationTestEnv(t)
	ctx := context.Background()
	admin := env.admin()

	if _, err := admin.MigrateApplicationRelease(clientCtx(), env.request("dev")); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("client migrate code = %v err=%v", status.Code(err), err)
	}

	preview, err := admin.MigrateApplicationRelease(adminCtx(), env.request("dev"))
	if err != nil {
		t.Fatal(err)
	}
	if preview.GetPlanDigest() == "" || !preview.GetValid() || preview.GetExecuted() || preview.GetDefinitionChanged() {
		t.Fatalf("preview state = %+v", preview)
	}
	if preview.GetReleaseName() != "runtime" || preview.GetSchemaVersion() != env.schema.Version || preview.GetSourceVersion() == 0 || preview.GetSourceActivationRevision() == 0 {
		t.Fatalf("preview coordinates = %+v", preview)
	}
	if preview.GetRelease() != nil || preview.GetActivation() != nil {
		t.Fatalf("preview carried execute-only fields: %+v", preview)
	}
	if len(preview.GetEntries()) != 3 {
		t.Fatalf("preview entries = %+v", preview.GetEntries())
	}
	renamed := migrationEntry(t, preview.GetEntries(), "db")
	if renamed.GetSource() != "renamed" || renamed.GetKind() != domain.ReleaseEntryParameter || renamed.GetRef().GetKey() != "database" || renamed.GetFromVersion() != 1 || renamed.GetToVersion() != 1 {
		t.Fatalf("renamed entry = %+v", renamed)
	}
	edited := migrationEntry(t, preview.GetEntries(), "rate_limits")
	if edited.GetSource() != "edited" || edited.GetRef().GetKey() != "rate_limits" || edited.GetFromVersion() != 1 || edited.GetToVersion() != 2 {
		t.Fatalf("edited entry = %+v", edited)
	}
	preserved := migrationEntry(t, preview.GetEntries(), "db_password")
	if preserved.GetSource() != "preserved" || preserved.GetKind() != domain.ReleaseEntrySecret || preserved.GetFromVersion() != 1 || preserved.GetToVersion() != 1 {
		t.Fatalf("preserved entry = %+v", preserved)
	}
	// prod has no release on the target track yet, so it is reported without
	// an active version.
	if len(preview.GetAffectedEnvironments()) != 1 || preview.GetAffectedEnvironments()[0].GetEnvironment() != "prod" || preview.GetAffectedEnvironments()[0].GetActiveVersion() != 0 || preview.GetAffectedEnvironments()[0].GetSchemaVersion() != 0 {
		t.Fatalf("affected environments = %+v", preview.GetAffectedEnvironments())
	}
	wire, err := proto.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"hunter2", "db.internal"} {
		if strings.Contains(string(wire), leaked) {
			t.Fatalf("preview exposed resource value %q", leaked)
		}
	}

	// Executing without the reviewed digest is rejected before any state is read.
	withoutDigest := env.request("dev")
	withoutDigest.Execute = true
	if _, err := admin.MigrateApplicationRelease(adminCtx(), withoutDigest); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("execute without digest code = %v err=%v", status.Code(err), err)
	}

	// A digest from a different reviewed request is stale.
	stale := env.request("dev")
	stale.Changes[1].Value = proto.String("13")
	stale.Execute, stale.PlanDigest = true, preview.GetPlanDigest()
	if _, err := admin.MigrateApplicationRelease(adminCtx(), stale); status.Code(err) != codes.Aborted {
		t.Fatalf("stale digest code = %v err=%v", status.Code(err), err)
	}

	execute := env.request("dev")
	execute.Execute, execute.PlanDigest = true, preview.GetPlanDigest()
	result, err := admin.MigrateApplicationRelease(adminCtx(), execute)
	if err != nil {
		t.Fatal(err)
	}
	if !result.GetExecuted() || !result.GetValid() || result.GetPlanDigest() != preview.GetPlanDigest() {
		t.Fatalf("execute state = %+v", result)
	}
	if result.GetRelease() == nil || result.GetRelease().GetVersion() != 1 || result.GetRelease().GetSchemaVersion() != env.schema.Version || len(result.GetRelease().GetEntries()) != 3 {
		t.Fatalf("execute release = %+v", result.GetRelease())
	}
	if result.GetActivation() == nil || !result.GetActivation().GetChanged() || result.GetActivation().GetActivationRevision() <= preview.GetSourceActivationRevision() || result.GetActivation().GetPreviousVersion() != 0 {
		t.Fatalf("execute activation = %+v", result.GetActivation())
	}
	pr := core.Principal{Identity: domain.Identity{Name: "admin", Kind: domain.IdentityKindAdmin}, Method: domain.AuthMethodToken}
	active, err := env.svc.GetActiveConfigurationRelease(ctx, pr, domain.ReleaseTrack{Namespace: domain.NamespaceRef{Env: "dev", App: env.app.Name}, Name: "runtime", SchemaVersion: env.schema.Version})
	if err != nil || active.Release.Version != 1 || active.ActivationRevision != result.GetActivation().GetActivationRevision() {
		t.Fatalf("target track active release = %+v err=%v", active, err)
	}

	// Replaying the consumed plan cannot create a second release.
	if _, err := admin.MigrateApplicationRelease(adminCtx(), execute); status.Code(err) != codes.Aborted {
		t.Fatalf("replayed digest code = %v err=%v", status.Code(err), err)
	}
}

func TestMigrateApplicationReleaseGRPCValidationAndExpectedSource(t *testing.T) {
	env := newMigrationTestEnv(t)
	admin := env.admin()

	invalid := env.request("dev")
	invalid.Changes[1].Value = proto.String("-10")
	preview, err := admin.MigrateApplicationRelease(adminCtx(), invalid)
	if err != nil {
		t.Fatal(err)
	}
	if preview.GetValid() || preview.GetExecuted() || preview.GetPlanDigest() == "" || len(preview.GetValidation()) == 0 {
		t.Fatalf("invalid preview = %+v", preview)
	}
	violation := preview.GetValidation()[0]
	if violation.GetAlias() != "rate_limits" || violation.GetCode() != domain.ReleaseValidationSchema || violation.GetInstancePointer() != "/rate_limits" || violation.GetSchemaPointer() == "" || violation.GetMessage() == "" {
		t.Fatalf("validation error = %+v", violation)
	}
	if strings.Contains(violation.GetMessage(), "-10") {
		t.Fatalf("validation echoed the candidate value: %+v", violation)
	}
	invalid.Execute, invalid.PlanDigest = true, preview.GetPlanDigest()
	applied, err := admin.MigrateApplicationRelease(adminCtx(), invalid)
	if err != nil || applied.GetExecuted() || applied.GetValid() || applied.GetRelease() != nil {
		t.Fatalf("invalid execute = %+v err=%v", applied, err)
	}

	// Compare-and-swap on the source: both fields or neither.
	valid, err := admin.MigrateApplicationRelease(adminCtx(), env.request("dev"))
	if err != nil {
		t.Fatal(err)
	}
	partial := env.request("dev")
	partial.ExpectedSourceVersion = proto.Uint64(valid.GetSourceVersion())
	if _, err := admin.MigrateApplicationRelease(adminCtx(), partial); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("partial expected source code = %v err=%v", status.Code(err), err)
	}
	partial.ExpectedSourceActivationRevision = proto.Uint64(valid.GetSourceActivationRevision())
	if _, err := admin.MigrateApplicationRelease(adminCtx(), partial); err != nil {
		t.Fatalf("matching expected source: %v", err)
	}
	partial.ExpectedSourceActivationRevision = proto.Uint64(valid.GetSourceActivationRevision() + 1)
	if _, err := admin.MigrateApplicationRelease(adminCtx(), partial); status.Code(err) != codes.Aborted {
		t.Fatalf("changed expected source code = %v err=%v", status.Code(err), err)
	}
}

// Proto presence must survive the wire: an unset change value keeps the pin,
// an explicitly empty value is an edit to "", and the compare-and-swap fields
// distinguish unset from zero.
func TestApplicationReleaseMigrationInputFromProtoPreservesPresence(t *testing.T) {
	request := &kmsv1.MigrateApplicationReleaseRequest{
		Namespace: pNS("dev", "worker"), SourceSchemaVersion: 1, SchemaVersion: 2,
		Contract: []*kmsv1.ApplicationContractField{{Alias: "runtime", Kind: "parameter", ContentType: "json"}, {Alias: "db", Kind: "secret"}},
		Changes: []*kmsv1.ApplicationMigrationChange{
			{Alias: "runtime", FromAlias: "settings", Key: "config", Version: 4},
			{Alias: "empty", Value: proto.String("")},
			{Alias: "edited", Value: proto.String("{}"), ContentType: "json"},
		},
		MetadataJson: `{"ticket":"OPS-1"}`, Execute: true, PlanDigest: strings.Repeat("a", 64),
		ExpectedSourceVersion: proto.Uint64(0), ExpectedSourceActivationRevision: proto.Uint64(9),
	}
	wire, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded := &kmsv1.MigrateApplicationReleaseRequest{}
	if err := proto.Unmarshal(wire, decoded); err != nil {
		t.Fatal(err)
	}
	in := applicationReleaseMigrationInputFromProto(decoded)
	if in.Namespace != (domain.NamespaceRef{Env: "dev", App: "worker"}) || in.SourceSchemaVersion != 1 || in.SchemaVersion != 2 || !in.Execute || in.PlanDigest != request.PlanDigest || in.Metadata != request.MetadataJson {
		t.Fatalf("scalar input = %+v", in)
	}
	if len(in.Contract) != 2 || in.Contract[0] != (domain.ApplicationContractField{Alias: "runtime", Kind: "parameter", ContentType: "json"}) || in.Contract[1] != (domain.ApplicationContractField{Alias: "db", Kind: "secret"}) {
		t.Fatalf("contract = %+v", in.Contract)
	}
	if len(in.Changes) != 3 {
		t.Fatalf("changes = %+v", in.Changes)
	}
	if pin := in.Changes[0]; pin.Value != nil || pin.Alias != "runtime" || pin.FromAlias != "settings" || pin.Key != "config" || pin.Version != 4 {
		t.Fatalf("pin change = %+v", pin)
	}
	if empty := in.Changes[1]; empty.Value == nil || *empty.Value != "" {
		t.Fatalf("empty value change = %+v", empty)
	}
	if edited := in.Changes[2]; edited.Value == nil || *edited.Value != "{}" || edited.ContentType != "json" {
		t.Fatalf("edited change = %+v", edited)
	}
	if in.ExpectedSourceVersion == nil || *in.ExpectedSourceVersion != 0 || in.ExpectedSourceActivationRevision == nil || *in.ExpectedSourceActivationRevision != 9 {
		t.Fatalf("expected source = %v %v", in.ExpectedSourceVersion, in.ExpectedSourceActivationRevision)
	}

	unset := applicationReleaseMigrationInputFromProto(&kmsv1.MigrateApplicationReleaseRequest{})
	if unset.ExpectedSourceVersion != nil || unset.ExpectedSourceActivationRevision != nil || unset.Namespace != (domain.NamespaceRef{}) || len(unset.Contract) != 0 || len(unset.Changes) != 0 {
		t.Fatalf("unset input = %+v", unset)
	}
}

func TestToProtoApplicationReleaseMigrationResult(t *testing.T) {
	ns := domain.NamespaceRef{Env: "dev", App: "worker"}
	release := domain.ConfigurationRelease{
		Namespace: ns, Name: "runtime", Version: 1, SchemaVersion: 2,
		Entries: []domain.ConfigurationReleaseEntry{
			{Alias: "runtime", Kind: "parameter", Ref: domain.Ref{NS: ns, Key: "runtime"}, Version: 3, ContentType: "json", ParameterDigest: strings.Repeat("a", 64)},
			{Alias: "db", Kind: "secret", Ref: domain.Ref{NS: ns, Key: "db"}, Version: 4},
		},
		Digest: strings.Repeat("b", 64), Metadata: `{"ticket":"OPS-1"}`,
		CreatedBy: "admin", CreatedAt: time.Unix(1_700_000_000, 0),
	}
	result := domain.ApplicationReleaseMigrationResult{
		PlanDigest: strings.Repeat("c", 64), Valid: true, Executed: true, DefinitionChanged: true,
		ReleaseName: "runtime", SourceVersion: 7, SourceActivationRevision: 8, SchemaVersion: 2,
		Entries: []domain.ApplicationReleasePlanEntry{
			{Alias: "runtime", Kind: "parameter", Ref: domain.Ref{NS: ns, Key: "settings"}, FromVersion: 2, ToVersion: 3, Source: "renamed"},
			{Alias: "db", Kind: "secret", Ref: domain.Ref{NS: ns, Key: "db"}, FromVersion: 4, ToVersion: 4, Source: "preserved"},
		},
		Validation: []domain.ReleaseValidationError{
			{Alias: "runtime", Code: "schema_violation", SchemaPointer: "/properties/runtime", Message: "value does not match schema", InstancePointer: "/runtime/limit"},
		},
		AffectedEnvironments: []domain.ApplicationMigrationEnvironment{{Environment: "prod", ActiveVersion: 5, SchemaVersion: 1}},
		Release:              &release,
		Activation:           &domain.ShipActivation{ActivationRevision: 9, PreviousVersion: 0, Changed: true},
	}

	got := toProtoApplicationReleaseMigrationResult(result)
	if got.GetPlanDigest() != result.PlanDigest || !got.GetValid() || !got.GetExecuted() || !got.GetDefinitionChanged() {
		t.Fatalf("response state = %+v", got)
	}
	if got.GetReleaseName() != "runtime" || got.GetSourceVersion() != 7 || got.GetSourceActivationRevision() != 8 || got.GetSchemaVersion() != 2 {
		t.Fatalf("response coordinates = %+v", got)
	}
	if len(got.GetEntries()) != 2 || got.GetEntries()[0].GetSource() != "renamed" || got.GetEntries()[0].GetRef().GetKey() != "settings" || got.GetEntries()[0].GetFromVersion() != 2 || got.GetEntries()[0].GetToVersion() != 3 {
		t.Fatalf("response entries = %+v", got.GetEntries())
	}
	if got.GetEntries()[1].GetKind() != "secret" || got.GetEntries()[1].GetSource() != "preserved" || got.GetEntries()[1].GetRef().GetNamespace().GetEnv() != "dev" {
		t.Fatalf("secret plan entry = %+v", got.GetEntries()[1])
	}
	if len(got.GetValidation()) != 1 || got.GetValidation()[0].GetCode() != "schema_violation" || got.GetValidation()[0].GetInstancePointer() != "/runtime/limit" || got.GetValidation()[0].GetSchemaPointer() != "/properties/runtime" {
		t.Fatalf("response validation = %+v", got.GetValidation())
	}
	if len(got.GetAffectedEnvironments()) != 1 || got.GetAffectedEnvironments()[0].GetEnvironment() != "prod" || got.GetAffectedEnvironments()[0].GetActiveVersion() != 5 || got.GetAffectedEnvironments()[0].GetSchemaVersion() != 1 {
		t.Fatalf("response environments = %+v", got.GetAffectedEnvironments())
	}
	if got.GetRelease() == nil || got.GetRelease().GetVersion() != 1 || got.GetRelease().GetSchemaVersion() != 2 || got.GetRelease().GetEntries()[1].GetVersion() != 4 {
		t.Fatalf("response release = %+v", got.GetRelease())
	}
	if got.GetActivation() == nil || got.GetActivation().GetActivationRevision() != 9 || got.GetActivation().GetPreviousVersion() != 0 || !got.GetActivation().GetChanged() {
		t.Fatalf("response activation = %+v", got.GetActivation())
	}

	preview := toProtoApplicationReleaseMigrationResult(domain.ApplicationReleaseMigrationResult{
		PlanDigest: strings.Repeat("d", 64), ReleaseName: "runtime",
	})
	if preview.GetRelease() != nil || preview.GetActivation() != nil {
		t.Fatalf("preview unexpectedly included execute-only fields: %+v", preview)
	}
	if preview.Entries == nil || preview.Validation == nil || preview.AffectedEnvironments == nil {
		t.Fatalf("preview lists must be empty, not nil: %+v", preview)
	}
}
