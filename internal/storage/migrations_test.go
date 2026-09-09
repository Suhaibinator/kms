package storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"gorm.io/gorm"
)

func storageMigrationFixture(t *testing.T) (*SQLStore, ApplicationMigrationTransaction) {
	t.Helper()
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "dev", "app")
	ns := nsRef("dev", "app")
	r := ref("dev", "app", "config")
	if _, _, err := st.PutParameter(ctx, r, "old", "string", "{}", "admin"); err != nil {
		t.Fatal(err)
	}
	base, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{Namespace: ns, Name: "runtime", Digest: "base", Entries: []domain.ConfigurationReleaseEntry{{Alias: "config", Kind: domain.ReleaseEntryParameter, Ref: r, Version: 1, ContentType: "string", Metadata: "{}", ParameterDigest: fmt.Sprintf("%x", sha256.Sum256([]byte("old")))}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.ActivateConfigurationRelease(ctx, domain.ReleaseTrack{Namespace: ns, Name: "runtime"}, base.Version, nil); err != nil {
		t.Fatal(err)
	}
	resources := []MigrationResource{{Kind: domain.ReleaseEntryParameter, Key: "config", Version: 1, Write: true}}
	schema, err := st.CreateConfigurationSchema(ctx, domain.ConfigurationSchema{Application: "app", ReleaseName: "runtime", Schema: `{"type":"object","x-kms-contract":[{"alias":"renamed","kind":"parameter","content_type":"string"}]}`, Digest: "target-schema"})
	if err != nil {
		t.Fatal(err)
	}
	target := domain.ReleaseTrack{Namespace: ns, Name: "runtime", SchemaVersion: schema.Version}
	state, err := st.ApplicationMigrationSnapshot(ctx, base.Track(), target, resources...)
	if err != nil {
		t.Fatal(err)
	}
	active, err := st.GetActiveConfigurationRelease(ctx, base.Track())
	if err != nil {
		t.Fatal(err)
	}
	return st, ApplicationMigrationTransaction{ExpectedSourceVersion: base.Version, ExpectedSourceActivationRevision: active.ActivationRevision, Resources: resources, Namespace: ns, Snapshot: state.Digest, Contract: []domain.ApplicationContractField{{Alias: "renamed", Kind: domain.ReleaseEntryParameter, ContentType: "string"}}, ExpectedActiveVersion: 0, Release: domain.ConfigurationRelease{SchemaVersion: schema.Version, Namespace: ns, Name: "runtime", Digest: "candidate", CreatedBy: "admin", Entries: []domain.ConfigurationReleaseEntry{{Alias: "renamed", Kind: domain.ReleaseEntryParameter, Ref: r, Version: 2, ContentType: "string", Metadata: "{}", ParameterDigest: fmt.Sprintf("%x", sha256.Sum256([]byte("new")))}}}, Writes: []MigrationParameterWrite{{Alias: "renamed", Key: "config", Value: "new", ContentType: "string", Version: 2}}, Audit: domain.AuditEvent{EventType: "application.release.migrate", Decision: "allow"}}
}
func TestApplicationMigrationTransactionRollsBackEveryStage(t *testing.T) {
	for _, table := range []string{"parameter_versions", "configuration_releases", "configuration_release_activations", "parameter.write", "configuration_release.create", "configuration_release.activate", "application.release.migrate"} {
		t.Run(table, func(t *testing.T) {
			ctx := context.Background()
			st, in := storageMigrationFixture(t)
			in.ResourceAudits = []domain.AuditEvent{
				{EventType: "parameter.write", ResourceType: domain.ResourceParameter, ResourceVersion: 2, Decision: "allow"},
				{EventType: "configuration_release.create", ResourceType: domain.ResourceConfigurationRelease, Decision: "allow"},
				{EventType: "configuration_release.activate", ResourceType: domain.ResourceConfigurationRelease, Decision: "allow"},
			}
			before, _ := st.GetApplication(ctx, "app")
			revision, _ := st.CurrentRevision(ctx)
			failure := errors.New("injected transaction failure")
			if err := st.db.Callback().Create().Before("gorm:create").Register("migration_failure", func(tx *gorm.DB) {
				audit, isAudit := tx.Statement.Dest.(*auditEventModel)
				if tx.Statement.Table == table || (isAudit && audit.EventType == table) {
					_ = tx.AddError(failure)
				}
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := st.ApplyApplicationMigration(ctx, in); !errors.Is(err, failure) {
				t.Fatalf("error=%v", err)
			}
			after, _ := st.GetApplication(ctx, "app")
			if !reflect.DeepEqual(before, after) {
				t.Fatal("definition changed on failed transaction")
			}
			active, err := st.GetActiveConfigurationRelease(ctx, domain.ReleaseTrack{Namespace: in.Namespace, Name: "runtime"})
			if err != nil || active.Release.Version != 1 {
				t.Fatalf("activation changed: %+v %v", active, err)
			}
			var count int64
			st.db.Model(&configurationReleaseModel{}).Count(&count)
			if count != 1 {
				t.Fatalf("release leaked: %d", count)
			}
			p, err := st.GetParameter(ctx, ref("dev", "app", "config"), 0, "")
			if err != nil || p.Version != 1 || p.Value != "old" {
				t.Fatalf("parameter leaked: %+v %v", p, err)
			}
			st.db.Model(&auditEventModel{}).Count(&count)
			if count != 0 {
				t.Fatalf("audit leaked: %d", count)
			}
			gotRevision, _ := st.CurrentRevision(ctx)
			if revision != gotRevision {
				t.Fatal("changelog leaked")
			}
		})
	}
}
func TestApplicationMigrationTransactionConcurrentCAS(t *testing.T) {
	ctx := context.Background()
	st, in := storageMigrationFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		wg.Go(func() { ; <-start; _, err := st.ApplyApplicationMigration(ctx, in); errs <- err })
	}
	close(start)
	wg.Wait()
	close(errs)
	success, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, domain.ErrAborted) {
			conflicts++
		} else {
			t.Fatalf("unexpected concurrency error: %v", err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
	var count int64
	st.db.Model(&configurationReleaseModel{}).Count(&count)
	if count != 2 {
		t.Fatalf("releases=%d", count)
	}
	st.db.Model(&auditEventModel{}).Where("event_type=?", "application.release.migrate").Count(&count)
	if count != 1 {
		t.Fatalf("audits=%d", count)
	}
}

func TestApplicationMigrationNamespaceIncarnationCAS(t *testing.T) {
	ctx := context.Background()
	st, in := storageMigrationFixture(t)
	// Model an external teardown/recreation after review. Normal deletion guards
	// require removing releases/resources first; the recreated path has a new ID.
	for _, table := range []string{"configuration_release_labels", "configuration_release_activations", "configuration_release_entries", "configuration_releases", "parameter_labels", "parameter_versions", "parameters"} {
		if err := st.db.Exec("DELETE FROM " + table).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := st.DeleteNamespace(ctx, in.Namespace); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: in.Namespace}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyApplicationMigration(ctx, in); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("recreated namespace accepted: %v", err)
	}
	var count int64
	st.db.Model(&parameterModel{}).Count(&count)
	if count != 0 {
		t.Fatal("parameter written into replacement namespace")
	}
}

func TestApplicationMigrationPreservedParameterSnapshot(t *testing.T) {
	ctx := context.Background()
	st, in := storageMigrationFixture(t)
	resources := []MigrationResource{{Kind: domain.ReleaseEntryParameter, Key: "config", Version: 1}}
	before, err := st.ApplicationMigrationSnapshot(ctx, domain.ReleaseTrack{Namespace: in.Namespace, Name: in.Release.Name, SchemaVersion: in.SourceSchemaVersion}, in.Release.Track(), resources...)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.PutParameter(ctx, ref("dev", "app", "config"), "unused", "string", "{}", "other"); err != nil {
		t.Fatal(err)
	}
	after, err := st.ApplicationMigrationSnapshot(ctx, domain.ReleaseTrack{Namespace: in.Namespace, Name: in.Release.Name, SchemaVersion: in.SourceSchemaVersion}, in.Release.Track(), resources...)
	if err != nil {
		t.Fatal(err)
	}
	if before.Digest != after.Digest {
		t.Fatal("unused parameter version invalidated preserved pin")
	}
	if err := st.db.Model(&parameterVersionModel{}).Where("version_number = ?", 1).Update("metadata_json", `{"changed":true}`).Error; err != nil {
		t.Fatal(err)
	}
	after, err = st.ApplicationMigrationSnapshot(ctx, domain.ReleaseTrack{Namespace: in.Namespace, Name: in.Release.Name, SchemaVersion: in.SourceSchemaVersion}, in.Release.Track(), resources...)
	if err != nil {
		t.Fatal(err)
	}
	if before.Digest == after.Digest {
		t.Fatal("pinned row mutation did not invalidate snapshot")
	}
	in.Resources, in.Snapshot = resources, before.Digest
	if _, err = st.ApplyApplicationMigration(ctx, in); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("changed pinned row accepted: %v", err)
	}
}

func TestApplicationMigrationPreservesSourceAndApplicationDefault(t *testing.T) {
	ctx := context.Background()
	st, in := storageMigrationFixture(t)
	source := domain.ReleaseTrack{Namespace: in.Namespace, Name: in.Release.Name, SchemaVersion: in.SourceSchemaVersion}
	before, err := st.GetActiveConfigurationRelease(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	appBefore, err := st.GetApplication(ctx, in.Namespace.App)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := st.ApplyApplicationMigration(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Release.Version != 1 || migrated.Release.SchemaVersion != in.Release.SchemaVersion || migrated.PreviousVersion != 0 {
		t.Fatalf("target activation=%+v", migrated)
	}
	after, err := st.GetActiveConfigurationRelease(ctx, source)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("source changed: %+v err=%v", after, err)
	}
	appAfter, err := st.GetApplication(ctx, in.Namespace.App)
	if err != nil || !reflect.DeepEqual(appBefore, appAfter) {
		t.Fatalf("default definition changed: %+v err=%v", appAfter, err)
	}
}

func TestApplicationMigrationRejectsEitherTrackChangingAfterPreview(t *testing.T) {
	for _, which := range []string{"source", "target"} {
		t.Run(which, func(t *testing.T) {
			ctx := context.Background()
			st, in := storageMigrationFixture(t)
			if which == "source" {
				original, err := st.GetConfigurationRelease(ctx, domain.ReleaseTrack{Namespace: in.Namespace, Name: in.Release.Name, SchemaVersion: in.SourceSchemaVersion}, 1)
				if err != nil {
					t.Fatal(err)
				}
				next, err := st.CreateConfigurationRelease(ctx, original)
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := st.ActivateConfigurationRelease(ctx, next.Track(), next.Version, nil); err != nil {
					t.Fatal(err)
				}
			} else {
				target := in.Release
				target.Entries[0].Version = 1
				target.Entries[0].ParameterDigest = fmt.Sprintf("%x", sha256.Sum256([]byte("old")))
				other, err := st.CreateConfigurationRelease(ctx, target)
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := st.ActivateConfigurationRelease(ctx, other.Track(), other.Version, nil); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := st.ApplyApplicationMigration(ctx, in); !errors.Is(err, domain.ErrAborted) {
				t.Fatalf("stale %s accepted: %v", which, err)
			}
		})
	}
}
