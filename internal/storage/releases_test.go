package storage

import (
	"context"
	"errors"
	"testing"
	"time"

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
	ack := domain.ReleaseAcknowledgement{Namespace: ns, ReleaseName: "runtime", ReleaseVersion: 1, ActivationRevision: 7, ClientName: "api", InstanceID: "replica-1", Identity: "client", State: domain.ReleaseStateRejected, RejectionCategory: domain.ReleaseRejectSuperseded, Diagnostic: "redacted", ClientTimestamp: time.Now(), ServerTimestamp: time.Now(), Connected: true}
	if err := st.UpsertReleaseAcknowledgement(ctx, ack); err != nil {
		t.Fatal(err)
	}
	rows, _, err := st.ListReleaseAcknowledgements(ctx, ns, "runtime", ListPage{})
	if err != nil || len(rows) != 1 || rows[0].State != domain.ReleaseStateRejected {
		t.Fatalf("acks=%+v err=%v", rows, err)
	}
	if err := st.SetReleaseInstanceConnected(ctx, domain.ReleaseSubscriberConnection{Namespace: ns, ReleaseName: "runtime", ClientName: "api", InstanceID: "replica-1", ConnectionID: "one", Connected: true, ServerTimestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetReleaseInstanceConnected(ctx, domain.ReleaseSubscriberConnection{Namespace: ns, ReleaseName: "runtime", ClientName: "api", InstanceID: "replica-1", ConnectionID: "one", Connected: false, ServerTimestamp: time.Now()}); err != nil {
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
	ack := func(active domain.ActiveConfigurationRelease, at time.Time) domain.ReleaseAcknowledgement {
		return domain.ReleaseAcknowledgement{
			Namespace: ns, ReleaseName: "runtime", ReleaseVersion: active.Release.Version,
			ActivationRevision: active.ActivationRevision, ClientName: "api", InstanceID: "replica-1",
			Identity: "client", State: domain.ReleaseStateReceived, ClientTimestamp: at,
			ServerTimestamp: at, Connected: true,
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

func TestReleaseAcknowledgementPaginationIsStableAndComplete(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	ns := nsRef("prod", "app")
	states := []string{domain.ReleaseStateReceived, domain.ReleaseStatePrepared, domain.ReleaseStateApplied, domain.ReleaseStateRejected}
	base := time.Now().UTC().Add(-time.Minute)
	for i, state := range states {
		at := base.Add(time.Duration(i) * time.Second)
		ack := domain.ReleaseAcknowledgement{
			Namespace: ns, ReleaseName: "runtime", ReleaseVersion: 1,
			ActivationRevision: 7, ClientName: "api", InstanceID: "replica-1",
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
