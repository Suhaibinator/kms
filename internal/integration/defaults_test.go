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

func TestLoopbackDefaultsReleaseCreationCarriesSecretAndStaysInactive(t *testing.T) {
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

	releasePreview, err := adminClient.CreateApplicationRelease(authCtx, &kmsv1.CreateApplicationReleaseRequest{
		Namespace: networkNS(ns.Env, ns.App), Artifact: artifact, MetadataJson: `{"source":"integration"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !releasePreview.GetValid() || releasePreview.GetExecuted() || releasePreview.GetPlanDigest() == "" || releasePreview.GetBaseReleaseVersion() != 0 {
		t.Fatalf("first release preview = %+v", releasePreview)
	}
	createdFirst, err := adminClient.CreateApplicationRelease(authCtx, &kmsv1.CreateApplicationReleaseRequest{
		Namespace: networkNS(ns.Env, ns.App), Artifact: artifact, MetadataJson: `{"source":"integration"}`,
		Execute: true, PlanDigest: releasePreview.GetPlanDigest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	firstRelease := createdFirst.GetRelease()
	if !createdFirst.GetExecuted() || !createdFirst.GetCreated() || firstRelease == nil || firstRelease.GetVersion() != 1 {
		t.Fatalf("first release create = %+v", createdFirst)
	}
	releases := kmsv1.NewConfigurationReleaseServiceClient(e.adminConn)
	if _, err := releases.GetActiveRelease(authCtx, &kmsv1.GetActiveReleaseRequest{
		Namespace: networkNS(ns.Env, ns.App), Name: "runtime",
	}); status.Code(err) != codes.NotFound {
		t.Fatalf("release creation unexpectedly activated v1: %v", err)
	}
	zero := uint64(0)
	activatedFirst, err := releases.ActivateRelease(authCtx, &kmsv1.ActivateReleaseRequest{
		Namespace: networkNS(ns.Env, ns.App), Name: "runtime", Version: firstRelease.GetVersion(), ExpectedCurrentVersion: &zero,
	})
	if err != nil || !activatedFirst.GetChanged() {
		t.Fatalf("activate first release = %+v err=%v", activatedFirst, err)
	}

	rotated, err := e.svc.PutSecret(ctx, admin, core.PutSecretInput{
		Ref: domain.Ref{NS: ns, Key: "api_key"}, Value: []byte("rotated-secret"), ContentType: "text/plain", Metadata: "{}",
	})
	if err != nil || rotated.Version != 2 {
		t.Fatalf("rotate secret = %+v err=%v", rotated, err)
	}
	updatedArtifact, err := configstore.EncodeDefaultsArtifact(configstore.DefaultsArtifact{
		Format: configstore.DefaultsArtifactFormat, Profile: "dev", SchemaSHA256: schema.GetDigest(),
		Contract: []configstore.ContractEntry{
			{Alias: "api_key", Kind: configstore.ContractKindSecret},
			{Alias: "runtime", Kind: configstore.ContractKindParameter, ContentType: "json"},
		},
		Parameters: []configstore.DefaultsParameter{{Alias: "runtime", ContentType: "json", Value: `{"message":"updated"}`}},
	})
	if err != nil {
		t.Fatal(err)
	}
	updatePreview, err := adminClient.ApplyApplicationDefaults(authCtx, &kmsv1.ApplyApplicationDefaultsRequest{
		Namespace: networkNS(ns.Env, ns.App), Artifact: updatedArtifact, Overwrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := adminClient.ApplyApplicationDefaults(authCtx, &kmsv1.ApplyApplicationDefaultsRequest{
		Namespace: networkNS(ns.Env, ns.App), Artifact: updatedArtifact, Overwrite: true,
		Execute: true, PlanDigest: updatePreview.GetPlanDigest(),
	})
	if err != nil || !updated.GetExecuted() || updated.GetEntries()[0].GetAppliedVersion() != 2 {
		t.Fatalf("updated defaults = %+v err=%v", updated, err)
	}

	nextPreview, err := adminClient.CreateApplicationRelease(authCtx, &kmsv1.CreateApplicationReleaseRequest{
		Namespace: networkNS(ns.Env, ns.App), Artifact: updatedArtifact, MetadataJson: `{"source":"integration"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	var parameterPin, secretPin *kmsv1.ApplicationReleasePlanEntry
	for _, entry := range nextPreview.GetEntries() {
		switch entry.GetAlias() {
		case "runtime":
			parameterPin = entry
		case "api_key":
			secretPin = entry
		}
	}
	if !nextPreview.GetValid() || parameterPin == nil || parameterPin.GetToVersion() != 2 ||
		secretPin == nil || secretPin.GetFromVersion() != 1 || secretPin.GetToVersion() != 1 || secretPin.GetSource() != domain.ApplicationReleaseSourceCarriedActiveSecret {
		t.Fatalf("next release preview = %+v", nextPreview)
	}
	createdNext, err := adminClient.CreateApplicationRelease(authCtx, &kmsv1.CreateApplicationReleaseRequest{
		Namespace: networkNS(ns.Env, ns.App), Artifact: updatedArtifact, MetadataJson: `{"source":"integration"}`,
		Execute: true, PlanDigest: nextPreview.GetPlanDigest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	nextRelease := createdNext.GetRelease()
	if !createdNext.GetCreated() || nextRelease == nil || nextRelease.GetVersion() != 2 {
		t.Fatalf("next release create = %+v", createdNext)
	}
	stillActive, err := releases.GetActiveRelease(authCtx, &kmsv1.GetActiveReleaseRequest{
		Namespace: networkNS(ns.Env, ns.App), Name: "runtime",
	})
	if err != nil || stillActive.GetRelease().GetVersion() != 1 || stillActive.GetActivationRevision() != activatedFirst.GetActivationRevision() {
		t.Fatalf("release creation changed activation: %+v err=%v", stillActive, err)
	}
	listed, err := releases.ListReleases(authCtx, &kmsv1.ListReleasesRequest{
		Namespace: networkNS(ns.Env, ns.App), Name: "runtime", PageSize: 10,
	})
	if err != nil || len(listed.GetReleases()) != 2 {
		t.Fatalf("portal release list = %+v err=%v", listed, err)
	}
	expectedFirst := uint64(1)
	activatedNext, err := releases.ActivateRelease(authCtx, &kmsv1.ActivateReleaseRequest{
		Namespace: networkNS(ns.Env, ns.App), Name: "runtime", Version: nextRelease.GetVersion(), ExpectedCurrentVersion: &expectedFirst,
	})
	if err != nil || !activatedNext.GetChanged() || activatedNext.GetCurrentVersion() != 2 {
		t.Fatalf("activate next release = %+v err=%v", activatedNext, err)
	}
}
