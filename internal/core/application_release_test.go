package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
)

func applicationPlanEntry(t *testing.T, result domain.ApplicationReleaseCreateResult, alias string) domain.ApplicationReleasePlanEntry {
	t.Helper()
	for _, entry := range result.Entries {
		if entry.Alias == alias {
			return entry
		}
	}
	t.Fatalf("missing application release entry %q: %+v", alias, result.Entries)
	return domain.ApplicationReleasePlanEntry{}
}

func TestBuildApplicationReleasePlanRejectsInvalidContractSize(t *testing.T) {
	oversized := make([]domain.ApplicationContractField, maxReleaseEntries+1)
	for _, contract := range [][]domain.ApplicationContractField{nil, oversized} {
		_, err := (&Service{}).buildApplicationReleasePlan(context.Background(), adminPrincipal(), domain.Namespace{NamespaceRef: domain.NamespaceRef{Env: "dev", App: "app"}}, domain.Application{Name: "app", Contract: contract}, configstore.DefaultsArtifact{}, nil, "{}")
		if !errors.Is(err, domain.ErrFailedPrecondition) {
			t.Fatalf("contract size %d error = %v", len(contract), err)
		}
	}
}

func TestCreateApplicationReleaseCarriesActiveSecretAndNeverActivates(t *testing.T) {
	ctx := context.Background()
	svc, store := newConsoleTestService(t)
	admin := adminPrincipal()
	app := seedConsoleApp(t, svc, admin, "dev", "prod")
	ns := domain.NamespaceRef{Env: "dev", App: app.Name}
	secretNS := ns
	baseline, err := svc.CreateConfigurationRelease(ctx, admin, domain.CreateConfigurationReleaseInput{
		Namespace: ns, Name: app.ReleaseName, SchemaVersion: app.SchemaVersion, Metadata: `{"source":"baseline"}`,
		Entries: []domain.ReleaseEntrySelector{
			{Alias: "database", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: ns, Key: "database"}, Label: domain.LabelCurrent},
			{Alias: "db_password", Kind: domain.ReleaseEntrySecret, Ref: domain.Ref{NS: secretNS, Key: "db_password"}, Label: domain.LabelCurrent},
			{Alias: "rate_limits", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: ns, Key: "rate_limits"}, Label: domain.LabelCurrent},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	zero := uint64(0)
	active, _, err := svc.ActivateConfigurationRelease(ctx, admin, ns, app.ReleaseName, baseline.Version, &zero)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PutSecret(ctx, admin, PutSecretInput{Ref: domain.Ref{NS: secretNS, Key: "db_password"}, Value: []byte("rotated"), ContentType: "text/plain", Metadata: "{}"}); err != nil {
		t.Fatal(err)
	}
	artifact := consoleDefaultsArtifact(t, `{"host":"db.internal"}`, "5")
	preview, err := svc.CreateApplicationRelease(ctx, admin, domain.ApplicationReleaseCreateInput{Namespace: ns, Artifact: artifact, Metadata: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Valid || preview.Executed || preview.PlanDigest == "" || preview.BaseReleaseVersion != baseline.Version {
		t.Fatalf("preview = %+v", preview)
	}
	secret := applicationPlanEntry(t, preview, "db_password")
	if secret.Source != domain.ApplicationReleaseSourceCarriedActiveSecret || secret.FromVersion != 1 || secret.ToVersion != 1 || secret.Ref.NS != secretNS || secret.Ref.Key != "db_password" {
		t.Fatalf("carried secret = %+v", secret)
	}
	parameter := applicationPlanEntry(t, preview, "rate_limits")
	if parameter.Source != domain.ApplicationReleaseSourceGeneratedDefault || parameter.ToVersion != 1 {
		t.Fatalf("generated parameter = %+v", parameter)
	}
	executed, err := svc.CreateApplicationRelease(ctx, admin, domain.ApplicationReleaseCreateInput{Namespace: ns, Artifact: artifact, Metadata: "{}", Execute: true, PlanDigest: preview.PlanDigest})
	if err != nil {
		t.Fatal(err)
	}
	if !executed.Valid || !executed.Executed || !executed.Created || executed.Release == nil || executed.Release.Version != 2 {
		t.Fatalf("executed = %+v", executed)
	}
	retryPreview, err := svc.CreateApplicationRelease(ctx, admin, domain.ApplicationReleaseCreateInput{Namespace: ns, Artifact: artifact, Metadata: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	retried, err := svc.CreateApplicationRelease(ctx, admin, domain.ApplicationReleaseCreateInput{
		Namespace: ns, Artifact: artifact, Metadata: "{}", Execute: true, PlanDigest: retryPreview.PlanDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !retried.Executed || retried.Created || retried.Release == nil || retried.Release.Version != executed.Release.Version {
		t.Fatalf("idempotent retry = %+v", retried)
	}
	stillActive, err := store.GetActiveConfigurationRelease(ctx, ns, app.ReleaseName)
	if err != nil || stillActive.Release.Version != active.Release.Version || stillActive.ActivationRevision != active.ActivationRevision {
		t.Fatalf("application release creation activated: before=%+v after=%+v err=%v", active, stillActive, err)
	}
}

func TestCreateApplicationReleaseBootstrapsCurrentAndRejectsStalePlan(t *testing.T) {
	ctx := context.Background()
	svc, store := newConsoleTestService(t)
	admin := adminPrincipal()
	app := seedConsoleApp(t, svc, admin)
	ns := domain.NamespaceRef{Env: "dev", App: app.Name}
	// Inactive history must not become the source for a fresh environment's
	// secret. The current label advances to v2 after this v1 release.
	if _, err := svc.CreateConfigurationRelease(ctx, admin, domain.CreateConfigurationReleaseInput{
		Namespace: ns, Name: app.ReleaseName, SchemaVersion: app.SchemaVersion, Metadata: "{}",
		Entries: []domain.ReleaseEntrySelector{
			{Alias: "database", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: ns, Key: "database"}, Version: 1},
			{Alias: "db_password", Kind: domain.ReleaseEntrySecret, Ref: domain.Ref{NS: ns, Key: "db_password"}, Version: 1},
			{Alias: "rate_limits", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: ns, Key: "rate_limits"}, Version: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PutSecret(ctx, admin, PutSecretInput{Ref: domain.Ref{NS: ns, Key: "db_password"}, Value: []byte("v2"), ContentType: "text/plain", Metadata: "{}"}); err != nil {
		t.Fatal(err)
	}
	artifact := consoleDefaultsArtifact(t, `{"host":"db.internal"}`, "5")
	preview, err := svc.CreateApplicationRelease(ctx, admin, domain.ApplicationReleaseCreateInput{Namespace: ns, Artifact: artifact, Metadata: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	secret := applicationPlanEntry(t, preview, "db_password")
	if secret.Source != domain.ApplicationReleaseSourceResolvedCurrentSecret || secret.ToVersion != 2 {
		t.Fatalf("bootstrapped secret = %+v", secret)
	}
	if _, _, err := svc.PutParameter(ctx, admin, domain.Ref{NS: ns, Key: "rate_limits"}, "6", "integer", "{}"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateApplicationRelease(ctx, admin, domain.ApplicationReleaseCreateInput{Namespace: ns, Artifact: artifact, Metadata: "{}", Execute: true, PlanDigest: preview.PlanDigest}); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("stale plan error = %v", err)
	}
	rows, _, err := store.ListConfigurationReleases(ctx, ns, app.ReleaseName, storage.ListPage{Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("stale execute created a release: rows=%+v err=%v", rows, err)
	}
	if _, err := store.DeleteSecret(ctx, domain.Ref{NS: ns, Key: "db_password"}); err != nil {
		t.Fatal(err)
	}
	missing, err := svc.CreateApplicationRelease(ctx, admin, domain.ApplicationReleaseCreateInput{
		Namespace: ns, Artifact: consoleDefaultsArtifact(t, `{"host":"db.internal"}`, "6"), Metadata: "{}",
	})
	if err != nil {
		t.Fatal(err)
	}
	if missing.Valid || len(missing.MissingSecrets) != 1 || missing.MissingSecrets[0] != "db_password" {
		t.Fatalf("missing secret preview = %+v", missing)
	}
	if len(missing.Validation) != 1 || missing.Validation[0].Alias != "db_password" || missing.Validation[0].Code != domain.ReleaseValidationNotFound {
		t.Fatalf("missing secret validation = %+v", missing.Validation)
	}
}

func TestCreateApplicationReleaseRejectsSourceAndDefinitionDrift(t *testing.T) {
	ctx := context.Background()
	svc, _ := newConsoleTestService(t)
	admin := adminPrincipal()
	app := seedConsoleApp(t, svc, admin)
	ns := domain.NamespaceRef{Env: "dev", App: app.Name}

	if _, _, err := svc.PutParameter(ctx, admin, domain.Ref{NS: ns, Key: "rate_limits"}, "6", "integer", "{}"); err != nil {
		t.Fatal(err)
	}
	parameterDrift, err := svc.CreateApplicationRelease(ctx, admin, domain.ApplicationReleaseCreateInput{
		Namespace: ns, Artifact: consoleDefaultsArtifact(t, `{"host":"db.internal"}`, "5"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if parameterDrift.Valid || len(parameterDrift.Validation) != 1 ||
		parameterDrift.Validation[0].Alias != "rate_limits" || parameterDrift.Validation[0].Code != domain.ReleaseValidationDefaultMismatch {
		t.Fatalf("parameter drift preview = %+v", parameterDrift)
	}

	contractDriftArtifact, err := configstore.EncodeDefaultsArtifact(configstore.DefaultsArtifact{
		Format: configstore.DefaultsArtifactFormat, Profile: "dev", SchemaSHA256: sha256Hex([]byte(consoleSchema)),
		Contract: []configstore.ContractEntry{
			{Alias: "database", Kind: configstore.ContractKindParameter, ContentType: "json"},
			{Alias: "rate_limits", Kind: configstore.ContractKindParameter, ContentType: "integer"},
		},
		Parameters: []configstore.DefaultsParameter{
			{Alias: "database", ContentType: "json", Value: `{"host":"db.internal"}`},
			{Alias: "rate_limits", ContentType: "integer", Value: "6"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateApplicationRelease(ctx, admin, domain.ApplicationReleaseCreateInput{Namespace: ns, Artifact: contractDriftArtifact}); !errors.Is(err, domain.ErrFailedPrecondition) || !strings.Contains(err.Error(), "defaults apply") {
		t.Fatalf("contract drift error = %v", err)
	}

	unregisteredSchemaArtifact, err := configstore.EncodeDefaultsArtifact(configstore.DefaultsArtifact{
		Format: configstore.DefaultsArtifactFormat, Profile: "dev", SchemaSHA256: strings.Repeat("a", 64),
		Contract: []configstore.ContractEntry{
			{Alias: "database", Kind: configstore.ContractKindParameter, ContentType: "json"},
			{Alias: "db_password", Kind: configstore.ContractKindSecret},
			{Alias: "rate_limits", Kind: configstore.ContractKindParameter, ContentType: "integer"},
		},
		Parameters: []configstore.DefaultsParameter{
			{Alias: "database", ContentType: "json", Value: `{"host":"db.internal"}`},
			{Alias: "rate_limits", ContentType: "integer", Value: "6"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateApplicationRelease(ctx, admin, domain.ApplicationReleaseCreateInput{Namespace: ns, Artifact: unregisteredSchemaArtifact}); !errors.Is(err, domain.ErrFailedPrecondition) || !strings.Contains(err.Error(), "schema upload") {
		t.Fatalf("unregistered schema drift error = %v", err)
	}

	const changedSchema = `{"description":"changed","type":"object","properties":{"database":{"type":"object"},"rate_limits":{"type":"integer","minimum":0}},"required":["database","rate_limits"],"additionalProperties":false}`
	registered, err := svc.CreateConfigurationSchema(ctx, admin, app.Name, changedSchema, "{}")
	if err != nil {
		t.Fatal(err)
	}
	registeredSchemaArtifact, err := configstore.EncodeDefaultsArtifact(configstore.DefaultsArtifact{
		Format: configstore.DefaultsArtifactFormat, Profile: "dev", SchemaSHA256: registered.Digest,
		Contract: []configstore.ContractEntry{
			{Alias: "database", Kind: configstore.ContractKindParameter, ContentType: "json"},
			{Alias: "db_password", Kind: configstore.ContractKindSecret},
			{Alias: "rate_limits", Kind: configstore.ContractKindParameter, ContentType: "integer"},
		},
		Parameters: []configstore.DefaultsParameter{
			{Alias: "database", ContentType: "json", Value: `{"host":"db.internal"}`},
			{Alias: "rate_limits", ContentType: "integer", Value: "6"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateApplicationRelease(ctx, admin, domain.ApplicationReleaseCreateInput{Namespace: ns, Artifact: registeredSchemaArtifact}); !errors.Is(err, domain.ErrFailedPrecondition) || !strings.Contains(err.Error(), "defaults apply") || strings.Contains(err.Error(), "schema upload") {
		t.Fatalf("registered schema drift error = %v", err)
	}
}

func TestCreateApplicationReleaseRejectsArchivedApplicationBeforeNamespaceLookup(t *testing.T) {
	ctx := context.Background()
	svc, _ := newConsoleTestService(t)
	admin := adminPrincipal()
	const schema = `{"type":"object","properties":{"runtime":{"type":"string"}},"required":["runtime"],"additionalProperties":false}`
	app, _, err := svc.CreateApplicationWithSchema(ctx, admin, domain.Application{
		Name: "archived-app", ReleaseName: "runtime",
		Contract: []domain.ApplicationContractField{{Alias: "runtime", Kind: domain.ReleaseEntryParameter, ContentType: "string"}},
	}, schema, "{}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ArchiveApplication(ctx, admin, app.Name); err != nil {
		t.Fatal(err)
	}
	artifact, err := configstore.EncodeDefaultsArtifact(configstore.DefaultsArtifact{
		Format: configstore.DefaultsArtifactFormat, Profile: "dev", SchemaSHA256: sha256Hex([]byte(schema)),
		Contract:   []configstore.ContractEntry{{Alias: "runtime", Kind: configstore.ContractKindParameter, ContentType: "string"}},
		Parameters: []configstore.DefaultsParameter{{Alias: "runtime", ContentType: "string", Value: "default"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.CreateApplicationRelease(ctx, admin, domain.ApplicationReleaseCreateInput{
		Namespace: domain.NamespaceRef{Env: "dev", App: app.Name}, Artifact: artifact,
	})
	if !errors.Is(err, domain.ErrFailedPrecondition) || !strings.Contains(err.Error(), "archived") {
		t.Fatalf("archived application error = %v", err)
	}
}

func TestCreateApplicationReleaseOmitsRemovedAliasAndResolvesNewSecretCurrent(t *testing.T) {
	ctx := context.Background()
	svc, _ := newConsoleTestService(t)
	admin := adminPrincipal()
	app := seedConsoleApp(t, svc, admin)
	ns := domain.NamespaceRef{Env: "dev", App: app.Name}
	baseline, err := svc.CreateConfigurationRelease(ctx, admin, domain.CreateConfigurationReleaseInput{
		Namespace: ns, Name: app.ReleaseName, SchemaVersion: app.SchemaVersion, Metadata: "{}",
		Entries: []domain.ReleaseEntrySelector{
			{Alias: "database", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: ns, Key: "database"}, Label: domain.LabelCurrent},
			{Alias: "db_password", Kind: domain.ReleaseEntrySecret, Ref: domain.Ref{NS: ns, Key: "db_password"}, Label: domain.LabelCurrent},
			{Alias: "rate_limits", Kind: domain.ReleaseEntryParameter, Ref: domain.Ref{NS: ns, Key: "rate_limits"}, Label: domain.LabelCurrent},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	zero := uint64(0)
	if _, _, err := svc.ActivateConfigurationRelease(ctx, admin, ns, app.ReleaseName, baseline.Version, &zero); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PutSecret(ctx, admin, PutSecretInput{
		Ref: domain.Ref{NS: ns, Key: "signing_key"}, Value: []byte("new-secret"), ContentType: "text/plain", Metadata: "{}",
	}); err != nil {
		t.Fatal(err)
	}
	artifact, err := configstore.EncodeDefaultsArtifact(configstore.DefaultsArtifact{
		Format: configstore.DefaultsArtifactFormat, Profile: "dev", SchemaSHA256: sha256Hex([]byte(consoleSchema)),
		Contract: []configstore.ContractEntry{
			{Alias: "database", Kind: configstore.ContractKindParameter, ContentType: "json"},
			{Alias: "rate_limits", Kind: configstore.ContractKindParameter, ContentType: "integer"},
			{Alias: "signing_key", Kind: configstore.ContractKindSecret},
		},
		Parameters: []configstore.DefaultsParameter{
			{Alias: "database", ContentType: "json", Value: `{"host":"db.internal"}`},
			{Alias: "rate_limits", ContentType: "integer", Value: "5"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	definitionPreview, err := svc.ApplyApplicationDefaults(ctx, admin, domain.DefaultsApplyInput{
		Namespace: ns, Artifact: artifact, UpdateDefinition: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyApplicationDefaults(ctx, admin, domain.DefaultsApplyInput{
		Namespace: ns, Artifact: artifact, UpdateDefinition: true, Execute: true, PlanDigest: definitionPreview.PlanDigest,
	}); err != nil {
		t.Fatal(err)
	}
	preview, err := svc.CreateApplicationRelease(ctx, admin, domain.ApplicationReleaseCreateInput{Namespace: ns, Artifact: artifact})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Valid || len(preview.Entries) != 3 {
		t.Fatalf("updated-contract release preview = %+v", preview)
	}
	for _, entry := range preview.Entries {
		if entry.Alias == "db_password" {
			t.Fatalf("removed alias survived in preview: %+v", preview.Entries)
		}
	}
	newSecret := applicationPlanEntry(t, preview, "signing_key")
	if newSecret.Source != domain.ApplicationReleaseSourceResolvedCurrentSecret || newSecret.FromVersion != 0 || newSecret.ToVersion != 1 {
		t.Fatalf("new secret pin = %+v", newSecret)
	}
}
