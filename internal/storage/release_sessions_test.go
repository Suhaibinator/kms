package storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
	"gorm.io/gorm"
)

func TestReleaseSessionPinLifecycle(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	ns := nsRef("prod", "app")
	track := domain.ReleaseTrack{Namespace: ns, Name: "runtime"}
	create := func() uint64 {
		r, e := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{Namespace: ns, Name: track.Name, Digest: "digest", Metadata: "{}"})
		if e != nil {
			t.Fatal(e)
		}
		return r.Version
	}
	first, unpublished, next := create(), create(), create()
	active, _, e := st.ActivateConfigurationRelease(ctx, track, first, nil)
	if e != nil {
		t.Fatal(e)
	}
	session := func(id string) domain.ReleaseSessionRef {
		return domain.ReleaseSessionRef{Track: track, Identity: "client", ClientName: "api", InstanceID: "stable-name", SessionID: id}
	}
	a, b := session("a"), session("b")
	for _, r := range []domain.ReleaseSessionRef{a, b} {
		if e := st.RegisterReleaseSession(ctx, r, false); e != nil {
			t.Fatal(e)
		}
		if e := st.ConnectReleaseSession(ctx, r, "connection", true); e != nil {
			t.Fatal(e)
		}
	}
	target, e := st.SetReleasePin(ctx, a, unpublished, 0, domain.AuditEvent{ActorIdentity: "operator", EventType: "configuration_release.pin"})
	if e != nil {
		t.Fatal(e)
	}
	if target.Release.Version != unpublished || !target.Pinned || target.ActivationRevision != 0 || target.TargetRevision <= active.ActivationRevision {
		t.Fatalf("pin target: %+v", target)
	}
	if _, e := st.SetReleasePin(ctx, a, next, 0, domain.AuditEvent{}); !errors.Is(e, domain.ErrAborted) {
		t.Fatalf("stale guard: %v", e)
	}
	ack := domain.ReleaseAcknowledgement{TargetRevision: target.TargetRevision, ReleaseVersion: unpublished, State: "applied", ClientTimestamp: time.Now(), ConnectionID: "connection"}
	if e := st.AcknowledgeReleaseSession(ctx, a, ack); e != nil {
		t.Fatal(e)
	}
	ack.ReleaseVersion = first
	if e := st.AcknowledgeReleaseSession(ctx, a, ack); !errors.Is(e, domain.ErrFailedPrecondition) {
		t.Fatalf("forged ack: %v", e)
	}
	updated, _, e := st.ActivateConfigurationRelease(ctx, track, next, nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range []domain.ReleaseSessionRef{a, b} {
		got, e := st.ResolveInstanceRelease(ctx, r)
		if e != nil {
			t.Fatal(e)
		}
		want := next
		if r == a {
			want = unpublished
		}
		if got.Release.Version != want {
			t.Fatalf("session %s target: %+v", r.SessionID, got)
		}
	}
	if e := st.ResetReleaseInstanceConnections(ctx, time.Now()); e != nil {
		t.Fatal(e)
	}
	if e := st.RegisterReleaseSession(ctx, a, true); e != nil {
		t.Fatal(e)
	}
	resumed, e := st.ResolveInstanceRelease(ctx, a)
	if e != nil || resumed.PinRevision != target.PinRevision || !resumed.Pinned {
		t.Fatalf("server restart lost pin: %+v %v", resumed, e)
	}
	if _, e := st.SetReleasePin(ctx, a, next, target.PinRevision, domain.AuditEvent{}); !errors.Is(e, domain.ErrFailedPrecondition) {
		t.Fatalf("offline pin: %v", e)
	}
	freed, e := st.SetReleasePin(ctx, a, 0, target.PinRevision, domain.AuditEvent{})
	if e != nil || freed.Pinned || freed.Release.Version != next || freed.TargetRevision <= updated.ActivationRevision {
		t.Fatalf("unpin: %+v %v", freed, e)
	}
	replacement := session("new-process")
	if e := st.RegisterReleaseSession(ctx, replacement, false); e != nil {
		t.Fatal(e)
	}
	got, e := st.ResolveInstanceRelease(ctx, replacement)
	if e != nil || got.Pinned || got.Release.Version != next {
		t.Fatalf("replacement inherited pin: %+v %v", got, e)
	}
	rows, _, e := st.ListReleaseAcknowledgements(ctx, domain.ReleaseFilter{Namespace: ns, Name: track.Name}, ListPage{Limit: 10})
	if e != nil || len(rows) != 3 {
		t.Fatalf("subscriber rows %d %v", len(rows), e)
	}
	bad := a
	bad.Identity = "other"
	if _, e := st.ResolveInstanceRelease(ctx, bad); e == nil {
		t.Fatal("cross identity session read accepted")
	}
	if _, e := st.PruneReleaseAcknowledgements(ctx, time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	if e := st.RegisterReleaseSession(ctx, a, true); !errors.Is(e, domain.ErrFailedPrecondition) {
		t.Fatalf("expired session resurrected: %v", e)
	}
}
func TestReleaseSessionMigrationPreservesBaseline3(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kms.db")
	st, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	seedNS(t, st, "prod", "app")
	ctx := context.Background()
	if _, _, e := st.PutParameter(ctx, ref("prod", "app", "value"), "retained", "string", "{}", "admin"); e != nil {
		t.Fatal(e)
	}

	ns := nsRef("prod", "app")
	schema, err := st.CreateConfigurationSchema(ctx, domain.ConfigurationSchema{Application: "app", ReleaseName: "runtime", Schema: `{"type":"object"}`, Digest: "schema-digest", Metadata: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIdentity(ctx, CreateIdentityParams{Name: "baseline-client", Kind: domain.IdentityKindClient, Namespace: &ns, TokenHash: []byte("unchanged-hash")}); err != nil {
		t.Fatal(err)
	}
	track := domain.ReleaseTrack{Namespace: ns, Name: "runtime", SchemaVersion: schema.Version}
	release, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{Namespace: ns, Name: track.Name, SchemaVersion: schema.Version, Digest: "unchanged-release-digest", Metadata: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	active, _, err := st.ActivateConfigurationRelease(ctx, track, release.Version, nil)
	if err != nil {
		t.Fatal(err)
	}
	connection := domain.ReleaseSubscriberConnection{Namespace: ns, ReleaseName: track.Name, SchemaVersion: schema.Version, ClientName: "api", InstanceID: "old-client", Identity: "baseline-client", ConnectionID: "old", Connected: true, ServerTimestamp: time.Now()}
	if err := st.SetReleaseInstanceConnected(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertReleaseAcknowledgement(ctx, domain.ReleaseAcknowledgement{Namespace: ns, ReleaseName: track.Name, SchemaVersion: schema.Version, ReleaseVersion: release.Version, ActivationRevision: active.ActivationRevision, ClientName: connection.ClientName, InstanceID: connection.InstanceID, Identity: connection.Identity, ConnectionID: connection.ConnectionID, State: "applied", ServerTimestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// Compare every baseline data row, including identity credentials, release
	// digests, schemas, change history and subscriber lifecycle, after upgrade.
	baselineRows := func() map[string][]map[string]any {
		t.Helper()
		tables, err := st.db.Migrator().GetTables()
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][]map[string]any{}
		for _, table := range tables {
			if table == "schema_migrations" || table == "release_sessions" || table == "release_target_deliveries" || table == "sqlite_sequence" {
				continue
			}
			var rows []map[string]any
			if err := st.db.Table(table).Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			out[table] = rows
		}
		return out
	}
	before := baselineRows()
	// Removing only the new tables constructs the exact pre-feature baseline.
	if e := st.db.Migrator().DropTable(&releaseTargetDeliveryModel{}, &releaseSessionModel{}); e != nil {
		t.Fatal(e)
	}
	if e := st.db.Model(&schemaMigrationModel{}).Where("version = ?", schemaVersion).Update("version", 3).Error; e != nil {
		t.Fatal(e)
	}
	if e := verifyReleaseBaseline3(st.db); e != nil {
		t.Fatal(e)
	}
	// An injected DDL failure rolls back both schema changes and the stamp.
	if e := upgradeReleaseSessionsWithVerifier(st.db, func(tx *gorm.DB) error {
		if e := verifyBaselineDB(tx); e != nil {
			t.Fatal(e)
		}
		return errors.New("injected verification failure")
	}); e == nil {
		t.Fatal("expected rollback")
	}
	if e := verifyReleaseBaseline3(st.db); e != nil {
		t.Fatal(e)
	}
	if e := st.Close(); e != nil {
		t.Fatal(e)
	}
	st, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = st.Close() }()
	got, e := st.GetParameter(ctx, ref("prod", "app", "value"), 0, "")
	if e != nil || got.Value != "retained" {
		t.Fatalf("upgrade data: %+v %v", got, e)
	}
	if !reflect.DeepEqual(before, baselineRows()) {
		t.Fatal("migration changed baseline data")
	}
	if e := verifyBaselineDB(st.db); e != nil {
		t.Fatal(e)
	}
}
func TestReleaseSessionPinAuditFailureRollsBack(t *testing.T) {
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	ctx := context.Background()
	ns := nsRef("prod", "app")
	track := domain.ReleaseTrack{Namespace: ns, Name: "runtime"}
	rel, e := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{Namespace: ns, Name: track.Name, Digest: "d", Metadata: "{}"})
	if e != nil {
		t.Fatal(e)
	}
	r := domain.ReleaseSessionRef{Track: track, Identity: "client", ClientName: "api", InstanceID: "a", SessionID: "a"}
	if e := st.RegisterReleaseSession(ctx, r, false); e != nil {
		t.Fatal(e)
	}
	if e := st.ConnectReleaseSession(ctx, r, "c", true); e != nil {
		t.Fatal(e)
	}
	if e := st.db.Exec("CREATE TRIGGER deny_pin_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'injected'); END").Error; e != nil {
		t.Fatal(e)
	}
	before, _ := st.CurrentRevision(ctx)
	if _, e := st.SetReleasePin(ctx, r, rel.Version, 0, domain.AuditEvent{}); e == nil {
		t.Fatal("audit failure accepted")
	}
	after, _ := st.CurrentRevision(ctx)
	got, e := st.ResolveInstanceRelease(ctx, r)
	if e != nil || got.Pinned || before != after {
		t.Fatalf("non-atomic pin: %+v %v revisions %d/%d", got, e, before, after)
	}
}

func TestReleaseSessionProtectsInactivePinnedResources(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	ns := nsRef("prod", "app")
	resource := ref("prod", "app", "setting")
	if _, _, err := st.PutParameter(ctx, resource, "one", "string", "{}", "admin"); err != nil {
		t.Fatal(err)
	}
	release, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{Namespace: ns, Name: "runtime", Digest: "d", Metadata: "{}", Entries: []domain.ConfigurationReleaseEntry{{Alias: "setting", Kind: "parameter", Ref: resource, Version: 1, ContentType: "string", ParameterDigest: fmt.Sprintf("%x", sha256.Sum256([]byte("one"))), Metadata: "{}"}}})
	if err != nil {
		t.Fatal(err)
	}
	session := domain.ReleaseSessionRef{Track: release.Track(), ClientName: "api", InstanceID: "one", SessionID: "one", Identity: "client"}
	if err := st.RegisterReleaseSession(ctx, session, false); err != nil {
		t.Fatal(err)
	}
	if err := st.ConnectReleaseSession(ctx, session, "one", true); err != nil {
		t.Fatal(err)
	}
	target, err := st.SetReleasePin(ctx, session, release.Version, 0, domain.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ConnectReleaseSession(ctx, session, "one", false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteParameter(ctx, resource); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("disconnected pin failed to protect resource: %v", err)
	}
	if _, err := st.SetReleasePin(ctx, session, 0, target.PinRevision, domain.AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	empty, err := st.ResolveInstanceRelease(ctx, session)
	if err != nil || empty.Release.Version != 0 || empty.Pinned || empty.TargetRevision <= target.TargetRevision {
		t.Fatalf("unpin without active: %+v %v", empty, err)
	}
	if _, err := st.DeleteParameter(ctx, resource); err != nil {
		t.Fatalf("unpin failed to release protection: %v", err)
	}
}

func TestReleaseSessionConcurrentGuardsAndConnectionFencing(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kms.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	seedNS(t, st, "prod", "app")
	track := domain.ReleaseTrack{Namespace: nsRef("prod", "app"), Name: "runtime"}
	for range 2 {
		if _, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{Namespace: track.Namespace, Name: track.Name, Digest: "d", Metadata: "{}"}); err != nil {
			t.Fatal(err)
		}
	}
	session := domain.ReleaseSessionRef{Track: track, Identity: "admin", ClientName: "api", InstanceID: "stable", SessionID: "process"}
	if err := st.RegisterReleaseSession(ctx, session, false); err != nil {
		t.Fatal(err)
	}
	if err := st.ConnectReleaseSession(ctx, session, "first", true); err != nil {
		t.Fatal(err)
	}
	start, results := make(chan struct{}), make(chan error, 2)
	for _, version := range []uint64{1, 2} {
		go func() {
			<-start
			_, err := st.SetReleasePin(ctx, session, version, 0, domain.AuditEvent{})
			results <- err
		}()
	}
	close(start)
	var succeeded, aborted int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			succeeded++
		case errors.Is(err, domain.ErrAborted):
			aborted++
		default:
			t.Fatal(err)
		}
	}
	if succeeded != 1 || aborted != 1 {
		t.Fatalf("guard winners=%d stale=%d", succeeded, aborted)
	}
	target, err := st.ResolveInstanceRelease(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ConnectReleaseSession(ctx, session, "replacement-connection", true); err != nil {
		t.Fatal(err)
	}
	if err := st.ConnectReleaseSession(ctx, session, "first", false); err != nil {
		t.Fatal(err)
	}
	ack := domain.ReleaseAcknowledgement{TargetRevision: target.TargetRevision, ReleaseVersion: target.Release.Version, State: "applied", Sequence: 5, ConnectionID: "first"}
	if err := st.AcknowledgeReleaseSession(ctx, session, ack); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("superseded connection ACK: %v", err)
	}
	ack.ConnectionID = "replacement-connection"
	if err := st.AcknowledgeReleaseSession(ctx, session, ack); err != nil {
		t.Fatal(err)
	}
	ack.State, ack.Sequence = "received", 1
	if err := st.AcknowledgeReleaseSession(ctx, session, ack); err != nil {
		t.Fatal(err)
	}
	rows, _, err := st.ListReleaseAcknowledgements(ctx, domain.ReleaseFilter{Namespace: track.Namespace, Name: track.Name}, ListPage{})
	if err != nil || len(rows) != 1 || !rows[0].Connected || rows[0].State != "applied" {
		t.Fatalf("late ACK/disconnect regressed state: %+v %v", rows, err)
	}
	for _, mutate := range []func(*domain.ReleaseSessionRef){
		func(r *domain.ReleaseSessionRef) { r.Identity = "other" }, func(r *domain.ReleaseSessionRef) { r.ClientName = "other" },
		func(r *domain.ReleaseSessionRef) { r.InstanceID = "other" }, func(r *domain.ReleaseSessionRef) { r.Track.SchemaVersion = 2 },
		func(r *domain.ReleaseSessionRef) { r.Track.Name = "other" }, func(r *domain.ReleaseSessionRef) { r.SessionID = "other" },
	} {
		bad := session
		mutate(&bad)
		if _, err := st.ResolveInstanceRelease(ctx, bad); !errors.Is(err, domain.ErrFailedPrecondition) {
			t.Fatalf("session scope escaped: %+v %v", bad, err)
		}
	}
	// Reopen SQLite, then perform the same liveness reset as a KMS restart.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ResetReleaseInstanceConnections(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	resumed, err := st.ResolveInstanceRelease(ctx, session)
	if err != nil || !resumed.Pinned || resumed.PinRevision != target.PinRevision {
		t.Fatalf("restart lost assignment: %+v %v", resumed, err)
	}
	if err := st.DeleteNamespace(ctx, track.Namespace); err != nil {
		t.Fatal(err)
	}
	seedNS(t, st, "prod", "app")
	if err := st.RegisterReleaseSession(ctx, session, true); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("recreated namespace inherited session: %v", err)
	}
	var count int64
	if err := st.db.Model(&releaseTargetDeliveryModel{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("retired namespace retained delivery evidence: %d %v", count, err)
	}
}

func TestListReleaseAcknowledgementsOmitsDepartedSessions(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	ns := nsRef("prod", "app")
	track := domain.ReleaseTrack{Namespace: ns, Name: "runtime"}
	r, e := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{Namespace: ns, Name: track.Name, Digest: "digest", Metadata: "{}"})
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = st.ActivateConfigurationRelease(ctx, track, r.Version, nil); e != nil {
		t.Fatal(e)
	}
	session := func(id string) domain.ReleaseSessionRef {
		return domain.ReleaseSessionRef{Track: track, Identity: "client", ClientName: "api", InstanceID: id, SessionID: id}
	}
	live, gone, pinned := session("live"), session("gone"), session("pinned")
	for _, ref := range []domain.ReleaseSessionRef{live, gone, pinned} {
		if e := st.RegisterReleaseSession(ctx, ref, false); e != nil {
			t.Fatal(e)
		}
		if e := st.ConnectReleaseSession(ctx, ref, "connection", true); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := st.SetReleasePin(ctx, pinned, r.Version, 0, domain.AuditEvent{ActorIdentity: "operator", EventType: "configuration_release.pin"}); e != nil {
		t.Fatal(e)
	}
	for _, ref := range []domain.ReleaseSessionRef{gone, pinned} {
		if e := st.ConnectReleaseSession(ctx, ref, "connection", false); e != nil {
			t.Fatal(e)
		}
	}
	filter := domain.ReleaseFilter{Namespace: ns, Name: track.Name}
	ids := func(page ListPage) []string {
		rows, _, e := st.ListReleaseAcknowledgements(ctx, filter, page)
		if e != nil {
			t.Fatal(e)
		}
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			out = append(out, row.InstanceID)
		}
		sort.Strings(out)
		return out
	}
	all := []string{"gone", "live", "pinned"}
	if got := ids(ListPage{}); !reflect.DeepEqual(got, all) {
		t.Fatalf("no cutoff = %v", got)
	}
	if got := ids(ListPage{DepartedBefore: time.Now().Add(-time.Minute)}); !reflect.DeepEqual(got, all) {
		t.Fatalf("cutoff before the disconnects = %v", got)
	}
	// Past the cutoff only the unpinned disconnected session leaves the list;
	// the connected one and the pinned one stay.
	if got := ids(ListPage{DepartedBefore: time.Now().Add(time.Minute)}); !reflect.DeepEqual(got, []string{"live", "pinned"}) {
		t.Fatalf("cutoff after the disconnects = %v", got)
	}
}
