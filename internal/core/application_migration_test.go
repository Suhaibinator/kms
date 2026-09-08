package core

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

func migrationFixture(t *testing.T) (*Service, *storage.SQLStore, domain.ApplicationReleaseMigrationInput) {
	t.Helper()
	ctx := context.Background()
	svc, st := newConsoleTestService(t)
	pr := adminPrincipal()
	app := seedConsoleApp(t, svc, pr, "dev", "prod")
	for _, env := range []string{"dev", "prod"} {
		r, err := svc.ShipApplicationChange(ctx, pr, domain.ShipInput{SchemaVersion: &app.SchemaVersion, Application: app.Name, Environment: env})
		if err != nil || r.Status != domain.ShipStatusActivated {
			t.Fatalf("seed ship: %+v %v", r, err)
		}
	}
	schema, err := svc.CreateConfigurationSchema(ctx, pr, app.Name, `{"type":"object","properties":{"db":{"type":"object"},"rate_limits":{"type":"integer","minimum":10}},"required":["db","rate_limits"],"additionalProperties":false}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	return svc, st, domain.ApplicationReleaseMigrationInput{SourceSchemaVersion: app.SchemaVersion, Namespace: domain.NamespaceRef{Env: "dev", App: app.Name}, SchemaVersion: schema.Version, Contract: []domain.ApplicationContractField{{Alias: "db", Kind: domain.ReleaseEntryParameter, ContentType: "json"}, {Alias: "db_password", Kind: domain.ReleaseEntrySecret}, {Alias: "rate_limits", Kind: domain.ReleaseEntryParameter, ContentType: "integer"}}, Changes: []domain.ApplicationMigrationChange{{Alias: "db", FromAlias: "database"}, {Alias: "rate_limits", Value: new("12")}}}
}
func TestApplicationMigrationAtomicAndExactPins(t *testing.T) {
	ctx := context.Background()
	svc, st, in := migrationFixture(t)
	pr := adminPrincipal()
	// Current values have drifted since activation. Unedited aliases must carry
	// active pins, while the edited key allocates after all existing versions.
	if _, _, err := svc.PutParameter(ctx, pr, domain.Ref{NS: in.Namespace, Key: "database"}, `{"host":"wrong"}`, "json", "{}"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PutSecret(ctx, pr, PutSecretInput{Ref: domain.Ref{NS: in.Namespace, Key: "db_password"}, Value: []byte("rotated secret"), ContentType: "text/plain"}); err != nil {
		t.Fatal(err)
	}
	before, err := st.ApplicationMigrationSnapshot(ctx, domain.ReleaseTrack{Namespace: in.Namespace, Name: "runtime", SchemaVersion: in.SourceSchemaVersion}, domain.ReleaseTrack{Namespace: in.Namespace, Name: "runtime", SchemaVersion: in.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := svc.MigrateApplicationRelease(ctx, pr, in)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Valid || preview.Executed || preview.DefinitionChanged || preview.PlanDigest == "" || len(preview.AffectedEnvironments) != 1 {
		t.Fatalf("preview: %+v", preview)
	}
	after, _ := st.ApplicationMigrationSnapshot(ctx, domain.ReleaseTrack{Namespace: in.Namespace, Name: "runtime", SchemaVersion: in.SourceSchemaVersion}, domain.ReleaseTrack{Namespace: in.Namespace, Name: "runtime", SchemaVersion: in.SchemaVersion})
	if before.Digest != after.Digest {
		t.Fatal("preview mutated state")
	}
	for _, entry := range preview.Entries {
		if entry.Alias == "db" || entry.Alias == "db_password" {
			if entry.ToVersion != 1 {
				t.Fatalf("did not preserve exact pin: %+v", entry)
			}
		}
	}
	in.Execute = true
	in.PlanDigest = preview.PlanDigest
	got, err := svc.MigrateApplicationRelease(ctx, pr, in)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Executed || got.Release == nil || got.Release.Version != 1 || got.Activation == nil || got.Activation.PreviousVersion != 0 {
		t.Fatalf("apply: %+v", got)
	}
	app, _ := st.GetApplication(ctx, in.Namespace.App)
	if app.SchemaVersion != in.SourceSchemaVersion || app.Contract[0].Alias != "database" {
		t.Fatalf("definition: %+v", app)
	}
	prod, err := st.GetActiveConfigurationRelease(ctx, domain.ReleaseTrack{Namespace: domain.NamespaceRef{Env: "prod", App: in.Namespace.App}, Name: "runtime", SchemaVersion: in.SourceSchemaVersion})
	if err != nil || prod.Release.Version != 1 || prod.Release.SchemaVersion == in.SchemaVersion {
		t.Fatalf("other environment changed: %+v %v", prod, err)
	}
	events, _, err := st.ListAudit(ctx, domain.AuditFilter{EventType: "application.release.migrate"}, storage.ListPage{})
	if err != nil || len(events) != 1 {
		t.Fatalf("audit: %+v %v", events, err)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "hunter2") || strings.Contains(string(b), "rotated secret") {
		t.Fatal("secret in response")
	}
	// Reusing an executed preview is stale, even if definition is already target.
	if _, err := svc.MigrateApplicationRelease(ctx, pr, in); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("replayed plan: %v", err)
	}
	// A second environment can still migrate its old active release after the
	// shared application definition has already moved to this schema.
	in.Namespace.Env = "prod"
	in.Execute = false
	in.PlanDigest = ""
	p, err := svc.MigrateApplicationRelease(ctx, pr, in)
	if err != nil || !p.Valid {
		t.Fatalf("followup: %+v %v", p, err)
	}
	in.Execute = true
	in.PlanDigest = p.PlanDigest
	if _, err = svc.MigrateApplicationRelease(ctx, pr, in); err != nil {
		t.Fatal(err)
	}
}
func TestApplicationMigrationInvalidAndStaleNeverMutate(t *testing.T) {
	for _, scenario := range []string{"invalid", "changed_request", "resource_drift", "activation_aba", "definition_drift"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			svc, st, in := migrationFixture(t)
			pr := adminPrincipal()
			preview, err := svc.MigrateApplicationRelease(ctx, pr, in)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "invalid":
				in.Changes[1].Value = new("-10")
				preview, err = svc.MigrateApplicationRelease(ctx, pr, in)
				if err != nil || preview.Valid {
					t.Fatalf("invalid: %+v %v", preview, err)
				}
			case "changed_request":
				in.Changes[1].Value = new("13")
			case "resource_drift":
				_, _, err = svc.PutParameter(ctx, pr, domain.Ref{NS: in.Namespace, Key: "rate_limits"}, "15", "integer", "{}")
			case "activation_aba":
				other, e := svc.ShipApplicationChange(ctx, pr, domain.ShipInput{SchemaVersion: &in.SourceSchemaVersion, Application: in.Namespace.App, Environment: "dev", Changes: []domain.ShipChange{{Alias: "rate_limits", Value: new("7")}}})
				if e != nil {
					t.Fatal(e)
				}
				_, _, err = svc.ActivateConfigurationRelease(ctx, pr, domain.ReleaseTrack{Namespace: in.Namespace, Name: "runtime", SchemaVersion: in.SourceSchemaVersion}, 1, &other.Release.Version)
			case "definition_drift":
				app, _ := st.GetApplication(ctx, in.Namespace.App)
				app.Description = "changed"
				_, err = st.UpdateApplication(ctx, app)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, _ := st.ApplicationMigrationSnapshot(ctx, domain.ReleaseTrack{Namespace: in.Namespace, Name: "runtime", SchemaVersion: in.SourceSchemaVersion}, domain.ReleaseTrack{Namespace: in.Namespace, Name: "runtime", SchemaVersion: in.SchemaVersion})
			in.Execute = true
			in.PlanDigest = preview.PlanDigest
			result, err := svc.MigrateApplicationRelease(ctx, pr, in)
			if scenario == "invalid" {
				if err != nil || result.Executed || result.Valid {
					t.Fatalf("invalid apply: %+v %v", result, err)
				}
			} else if !errors.Is(err, domain.ErrAborted) {
				t.Fatalf("stale apply: %+v %v", result, err)
			}
			after, _ := st.ApplicationMigrationSnapshot(ctx, domain.ReleaseTrack{Namespace: in.Namespace, Name: "runtime", SchemaVersion: in.SourceSchemaVersion}, domain.ReleaseTrack{Namespace: in.Namespace, Name: "runtime", SchemaVersion: in.SchemaVersion})
			if before.Digest != after.Digest {
				t.Fatal("failed migration changed state")
			}
		})
	}
}
func TestApplicationMigrationRejectsSecretPlaintext(t *testing.T) {
	svc, _, in := migrationFixture(t)
	in.Changes = append(in.Changes, domain.ApplicationMigrationChange{Alias: "db_password", Value: new("plaintext")})
	if _, err := svc.MigrateApplicationRelease(context.Background(), adminPrincipal(), in); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("secret edit: %v", err)
	}
}

func TestApplicationMigrationParameterSizeLimit(t *testing.T) {
	svc, _, in := migrationFixture(t)
	in.Changes[1].Value = new(strings.Repeat("1", maxValueBytes+1))
	if _, err := svc.MigrateApplicationRelease(context.Background(), adminPrincipal(), in); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("oversized edit: %v", err)
	}
}

type migrationInterleavingStore struct {
	*storage.SQLStore
	snapshotCalls  int
	beforeSnapshot func(int)
	beforeApply    func()
	failApply      bool
}

func (st *migrationInterleavingStore) ApplicationMigrationSnapshot(ctx context.Context, source, target domain.ReleaseTrack, resources ...storage.MigrationResource) (storage.MigrationSnapshot, error) {
	st.snapshotCalls++
	if st.beforeSnapshot != nil {
		st.beforeSnapshot(st.snapshotCalls)
	}
	return st.SQLStore.ApplicationMigrationSnapshot(ctx, source, target, resources...)
}
func (st *migrationInterleavingStore) ApplyApplicationMigration(ctx context.Context, in storage.ApplicationMigrationTransaction) (domain.ActiveConfigurationRelease, error) {
	if st.beforeApply != nil {
		st.beforeApply()
	}
	if st.failApply {
		return domain.ActiveConfigurationRelease{}, errors.New("injected apply failure")
	}
	return st.SQLStore.ApplyApplicationMigration(ctx, in)
}
func TestApplicationMigrationDefinitionSnapshotRace(t *testing.T) {
	ctx := context.Background()
	svc, st, in := migrationFixture(t)
	svc.store = &migrationInterleavingStore{SQLStore: st, beforeSnapshot: func(call int) {
		if call == 2 {
			app, err := st.GetApplication(ctx, in.Namespace.App)
			if err != nil {
				t.Fatal(err)
			}
			app.Description = "concurrent definition change"
			if _, err = st.UpdateApplication(ctx, app); err != nil {
				t.Fatal(err)
			}
		}
	}}
	if _, err := svc.MigrateApplicationRelease(ctx, adminPrincipal(), in); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("racing definition was accepted: %v", err)
	}
}
func TestApplicationMigrationFinalTransactionCASAndNotification(t *testing.T) {
	for _, scenario := range []string{"resource", "apply_failure", "success"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			svc, st, in := migrationFixture(t)
			pr := adminPrincipal()
			preview, err := svc.MigrateApplicationRelease(ctx, pr, in)
			if err != nil {
				t.Fatal(err)
			}
			notifications, cancel := svc.SubscribeReleaseSubscribers(domain.ReleaseTrack{Namespace: in.Namespace, Name: "runtime", SchemaVersion: in.SchemaVersion})
			defer cancel()
			wrapped := &migrationInterleavingStore{SQLStore: st}
			if scenario == "resource" {
				wrapped.beforeApply = func() {
					if _, _, err := st.PutParameter(ctx, domain.Ref{NS: in.Namespace, Key: "rate_limits"}, "40", "integer", "{}", "other"); err != nil {
						t.Fatal(err)
					}
				}
			}
			if scenario == "apply_failure" {
				wrapped.failApply = true
			}
			svc.store = wrapped
			in.Execute = true
			in.PlanDigest = preview.PlanDigest
			got, err := svc.MigrateApplicationRelease(ctx, pr, in)
			if scenario == "success" {
				if err != nil || !got.Executed {
					t.Fatalf("success: %+v %v", got, err)
				}
				select {
				case <-notifications:
				default:
					t.Fatal("committed migration did not notify")
				}
			} else {
				if err == nil {
					t.Fatal("failed apply reported success")
				}
				select {
				case <-notifications:
					t.Fatal("failed migration notified subscribers")
				default:
				}
				a, _ := st.GetActiveConfigurationRelease(ctx, domain.ReleaseTrack{Namespace: in.Namespace, Name: "runtime", SchemaVersion: in.SourceSchemaVersion})
				if a.Release.Version != 1 {
					t.Fatal("failed migration activated")
				}
			}
		})
	}
}

func TestApplicationMigrationMissingAndDestroyedSecretReferences(t *testing.T) {
	for _, scenario := range []string{"missing", "destroyed"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			svc, st, in := migrationFixture(t)
			version := uint64(999)
			if scenario == "destroyed" {
				ref := domain.Ref{NS: in.Namespace, Key: "db_password"}
				if _, err := svc.PutSecret(ctx, adminPrincipal(), PutSecretInput{Ref: ref, Value: []byte("unreferenced rotation"), ContentType: "text/plain"}); err != nil {
					t.Fatal(err)
				}
				version = 2
				if _, err := st.DestroySecretVersion(ctx, ref, version); err != nil {
					t.Fatal(err)
				}
			}
			in.Changes = append(in.Changes, domain.ApplicationMigrationChange{Alias: "db_password", Version: version})
			result, err := svc.MigrateApplicationRelease(ctx, adminPrincipal(), in)
			if err != nil || result.Valid || len(result.Validation) == 0 {
				t.Fatalf("invalid secret reference: %+v %v", result, err)
			}
			in.Execute = true
			in.PlanDigest = result.PlanDigest
			if got, err := svc.MigrateApplicationRelease(ctx, adminPrincipal(), in); err != nil || got.Executed {
				t.Fatalf("invalid secret applied: %+v %v", got, err)
			}
		})
	}
}

func TestApplicationMigrationExpectedSource(t *testing.T) {
	ctx := context.Background()
	svc, st, in := migrationFixture(t)
	pr := adminPrincipal()
	source, err := st.GetActiveConfigurationRelease(ctx, domain.ReleaseTrack{Namespace: in.Namespace, Name: "runtime", SchemaVersion: in.SourceSchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	in.ExpectedSourceVersion = &source.Release.Version
	if _, err = svc.MigrateApplicationRelease(ctx, pr, in); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("partial expected source: %v", err)
	}
	in.ExpectedSourceActivationRevision = &source.ActivationRevision
	if _, err = svc.MigrateApplicationRelease(ctx, pr, in); err != nil {
		t.Fatal(err)
	}
	other, err := svc.ShipApplicationChange(ctx, pr, domain.ShipInput{SchemaVersion: &in.SourceSchemaVersion, Application: in.Namespace.App, Environment: "dev", Changes: []domain.ShipChange{{Alias: "rate_limits", Value: new("7")}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.MigrateApplicationRelease(ctx, pr, in); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("changed source: %v", err)
	}
	if _, _, err = svc.ActivateConfigurationRelease(ctx, pr, domain.ReleaseTrack{Namespace: in.Namespace, Name: "runtime", SchemaVersion: in.SourceSchemaVersion}, source.Release.Version, &other.Release.Version); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.MigrateApplicationRelease(ctx, pr, in); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("source ABA: %v", err)
	}
}

func TestApplicationMigrationUnusedParameterVersionAfterPreview(t *testing.T) {
	ctx := context.Background()
	svc, _, in := migrationFixture(t)
	pr := adminPrincipal()
	preview, err := svc.MigrateApplicationRelease(ctx, pr, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = svc.PutParameter(ctx, pr, domain.Ref{NS: in.Namespace, Key: "database"}, `{"host":"unused-after-review"}`, "json", "{}"); err != nil {
		t.Fatal(err)
	}
	in.Execute, in.PlanDigest = true, preview.PlanDigest
	result, err := svc.MigrateApplicationRelease(ctx, pr, in)
	if err != nil || !result.Executed {
		t.Fatalf("unused version invalidated exact pins: %+v %v", result, err)
	}
	for _, entry := range result.Release.Entries {
		if entry.Alias == "db" && entry.Version != 1 {
			t.Fatalf("preserved pin changed: %+v", entry)
		}
	}
}

func TestApplicationMigrationExecuteRequiresDigestBeforeSnapshot(t *testing.T) {
	svc, st, in := migrationFixture(t)
	wrapped := &migrationInterleavingStore{SQLStore: st}
	svc.store = wrapped
	in.Execute = true
	if _, err := svc.MigrateApplicationRelease(context.Background(), adminPrincipal(), in); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("missing digest: %v", err)
	}
	if wrapped.snapshotCalls != 0 {
		t.Fatal("missing digest read migration state")
	}
}

func TestApplicationMigrationCorrelatedResourceAudits(t *testing.T) {
	ctx := context.Background()
	svc, st, in := migrationFixture(t)
	pr := adminPrincipal()
	pr.RequestID, pr.RemoteAddr, pr.UserAgent = "migration-request", "192.0.2.42", "migration-client"
	preview, err := svc.MigrateApplicationRelease(ctx, pr, in)
	if err != nil {
		t.Fatal(err)
	}
	readEvents := func() ([]domain.AuditEvent, error) {
		all, _, err := st.ListAudit(ctx, domain.AuditFilter{}, storage.ListPage{Limit: 1000})
		var selected []domain.AuditEvent
		for _, event := range all {
			if event.RequestID == pr.RequestID {
				selected = append(selected, event)
			}
		}
		return selected, err
	}
	events, err := readEvents()
	if err != nil || len(events) != 0 {
		t.Fatalf("preview emitted mutation audits: %+v %v", events, err)
	}
	in.Execute, in.PlanDigest = true, preview.PlanDigest
	result, err := svc.MigrateApplicationRelease(ctx, pr, in)
	if err != nil {
		t.Fatal(err)
	}
	events, err = readEvents()
	if err != nil || len(events) != 4 {
		t.Fatalf("migration audits: %+v %v", events, err)
	}
	seen := map[string]bool{}
	for _, event := range events {
		if seen[event.EventType] {
			t.Fatalf("duplicate audit: %+v", event)
		}
		seen[event.EventType] = true
		if event.ActorIdentity != pr.Identity.Name || event.ActorType != pr.Identity.Kind || event.SourceIP != pr.RemoteAddr || event.UserAgent != pr.UserAgent || event.ResourceNamespaceID == 0 || event.ResourceEnv != in.Namespace.Env || event.ResourceApp != in.Namespace.App || event.Decision != "allow" || event.CreatedAt.IsZero() {
			t.Fatalf("missing audit context: %+v", event)
		}
		expectedKey, expectedVersion := "runtime", result.Release.Version
		if event.EventType == "parameter.write" {
			expectedKey, expectedVersion = "rate_limits", uint64(2)
		}
		if event.ResourceKey != expectedKey || event.ResourceVersion != expectedVersion {
			t.Fatalf("wrong resource: %+v", event)
		}
		var meta map[string]string
		if err := json.Unmarshal([]byte(event.Metadata), &meta); err != nil {
			t.Fatal(err)
		}
		if event.EventType == "application.release.migrate" && (meta["schema_version"] != "2" || meta["source_version"] != "1" || meta["source_activation_revision"] == "" || meta["previous_version"] != "0" || meta["parameter_write_count"] != "1") {
			t.Fatalf("missing migration metadata: %+v", meta)
		}
		if event.EventType == "configuration_release.activate" && meta["previous_version"] != "0" {
			t.Fatalf("missing activation metadata: %+v", meta)
		}
		if event.ResourceType == domain.ResourceConfigurationRelease && meta["schema_version"] != "2" {
			t.Fatalf("missing destination schema: %+v", event)
		}
		if event.EventType == "configuration_release.activate" || event.EventType == "application.release.migrate" {
			if meta["activation_revision"] != fmt.Sprint(result.Activation.ActivationRevision) || meta["source_schema_version"] != "1" {
				t.Fatalf("missing activation identity: %+v", event)
			}
		}
		for key := range meta {
			if key != "activation_revision" && key != "source_schema_version" && key != "operation" && key != "schema_version" && key != "source_version" && key != "source_activation_revision" && key != "previous_version" && key != "parameter_write_count" {
				t.Fatalf("unexpected potentially sensitive metadata: %s", key)
			}
		}
	}
	for _, name := range []string{"parameter.write", "configuration_release.create", "configuration_release.activate", "application.release.migrate"} {
		if !seen[name] {
			t.Fatalf("missing %s", name)
		}
	}
}
