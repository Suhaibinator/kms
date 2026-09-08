package storage

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

func TestApplyDefaultsRollsBackEveryVersionAndChange(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	namespace := seedNS(t, store, "dev", "worker")
	app, err := store.GetApplication(ctx, "worker")
	if err != nil {
		t.Fatal(err)
	}
	app.Contract = []domain.ApplicationContractField{
		{Alias: "a", Kind: domain.ReleaseEntryParameter, ContentType: "string"},
		{Alias: "b", Kind: domain.ReleaseEntryParameter, ContentType: "string"},
	}
	if _, err := store.UpdateApplication(ctx, app); err != nil {
		t.Fatal(err)
	}
	app, err = store.GetApplication(ctx, app.Name)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := store.CreateConfigurationSchema(ctx, domain.ConfigurationSchema{Application: app.Name, ReleaseName: app.ReleaseName, Schema: `{"type":"object"}`, Digest: strings.Repeat("a", 64), Metadata: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Exec(`CREATE TRIGGER fail_defaults_b BEFORE INSERT ON parameter_versions
		WHEN (SELECT name FROM parameters WHERE id = NEW.parameter_id) = 'b'
		BEGIN SELECT RAISE(ABORT, 'forced defaults failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	in := DefaultsApplyTransaction{
		Namespace: namespace.NamespaceRef, NamespaceID: namespace.ID, ReleaseName: "runtime",
		Contract: app.Contract, ResolutionState: []DefaultsResolutionState{{Environment: "dev", NamespaceID: namespace.ID}},
		Parameters: []DefaultsParameterExpectation{
			{Alias: "a", Key: "a", Value: "first", ContentType: "string", Write: true},
			{Alias: "b", Key: "b", Value: "second", ContentType: "string", Write: true},
		},
		CreatedBy:                        "admin",
		UpdateDefinition:                 true,
		ExpectedApplicationSchemaVersion: app.SchemaVersion, ExpectedApplicationContract: app.Contract, ExpectedApplicationUpdatedAt: app.UpdatedAt,
		SchemaDigest: schema.Digest, DesiredSchemaVersion: schema.Version, DesiredContract: app.Contract,
	}
	if _, err := store.ApplyDefaults(ctx, in); err == nil || !strings.Contains(err.Error(), "forced defaults failure") {
		t.Fatalf("expected forced second-write failure, got %v", err)
	}
	for _, key := range []string{"a", "b"} {
		if _, err := store.GetParameter(ctx, domain.Ref{NS: namespace.NamespaceRef, Key: key}, 0, ""); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("parameter %s survived rollback: %v", key, err)
		}
	}
	if revision, err := store.CurrentRevision(ctx); err != nil || revision != 0 {
		t.Fatalf("revision after rollback = %d err=%v", revision, err)
	}
	unchanged, err := store.GetApplication(ctx, "worker")
	if err != nil || unchanged.SchemaVersion != app.SchemaVersion || len(unchanged.Contract) != 2 || unchanged.Contract[0].Alias != "a" {
		t.Fatalf("application definition survived rollback incorrectly: %+v err=%v", unchanged, err)
	}
}

func TestApplyDefaultsRejectsStaleResourceInventory(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	namespace := seedNS(t, store, "dev", "worker")
	app, err := store.GetApplication(ctx, "worker")
	if err != nil {
		t.Fatal(err)
	}
	app.Contract = []domain.ApplicationContractField{{Alias: "runtime", Kind: domain.ReleaseEntryParameter, ContentType: "string"}}
	if _, err := store.UpdateApplication(ctx, app); err != nil {
		t.Fatal(err)
	}
	in := DefaultsApplyTransaction{
		Namespace: namespace.NamespaceRef, NamespaceID: namespace.ID, ReleaseName: "runtime", Contract: app.Contract,
		ResolutionState: []DefaultsResolutionState{{Environment: "dev", NamespaceID: namespace.ID}},
		Parameters:      []DefaultsParameterExpectation{{Alias: "runtime", Key: "runtime", Value: "default", ContentType: "string", Write: true}},
		CreatedBy:       "admin",
	}
	if _, _, err := store.PutParameter(ctx, domain.Ref{NS: namespace.NamespaceRef, Key: "other"}, "race", "string", "{}", "racer"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyDefaults(ctx, in); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("stale inventory error = %v", err)
	}
	if _, err := store.GetParameter(ctx, domain.Ref{NS: namespace.NamespaceRef, Key: "runtime"}, 0, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("stale apply wrote runtime: %v", err)
	}
}

func TestApplyDefaultsSchemaFreeTrackRejectsRegistryDigest(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	ns := seedNS(t, st, "dev", "schemafree")
	schema, err := st.CreateConfigurationSchema(ctx, domain.ConfigurationSchema{Application: ns.App, ReleaseName: "runtime", Schema: `{"type":"object"}`, Digest: strings.Repeat("b", 64), Metadata: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	app, err := st.GetApplication(ctx, ns.App)
	if err != nil {
		t.Fatal(err)
	}
	app.SchemaVersion = schema.Version
	app.Contract = []domain.ApplicationContractField{{Alias: "foreign", Kind: domain.ReleaseEntryParameter, ContentType: "integer"}}
	if _, err := st.UpdateApplication(ctx, app); err != nil {
		t.Fatal(err)
	}
	in := DefaultsApplyTransaction{
		Namespace: ns.NamespaceRef, NamespaceID: ns.ID, ReleaseName: "runtime", SchemaVersion: 0, SchemaDigest: schema.Digest,
		Contract:        []domain.ApplicationContractField{{Alias: "setting", Kind: domain.ReleaseEntryParameter, ContentType: "string"}},
		ResolutionState: []DefaultsResolutionState{{Environment: ns.Env, NamespaceID: ns.ID, SchemaVersion: 0}},
		Parameters:      []DefaultsParameterExpectation{{Alias: "setting", Key: "setting", Value: "default", ContentType: "string", Write: true}}, CreatedBy: "admin",
	}
	if _, err := st.ApplyDefaults(ctx, in); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("schema0 accepted registry digest: %v", err)
	}
	if _, err := st.GetParameter(ctx, domain.Ref{NS: ns.NamespaceRef, Key: "setting"}, 0, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("mismatched schema wrote parameter: %v", err)
	}
	in.SchemaDigest = ""
	writes, err := st.ApplyDefaults(ctx, in)
	if err != nil || len(writes) != 1 {
		t.Fatalf("schema-free defaults: %+v %v", writes, err)
	}
	exact, err := st.GetConfigurationSchemaContract(ctx, ns.App, "runtime", 0)
	if err != nil || len(exact) != 1 || exact[0].Alias != "setting" {
		t.Fatalf("schema-free contract: %+v %v", exact, err)
	}
	persisted, err := st.GetApplication(ctx, ns.App)
	if err != nil || persisted.SchemaVersion != schema.Version {
		t.Fatalf("schema-free import changed default: %+v %v", persisted, err)
	}
	registered, err := st.GetConfigurationSchemaContract(ctx, ns.App, "runtime", schema.Version)
	if err != nil || len(registered) != 1 || registered[0].Alias != "foreign" {
		t.Fatalf("schema-free import changed registry contract: %+v %v", registered, err)
	}
}
