package core

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

type projectionTestStore struct {
	*storage.SQLStore
	rows   []domain.ReleaseAcknowledgement
	active map[domain.ReleaseTrack]domain.ActiveConfigurationRelease
	err    error
}

func (s *projectionTestStore) ReadReleaseProjection(context.Context, domain.ReleaseFilter) ([]domain.ReleaseAcknowledgement, map[domain.ReleaseTrack]domain.ActiveConfigurationRelease, error) {
	return s.rows, s.active, s.err
}

func TestReleaseProjectionCompletePaginationAndSnapshotParity(t *testing.T) {
	ctx := context.Background()
	_, sqlStore := newConsoleTestService(t)
	track := domain.ReleaseTrack{Namespace: domain.NamespaceRef{Env: "dev", App: "app"}, Name: "runtime", SchemaVersion: 1}
	store := &projectionTestStore{SQLStore: sqlStore, active: map[domain.ReleaseTrack]domain.ActiveConfigurationRelease{track: {Release: domain.ConfigurationRelease{Version: 4}, ActivationRevision: 153}}}
	for i := 0; i < 1005; i++ {
		store.rows = append(store.rows, domain.ReleaseAcknowledgement{Namespace: track.Namespace, ReleaseName: track.Name, SchemaVersion: 1, SessionID: fmt.Sprintf("s-%04d", i), InstanceID: fmt.Sprintf("i-%04d", i), Identity: "service", ClientName: "api", Connected: true, State: "applied", Sequence: 3, TargetRevision: 153, DesiredRevision: 153, ActivationRevision: 153, ReleaseVersion: 4, DesiredVersion: 4, LastAppliedVersion: 4, LastAppliedRevision: 153, LastAppliedSequence: 3})
	}
	svc := New(store, nil, "test")
	filter := domain.ReleaseFilter{Namespace: track.Namespace, Name: track.Name, SchemaVersion: &track.SchemaVersion}
	first, next, err := svc.ListReleaseSubscriberProjection(ctx, adminPrincipal(), filter, storage.ListPage{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Instances) != 1000 || next == "" || !first.Summary.Complete || first.Summary.AppliedCurrent != 1005 || first.Summary.Total != 1005 || len(first.Subscribers) != 0 {
		t.Fatalf("incomplete first page: %+v rows=%d", first.Summary, len(first.Instances))
	}
	second, end, err := svc.ListReleaseSubscriberProjection(ctx, adminPrincipal(), filter, storage.ListPage{Limit: 1000, Token: next})
	if err != nil || end != "" || len(second.Instances) != 5 || !reflect.DeepEqual(first.Summary, second.Summary) {
		t.Fatalf("second page=%+v err=%v", second.Summary, err)
	}
	snapshot, err := svc.GetReleaseRolloutSnapshot(ctx, adminPrincipal(), track)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ProjectionRevision != first.ProjectionRevision || !reflect.DeepEqual(snapshot.Instances, append(first.Instances, second.Instances...)) || !reflect.DeepEqual(snapshot.Summary, first.Summary) {
		t.Fatal("polling and streaming projections disagree")
	}
	// Storage enumeration order must not affect pagination or revision.
	rand.Shuffle(len(store.rows), func(i, j int) { store.rows[i], store.rows[j] = store.rows[j], store.rows[i] })
	shuffled, _, err := svc.ListReleaseSubscriberProjection(ctx, adminPrincipal(), filter, storage.ListPage{Limit: 1000})
	if err != nil || shuffled.ProjectionRevision != first.ProjectionRevision {
		t.Fatalf("order-dependent projection: %v", err)
	}
	store.rows[0].Connected = false
	if _, _, err := svc.ListReleaseSubscriberProjection(ctx, adminPrincipal(), filter, storage.ListPage{Token: next}); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("mixed-snapshot cursor accepted: %v", err)
	}
	store.err = errors.New("projection unavailable")
	if _, _, err := svc.ListReleaseSubscriberProjection(ctx, adminPrincipal(), filter, storage.ListPage{}); !errors.Is(err, store.err) {
		t.Fatalf("lookup failure hidden: %v", err)
	}
	if _, err := svc.GetReleaseRolloutSnapshot(ctx, adminPrincipal(), track); !errors.Is(err, store.err) {
		t.Fatalf("stream lookup failure hidden: %v", err)
	}
}

func TestReleaseProjectionPinAndHistoryClassification(t *testing.T) {
	base := domain.ReleaseAcknowledgement{SessionID: "session", ReleaseName: "runtime", Connected: true, DesiredRevision: 200, TargetRevision: 200, ReleaseVersion: 4, DesiredVersion: 4, ActivationRevision: 153, State: "applied", PinVersion: 4, LastAppliedVersion: 4, LastAppliedRevision: 200, LastAppliedSequence: 3}
	summary, state := computeRollout([]domain.ReleaseAcknowledgement{base}, "runtime", 153, time.Now(), 4)
	if state != domain.RolloutStateApplied || summary.Pinned != 1 || summary.DifferentPins != 0 {
		t.Fatalf("fleet matching pin: %+v %s", summary, state)
	}
	base.PinVersion, base.ReleaseVersion, base.DesiredVersion = 3, 3, 3
	summary, state = computeRollout([]domain.ReleaseAcknowledgement{base}, "runtime", 153, time.Now(), 4)
	if state != domain.RolloutStatePinned || summary.DifferentPins != 1 {
		t.Fatalf("different pin: %+v %s", summary, state)
	}
	base.State = "rejected"
	summary, state = computeRollout([]domain.ReleaseAcknowledgement{base}, "runtime", 153, time.Now(), 4)
	if state != domain.RolloutStateDegraded || summary.Rejected != 1 || summary.RejectedInstances[0].Classification != "rejected" || summary.RejectedInstances[0].LastAppliedVersion != 4 {
		t.Fatalf("rejected pin erased serving evidence: %+v %s", summary, state)
	}
	base.Connected = false
	base.ServerTimestamp = time.Now().Add(-time.Hour)
	summary, state = computeRollout([]domain.ReleaseAcknowledgement{base}, "runtime", 153, time.Now(), 4)
	if state != domain.RolloutStateNoSubscribers || summary.Stale != 1 || summary.Rejected != 0 || summary.Total != 1 {
		t.Fatalf("disconnected history affects health: %+v %s", summary, state)
	}
}

func TestReleaseTransportOverlapDeduplicatesOnlySameSession(t *testing.T) {
	old := domain.Subscriber{ReleaseName: "runtime", SchemaVersion: 1, Namespaces: []domain.NamespaceRef{{Env: "dev", App: "app"}}, SessionID: "session", Identity: "service", ClientName: "api", InstanceID: "one", ConnectedAt: time.Now().Add(-time.Minute)}
	current := old
	current.ConnectedAt = time.Now()
	current.RemoteAddr = "current"
	ordinary := domain.Subscriber{ClientName: "ordinary"}
	for _, rows := range [][]domain.Subscriber{{old, current, ordinary, ordinary}, {current, old, ordinary, ordinary}} {
		out := deduplicateReleaseTransports(rows)
		if len(out) != 3 || out[0].RemoteAddr != "current" {
			t.Fatalf("overlapping streams double-counted: %+v", out)
		}
	}
	other := old
	other.SessionID = "new-process"
	if got := deduplicateReleaseTransports([]domain.Subscriber{old, other}); len(got) != 2 {
		t.Fatal("different process sessions conflated")
	}
}
