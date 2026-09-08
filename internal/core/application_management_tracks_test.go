package core

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

func TestApplicationManagementRequiresExplicitSchemaVersion(t *testing.T) {
	ctx := context.Background()
	svc, st := newConsoleTestService(t)
	pr := adminPrincipal()
	app := seedConsoleApp(t, svc, pr, "dev", "qa")
	ns := domain.NamespaceRef{Env: "dev", App: app.Name}
	first, err := svc.ShipApplicationChange(ctx, pr, domain.ShipInput{Application: app.Name, Environment: ns.Env, SchemaVersion: &app.SchemaVersion})
	if err != nil || first.Status != domain.ShipStatusActivated {
		t.Fatalf("seed old track: %+v %v", first, err)
	}
	newer, err := svc.CreateConfigurationSchema(ctx, pr, app.Name, `{"type":"object","x-kms-contract":[{"alias":"extra","kind":"parameter","content_type":"string"}]}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	latest, err := svc.ShipApplicationChange(ctx, pr, domain.ShipInput{Application: app.Name, Environment: ns.Env, SchemaVersion: &newer.Version, Changes: []domain.ShipChange{{Alias: "extra", Value: new("initial")}}})
	if err != nil || latest.Status != domain.ShipStatusActivated {
		t.Fatalf("seed new track: %+v %v", latest, err)
	}
	beforeRevision, err := st.CurrentRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeNamespaces, _, err := st.ListNamespaces(ctx, storage.ListPage{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	beforeReleases, _, err := st.ListConfigurationReleases(ctx, domain.ReleaseFilter{Namespace: ns}, storage.ListPage{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, dryRun := range []bool{false, true} {
		if _, err := svc.ShipApplicationChange(ctx, pr, domain.ShipInput{Application: app.Name, Environment: ns.Env, DryRun: dryRun, Changes: []domain.ShipChange{{Alias: "extra", Value: new("must not write")}}}); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("ship without selector (dry_run=%v): %v", dryRun, err)
		}
	}
	for _, target := range []string{"prod", "qa"} {
		if _, err := svc.CloneApplicationEnvironment(ctx, pr, domain.CloneEnvironmentInput{Application: app.Name, SourceEnv: ns.Env, TargetEnv: target, CopyValues: true}); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("clone without selector to %s: %v", target, err)
		}
	}
	if after, err := st.CurrentRevision(ctx); err != nil || after != beforeRevision {
		t.Fatalf("rejected calls wrote resource/release/activation revisions: before=%d after=%d err=%v", beforeRevision, after, err)
	}
	if after, _, err := st.ListNamespaces(ctx, storage.ListPage{Limit: 100}); err != nil || !reflect.DeepEqual(beforeNamespaces, after) {
		t.Fatalf("rejected calls changed namespaces: %+v %v", after, err)
	}
	if after, _, err := st.ListConfigurationReleases(ctx, domain.ReleaseFilter{Namespace: ns}, storage.ListPage{Limit: 100}); err != nil || !reflect.DeepEqual(beforeReleases, after) {
		t.Fatalf("rejected calls changed releases: %+v %v", after, err)
	}
	for _, seeded := range []domain.ShipResult{first, latest} {
		track := domain.ReleaseTrack{Namespace: ns, Name: app.ReleaseName, SchemaVersion: seeded.Release.SchemaVersion}
		active, err := st.GetActiveConfigurationRelease(ctx, track)
		if err != nil || active.Release.Version != seeded.Release.Version || active.ActivationRevision != seeded.Activation.ActivationRevision {
			t.Fatalf("rejected calls changed active track %+v: %+v %v", track, active, err)
		}
	}

	// The older contract remains writable after the newest track is active.
	older, err := svc.ShipApplicationChange(ctx, pr, domain.ShipInput{Application: app.Name, Environment: ns.Env, SchemaVersion: &app.SchemaVersion, Changes: []domain.ShipChange{{Alias: "rate_limits", Value: new("7")}}})
	if err != nil || older.Status != domain.ShipStatusActivated || older.Release.SchemaVersion != app.SchemaVersion || older.Release.Version != 2 {
		t.Fatalf("ship older schema: %+v %v", older, err)
	}
	cloned, err := svc.CloneApplicationEnvironment(ctx, pr, domain.CloneEnvironmentInput{Application: app.Name, SourceEnv: ns.Env, TargetEnv: "prod", SchemaVersion: &app.SchemaVersion, CopyValues: true})
	if err != nil || !cloned.NamespaceCreated || len(cloned.Items) != len(app.Contract) {
		t.Fatalf("clone older schema: %+v %v", cloned, err)
	}
	if item := cloneItem(t, cloned, "rate_limits"); item.Action != domain.CloneItemCopied || item.SourceVersion != 2 {
		t.Fatalf("older clone did not resolve its selected source: %+v", item)
	}
	if _, err := st.GetParameter(ctx, domain.Ref{NS: cloned.Namespace.NamespaceRef, Key: "extra"}, 0, domain.LabelCurrent); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("older clone copied the newer contract: %v", err)
	}
	active, err := st.GetActiveConfigurationRelease(ctx, domain.ReleaseTrack{Namespace: ns, Name: app.ReleaseName, SchemaVersion: newer.Version})
	if err != nil || active.ActivationRevision != latest.Activation.ActivationRevision {
		t.Fatalf("older management changed newer activation: %+v %v", active, err)
	}
}

func TestApplicationManagementExplicitSchemaFreeTrack(t *testing.T) {
	ctx := context.Background()
	svc, st := newConsoleTestService(t)
	pr := adminPrincipal()
	app, err := svc.CreateApplication(ctx, pr, domain.Application{Name: "legacy", ReleaseName: "runtime", Contract: []domain.ApplicationContractField{{Alias: "setting", Kind: domain.ReleaseEntryParameter, ContentType: "string"}}})
	if err != nil {
		t.Fatal(err)
	}
	ns := domain.NamespaceRef{Env: "dev", App: app.Name}
	if _, err := svc.CreateNamespace(ctx, pr, ns, "", []domain.AuthMethod{domain.AuthMethodToken}); err != nil {
		t.Fatal(err)
	}
	newer, err := svc.CreateConfigurationSchema(ctx, pr, app.Name, `{"type":"object","x-kms-contract":[{"alias":"extra","kind":"parameter","content_type":"integer"}]}`, "{}")
	if err != nil {
		t.Fatal(err)
	}
	zero := uint64(0)
	shipped, err := svc.ShipApplicationChange(ctx, pr, domain.ShipInput{Application: app.Name, Environment: ns.Env, SchemaVersion: &zero, Changes: []domain.ShipChange{{Alias: "setting", Value: new("schema-free")}}})
	if err != nil || shipped.Status != domain.ShipStatusActivated || shipped.Release.SchemaVersion != 0 {
		t.Fatalf("schema-free ship: %+v %v", shipped, err)
	}
	cloned, err := svc.CloneApplicationEnvironment(ctx, pr, domain.CloneEnvironmentInput{Application: app.Name, SourceEnv: ns.Env, TargetEnv: "prod", SchemaVersion: &zero, CopyValues: true})
	if err != nil || len(cloned.Items) != 1 || cloneItem(t, cloned, "setting").Action != domain.CloneItemCopied {
		t.Fatalf("schema-free clone: %+v %v", cloned, err)
	}
	if _, err := st.GetActiveConfigurationRelease(ctx, domain.ReleaseTrack{Namespace: ns, Name: app.ReleaseName, SchemaVersion: newer.Version}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("schema-free management activated newer schema: %v", err)
	}
}
