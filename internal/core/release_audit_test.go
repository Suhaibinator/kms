package core

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

func auditMetadataForTest(t *testing.T, event domain.AuditEvent) map[string]string {
	t.Helper()
	var metadata map[string]string
	// A string map deliberately rejects accidental numeric JSON metadata.
	if err := json.Unmarshal([]byte(event.Metadata), &metadata); err != nil {
		t.Fatal(err)
	}
	return metadata
}

func TestReleaseLifecycleAuditIdentifiesDuplicateVersionsAcrossTracks(t *testing.T) {
	ctx := context.Background()
	svc, st := newConsoleTestService(t)
	pr := adminPrincipal()
	app, err := svc.CreateApplication(ctx, pr, domain.Application{Name: "audittracks", ReleaseName: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	ns := domain.NamespaceRef{Env: "dev", App: app.Name}
	namespace, err := svc.CreateNamespace(ctx, pr, ns, "", []domain.AuthMethod{domain.AuthMethodToken})
	if err != nil {
		t.Fatal(err)
	}
	ref := domain.Ref{NS: ns, Key: "setting"}
	if _, _, err = svc.PutParameter(ctx, pr, ref, "1", "integer", "{}"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"type":"object","title":"one"}`, `{"type":"object","title":"two"}`} {
		if _, err = svc.CreateConfigurationSchema(ctx, pr, app.Name, raw, "{}"); err != nil {
			t.Fatal(err)
		}
	}
	// Newest first, then older and schema-free tracks: no newest/default fallback.
	for _, schema := range []uint64{2, 1, 0} {
		track := domain.ReleaseTrack{Namespace: ns, Name: app.ReleaseName, SchemaVersion: schema}
		requestID := fmt.Sprintf("track-%d", schema)
		actor := pr
		actor.RequestID = requestID
		create := func() domain.ConfigurationRelease {
			r, e := svc.CreateConfigurationRelease(ctx, actor, domain.CreateConfigurationReleaseInput{Namespace: ns, Name: app.ReleaseName, SchemaVersion: schema, Entries: []domain.ReleaseEntrySelector{{Alias: "setting", Kind: domain.ReleaseEntryParameter, Ref: ref}}})
			if e != nil {
				t.Fatal(e)
			}
			return r
		}
		first, second := create(), create()
		if first.Version != 1 || second.Version != 2 {
			t.Fatalf("unexpected versions: %d %d", first.Version, second.Version)
		}
		if _, _, err = svc.ActivateConfigurationRelease(ctx, actor, track, first.Version, nil); err != nil {
			t.Fatal(err)
		}
		if _, _, err = svc.ActivateConfigurationRelease(ctx, actor, track, second.Version, nil); err != nil {
			t.Fatal(err)
		}
		_, err = svc.RollbackConfigurationRelease(ctx, actor, track, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = svc.ValidateConfigurationRelease(ctx, actor, track, first.Version); err != nil {
			t.Fatal(err)
		}
		wrong := uint64(99)
		if _, _, err = svc.ActivateConfigurationRelease(ctx, actor, track, second.Version, &wrong); !errors.Is(err, domain.ErrAborted) {
			t.Fatalf("CAS: %v", err)
		}
		if _, err = svc.RollbackConfigurationRelease(ctx, actor, track, &wrong); !errors.Is(err, domain.ErrAborted) {
			t.Fatalf("rollback CAS: %v", err)
		}
		if _, _, err = svc.ActivateConfigurationRelease(ctx, actor, track, 99, nil); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("missing: %v", err)
		}
		if _, _, err = svc.ActivateConfigurationRelease(ctx, clientPrincipal("denied"), track, first.Version, nil); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("authorization: %v", err)
		}
		// Persist an inactive candidate whose resource is then deleted, yielding a
		// validation denial without modifying the active/previous protected pins.
		temporary := domain.Ref{NS: ns, Key: fmt.Sprintf("temporary-%d", schema)}
		if _, _, err = svc.PutParameter(ctx, actor, temporary, "1", "integer", "{}"); err != nil {
			t.Fatal(err)
		}
		invalid, err := svc.CreateConfigurationRelease(ctx, actor, domain.CreateConfigurationReleaseInput{Namespace: ns, Name: app.ReleaseName, SchemaVersion: schema, Entries: []domain.ReleaseEntrySelector{{Alias: "setting", Kind: domain.ReleaseEntryParameter, Ref: temporary}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = svc.DeleteParameter(ctx, actor, temporary); err != nil {
			t.Fatal(err)
		}
		if _, _, err = svc.ActivateConfigurationRelease(ctx, actor, track, invalid.Version, nil); !errors.Is(err, domain.ErrFailedPrecondition) {
			t.Fatalf("invalid activation: %v", err)
		}
		if _, err = svc.ValidateConfigurationRelease(ctx, actor, track, invalid.Version); err != nil {
			t.Fatal(err)
		}
		active, err := svc.GetActiveConfigurationRelease(ctx, actor, track)
		if err != nil {
			t.Fatal(err)
		}
		if err = svc.SetReleaseSubscriberConnected(ctx, track, "api", "replica", actor.Identity.Name, "connection", true); err != nil {
			t.Fatal(err)
		}
		ack := domain.ReleaseAcknowledgement{Namespace: ns, ReleaseName: track.Name, SchemaVersion: schema, ReleaseVersion: 1, ActivationRevision: active.ActivationRevision, ClientName: "api", InstanceID: "replica", ConnectionID: "connection", State: domain.ReleaseStateApplied}
		if err = svc.AcknowledgeConfigurationRelease(ctx, actor, ack); err != nil {
			t.Fatal(err)
		}
		ack.ActivationRevision = 999999
		var unavailable *domain.ReleaseAcknowledgementUnavailableError
		if err = svc.AcknowledgeConfigurationRelease(ctx, actor, ack); !errors.As(err, &unavailable) {
			t.Fatalf("unavailable ACK: %v", err)
		}
		events, _, err := st.ListAudit(ctx, domain.AuditFilter{App: app.Name}, storage.ListPage{Limit: 1000})
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]int{}
		for _, event := range events {
			if event.RequestID != requestID || event.ResourceType != domain.ResourceConfigurationRelease {
				continue
			}
			metadata := auditMetadataForTest(t, event)
			if metadata["schema_version"] != strconv.FormatUint(schema, 10) || event.ResourceNamespaceID != namespace.ID {
				t.Fatalf("incorrect identity: %+v", event)
			}
			seen[event.EventType+"/"+event.Decision]++
			if event.EventType == "configuration_release.acknowledge" && metadata["activation_revision"] != strconv.FormatUint(active.ActivationRevision, 10) {
				t.Fatalf("ACK identity: %+v", event)
			}
			if (event.EventType == "configuration_release.activate" || event.EventType == "configuration_release.rollback") && event.Decision == "allow" && metadata["activation_revision"] == "" {
				t.Fatalf("activation identity: %+v", event)
			}
		}
		for _, key := range []string{"configuration_release.create/allow", "configuration_release.activate/allow", "configuration_release.rollback/allow", "configuration_release.validate/allow", "configuration_release.validate/error", "configuration_release.activate/deny", "configuration_release.activate/error", "configuration_release.cas_conflict/deny"} {
			if seen[key] == 0 {
				t.Errorf("missing %s on schema%d", key, schema)
			}
		}
		if seen["configuration_release.acknowledge/allow"] != 1 {
			t.Fatalf("rejected ACK was audited: %v", seen)
		}
	}
	events, _, err := st.ListAudit(ctx, domain.AuditFilter{EventType: "authz.denial"}, storage.ListPage{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, event := range events {
		if event.ResourceType == domain.ResourceConfigurationRelease {
			seen[auditMetadataForTest(t, event)["schema_version"]] = true
		}
	}
	for _, schema := range []string{"0", "1", "2"} {
		if !seen[schema] {
			t.Fatalf("denial omitted schema%s: %+v", schema, events)
		}
	}
}

func TestManagementAuditsKeepExplicitOlderAndSchemaFreeSelection(t *testing.T) {
	ctx := context.Background()
	svc, st := newConsoleTestService(t)
	pr := adminPrincipal()
	app := seedConsoleApp(t, svc, pr, "dev")
	if _, err := svc.CreateConfigurationSchema(ctx, pr, app.Name, `{"type":"object","title":"newest"}`, "{}"); err != nil {
		t.Fatal(err)
	}
	shipped, err := svc.ShipApplicationChange(ctx, pr, domain.ShipInput{Application: app.Name, Environment: "dev", SchemaVersion: &app.SchemaVersion})
	if err != nil || shipped.Status != domain.ShipStatusActivated {
		t.Fatalf("ship: %+v %v", shipped, err)
	}
	if _, err = svc.CloneApplicationEnvironment(ctx, pr, domain.CloneEnvironmentInput{Application: app.Name, SourceEnv: "dev", TargetEnv: "prod", SchemaVersion: &app.SchemaVersion, CopyValues: true}); err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []string{"application.ship", "application.environment_clone"} {
		events, _, e := st.ListAudit(ctx, domain.AuditFilter{EventType: eventType}, storage.ListPage{})
		if e != nil || len(events) != 1 {
			t.Fatalf("%s audits: %+v %v", eventType, events, e)
		}
		metadata := auditMetadataForTest(t, events[0])
		if metadata["schema_version"] != "1" {
			t.Fatalf("used newest track: %+v", events[0])
		}
		if eventType == "application.ship" && metadata["activation_revision"] != fmt.Sprint(shipped.Activation.ActivationRevision) {
			t.Fatalf("ship activation identity: %+v", events[0])
		}
	}
	for _, selected := range []*uint64{new(uint64(0)), nil} {
		actor := pr
		actor.RequestID = "unscoped"
		if selected != nil {
			actor.RequestID = "schema-free"
		}
		_, err = svc.ApplyApplicationDefaults(ctx, actor, domain.DefaultsApplyInput{Namespace: domain.NamespaceRef{Env: "dev", App: app.Name}, SchemaVersion: selected, Artifact: []byte("invalid")})
		if !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("invalid defaults: %v", err)
		}
	}
	events, _, err := st.ListAudit(ctx, domain.AuditFilter{EventType: "application.defaults.preview"}, storage.ListPage{})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		metadata := auditMetadataForTest(t, event)
		if event.RequestID == "schema-free" && metadata["schema_version"] != "0" {
			t.Fatalf("lost explicit zero: %+v", event)
		}
		if event.RequestID == "unscoped" {
			if _, ok := metadata["schema_version"]; ok {
				t.Fatalf("invented track: %+v", event)
			}
		}
	}
	denied := clientPrincipal("denied")
	if _, err = svc.ShipApplicationChange(ctx, denied, domain.ShipInput{Application: app.Name, Environment: "dev", SchemaVersion: &app.SchemaVersion}); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("denial: %v", err)
	}
	events, _, err = st.ListAudit(ctx, domain.AuditFilter{EventType: "application.ship", Decision: "deny"}, storage.ListPage{})
	if err != nil || len(events) != 1 {
		t.Fatalf("denied audits: %+v %v", events, err)
	}
	if auditMetadataForTest(t, events[0])["schema_version"] != "1" {
		t.Fatalf("management denial lost selection: %+v", events[0])
	}
}

func TestMigrationAuditsSeparateSourceAndDestinationActivation(t *testing.T) {
	ctx := context.Background()
	svc, st, in := migrationFixture(t)
	pr := adminPrincipal()
	for attempt := range 2 {
		if attempt == 1 {
			shipped, err := svc.ShipApplicationChange(ctx, pr, domain.ShipInput{Application: in.Namespace.App, Environment: in.Namespace.Env, SchemaVersion: &in.SourceSchemaVersion, Changes: []domain.ShipChange{{Alias: "rate_limits", Value: new("15")}}})
			if err != nil || shipped.Release.Version != 2 {
				t.Fatalf("advance source: %+v %v", shipped, err)
			}
		}
		in.Execute = false
		in.PlanDigest = ""
		preview, err := svc.MigrateApplicationRelease(ctx, pr, in)
		if err != nil || !preview.Valid {
			t.Fatalf("preview: %+v %v", preview, err)
		}
		in.Execute = true
		in.PlanDigest = preview.PlanDigest
		actor := pr
		actor.RequestID = fmt.Sprintf("migration-%d", attempt)
		result, err := svc.MigrateApplicationRelease(ctx, actor, in)
		if err != nil {
			t.Fatal(err)
		}
		events, _, err := st.ListAudit(ctx, domain.AuditFilter{App: in.Namespace.App}, storage.ListPage{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		seen := 0
		for _, event := range events {
			if event.RequestID != actor.RequestID || (event.EventType != "configuration_release.activate" && event.EventType != "application.release.migrate") {
				continue
			}
			seen++
			metadata := auditMetadataForTest(t, event)
			if metadata["schema_version"] != "2" || metadata["source_schema_version"] != "1" || metadata["previous_version"] != fmt.Sprint(attempt) || metadata["activation_revision"] != fmt.Sprint(result.Activation.ActivationRevision) || metadata["source_activation_revision"] != fmt.Sprint(preview.SourceActivationRevision) {
				t.Fatalf("mixed source/destination activation: %+v", event)
			}
			if metadata["source_version"] != fmt.Sprint(attempt+1) {
				t.Fatalf("missing distinct source release: %+v", event)
			}
		}
		if seen != 2 {
			t.Fatalf("migration audit count: %d", seen)
		}
	}
}

func TestReleaseReadAndSubscriberDenialsAuditExactTrack(t *testing.T) {
	ctx := context.Background()
	svc, st := newConsoleTestService(t)
	app := seedConsoleApp(t, svc, adminPrincipal())
	ns := domain.NamespaceRef{Env: "dev", App: app.Name}
	if _, err := svc.CreateConfigurationSchema(ctx, adminPrincipal(), app.Name, `{"type":"object","title":"newer"}`, "{}"); err != nil {
		t.Fatal(err)
	}
	created, err := svc.CreateIdentity(ctx, adminPrincipal(), CreateIdentityInput{Name: "denied", Kind: domain.IdentityKindClient, AuthMethods: []domain.AuthMethod{domain.AuthMethodToken}})
	if err != nil {
		t.Fatal(err)
	}
	pr := clientPrincipalTok("denied", created.Token)
	operations := []struct {
		name      string
		eventType string
		call      func(Principal, domain.ReleaseTrack) error
	}{
		{"list", "authz.denial", func(pr Principal, track domain.ReleaseTrack) error {
			_, _, err := svc.ListConfigurationReleases(ctx, pr, trackFilter(track), storage.ListPage{})
			return err
		}},
		{"subscribers", "configuration_release.subscribers", func(pr Principal, track domain.ReleaseTrack) error {
			_, _, _, err := svc.ListReleaseSubscribers(ctx, pr, trackFilter(track), storage.ListPage{})
			return err
		}},
		{"rollout", "configuration_release.subscribers", func(pr Principal, track domain.ReleaseTrack) error {
			_, err := svc.GetReleaseRolloutSnapshot(ctx, pr, track)
			return err
		}},
		{"watch", "authz.denial", func(pr Principal, track domain.ReleaseTrack) error {
			return svc.AuthorizeReleaseWatch(ctx, pr, track)
		}},
		{"reauthorize", "authz.denial", func(pr Principal, track domain.ReleaseTrack) error {
			return svc.ReauthorizeReleaseWatch(ctx, pr, track)
		}},
	}
	for _, schema := range []uint64{0, 2} {
		track := domain.ReleaseTrack{Namespace: ns, Name: app.ReleaseName, SchemaVersion: schema}
		for _, op := range operations {
			actor := pr
			actor.RequestID = fmt.Sprintf("%s-schema-%d", op.name, schema)
			if err := op.call(actor, track); !errors.Is(err, domain.ErrPermissionDenied) {
				t.Fatalf("%s: %v", actor.RequestID, err)
			}
			events, _, err := st.ListAudit(ctx, domain.AuditFilter{EventType: op.eventType, Decision: "deny", ActorIdentity: actor.Identity.Name}, storage.ListPage{Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			found := 0
			for _, event := range events {
				if event.RequestID != actor.RequestID {
					continue
				}
				found++
				metadata := auditMetadataForTest(t, event)
				if event.ResourceType != domain.ResourceConfigurationRelease || event.ResourceEnv != ns.Env || event.ResourceApp != ns.App || event.ResourceKey != track.Name || metadata["schema_version"] != fmt.Sprint(schema) {
					t.Fatalf("denial lost exact identity: %+v", event)
				}
				if _, ok := metadata["activation_revision"]; ok {
					t.Fatalf("denial invented activation: %+v", event)
				}
			}
			if found != 1 {
				t.Fatalf("%s: got %d denial events", actor.RequestID, found)
			}
		}
	}

	// Schema selection adds audit metadata, not a new permission boundary.
	if _, err := svc.CreatePolicy(ctx, adminPrincipal(), domain.Policy{Name: "read-tracks", Subject: pr.Identity.Name, Allow: []domain.PolicyRule{
		{Operation: domain.OpConfigurationReleaseList, Env: ns.Env, App: ns.App},
		{Operation: domain.OpConfigurationReleaseWatch, Env: ns.Env, App: ns.App},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, schema := range []uint64{0, 2} {
		track := domain.ReleaseTrack{Namespace: ns, Name: app.ReleaseName, SchemaVersion: schema}
		if _, _, err := svc.ListConfigurationReleases(ctx, pr, trackFilter(track), storage.ListPage{}); err != nil {
			t.Fatalf("namespace list permission for schema %d: %v", schema, err)
		}
		if err := svc.ReauthorizeReleaseWatch(ctx, pr, track); err != nil {
			t.Fatalf("namespace watch permission for schema %d: %v", schema, err)
		}
	}
}

func TestReleaseWatchMethodDenialsPreserveTrackNameAndSchema(t *testing.T) {
	ctx := context.Background()
	svc, st := newConsoleTestService(t)
	app := seedConsoleApp(t, svc, adminPrincipal())
	ns := domain.NamespaceRef{Env: "dev", App: app.Name}
	if _, err := st.UpdateNamespace(ctx, ns, "", []domain.AuthMethod{domain.AuthMethodMTLS}); err != nil {
		t.Fatal(err)
	}
	for _, schema := range []uint64{0, 1} {
		if err := svc.AuthorizeReleaseWatch(ctx, clientPrincipal("denied"), domain.ReleaseTrack{Namespace: ns, Name: app.ReleaseName, SchemaVersion: schema}); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("method denial: %v", err)
		}
	}
	events, _, err := st.ListAudit(ctx, domain.AuditFilter{EventType: "authz.method_denied"}, storage.ListPage{Limit: 10})
	if err != nil || len(events) != 2 {
		t.Fatalf("method denials: %+v %v", events, err)
	}
	seen := map[string]bool{}
	for _, event := range events {
		if event.ResourceEnv != ns.Env || event.ResourceApp != ns.App || event.ResourceKey != app.ReleaseName {
			t.Fatalf("method denial lost release identity: %+v", event)
		}
		seen[auditMetadataForTest(t, event)["schema_version"]] = true
	}
	if !seen["0"] || !seen["1"] {
		t.Fatalf("method denial schema selections: %v", seen)
	}
}

func TestCrossTrackListDenialsDoNotInventSchema(t *testing.T) {
	ctx := context.Background()
	svc, st := newConsoleTestService(t)
	app := seedConsoleApp(t, svc, adminPrincipal())
	ns := domain.NamespaceRef{Env: "dev", App: app.Name}
	pr := clientPrincipal("denied")
	ctx = withReleaseAuditTrack(ctx, domain.ReleaseTrack{Namespace: ns, Name: app.ReleaseName, SchemaVersion: 2})
	for _, filter := range []domain.ReleaseFilter{
		{Namespace: ns, Name: app.ReleaseName},
		{Namespace: ns, SchemaVersion: new(uint64(0))},
		{Namespace: ns},
	} {
		if _, _, err := svc.ListConfigurationReleases(ctx, pr, filter, storage.ListPage{}); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("list denial: %v", err)
		}
		if _, _, _, err := svc.ListReleaseSubscribers(ctx, pr, filter, storage.ListPage{}); !errors.Is(err, domain.ErrPermissionDenied) {
			t.Fatalf("subscriber denial: %v", err)
		}
	}
	events, _, err := st.ListAudit(ctx, domain.AuditFilter{Decision: "deny", ActorIdentity: pr.Identity.Name}, storage.ListPage{Limit: 100})
	if err != nil || len(events) != 6 {
		t.Fatalf("cross-track denials: %+v %v", events, err)
	}
	for _, event := range events {
		if _, ok := auditMetadataForTest(t, event)["schema_version"]; ok {
			t.Fatalf("cross-track denial invented schema: %+v", event)
		}
	}
}
