package storage

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

func trackFixture(t *testing.T) (*SQLStore, []domain.ReleaseTrack) {
	t.Helper()
	st := newStore(t)
	seedNS(t, st, "prod", "tracks")
	tracks := []domain.ReleaseTrack{{Namespace: nsRef("prod", "tracks"), Name: "runtime", SchemaVersion: 0}}
	for i := 1; i <= 2; i++ {
		schema, err := st.CreateConfigurationSchema(context.Background(), domain.ConfigurationSchema{Application: "tracks", ReleaseName: "runtime", Schema: `{"type":"object","x-kms-contract":[]}`, Digest: fmt.Sprint(i)})
		if err != nil {
			t.Fatal(err)
		}
		tracks = append(tracks, domain.ReleaseTrack{Namespace: tracks[0].Namespace, Name: "runtime", SchemaVersion: schema.Version})
	}
	return st, tracks
}
func createTrackRelease(t *testing.T, st *SQLStore, track domain.ReleaseTrack) domain.ConfigurationRelease {
	t.Helper()
	r, err := st.CreateConfigurationRelease(context.Background(), domain.ConfigurationRelease{Namespace: track.Namespace, Name: track.Name, SchemaVersion: track.SchemaVersion, Digest: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSchemaTracksConcurrentAllocationActivationAndRetention(t *testing.T) {
	st, tracks := trackFixture(t)
	ctx := context.Background()
	const versions = 8
	var wg sync.WaitGroup
	errs := make(chan error, len(tracks)*versions)
	for _, track := range tracks {
		for i := 0; i < versions; i++ {
			wg.Add(1)
			go func(track domain.ReleaseTrack) {
				defer wg.Done()
				_, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{Namespace: track.Namespace, Name: track.Name, SchemaVersion: track.SchemaVersion, Digest: "empty"})
				errs <- err
			}(track)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, track := range tracks {
		rows, _, err := st.ListConfigurationReleases(ctx, domain.ReleaseFilter{Namespace: track.Namespace, Name: track.Name, SchemaVersion: &track.SchemaVersion}, ListPage{Limit: 100})
		if err != nil || len(rows) != versions {
			t.Fatalf("track %d rows=%d err=%v", track.SchemaVersion, len(rows), err)
		}
		seen := map[uint64]bool{}
		for _, row := range rows {
			seen[row.Release.Version] = true
			if row.Release.SchemaVersion != track.SchemaVersion {
				t.Fatal("foreign release")
			}
		}
		for i := uint64(1); i <= versions; i++ {
			if !seen[i] {
				t.Fatalf("missing version %d", i)
			}
		}
	}
	// All tracks can activate the same release number from current=0 concurrently.
	errs = make(chan error, len(tracks))
	for _, track := range tracks {
		wg.Add(1)
		go func(track domain.ReleaseTrack) {
			defer wg.Done()
			zero := uint64(0)
			_, _, err := st.ActivateConfigurationRelease(ctx, track, 1, &zero)
			errs <- err
		}(track)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, track := range tracks {
		active, err := st.GetActiveConfigurationRelease(ctx, track)
		if err != nil || active.Release.SchemaVersion != track.SchemaVersion || active.Release.Version != 1 {
			t.Fatalf("active=%+v err=%v", active, err)
		}
		for _, other := range tracks {
			exists, err := st.ConfigurationReleaseActivationExists(ctx, other, 1, active.ActivationRevision)
			if err != nil || exists != (track == other) {
				t.Fatalf("foreign activation identity accepted: %v %v", exists, err)
			}
		}
	}
	// Retain current and one inactive per track, never one shared quota.
	old := fmtTime(time.Now().Add(-48 * time.Hour))
	if err := st.db.Model(&configurationReleaseModel{}).Where("1=1").Update("created_at", old).Error; err != nil {
		t.Fatal(err)
	}
	n, err := st.PruneConfigurationReleases(ctx, time.Hour, 1)
	if err != nil || n != len(tracks)*(versions-2) {
		t.Fatalf("pruned=%d err=%v", n, err)
	}
	for _, track := range tracks {
		r := createTrackRelease(t, st, track)
		if r.Version != versions+1 {
			t.Fatalf("counter reused %d", r.Version)
		}
		count, err := st.CountConfigurationReleases(ctx, domain.ReleaseFilter{Namespace: track.Namespace, Name: track.Name, SchemaVersion: &track.SchemaVersion})
		if err != nil || count != 3 {
			t.Fatalf("count=%d err=%v", count, err)
		}
	}
	all, _, err := st.ListConfigurationReleases(ctx, domain.ReleaseFilter{Namespace: tracks[0].Namespace}, ListPage{Limit: 100})
	if err != nil || len(all) != 9 {
		t.Fatalf("aggregate=%d err=%v", len(all), err)
	}
	current := 0
	for _, r := range all {
		if r.Current {
			current++
		}
	}
	if current != 3 {
		t.Fatalf("current labels=%d", current)
	}
}

func TestSchemaTrackSubscriberIdentityAndPagination(t *testing.T) {
	st, tracks := trackFixture(t)
	ctx := context.Background()
	at := time.Now().UTC()
	for _, track := range tracks {
		c := domain.ReleaseSubscriberConnection{Namespace: track.Namespace, ReleaseName: track.Name, SchemaVersion: track.SchemaVersion, ClientName: "same", InstanceID: "same", Identity: "same", ConnectionID: fmt.Sprint(track.SchemaVersion), Connected: true, ServerTimestamp: at}
		if err := st.SetReleaseInstanceConnected(ctx, c); err != nil {
			t.Fatal(err)
		}
		ack := domain.ReleaseAcknowledgement{Namespace: c.Namespace, ReleaseName: c.ReleaseName, SchemaVersion: c.SchemaVersion, ClientName: c.ClientName, InstanceID: c.InstanceID, Identity: c.Identity, ConnectionID: c.ConnectionID, State: domain.ReleaseStateApplied, ReleaseVersion: 1, ActivationRevision: 1, ClientTimestamp: at, ServerTimestamp: at}
		if err := st.UpsertReleaseAcknowledgement(ctx, ack); err != nil {
			t.Fatal(err)
		}
	}
	zero := tracks[0]
	if err := st.SetReleaseInstanceConnected(ctx, domain.ReleaseSubscriberConnection{Namespace: zero.Namespace, ReleaseName: zero.Name, ClientName: "same", InstanceID: "same", Identity: "same", ConnectionID: "0", ServerTimestamp: at.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	seen := map[uint64]bool{}
	page := ListPage{Limit: 1}
	for {
		rows, next, err := st.ListReleaseAcknowledgements(ctx, domain.ReleaseFilter{Namespace: zero.Namespace, Name: zero.Name}, page)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if seen[row.SchemaVersion] {
				t.Fatal("duplicate pagination row")
			}
			seen[row.SchemaVersion] = true
			if row.Connected != (row.SchemaVersion != 0) {
				t.Fatalf("cross-track disconnect: %+v", row)
			}
		}
		if next == "" {
			break
		}
		page.Token = next
	}
	if len(seen) != 3 {
		t.Fatalf("lost schemas in pagination: %v", seen)
	}
	foreign := domain.ReleaseAcknowledgement{Namespace: zero.Namespace, ReleaseName: zero.Name, SchemaVersion: 1, ClientName: "same", InstanceID: "same", Identity: "same", ConnectionID: "2", State: domain.ReleaseStateApplied, ServerTimestamp: at}
	if err := st.UpsertReleaseAcknowledgement(ctx, foreign); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("foreign generation accepted: %v", err)
	}
}

func TestSchemaContractAdoptionAndSchemaZeroRemainIndependent(t *testing.T) {
	st, tracks := trackFixture(t)
	ctx := context.Background()
	fields := []domain.ApplicationContractField{{Alias: "secret", Kind: domain.ReleaseEntrySecret}}
	if _, err := st.AdoptConfigurationSchemaContract(ctx, "tracks", "runtime", 0, fields); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetConfigurationSchemaContract(ctx, "tracks", "runtime", 0)
	if err != nil || len(got) != 1 || got[0].Alias != "secret" {
		t.Fatalf("schema0=%+v %v", got, err)
	}
	for _, track := range tracks[1:] {
		got, err := st.GetConfigurationSchemaContract(ctx, "tracks", "runtime", track.SchemaVersion)
		if err != nil || got == nil || len(got) != 0 {
			t.Fatalf("annotation empty contract=%+v err=%v", got, err)
		}
		if _, err := st.AdoptConfigurationSchemaContract(ctx, "tracks", "runtime", track.SchemaVersion, fields); !errors.Is(err, domain.ErrFailedPrecondition) {
			t.Fatalf("replaced immutable contract: %v", err)
		}
	}
	schema, err := st.GetConfigurationSchemaByDigest(ctx, "tracks", "runtime", "1")
	if err != nil || schema.Version != 1 {
		t.Fatalf("digest lookup=%+v %v", schema, err)
	}
	schema, err = st.GetConfigurationSchema(ctx, "tracks", "runtime", 0)
	if err != nil || schema.Version != 2 {
		t.Fatalf("registry latest changed: %+v %v", schema, err)
	}
}

func TestSchemaContractConcurrentFirstAdoptionHasOneWinner(t *testing.T) {
	st, _ := trackFixture(t)
	ctx := context.Background()
	schema, err := st.CreateConfigurationSchema(ctx, domain.ConfigurationSchema{Application: "tracks", ReleaseName: "runtime", Schema: `{"type":"object"}`, Digest: "handwritten"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, alias := range []string{"a", "b"} {
		wg.Add(1)
		go func(alias string) {
			defer wg.Done()
			_, err := st.AdoptConfigurationSchemaContract(ctx, "tracks", "runtime", schema.Version, []domain.ApplicationContractField{{Alias: alias, Kind: domain.ReleaseEntrySecret}})
			errs <- err
		}(alias)
	}
	wg.Wait()
	close(errs)
	success, rejected := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, domain.ErrFailedPrecondition) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || rejected != 1 {
		t.Fatalf("success=%d rejected=%d", success, rejected)
	}
}

func TestSchemaTrackNamespaceRecreationResetsCountersAndRejectsOldContext(t *testing.T) {
	st, tracks := trackFixture(t)
	ctx := context.Background()
	track := tracks[1]
	oldNS, err := st.GetNamespace(ctx, track.Namespace)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := BindNamespaceIncarnation(ctx, track.Namespace, oldNS.ID)
	if err != nil {
		t.Fatal(err)
	}
	first := createTrackRelease(t, st, track)
	createTrackRelease(t, st, track)
	active, _, err := st.ActivateConfigurationRelease(ctx, track, first.Version, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteNamespace(ctx, track.Namespace); err != nil {
		t.Fatal(err)
	}
	recreated, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: track.Namespace})
	if err != nil {
		t.Fatal(err)
	}
	if recreated.ID == oldNS.ID {
		t.Fatal("namespace incarnation reused")
	}
	next := createTrackRelease(t, st, track)
	if next.Version != 1 {
		t.Fatalf("new incarnation counter=%d", next.Version)
	}
	exists, err := st.ConfigurationReleaseActivationExists(ctx, track, 1, active.ActivationRevision)
	if err != nil || exists {
		t.Fatalf("old activation survived: %v %v", exists, err)
	}
	if _, err := st.GetConfigurationRelease(bound, track, 1); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("stale read accepted: %v", err)
	}
	if _, _, err := st.ActivateConfigurationRelease(bound, track, 1, nil); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("stale activation accepted: %v", err)
	}
}

func TestSchemaFreeReleaseCannotBypassEstablishedContract(t *testing.T) {
	st, tracks := trackFixture(t)
	ctx := context.Background()
	fields := []domain.ApplicationContractField{{Alias: "required", Kind: domain.ReleaseEntrySecret}}
	if _, err := st.AdoptConfigurationSchemaContract(ctx, "tracks", "runtime", 0, fields); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{Namespace: tracks[0].Namespace, Name: "runtime", Digest: "empty"}); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("bypassed schema0 contract: %v", err)
	}
}

func TestSchemaFreeFirstReleaseAdoptsContractTransactionally(t *testing.T) {
	st, tracks := trackFixture(t)
	ctx := context.Background()
	track := tracks[0]
	fields, err := st.GetConfigurationSchemaContract(ctx, track.Namespace.App, track.Name, 0)
	if err != nil || fields != nil {
		t.Fatalf("initial contract = %#v, %v", fields, err)
	}
	// An invalid pin must roll back first adoption along with the release.
	invalid := domain.ConfigurationRelease{Namespace: track.Namespace, Name: track.Name, Digest: "invalid", Entries: []domain.ConfigurationReleaseEntry{
		{Alias: "missing", Kind: domain.ReleaseEntryParameter, ContentType: "string", Ref: domain.Ref{NS: track.Namespace, Key: "missing"}, Version: 1},
	}}
	ns, err := st.GetNamespace(ctx, track.Namespace)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateLatestApplicationRelease(ctx, ApplicationReleaseCreate{
		Release: invalid, NamespaceID: ns.ID,
		Contract: []domain.ApplicationContractField{{Alias: "missing", Kind: domain.ReleaseEntryParameter, ContentType: "string"}},
	}); err == nil {
		t.Fatal("invalid pin was accepted")
	}
	fields, err = st.GetConfigurationSchemaContract(ctx, track.Namespace.App, track.Name, 0)
	if err != nil || fields != nil {
		t.Fatalf("failed release adopted contract = %#v, %v", fields, err)
	}
	first := createTrackRelease(t, st, track)
	if first.Version != 1 {
		t.Fatalf("failed allocation consumed version: %d", first.Version)
	}
	fields, err = st.GetConfigurationSchemaContract(ctx, track.Namespace.App, track.Name, 0)
	if err != nil || fields == nil || len(fields) != 0 {
		t.Fatalf("first release did not establish empty contract = %#v, %v", fields, err)
	}
	if _, err := st.CreateConfigurationRelease(ctx, invalid); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("later release changed established empty contract: %v", err)
	}
}
