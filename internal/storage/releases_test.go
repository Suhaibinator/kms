package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/Suhaibinator/kms/internal/domain"
)

func TestConfigurationReleaseActivationCASRollbackAndGuards(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	ns := nsRef("prod", "app")
	paramRef := ref("prod", "app", "config")
	secretRef := ref("prod", "app", "secret")
	if _, _, err := st.PutParameter(ctx, paramRef, `{"n":1}`, "json", "{}", "admin"); err != nil {
		t.Fatal(err)
	}
	putSecret(t, st, secretRef, false)

	create := func(digest string) domain.ConfigurationRelease {
		r, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{Namespace: ns, Name: "runtime", Digest: digest, Metadata: "{}", Entries: []domain.ConfigurationReleaseEntry{
			{Alias: "config", Kind: domain.ReleaseEntryParameter, Ref: paramRef, Version: 1, ContentType: "json", ParameterDigest: "abc", Metadata: "{}"},
			{Alias: "secret", Kind: domain.ReleaseEntrySecret, Ref: secretRef, Version: 1, Metadata: "{}"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r1, r2 := create("one"), create("two")
	zero := uint64(0)
	a1, changed, err := st.ActivateConfigurationRelease(ctx, ns, "runtime", r1.Version, &zero)
	if err != nil || !changed || a1.ActivationRevision == 0 {
		t.Fatalf("activate v1 = %+v, %v, %v", a1, changed, err)
	}
	before, _ := st.CurrentRevision(ctx)
	same, changed, err := st.ActivateConfigurationRelease(ctx, ns, "runtime", r1.Version, nil)
	if err != nil || changed || same.ActivationRevision != a1.ActivationRevision {
		t.Fatalf("idempotent activate = %+v,%v,%v", same, changed, err)
	}
	after, _ := st.CurrentRevision(ctx)
	if after != before {
		t.Fatalf("idempotent activation appended revision: %d -> %d", before, after)
	}
	wrong := uint64(99)
	if _, _, err := st.ActivateConfigurationRelease(ctx, ns, "runtime", r2.Version, &wrong); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("CAS err = %v", err)
	}
	expect1 := uint64(1)
	a2, changed, err := st.ActivateConfigurationRelease(ctx, ns, "runtime", r2.Version, &expect1)
	if err != nil || !changed || a2.PreviousVersion != 1 {
		t.Fatalf("activate v2 = %+v,%v,%v", a2, changed, err)
	}
	rollback, changed, err := st.ActivateConfigurationRelease(ctx, ns, "runtime", r1.Version, nil)
	if err != nil || !changed || rollback.PreviousVersion != 2 {
		t.Fatalf("rollback = %+v,%v,%v", rollback, changed, err)
	}
	if _, err := st.DeleteParameter(ctx, paramRef); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("protected parameter delete err=%v", err)
	}
	if _, err := st.DestroySecretVersion(ctx, secretRef, 1); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("protected secret destroy err=%v", err)
	}
	if _, err := st.DeleteSecret(ctx, secretRef); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("protected secret delete err=%v", err)
	}
	exists, err := st.ConfigurationReleaseActivationExists(ctx, ns, "runtime", 1, rollback.ActivationRevision)
	if err != nil || !exists {
		t.Fatalf("activation history exists=%v err=%v", exists, err)
	}
	rows, _, err := st.ListConfigurationReleases(ctx, ns, "runtime", ListPage{})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ActivationRevision == 0 {
			t.Errorf("version %d missing activation history", row.Release.Version)
		}
	}
}

