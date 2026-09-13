package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
	"gorm.io/gorm"
)

func reducerFixture(t *testing.T) (*SQLStore, domain.ReleaseSessionRef, domain.ReleaseAcknowledgement) {
	t.Helper()
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	ref := domain.ReleaseSessionRef{Track: domain.ReleaseTrack{Namespace: nsRef("prod", "app"), Name: "runtime"}, SessionID: "session", ClientName: "client", InstanceID: "instance", Identity: "identity"}
	r, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{Namespace: ref.Track.Namespace, Name: "runtime", Digest: "digest", Metadata: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.ActivateConfigurationRelease(ctx, ref.Track, r.Version, nil); err != nil {
		t.Fatal(err)
	}
	if err = st.RegisterReleaseSession(ctx, ref, false); err != nil {
		t.Fatal(err)
	}
	if err = st.ConnectReleaseSession(ctx, ref, "connection", true); err != nil {
		t.Fatal(err)
	}
	target, err := st.ResolveInstanceRelease(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	return st, ref, domain.ReleaseAcknowledgement{ConnectionID: "connection", TargetRevision: target.TargetRevision, ActivationRevision: target.ActivationRevision, ReleaseVersion: r.Version}
}

func TestSessionReducerRollbackDoesNotReportAccepted(t *testing.T) {
	st, ref, ack := reducerFixture(t)
	ack.Sequence, ack.State = 1, "applied"
	injected := errors.New("injected projection write failure")
	if err := st.db.Callback().Update().Before("gorm:update").Register("test:fail_projection", func(tx *gorm.DB) {
		if tx.Statement.Table == "release_sessions" {
			_ = tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	result, err := st.ReduceReleaseSessionAcknowledgement(context.Background(), ref, ack)
	if !errors.Is(err, injected) || result.Disposition != "unavailable" || result.Effective.State != "" {
		t.Fatalf("uncommitted result exposed: %+v %v", result, err)
	}
	var count int64
	if err := st.db.Model(&releaseSessionEventModel{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("ledger escaped rollback: %d %v", count, err)
	}
}

func TestSessionReducerImmutablePayloadAndFencing(t *testing.T) {
	st, ref, ack := reducerFixture(t)
	ctx := context.Background()
	ack.State, ack.Sequence = "applied", 1
	ack.ClientTimestamp = time.Date(2026, 1, 1, 0, 0, 0, 1, time.UTC)
	if _, err := st.ReduceReleaseSessionAcknowledgement(ctx, ref, ack); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*domain.ReleaseAcknowledgement){
		func(a *domain.ReleaseAcknowledgement) { a.ClientTimestamp = a.ClientTimestamp.Add(time.Nanosecond) },
		func(a *domain.ReleaseAcknowledgement) { a.Diagnostic = "different" },
		func(a *domain.ReleaseAcknowledgement) { a.AppliedDivergent = true },
		func(a *domain.ReleaseAcknowledgement) { a.ReleaseVersion++ },
		func(a *domain.ReleaseAcknowledgement) { a.TargetRevision++ },
	} {
		changed := ack
		change(&changed)
		result, err := st.ReduceReleaseSessionAcknowledgement(ctx, ref, changed)
		if err == nil || result.Disposition != "conflict" || result.Effective.Sequence != 1 {
			t.Fatalf("conflicting payload: %+v %v", result, err)
		}
	}
	for _, change := range []func(*domain.ReleaseSessionRef){
		func(r *domain.ReleaseSessionRef) { r.Identity = "other" },
		func(r *domain.ReleaseSessionRef) { r.Track.SchemaVersion++ },
		func(r *domain.ReleaseSessionRef) { r.ClientName = "other" },
		func(r *domain.ReleaseSessionRef) { r.InstanceID = "other" },
		func(r *domain.ReleaseSessionRef) { r.SessionID = "other" },
	} {
		changed := ref
		change(&changed)
		if _, err := st.ReduceReleaseSessionAcknowledgement(ctx, changed, ack); !errors.Is(err, domain.ErrFailedPrecondition) {
			t.Fatalf("scope bypass: %v", err)
		}
	}
	if err := st.ConnectReleaseSession(ctx, ref, "new", true); err != nil {
		t.Fatal(err)
	}
	if result, err := st.ReduceReleaseSessionAcknowledgement(ctx, ref, ack); !errors.Is(err, domain.ErrAborted) || result.Disposition == "duplicate" {
		t.Fatalf("old stream duplicate bypassed fence: %+v %v", result, err)
	}
	ack.ConnectionID = "new"
	if result, err := st.ReduceReleaseSessionAcknowledgement(ctx, ref, ack); err != nil || result.Disposition != "duplicate" {
		t.Fatalf("new stream duplicate: %+v %v", result, err)
	}
}

func TestSessionReducerReplayPermutations(t *testing.T) {
	for _, order := range [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		st, ref, base := reducerFixture(t)
		events := []domain.ReleaseAcknowledgement{base, base, base}
		for i, state := range []string{"received", "rejected", "applied"} {
			events[i].State = state
			events[i].Sequence = uint64(i + 1)
		}
		events[1].Diagnostic = "rejected reason"
		events[1].RejectionCategory = "restart_required"
		events[2].AppliedDivergent = true
		events[2].DivergentFieldCount = 2
		for _, index := range order {
			if _, err := st.ReduceReleaseSessionAcknowledgement(context.Background(), ref, events[index]); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.ConnectReleaseSession(context.Background(), ref, "reconnected", true); err != nil {
			t.Fatal(err)
		}
		for _, index := range order {
			event := events[index]
			event.ConnectionID = "reconnected"
			result, err := st.ReduceReleaseSessionAcknowledgement(context.Background(), ref, event)
			if err != nil || result.Disposition != "duplicate" {
				t.Fatalf("duplicate: %+v %v", result, err)
			}
			got := result.Effective
			if got.State != "applied" || got.Sequence != 3 || got.Diagnostic != "" || got.RejectionCategory != "" || !got.AppliedDivergent || got.DivergentFieldCount != 2 || got.LastAppliedSequence != 3 {
				t.Fatalf("order %v regressed: %+v", order, got)
			}
		}
		conflict := events[2]
		conflict.ConnectionID = "reconnected"
		conflict.State = "received"
		result, err := st.ReduceReleaseSessionAcknowledgement(context.Background(), ref, conflict)
		if err == nil || result.Disposition != "conflict" {
			t.Fatalf("conflict: %+v %v", result, err)
		}
	}
}

func TestSessionReducerRetentionAndZeroSequence(t *testing.T) {
	st, ref, ack := reducerFixture(t)
	ack.State = "applied"
	if _, err := st.ReduceReleaseSessionAcknowledgement(context.Background(), ref, ack); err == nil {
		t.Fatal("accepted zero sequence")
	}
	for seq := uint64(1); seq <= releaseSessionEventRetention+2; seq++ {
		ack.Sequence = seq
		if _, err := st.ReduceReleaseSessionAcknowledgement(context.Background(), ref, ack); err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := st.db.Model(&releaseSessionEventModel{}).Count(&count).Error; err != nil || count != releaseSessionEventRetention {
		t.Fatalf("retention %d: %v", count, err)
	}
	ack.Sequence = 1
	ack.State = "rejected"
	result, err := st.ReduceReleaseSessionAcknowledgement(context.Background(), ref, ack)
	if err != nil || result.Disposition != "stale" || result.Effective.State != "applied" {
		t.Fatalf("pruned replay: %+v %v", result, err)
	}
}

func TestSessionReducerSeparatesAttemptAndAppliedEvidence(t *testing.T) {
	st, ref, old := reducerFixture(t)
	ctx := context.Background()
	r, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{Namespace: ref.Track.Namespace, Name: "runtime", Digest: "next", Metadata: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.ActivateConfigurationRelease(ctx, ref.Track, r.Version, nil); err != nil {
		t.Fatal(err)
	}
	target, err := st.ResolveInstanceRelease(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	latest := old
	latest.TargetRevision, latest.ActivationRevision, latest.ReleaseVersion = target.TargetRevision, target.ActivationRevision, r.Version
	latest.Sequence, latest.State, latest.RejectionCategory = 4, "rejected", "validation"
	if _, err = st.ReduceReleaseSessionAcknowledgement(ctx, ref, latest); err != nil {
		t.Fatal(err)
	}
	old.Sequence, old.State = 3, "applied"
	got, err := st.ReduceReleaseSessionAcknowledgement(ctx, ref, old)
	if err != nil || got.Disposition != "stale" || got.Effective.State != "rejected" || got.Effective.ReleaseVersion != r.Version || got.Effective.LastAppliedVersion != old.ReleaseVersion || got.Effective.LastAppliedSequence != 3 {
		t.Fatalf("late applied evidence: %+v %v", got, err)
	}
	latest.Sequence, latest.State, latest.RejectionCategory = 5, "received", ""
	if _, err = st.ReduceReleaseSessionAcknowledgement(ctx, ref, latest); err != nil {
		t.Fatal(err)
	}
	latest.Sequence, latest.State = 6, "applied"
	got, err = st.ReduceReleaseSessionAcknowledgement(ctx, ref, latest)
	if err != nil || got.Effective.State != "applied" || got.Effective.LastAppliedVersion != r.Version || got.Effective.RejectionCategory != "" {
		t.Fatalf("retry recovery: %+v %v", got, err)
	}
}
