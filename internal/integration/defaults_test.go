package integration

import (
	"context"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestLoopbackDefaultsBootstrapAndFirstRelease(t *testing.T) {
	e := newLoopbackTLSEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin := core.Principal{Identity: domain.Identity{Name: "network-root", Kind: domain.IdentityKindAdmin}, Method: domain.AuthMethodToken}

	const appName = "defaults-e2e"
	ns := domain.NamespaceRef{Env: "dev", App: appName}
	contract := []domain.ApplicationContractField{
		{Alias: "api_key", Kind: domain.ReleaseEntrySecret},
		{Alias: "runtime", Kind: domain.ReleaseEntryParameter, ContentType: "json"},
	}
	_, firstSchema, err := e.svc.CreateApplicationWithSchema(ctx, admin, domain.Application{
		Name: appName, ReleaseName: "runtime", Contract: contract,
	}, `{"type":"object","properties":{"runtime":{"type":"object"}},"required":["runtime"],"additionalProperties":false}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateNamespace(ctx, admin, ns, "disposable defaults e2e", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PutSecret(ctx, admin, core.PutSecretInput{
		Ref: domain.Ref{NS: ns, Key: "api_key"}, Value: []byte("manual-secret"), ContentType: "text/plain", Metadata: "{}",
	}); err != nil {
		t.Fatal(err)
	}
	adminClient := kmsv1.NewAdminServiceClient(e.adminConn)
	schemaClient := kmsv1.NewConfigurationSchemaServiceClient(e.adminConn)
	authCtx := networkAuthContext(ctx, e.adminToken)
	changedDocument := `{"description":"source schema changed","type":"object","properties":{"runtime":{"type":"object"}},"required":["runtime"],"additionalProperties":false}`
	uploaded, err := schemaClient.CreateSchema(authCtx, &kmsv1.CreateSchemaRequest{
		Application: appName, SchemaJson: changedDocument, MetadataJson: `{"source":"integration"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	schema := uploaded.GetSchema()
	if schema.GetApplication() != appName || schema.GetReleaseName() != "runtime" || schema.GetVersion() != firstSchema.Version+1 {
		t.Fatalf("uploaded schema = %+v", schema)
	}
	if _, err := schemaClient.CreateSchema(authCtx, &kmsv1.CreateSchemaRequest{
		Application: appName, SchemaJson: changedDocument,
	}); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("duplicate schema upload error = %v, want AlreadyExists", err)
	}

	artifact, err := configstore.EncodeDefaultsArtifact(configstore.DefaultsArtifact{
		Format: configstore.DefaultsArtifactFormat, Profile: "dev", SchemaSHA256: schema.GetDigest(),
		Contract: []configstore.ContractEntry{
			{Alias: "api_key", Kind: configstore.ContractKindSecret},
			{Alias: "runtime", Kind: configstore.ContractKindParameter, ContentType: "json"},
		},
		Parameters: []configstore.DefaultsParameter{{Alias: "runtime", ContentType: "json", Value: `{"message":"hello"}`}},
	})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := adminClient.ApplyApplicationDefaults(authCtx, &kmsv1.ApplyApplicationDefaultsRequest{
		Namespace: networkNS(ns.Env, ns.App), Artifact: artifact, UpdateDefinition: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.GetExecuted() || !preview.GetDefinitionChanged() || preview.GetPlanDigest() == "" || len(preview.GetEntries()) != 1 || preview.GetEntries()[0].GetStatus() != domain.DefaultsStatusCreate || len(preview.GetMissingSecrets()) != 0 {
		t.Fatalf("preview = %+v", preview)
	}
	applied, err := adminClient.ApplyApplicationDefaults(authCtx, &kmsv1.ApplyApplicationDefaultsRequest{
		Namespace: networkNS(ns.Env, ns.App), Artifact: artifact, UpdateDefinition: true, Execute: true, PlanDigest: preview.GetPlanDigest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !applied.GetExecuted() || !applied.GetDefinitionUpdated() || applied.GetEntries()[0].GetAppliedVersion() != 1 || applied.GetEntries()[0].GetRevision() == 0 {
		t.Fatalf("applied = %+v", applied)
	}
	repinned, err := e.svc.GetApplication(ctx, admin, appName)
	if err != nil || repinned.SchemaVersion != schema.GetVersion() {
		t.Fatalf("repinned application = %+v err=%v", repinned, err)
	}

	shipped, err := e.svc.ShipApplicationChange(ctx, admin, domain.ShipInput{Application: appName, Environment: ns.Env})
	if err != nil {
		t.Fatal(err)
	}
	if shipped.Status != domain.ShipStatusActivated || shipped.Release == nil || shipped.Release.Version != 1 || len(shipped.Parameters) != 0 {
		t.Fatalf("zero-edit first release = %+v", shipped)
	}
}
