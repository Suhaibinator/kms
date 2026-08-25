package integration

import (
	"context"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
)

func TestLoopbackDefaultsBootstrapAndFirstRelease(t *testing.T) {
	e := newLoopbackTLSEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin := core.Principal{Identity: domain.Identity{Name: "network-root", Kind: domain.IdentityKindAdmin}, Method: domain.AuthMethodToken}

	const appName = "defaults-e2e"
	ns := domain.NamespaceRef{Env: "dev", App: appName}
	schema, err := e.svc.CreateConfigurationSchema(ctx, admin, "runtime", `{"type":"object","properties":{"runtime":{"type":"object"}},"required":["runtime"],"additionalProperties":false}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	contract := []domain.ApplicationContractField{
		{Alias: "api_key", Kind: domain.ReleaseEntrySecret},
		{Alias: "runtime", Kind: domain.ReleaseEntryParameter, ContentType: "json"},
	}
	if _, err := e.svc.CreateApplication(ctx, admin, domain.Application{
		Name: appName, ReleaseName: "runtime", SchemaID: schema.ID, SchemaVersion: schema.Version, Contract: contract,
	}); err != nil {
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

	artifact, err := configstore.EncodeDefaultsArtifact(configstore.DefaultsArtifact{
		Format: configstore.DefaultsArtifactFormat, Profile: "dev", SchemaSHA256: schema.Digest,
		Contract: []configstore.ContractEntry{
			{Alias: "api_key", Kind: configstore.ContractKindSecret},
			{Alias: "runtime", Kind: configstore.ContractKindParameter, ContentType: "json"},
		},
		Parameters: []configstore.DefaultsParameter{{Alias: "runtime", ContentType: "json", Value: `{"message":"hello"}`}},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := kmsv1.NewAdminServiceClient(e.adminConn)
	authCtx := networkAuthContext(ctx, e.adminToken)
	preview, err := client.ApplyApplicationDefaults(authCtx, &kmsv1.ApplyApplicationDefaultsRequest{
		Namespace: networkNS(ns.Env, ns.App), Artifact: artifact,
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.GetExecuted() || preview.GetPlanDigest() == "" || len(preview.GetEntries()) != 1 || preview.GetEntries()[0].GetStatus() != domain.DefaultsStatusCreate || len(preview.GetMissingSecrets()) != 0 {
		t.Fatalf("preview = %+v", preview)
	}
	applied, err := client.ApplyApplicationDefaults(authCtx, &kmsv1.ApplyApplicationDefaultsRequest{
		Namespace: networkNS(ns.Env, ns.App), Artifact: artifact, Execute: true, PlanDigest: preview.GetPlanDigest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !applied.GetExecuted() || applied.GetEntries()[0].GetAppliedVersion() != 1 || applied.GetEntries()[0].GetRevision() == 0 {
		t.Fatalf("applied = %+v", applied)
	}

	shipped, err := e.svc.ShipApplicationChange(ctx, admin, domain.ShipInput{Application: appName, Environment: ns.Env})
	if err != nil {
		t.Fatal(err)
	}
	if shipped.Status != domain.ShipStatusActivated || shipped.Release == nil || shipped.Release.Version != 1 || len(shipped.Parameters) != 0 {
		t.Fatalf("zero-edit first release = %+v", shipped)
	}
}
