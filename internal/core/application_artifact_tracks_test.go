package core

import (
	"context"
	"errors"
	"fmt"
	"github.com/Suhaibinator/kms/internal/storage"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
)

type artifactTrackResult struct {
	digest   string
	schema   uint64
	executed bool
}

func runArtifactTrackOperation(ctx context.Context, svc *Service, ns domain.NamespaceRef, operation string, artifact []byte, selected *uint64, digest string) (artifactTrackResult, error) {
	if operation == "defaults" {
		out, err := svc.ApplyApplicationDefaults(ctx, adminPrincipal(), domain.DefaultsApplyInput{Namespace: ns, Artifact: artifact, SchemaVersion: selected, Execute: digest != "", PlanDigest: digest})
		return artifactTrackResult{out.PlanDigest, 0, out.Executed}, err
	}
	out, err := svc.CreateApplicationRelease(ctx, adminPrincipal(), domain.ApplicationReleaseCreateInput{Namespace: ns, Artifact: artifact, SchemaVersion: selected, Execute: digest != "", PlanDigest: digest})
	return artifactTrackResult{out.PlanDigest, out.SchemaVersion, out.Executed}, err
}

func TestArtifactOperationsResolveEmbeddedTrackAcrossRegistrations(t *testing.T) {
	for _, operation := range []string{"defaults", "release"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			svc, st := newConsoleTestService(t)
			app := seedConsoleApp(t, svc, adminPrincipal(), "dev")
			ns := domain.NamespaceRef{Env: "dev", App: app.Name}
			raw := consoleDefaultsArtifact(t, `{"host":"db.internal"}`, "5")
			preview, err := runArtifactTrackOperation(ctx, svc, ns, operation, raw, nil, "")
			if err != nil || (operation == "release" && preview.schema != app.SchemaVersion) {
				t.Fatalf("preview: %+v %v", preview, err)
			}
			newer, err := svc.CreateConfigurationSchema(ctx, adminPrincipal(), app.Name, `{"type":"object","x-kms-contract":[{"alias":"other","kind":"parameter","content_type":"string"}]}`, "{}")
			if err != nil {
				t.Fatal(err)
			}
			repeated, err := runArtifactTrackOperation(ctx, svc, ns, operation, raw, nil, "")
			if err != nil || (operation == "release" && repeated.schema != app.SchemaVersion) || repeated.digest != preview.digest {
				t.Fatalf("registration changed artifact preview: %+v %v", repeated, err)
			}
			executed, err := runArtifactTrackOperation(ctx, svc, ns, operation, raw, nil, preview.digest)
			if err != nil || !executed.executed || (operation == "release" && executed.schema != app.SchemaVersion) {
				t.Fatalf("execution changed track: %+v %v", executed, err)
			}
			eventType := "application.release.create"
			if operation == "defaults" {
				eventType = "application.defaults.apply"
			}
			audits, _, auditErr := st.ListAudit(ctx, domain.AuditFilter{EventType: eventType}, storage.ListPage{})
			if auditErr != nil || len(audits) != 1 {
				t.Fatalf("artifact audits: %+v %v", audits, auditErr)
			}
			metadata := auditMetadataForTest(t, audits[0])
			if metadata["schema_version"] != fmt.Sprint(app.SchemaVersion) {
				t.Fatalf("artifact audit selected newest track: %+v", audits[0])
			}
			if operation == "release" && metadata["release_version"] != "1" {
				t.Fatalf("generated release identity: %+v", audits[0])
			}
			if _, err := runArtifactTrackOperation(ctx, svc, ns, operation, raw, &app.SchemaVersion, ""); err != nil {
				t.Fatalf("matching numeric selection: %v", err)
			}
			if operation == "defaults" {
				missing := ns
				missing.Env = "missing"
				if _, err := runArtifactTrackOperation(ctx, svc, missing, operation, raw, nil, ""); !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("expected defaults preflight failure: %v", err)
				}
				failures, _, err := st.ListAudit(ctx, domain.AuditFilter{EventType: "application.defaults.preview", Decision: "error"}, storage.ListPage{})
				if err != nil || len(failures) != 1 || auditMetadataForTest(t, failures[0])["schema_version"] != fmt.Sprint(app.SchemaVersion) {
					t.Fatalf("failed digest-selected preflight omitted track: %+v %v", failures, err)
				}
			}
			for _, version := range []uint64{0, newer.Version} {
				if _, err := runArtifactTrackOperation(ctx, svc, ns, operation, raw, &version, ""); !errors.Is(err, domain.ErrFailedPrecondition) {
					t.Fatalf("mismatch schema%d: %v", version, err)
				}
			}
			saved, err := st.GetApplication(ctx, app.Name)
			if err != nil || saved.SchemaVersion != app.SchemaVersion {
				t.Fatalf("artifact operation repinned default: %+v %v", saved, err)
			}
			overview, err := svc.GetApplicationOverview(ctx, adminPrincipal(), app.Name, OverviewOptions{})
			if err != nil || overview.Application.SchemaVersion != newer.Version {
				t.Fatalf("overview lost newest selection: %+v %v", overview.Application, err)
			}
		})
	}
}

