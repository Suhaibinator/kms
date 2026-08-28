package storage

import (
	"context"
	"errors"
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
		CreatedBy:        "admin",
		UpdateDefinition: true,
		DesiredContract:  []domain.ApplicationContractField{{Alias: "replacement", Kind: domain.ReleaseEntryParameter, ContentType: "string"}},
	}
	if _, err := store.ApplyDefaults(ctx, in); err == nil {
		t.Fatal("ApplyDefaults succeeded despite forced second-write failure")
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
	if err != nil || len(unchanged.Contract) != 2 || unchanged.Contract[0].Alias != "a" {
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
