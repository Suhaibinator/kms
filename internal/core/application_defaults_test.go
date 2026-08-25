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

func consoleDefaultsArtifact(t *testing.T, database, rateLimits string) []byte {
	t.Helper()
	artifact, err := configstore.EncodeDefaultsArtifact(configstore.DefaultsArtifact{
		Format: configstore.DefaultsArtifactFormat, Profile: "dev", SchemaSHA256: sha256Hex([]byte(consoleSchema)),
		Contract: []configstore.ContractEntry{
			{Alias: "database", Kind: configstore.ContractKindParameter, ContentType: "json"},
			{Alias: "db_password", Kind: configstore.ContractKindSecret},
			{Alias: "rate_limits", Kind: configstore.ContractKindParameter, ContentType: "integer"},
		},
		Parameters: []configstore.DefaultsParameter{
			{Alias: "database", ContentType: "json", Value: database},
			{Alias: "rate_limits", ContentType: "integer", Value: rateLimits},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func defaultsEntry(t *testing.T, result domain.DefaultsApplyResult, alias string) domain.DefaultsApplyEntry {
	t.Helper()
	for _, entry := range result.Entries {
		if entry.Alias == alias {
			return entry
		}
	}
	t.Fatalf("missing defaults entry %q: %+v", alias, result.Entries)
	return domain.DefaultsApplyEntry{}
}

type countingDefaultsHub struct{ wakes int }

func (h *countingDefaultsHub) Wake()                            { h.wakes++ }
func (h *countingDefaultsHub) Subscribers() []domain.Subscriber { return nil }

func TestApplicationDefaultsPreviewExecuteIdempotencyAndRedaction(t *testing.T) {
	ctx := context.Background()
	svc, store := newConsoleTestService(t)
	admin := adminPrincipal()
	seedConsoleApp(t, svc, admin)
	ns := domain.NamespaceRef{Env: "dev", App: "gradethis"}
	artifact := consoleDefaultsArtifact(t, `{"host":"db.internal"}`, "7")
	hub := &countingDefaultsHub{}
	svc.SetHub(hub)

	preview, err := svc.ApplyApplicationDefaults(ctx, admin, domain.DefaultsApplyInput{Namespace: ns, Artifact: artifact})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Executed || preview.PlanDigest == "" || hub.wakes != 0 {
		t.Fatalf("preview = %+v wakes=%d", preview, hub.wakes)
	}
	if got := defaultsEntry(t, preview, "database"); got.Status != domain.DefaultsStatusUnchanged || got.CurrentVersion != 1 {
		t.Fatalf("database preview = %+v", got)
	}
	if got := defaultsEntry(t, preview, "rate_limits"); got.Status != domain.DefaultsStatusBlocked || got.CurrentVersion != 1 {
		t.Fatalf("rate_limits preview = %+v", got)
	}
	if len(preview.MissingSecrets) != 0 {
		t.Fatalf("missing secrets = %v", preview.MissingSecrets)
	}
	if _, err := svc.ApplyApplicationDefaults(ctx, admin, domain.DefaultsApplyInput{Namespace: ns, Artifact: artifact, Execute: true, PlanDigest: preview.PlanDigest}); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("blocked execute error = %v", err)
	}

	overwrite, err := svc.ApplyApplicationDefaults(ctx, admin, domain.DefaultsApplyInput{Namespace: ns, Artifact: artifact, Overwrite: true})
	if err != nil || defaultsEntry(t, overwrite, "rate_limits").Status != domain.DefaultsStatusUpdate {
		t.Fatalf("overwrite preview = %+v err=%v", overwrite, err)
	}
	executed, err := svc.ApplyApplicationDefaults(ctx, admin, domain.DefaultsApplyInput{
		Namespace: ns, Artifact: artifact, Overwrite: true, Execute: true, PlanDigest: overwrite.PlanDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !executed.Executed || hub.wakes != 1 {
		t.Fatalf("execute = %+v wakes=%d", executed, hub.wakes)
	}
	if got := defaultsEntry(t, executed, "rate_limits"); got.AppliedVersion != 2 || got.Revision == 0 {
		t.Fatalf("applied rate_limits = %+v", got)
	}
	database, err := store.GetParameter(ctx, domain.Ref{NS: ns, Key: "database"}, 0, "")
	if err != nil || database.Version != 1 {
		t.Fatalf("unchanged database = %+v err=%v", database, err)
	}

	retry, err := svc.ApplyApplicationDefaults(ctx, admin, domain.DefaultsApplyInput{Namespace: ns, Artifact: artifact})
	if err != nil || defaultsEntry(t, retry, "rate_limits").Status != domain.DefaultsStatusUnchanged {
		t.Fatalf("retry preview = %+v err=%v", retry, err)
	}
	noop, err := svc.ApplyApplicationDefaults(ctx, admin, domain.DefaultsApplyInput{Namespace: ns, Artifact: artifact, Execute: true, PlanDigest: retry.PlanDigest})
	if err != nil || !noop.Executed || hub.wakes != 1 {
		t.Fatalf("no-op execute = %+v err=%v wakes=%d", noop, err, hub.wakes)
	}

	events, _, err := store.ListAudit(ctx, domain.AuditFilter{Env: ns.Env, App: ns.App, EventType: "application.defaults.apply"}, storage.ListPage{Limit: 100})
	if err != nil || len(events) == 0 {
		t.Fatalf("defaults audit = %+v err=%v", events, err)
	}
	for _, event := range events {
		rendered := event.Metadata + event.ResourceKey
		if strings.Contains(rendered, "db.internal") || strings.Contains(rendered, "\"7\"") || strings.Contains(rendered, preview.ArtifactDigest) {
			t.Fatalf("audit leaked artifact data: %+v", event)
		}
	}
}

func TestApplicationDefaultsMissingSecretsFreshImportAndAuthorization(t *testing.T) {
	ctx := context.Background()
	svc, store := newConsoleTestService(t)
	admin := adminPrincipal()
	schema, err := svc.CreateConfigurationSchema(ctx, admin, "runtime", consoleSchema, "{}")
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.CreateApplication(ctx, admin, domain.Application{Name: "gradethis", ReleaseName: "runtime", SchemaID: schema.ID, SchemaVersion: schema.Version, Contract: []domain.ApplicationContractField{
		{Alias: "database", Kind: domain.ReleaseEntryParameter, ContentType: "json"},
		{Alias: "db_password", Kind: domain.ReleaseEntrySecret},
		{Alias: "rate_limits", Kind: domain.ReleaseEntryParameter, ContentType: "integer"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ns := domain.NamespaceRef{Env: "dev", App: "gradethis"}
	if _, err := svc.CreateNamespace(ctx, admin, ns, "", nil); err != nil {
		t.Fatal(err)
	}
	artifact := consoleDefaultsArtifact(t, `{"host":"localhost"}`, "5")
	preview, err := svc.ApplyApplicationDefaults(ctx, admin, domain.DefaultsApplyInput{Namespace: ns, Artifact: artifact})
	if err != nil || len(preview.MissingSecrets) != 1 || preview.MissingSecrets[0] != "db_password" {
		t.Fatalf("fresh preview = %+v err=%v", preview, err)
	}
	for _, entry := range preview.Entries {
		if entry.Status != domain.DefaultsStatusCreate {
			t.Fatalf("fresh entry = %+v", entry)
		}
	}
	executed, err := svc.ApplyApplicationDefaults(ctx, admin, domain.DefaultsApplyInput{Namespace: ns, Artifact: artifact, Execute: true, PlanDigest: preview.PlanDigest})
	if err != nil || !executed.Executed {
		t.Fatalf("fresh execute = %+v err=%v", executed, err)
	}
	if _, err := store.GetSecretInfo(ctx, domain.Ref{NS: ns, Key: "db_password"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("defaults import created a secret: %v", err)
	}
	client := clientPrincipal("client")
	if _, err := svc.ApplyApplicationDefaults(ctx, client, domain.DefaultsApplyInput{Namespace: ns, Artifact: artifact}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("client defaults error = %v", err)
	}
}

func TestApplicationDefaultsRejectsStalePlansAndRecreatedNamespaces(t *testing.T) {
	ctx := context.Background()
	svc, _ := newConsoleTestService(t)
	admin := adminPrincipal()
	seedConsoleApp(t, svc, admin)
	ns := domain.NamespaceRef{Env: "dev", App: "gradethis"}
	artifact := consoleDefaultsArtifact(t, `{"host":"db.internal"}`, "7")
	preview, err := svc.ApplyApplicationDefaults(ctx, admin, domain.DefaultsApplyInput{Namespace: ns, Artifact: artifact, Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.PutParameter(ctx, admin, domain.Ref{NS: ns, Key: "rate_limits"}, "6", "integer", "{}"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyApplicationDefaults(ctx, admin, domain.DefaultsApplyInput{Namespace: ns, Artifact: artifact, Overwrite: true, Execute: true, PlanDigest: preview.PlanDigest}); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("concurrent write error = %v", err)
	}

	// An empty application proves namespace incarnation is part of the digest.
	svc2, _ := newConsoleTestService(t)
	if _, err := svc2.CreateApplication(ctx, admin, domain.Application{Name: "worker", ReleaseName: "runtime", Contract: []domain.ApplicationContractField{{Alias: "runtime", Kind: domain.ReleaseEntryParameter, ContentType: "string"}}}); err != nil {
		t.Fatal(err)
	}
	workerNS := domain.NamespaceRef{Env: "dev", App: "worker"}
	if _, err := svc2.CreateNamespace(ctx, admin, workerNS, "", nil); err != nil {
		t.Fatal(err)
	}
	workerArtifact, err := configstore.EncodeDefaultsArtifact(configstore.DefaultsArtifact{
		Format: configstore.DefaultsArtifactFormat, Profile: "dev", SchemaSHA256: strings.Repeat("0", 64),
		Contract:   []configstore.ContractEntry{{Alias: "runtime", Kind: configstore.ContractKindParameter, ContentType: "string"}},
		Parameters: []configstore.DefaultsParameter{{Alias: "runtime", ContentType: "string", Value: "default"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	workerPreview, err := svc2.ApplyApplicationDefaults(ctx, admin, domain.DefaultsApplyInput{Namespace: workerNS, Artifact: workerArtifact})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc2.DeleteNamespace(ctx, admin, workerNS); err != nil {
		t.Fatal(err)
	}
	if _, err := svc2.CreateNamespace(ctx, admin, workerNS, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc2.ApplyApplicationDefaults(ctx, admin, domain.DefaultsApplyInput{Namespace: workerNS, Artifact: workerArtifact, Execute: true, PlanDigest: workerPreview.PlanDigest}); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("namespace recreation error = %v", err)
	}
}

func TestApplicationDefaultsPlanPinsCrossEnvironmentResourceInventory(t *testing.T) {
	ctx := context.Background()
	svc, _ := newConsoleTestService(t)
	admin := adminPrincipal()
	if _, err := svc.CreateApplication(ctx, admin, domain.Application{Name: "worker", ReleaseName: "runtime", Contract: []domain.ApplicationContractField{{Alias: "runtime", Kind: domain.ReleaseEntryParameter, ContentType: "string"}}}); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"dev", "prod"} {
		if _, err := svc.CreateNamespace(ctx, admin, domain.NamespaceRef{Env: env, App: "worker"}, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	artifact, err := configstore.EncodeDefaultsArtifact(configstore.DefaultsArtifact{
		Format: configstore.DefaultsArtifactFormat, Profile: "dev", SchemaSHA256: strings.Repeat("0", 64),
		Contract:   []configstore.ContractEntry{{Alias: "runtime", Kind: configstore.ContractKindParameter, ContentType: "string"}},
		Parameters: []configstore.DefaultsParameter{{Alias: "runtime", ContentType: "string", Value: "default"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ns := domain.NamespaceRef{Env: "dev", App: "worker"}
	preview, err := svc.ApplyApplicationDefaults(ctx, admin, domain.DefaultsApplyInput{Namespace: ns, Artifact: artifact})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.PutParameter(ctx, admin, domain.Ref{NS: domain.NamespaceRef{Env: "prod", App: "worker"}, Key: "runtime"}, "prod", "string", "{}"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyApplicationDefaults(ctx, admin, domain.DefaultsApplyInput{Namespace: ns, Artifact: artifact, Execute: true, PlanDigest: preview.PlanDigest}); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("cross-environment resource race error = %v", err)
	}
}

func TestApplicationDefaultsParserDelegatesToSDK(t *testing.T) {
	invalidUTF8 := append(consoleDefaultsArtifact(t, `{}`, "1"), 0xff)
	_, err := parseDefaultsArtifact(invalidUTF8)
	if !errors.Is(err, domain.ErrInvalidArgument) || len(err.Error()) > 128 || strings.Contains(err.Error(), "xff") {
		t.Fatalf("invalid UTF-8 error = %q", err)
	}
	artifact := consoleDefaultsArtifact(t, `{"ok":true}`, "1")
	withoutNewline := artifact[:len(artifact)-1]
	if _, err := configstore.ParseDefaultsArtifact(withoutNewline); err != nil {
		t.Fatalf("SDK parser rejected non-newline artifact: %v", err)
	}
	if _, err := parseDefaultsArtifact(withoutNewline); err != nil {
		t.Fatalf("server parser diverged from SDK: %v", err)
	}
}