func TestArtifactOperationsRequireExplicitSchemaFreeSelection(t *testing.T) {
	for _, operation := range []string{"defaults", "release"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			svc, st := newConsoleTestService(t)
			app := seedConsoleApp(t, svc, adminPrincipal(), "dev")
			ns := domain.NamespaceRef{Env: "dev", App: app.Name}
			artifact, err := configstore.ParseDefaultsArtifact(consoleDefaultsArtifact(t, `{"host":"db.internal"}`, "5"))
			if err != nil {
				t.Fatal(err)
			}
			artifact.SchemaSHA256 = ""
			raw, err := configstore.EncodeDefaultsArtifact(artifact)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runArtifactTrackOperation(ctx, svc, ns, operation, raw, nil, ""); !errors.Is(err, domain.ErrInvalidArgument) {
				t.Fatalf("missing schema0 selector: %v", err)
			}
			if _, err := runArtifactTrackOperation(ctx, svc, ns, operation, raw, &app.SchemaVersion, ""); !errors.Is(err, domain.ErrFailedPrecondition) {
				t.Fatalf("empty digest selected registered schema: %v", err)
			}
			zero := uint64(0)
			preview, err := runArtifactTrackOperation(ctx, svc, ns, operation, raw, &zero, "")
			if err != nil || preview.schema != 0 {
				t.Fatalf("schema0 preview: %+v %v", preview, err)
			}
			applied, err := runArtifactTrackOperation(ctx, svc, ns, operation, raw, &zero, preview.digest)
			if err != nil || !applied.executed || applied.schema != 0 {
				t.Fatalf("schema0 execution: %+v %v", applied, err)
			}
			contract, err := st.GetConfigurationSchemaContract(ctx, app.Name, app.ReleaseName, 0)
			if err != nil || len(contract) != len(artifact.Contract) {
				t.Fatalf("schema0 contract adoption: %+v %v", contract, err)
			}
			saved, err := st.GetApplication(ctx, app.Name)
			if err != nil || saved.SchemaVersion != app.SchemaVersion {
				t.Fatalf("schema0 repinned default: %+v %v", saved, err)
			}
			artifact.SchemaSHA256 = strings.Repeat("a", 64)
			unknown, err := configstore.EncodeDefaultsArtifact(artifact)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runArtifactTrackOperation(ctx, svc, ns, operation, unknown, nil, ""); !errors.Is(err, domain.ErrFailedPrecondition) {
				t.Fatalf("unknown digest: %v", err)
			}
		})
	}
}

func TestArtifactDefinitionUpdateUsesDigestInsteadOfNewest(t *testing.T) {
	ctx := context.Background()
	svc, st := newConsoleTestService(t)
	app := seedConsoleApp(t, svc, adminPrincipal(), "dev")
	schema, err := svc.CreateConfigurationSchema(ctx, adminPrincipal(), app.Name, strings.Replace(consoleSchema, `{"type":`, `{"description":"next","type":`, 1), "{}")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := configstore.ParseDefaultsArtifact(consoleDefaultsArtifact(t, `{"host":"db.internal"}`, "5"))
	if err != nil {
		t.Fatal(err)
	}
	artifact.SchemaSHA256 = schema.Digest
	raw, err := configstore.EncodeDefaultsArtifact(artifact)
	if err != nil {
		t.Fatal(err)
	}
	in := domain.DefaultsApplyInput{Namespace: domain.NamespaceRef{Env: "dev", App: app.Name}, Artifact: raw, UpdateDefinition: true}
	preview, err := svc.ApplyApplicationDefaults(ctx, adminPrincipal(), in)
	if err != nil || !preview.DefinitionChanged {
		t.Fatalf("definition preview: %+v %v", preview, err)
	}
	if _, err := svc.CreateConfigurationSchema(ctx, adminPrincipal(), app.Name, `{"type":"object","description":"unrelated newest"}`, "{}"); err != nil {
		t.Fatal(err)
	}
	in.Execute = true
	in.PlanDigest = preview.PlanDigest
	applied, err := svc.ApplyApplicationDefaults(ctx, adminPrincipal(), in)
	if err != nil || !applied.DefinitionUpdated {
		t.Fatalf("definition execution: %+v %v", applied, err)
	}
	saved, err := st.GetApplication(ctx, app.Name)
	if err != nil || saved.SchemaVersion != schema.Version {
		t.Fatalf("definition target: %+v %v", saved, err)
	}
}