func TestConfigurationReleaseActivationHistoryDoesNotCrossNamespaceIncarnations(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	old := seedNS(t, st, "prod", "app")
	ns := old.NamespaceRef
	revision, err := appendChange(st.db.WithContext(ctx), &changeLogModel{
		ResourceType:  domain.ResourceConfigurationRelease,
		NamespaceID:   old.ID,
		Env:           ns.Env,
		App:           ns.App,
		Key:           "runtime",
		ChangeType:    "activate",
		VersionNumber: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteNamespace(ctx, ns); err != nil {
		t.Fatalf("delete old namespace: %v", err)
	}
	recreated, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: ns, CreatedBy: "admin"})
	if err != nil {
		t.Fatalf("recreate namespace: %v", err)
	}
	if recreated.ID == old.ID {
		t.Fatalf("namespace row ID was reused: %d", recreated.ID)
	}
	exists, err := st.ConfigurationReleaseActivationExists(ctx, ns, "runtime", 1, revision)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("old namespace activation history was accepted for its recreated name")
	}
}

func TestConfigurationReleaseSourceNamespaceIncarnationIsImmutable(t *testing.T) {
	t.Run("inactive release cannot activate against recreated source", func(t *testing.T) {
		ctx := context.Background()
		st := newStore(t)
		sourceA := seedNS(t, st, "prod", "source")
		target := seedNS(t, st, "prod", "target")
		resource := ref("prod", "source", "config")
		if _, _, err := st.PutParameter(ctx, resource, "same-value", "string", "{}", "admin"); err != nil {
			t.Fatal(err)
		}
		release, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{
			Namespace: target.NamespaceRef, Name: "runtime", Digest: "digest", Metadata: "{}",
			Entries: []domain.ConfigurationReleaseEntry{{Alias: "config", Kind: domain.ReleaseEntryParameter, Ref: resource, Version: 1, ContentType: "string", ParameterDigest: "same"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		var persisted configurationReleaseEntryModel
		if err := st.db.Where("release_id = (SELECT id FROM configuration_releases WHERE namespace_id = ? AND name = ? AND version_number = ?)", target.ID, release.Name, release.Version).First(&persisted).Error; err != nil {
			t.Fatal(err)
		}
		if persisted.ResourceNamespaceID != sourceA.ID {
			t.Fatalf("persisted source namespace ID = %d, want %d", persisted.ResourceNamespaceID, sourceA.ID)
		}

		// The inactive release does not protect its old pin, so remove A and
		// reproduce an indistinguishable name/key/version/value in B.
		if _, err := st.DeleteParameter(ctx, resource); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteNamespace(ctx, sourceA.NamespaceRef); err != nil {
			t.Fatal(err)
		}
		sourceB, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: sourceA.NamespaceRef, CreatedBy: "admin"})
		if err != nil {
			t.Fatal(err)
		}
		if sourceB.ID == sourceA.ID {
			t.Fatalf("source namespace ID was reused: %d", sourceB.ID)
		}
		if _, _, err := st.PutParameter(ctx, resource, "same-value", "string", "{}", "admin"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.ActivateConfigurationRelease(ctx, target.NamespaceRef, release.Name, release.Version, nil); !errors.Is(err, domain.ErrFailedPrecondition) {
			t.Fatalf("activate old release against recreated source err = %v, want ErrFailedPrecondition", err)
		}
	})

	t.Run("legacy source pins fail activation closed", func(t *testing.T) {
		ctx := context.Background()
		st := newStore(t)
		seedNS(t, st, "prod", "source")
		target := seedNS(t, st, "prod", "target")
		resource := ref("prod", "source", "config")
		if _, _, err := st.PutParameter(ctx, resource, "value", "string", "{}", "admin"); err != nil {
			t.Fatal(err)
		}
		release, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{
			Namespace: target.NamespaceRef, Name: "runtime", Digest: "digest", Metadata: "{}",
			Entries: []domain.ConfigurationReleaseEntry{{Alias: "config", Kind: domain.ReleaseEntryParameter, Ref: resource, Version: 1}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.db.Model(&configurationReleaseEntryModel{}).Where("release_id = (SELECT id FROM configuration_releases WHERE namespace_id = ? AND name = ? AND version_number = ?)", target.ID, release.Name, release.Version).Update("resource_namespace_id", 0).Error; err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.ActivateConfigurationRelease(ctx, target.NamespaceRef, release.Name, release.Version, nil); !errors.Is(err, domain.ErrFailedPrecondition) {
			t.Fatalf("activate legacy source pin err = %v, want ErrFailedPrecondition", err)
		}
	})

	t.Run("legacy active pins remain deletion guards", func(t *testing.T) {
		ctx := context.Background()
		st := newStore(t)
		seedNS(t, st, "prod", "source")
		target := seedNS(t, st, "prod", "target")
		resource := ref("prod", "source", "config")
		if _, _, err := st.PutParameter(ctx, resource, "value", "string", "{}", "admin"); err != nil {
			t.Fatal(err)
		}
		release, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{
			Namespace: target.NamespaceRef, Name: "runtime", Digest: "digest", Metadata: "{}",
			Entries: []domain.ConfigurationReleaseEntry{{Alias: "config", Kind: domain.ReleaseEntryParameter, Ref: resource, Version: 1}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.ActivateConfigurationRelease(ctx, target.NamespaceRef, release.Name, release.Version, nil); err != nil {
			t.Fatal(err)
		}
		if err := st.db.Model(&configurationReleaseEntryModel{}).Where("release_id = (SELECT id FROM configuration_releases WHERE namespace_id = ? AND name = ? AND version_number = ?)", target.ID, release.Name, release.Version).Update("resource_namespace_id", 0).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := st.DeleteParameter(ctx, resource); !errors.Is(err, domain.ErrFailedPrecondition) {
			t.Fatalf("delete resource pinned by legacy active release err = %v, want ErrFailedPrecondition", err)
		}
	})

	t.Run("old exact pin does not protect recreated source", func(t *testing.T) {
		ctx := context.Background()
		st := newStore(t)
		sourceA := seedNS(t, st, "prod", "source")
		target := seedNS(t, st, "prod", "target")
		resource := ref("prod", "source", "config")
		if _, _, err := st.PutParameter(ctx, resource, "value", "string", "{}", "admin"); err != nil {
			t.Fatal(err)
		}
		release, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{
			Namespace: target.NamespaceRef, Name: "runtime", Digest: "digest", Metadata: "{}",
			Entries: []domain.ConfigurationReleaseEntry{{Alias: "config", Kind: domain.ReleaseEntryParameter, Ref: resource, Version: 1}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.ActivateConfigurationRelease(ctx, target.NamespaceRef, release.Name, release.Version, nil); err != nil {
			t.Fatal(err)
		}

		// Simulate recovery from an older build/operator repair that removed A
		// without consulting the release guard. The retained release must remain
		// tied to A and must not make a same-name B resource undeletable.
		if err := st.db.Transaction(func(tx *gorm.DB) error {
			var parameter parameterModel
			if err := tx.Where("namespace_id = ? AND name = ?", sourceA.ID, resource.Key).First(&parameter).Error; err != nil {
				return err
			}
			if err := tx.Where("parameter_id = ?", parameter.ID).Delete(&parameterLabelModel{}).Error; err != nil {
				return err
			}
			if err := tx.Where("parameter_id = ?", parameter.ID).Delete(&parameterVersionModel{}).Error; err != nil {
				return err
			}
			return tx.Delete(&parameter).Error
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteNamespace(ctx, sourceA.NamespaceRef); err != nil {
			t.Fatal(err)
		}
		sourceB, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: sourceA.NamespaceRef, CreatedBy: "admin"})
		if err != nil {
			t.Fatal(err)
		}
		if sourceB.ID == sourceA.ID {
			t.Fatalf("source namespace ID was reused: %d", sourceB.ID)
		}
		if _, _, err := st.PutParameter(ctx, resource, "value", "string", "{}", "admin"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DeleteParameter(ctx, resource); err != nil {
			t.Fatalf("old exact source pin protected recreated resource: %v", err)
		}
	})
}

func TestNamespaceDeleteRejectsConfigurationReleaseState(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	ns := nsRef("prod", "app")
	if _, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{Namespace: ns, Name: "runtime", Digest: "digest", Metadata: "{}"}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteNamespace(ctx, ns); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("DeleteNamespace err=%v, want failed precondition", err)
	}
}

func TestConfigurationSchemaAndReleaseAcknowledgementRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	ns := nsRef("prod", "app")
	schema, err := st.CreateConfigurationSchema(ctx, domain.ConfigurationSchema{ID: "runtime", Schema: `{"type":"object"}`, Digest: "d", Metadata: "{}"})
	if err != nil || schema.Version != 1 {
		t.Fatalf("schema=%+v err=%v", schema, err)
	}
	got, err := st.GetConfigurationSchema(ctx, "runtime", 1)
	if err != nil || got.Schema != schema.Schema {
		t.Fatalf("get schema=%+v err=%v", got, err)
	}
	if err := st.SetReleaseInstanceConnected(ctx, domain.ReleaseSubscriberConnection{Namespace: ns, ReleaseName: "runtime", ClientName: "api", InstanceID: "replica-1", Identity: "client", ConnectionID: "one", Connected: true, ServerTimestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	ack := domain.ReleaseAcknowledgement{Namespace: ns, ReleaseName: "runtime", ReleaseVersion: 1, ActivationRevision: 7, ClientName: "api", InstanceID: "replica-1", Identity: "client", ConnectionID: "one", State: domain.ReleaseStateRejected, RejectionCategory: domain.ReleaseRejectSuperseded, Diagnostic: "redacted", ClientTimestamp: time.Now(), ServerTimestamp: time.Now()}
	if err := st.UpsertReleaseAcknowledgement(ctx, ack); err != nil {
		t.Fatal(err)
	}
	rows, _, err := st.ListReleaseAcknowledgements(ctx, ns, "runtime", ListPage{})
	if err != nil || len(rows) != 1 || rows[0].State != domain.ReleaseStateRejected {
		t.Fatalf("acks=%+v err=%v", rows, err)
	}
	if err := st.SetReleaseInstanceConnected(ctx, domain.ReleaseSubscriberConnection{Namespace: ns, ReleaseName: "runtime", ClientName: "api", InstanceID: "replica-1", Identity: "client", ConnectionID: "one", Connected: false, ServerTimestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	rows, _, err = st.ListReleaseAcknowledgements(ctx, ns, "runtime", ListPage{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("acks after disconnect=%+v err=%v", rows, err)
	}
	if rows[0].Connected {
		t.Fatal("ack remained connected")
	}
}

func TestReleaseAcknowledgementDoesNotRegressAndSurvivesChangelogPrune(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	ns := nsRef("prod", "app")
	createAndActivate := func(digest string) domain.ActiveConfigurationRelease {
		release, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{Namespace: ns, Name: "runtime", Digest: digest, Metadata: "{}"})
		if err != nil {
			t.Fatal(err)
		}
		active, changed, err := st.ActivateConfigurationRelease(ctx, ns, "runtime", release.Version, nil)
		if err != nil || !changed {
			t.Fatalf("activate %s changed=%v err=%v", digest, changed, err)
		}
		return active
	}
	a1 := createAndActivate("one")
	a2 := createAndActivate("two")
	now := time.Now().UTC()
	if err := st.SetReleaseInstanceConnected(ctx, domain.ReleaseSubscriberConnection{
		Namespace: ns, ReleaseName: "runtime", ClientName: "api", InstanceID: "replica-1",
		Identity: "client", ConnectionID: "one", Connected: true, ServerTimestamp: now,
	}); err != nil {
		t.Fatal(err)
	}
	ack := func(active domain.ActiveConfigurationRelease, at time.Time) domain.ReleaseAcknowledgement {
		return domain.ReleaseAcknowledgement{
			Namespace: ns, ReleaseName: "runtime", ReleaseVersion: active.Release.Version,
			ActivationRevision: active.ActivationRevision, ClientName: "api", InstanceID: "replica-1",
			Identity: "client", ConnectionID: "one", State: domain.ReleaseStateReceived,
			ClientTimestamp: at, ServerTimestamp: at,
		}
	}
	if err := st.UpsertReleaseAcknowledgement(ctx, ack(a1, now)); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertReleaseAcknowledgement(ctx, ack(a2, now.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	// A delayed retry can have a later transport timestamp but must not replace
	// the state from a newer authoritative activation.
	if err := st.UpsertReleaseAcknowledgement(ctx, ack(a1, now.Add(2*time.Second))); err != nil {
		t.Fatal(err)
	}
	rows, _, err := st.ListReleaseAcknowledgements(ctx, ns, "runtime", ListPage{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("acks=%+v err=%v", rows, err)
	}
	if rows[0].ActivationRevision != a2.ActivationRevision || rows[0].ReleaseVersion != a2.Release.Version {
		t.Fatalf("ack regressed to %+v", rows[0])
	}
	if _, err := st.PruneChangeLog(ctx, time.Nanosecond, 0); err != nil {
		t.Fatal(err)
	}
	if exists, err := st.ConfigurationReleaseActivationExists(ctx, ns, "runtime", a1.Release.Version, a1.ActivationRevision); err != nil || !exists {
		t.Fatalf("historical activation after changelog prune exists=%v err=%v", exists, err)
	}
}

func TestReleaseConnectionIsVisibleFencedAndReset(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	ns := nsRef("prod", "app")
	connection := func(id string, connected bool) domain.ReleaseSubscriberConnection {
		return domain.ReleaseSubscriberConnection{Namespace: ns, ReleaseName: "runtime", ClientName: "api", InstanceID: "replica-1", Identity: "client", ConnectionID: id, Connected: connected, ServerTimestamp: time.Now().UTC()}
	}
	if err := st.SetReleaseInstanceConnected(ctx, connection("old", true)); err != nil {
		t.Fatal(err)
	}
	rows, _, err := st.ListReleaseAcknowledgements(ctx, ns, "runtime", ListPage{})
	if err != nil || len(rows) != 1 || rows[0].State != "" || !rows[0].Connected {
		t.Fatalf("registration-only rows=%+v err=%v", rows, err)
	}
	if err := st.SetReleaseInstanceConnected(ctx, connection("new", true)); err != nil {
		t.Fatal(err)
	}
	if err := st.SetReleaseInstanceConnected(ctx, connection("old", false)); err != nil {
		t.Fatal(err)
	}
	rows, _, err = st.ListReleaseAcknowledgements(ctx, ns, "runtime", ListPage{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("acks after stale disconnect=%+v err=%v", rows, err)
	}
	if !rows[0].Connected {
		t.Fatal("stale stream disconnect replaced newer connection")
	}
	if err := st.ResetReleaseInstanceConnections(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	rows, _, err = st.ListReleaseAcknowledgements(ctx, ns, "runtime", ListPage{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("acks after reset=%+v err=%v", rows, err)
	}
	if rows[0].Connected {
		t.Fatal("unclean-restart reset left connection marked live")
	}
}

func TestReleaseSubscriberStateIsScopedByIdentity(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	ns := nsRef("prod", "app")
	base := time.Now().UTC().Add(-time.Minute)

	connection := func(identity, id string, connected bool, at time.Time) domain.ReleaseSubscriberConnection {
		return domain.ReleaseSubscriberConnection{
			Namespace: ns, ReleaseName: "runtime", ClientName: "api", InstanceID: "replica-1",
			Identity: identity, ConnectionID: id, Connected: connected, ServerTimestamp: at,
		}
	}
	if err := st.SetReleaseInstanceConnected(ctx, connection("alice", "alice-1", true, base)); err != nil {
		t.Fatal(err)
	}
	if err := st.SetReleaseInstanceConnected(ctx, connection("bob", "bob-1", true, base.Add(time.Second))); err != nil {
		t.Fatal(err)
	}

	ack := func(identity, diagnostic string, clientAt, serverAt time.Time) domain.ReleaseAcknowledgement {
		return domain.ReleaseAcknowledgement{
			Namespace: ns, ReleaseName: "runtime", ReleaseVersion: 1, ActivationRevision: 7,
			ClientName: "api", InstanceID: "replica-1", Identity: identity, ConnectionID: identity + "-1",
			State: domain.ReleaseStateReceived, Diagnostic: diagnostic,
			ClientTimestamp: clientAt, ServerTimestamp: serverAt,
		}
	}
	if err := st.UpsertReleaseAcknowledgement(ctx, ack("alice", "old", base.Add(365*24*time.Hour), base.Add(2*time.Second))); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertReleaseAcknowledgement(ctx, ack("bob", "bob", base, base.Add(3*time.Second))); err != nil {
		t.Fatal(err)
	}
	// Client time is diagnostic only. A future value from an earlier receipt
	// must not block a later acknowledgement for the same activation.
	if err := st.UpsertReleaseAcknowledgement(ctx, ack("alice", "new", base.Add(-365*24*time.Hour), base.Add(4*time.Second))); err != nil {
		t.Fatal(err)
	}

	rows, _, err := st.ListReleaseAcknowledgements(ctx, ns, "runtime", ListPage{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("subscriber rows = %+v, want one row per authenticated identity", rows)
	}
	byIdentity := make(map[string]domain.ReleaseAcknowledgement, len(rows))
	for _, row := range rows {
		byIdentity[row.Identity] = row
	}
	if byIdentity["alice"].Diagnostic != "new" || !byIdentity["alice"].Connected {
		t.Fatalf("alice row = %+v", byIdentity["alice"])
	}
	if byIdentity["bob"].Diagnostic != "bob" || !byIdentity["bob"].Connected {
		t.Fatalf("bob row = %+v", byIdentity["bob"])
	}

	if err := st.SetReleaseInstanceConnected(ctx, connection("alice", "alice-1", false, base.Add(5*time.Second))); err != nil {
		t.Fatal(err)
	}
	rows, _, err = st.ListReleaseAcknowledgements(ctx, ns, "runtime", ListPage{})
	if err != nil {
		t.Fatal(err)
	}
	byIdentity = make(map[string]domain.ReleaseAcknowledgement, len(rows))
	for _, row := range rows {
		byIdentity[row.Identity] = row
	}
	if byIdentity["alice"].Connected || !byIdentity["bob"].Connected {
		t.Fatalf("identity-scoped disconnect changed the wrong row: %+v", byIdentity)
	}
}

func TestReleaseAcknowledgementPaginationIsStableAndComplete(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	ns := nsRef("prod", "app")
	states := []string{domain.ReleaseStateReceived, domain.ReleaseStatePrepared, domain.ReleaseStateApplied, domain.ReleaseStateRejected}
	base := time.Now().UTC().Add(-time.Minute)
	if err := st.SetReleaseInstanceConnected(ctx, domain.ReleaseSubscriberConnection{
		Namespace: ns, ReleaseName: "runtime", ClientName: "api", InstanceID: "replica-1",
		Identity: "client", ConnectionID: "one", Connected: true, ServerTimestamp: base,
	}); err != nil {
		t.Fatal(err)
	}
	for i, state := range states {
		at := base.Add(time.Duration(i) * time.Second)
		ack := domain.ReleaseAcknowledgement{
			Namespace: ns, ReleaseName: "runtime", ReleaseVersion: 1,
			ActivationRevision: 7, ClientName: "api", InstanceID: "replica-1", Identity: "client", ConnectionID: "one",
			State: state, ClientTimestamp: at, ServerTimestamp: at,
		}
		if err := st.UpsertReleaseAcknowledgement(ctx, ack); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	token := ""
	for {
		rows, next, err := st.ListReleaseAcknowledgements(ctx, ns, "runtime", ListPage{Limit: 2, Token: token})
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			got = append(got, row.State)
		}
		if next == "" {
			break
		}
		if next == token {
			t.Fatal("pagination cursor did not advance")
		}
		token = next
	}
	if len(got) != len(states) {
		t.Fatalf("states across pages=%v, want %d", got, len(states))
	}
	seen := make(map[string]bool, len(got))
	for _, state := range got {
		if seen[state] {
			t.Fatalf("duplicate state across pages: %q in %v", state, got)
		}
		seen[state] = true
	}
}

func TestLateReleaseAcknowledgementCannotResurrectDisconnectedSubscriber(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	ns := nsRef("prod", "app")
	base := time.Now().UTC().Add(-time.Minute)
	connection := func(connected bool, at time.Time) domain.ReleaseSubscriberConnection {
		return domain.ReleaseSubscriberConnection{
			Namespace: ns, ReleaseName: "runtime", ClientName: "api", InstanceID: "replica-1",
			Identity: "client", ConnectionID: "generation-1", Connected: connected, ServerTimestamp: at,
		}
	}
	if err := st.SetReleaseInstanceConnected(ctx, connection(true, base)); err != nil {
		t.Fatal(err)
	}
	ack := domain.ReleaseAcknowledgement{
		Namespace: ns, ReleaseName: "runtime", ReleaseVersion: 1, ActivationRevision: 7,
		ClientName: "api", InstanceID: "replica-1", Identity: "client", ConnectionID: "generation-1",
		State: domain.ReleaseStateReceived, ClientTimestamp: base, ServerTimestamp: base,
	}
	if err := st.UpsertReleaseAcknowledgement(ctx, ack); err != nil {
		t.Fatal(err)
	}
	if err := st.SetReleaseInstanceConnected(ctx, connection(false, base.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	ack.State = domain.ReleaseStatePrepared
	ack.ServerTimestamp = base.Add(2 * time.Second)
	if err := st.UpsertReleaseAcknowledgement(ctx, ack); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("late acknowledgement err = %v, want ErrAborted", err)
	}
	if _, err := st.PruneReleaseAcknowledgements(ctx, base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	rows, _, err := st.ListReleaseAcknowledgements(ctx, ns, "runtime", ListPage{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("pruned disconnected subscriber resurrected by late acknowledgement: %+v", rows)
	}
}

func TestReleaseAcknowledgementPaginationUsesIdentityTieBreaker(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	ns := nsRef("prod", "app")
	at := time.Now().UTC().Add(-time.Minute)
	for _, identity := range []string{"alice", "bob"} {
		connectionID := identity + "-1"
		if err := st.SetReleaseInstanceConnected(ctx, domain.ReleaseSubscriberConnection{
			Namespace: ns, ReleaseName: "runtime", ClientName: "api", InstanceID: "replica-1",
			Identity: identity, ConnectionID: connectionID, Connected: true, ServerTimestamp: at,
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.UpsertReleaseAcknowledgement(ctx, domain.ReleaseAcknowledgement{
			Namespace: ns, ReleaseName: "runtime", ReleaseVersion: 1, ActivationRevision: 7,
			ClientName: "api", InstanceID: "replica-1", Identity: identity, ConnectionID: connectionID,
			State: domain.ReleaseStateReceived, ClientTimestamp: at, ServerTimestamp: at,
		}); err != nil {
			t.Fatal(err)
		}
	}

	var identities []string
	token := ""
	for {
		rows, next, err := st.ListReleaseAcknowledgements(ctx, ns, "runtime", ListPage{Limit: 1, Token: token})
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.State != "" {
				identities = append(identities, row.Identity)
			}
		}
		if next == "" {
			break
		}
		if next == token {
			t.Fatal("pagination cursor did not advance")
		}
		token = next
	}
	if len(identities) != 2 || identities[0] == identities[1] {
		t.Fatalf("identities across one-row pages = %v, want alice and bob exactly once", identities)
	}
}

func TestReleaseAcknowledgementPaginationRejectsPreIdentityCursor(t *testing.T) {
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	legacy := encodeToken(`{"server_timestamp":"2026-01-01T00:00:00.000000000Z","release_name":"runtime","client_name":"api","instance_id":"one","state":"received"}`)
	_, _, err := st.ListReleaseAcknowledgements(context.Background(), nsRef("prod", "app"), "runtime", ListPage{Token: legacy})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("legacy pre-identity cursor err = %v, want ErrInvalidArgument", err)
	}
}

func TestPruneConfigurationReleasesProtectsReplayDependencies(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	ns := nsRef("prod", "app")
	var releases []domain.ConfigurationRelease
	for i := 0; i < 4; i++ {
		r, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{Namespace: ns, Name: "runtime", Digest: string(rune('a' + i)), Metadata: "{}"})
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, r)
		if _, changed, err := st.ActivateConfigurationRelease(ctx, ns, "runtime", r.Version, nil); err != nil || !changed {
			t.Fatalf("activate %d changed=%v err=%v", r.Version, changed, err)
		}
	}
	if err := st.db.Model(&configurationReleaseModel{}).Where("1 = 1").Update("created_at", fmtTime(time.Now().Add(-365*24*time.Hour))).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.db.Model(&configurationReleaseActivationModel{}).Where("1 = 1").Update("activated_at", fmtTime(time.Now().Add(-365*24*time.Hour))).Error; err != nil {
		t.Fatal(err)
	}
	removed, err := st.PruneConfigurationReleases(ctx, time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("removed %d releases still needed by labels/replay", removed)
	}
	if _, err := st.PruneChangeLog(ctx, time.Nanosecond, 0); err != nil {
		t.Fatal(err)
	}
	removed, err = st.PruneConfigurationReleases(ctx, time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed=%d want historical v1 only", removed)
	}
	if _, err := st.GetConfigurationRelease(ctx, ns, "runtime", releases[0].Version); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("v1 after prune err=%v", err)
	}
}

func TestPruneConfigurationReleasesRetainsInactiveCountBeyondLabels(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	ns := nsRef("prod", "app")
	var releases []domain.ConfigurationRelease
	for i := 0; i < 5; i++ {
		release, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{Namespace: ns, Name: "runtime", Digest: string(rune('a' + i)), Metadata: "{}"})
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	if _, changed, err := st.ActivateConfigurationRelease(ctx, ns, "runtime", releases[3].Version, nil); err != nil || !changed {
		t.Fatalf("activate v4 changed=%v err=%v", changed, err)
	}
	if _, changed, err := st.ActivateConfigurationRelease(ctx, ns, "runtime", releases[4].Version, nil); err != nil || !changed {
		t.Fatalf("activate v5 changed=%v err=%v", changed, err)
	}
	if err := st.db.Model(&configurationReleaseModel{}).Where("1 = 1").Update("created_at", fmtTime(time.Now().Add(-365*24*time.Hour))).Error; err != nil {
		t.Fatal(err)
	}
	removed, err := st.PruneConfigurationReleases(ctx, time.Hour, 2)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed=%d, want only oldest inactive release", removed)
	}
	for _, release := range releases[1:] {
		if _, err := st.GetConfigurationRelease(ctx, ns, "runtime", release.Version); err != nil {
			t.Fatalf("retained version %d: %v", release.Version, err)
		}
	}
}
