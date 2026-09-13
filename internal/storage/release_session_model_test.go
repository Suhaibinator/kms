package storage

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

// acknowledgementReference deliberately recomputes its answer from the event
// set. It does not use the production incremental reducer, fingerprint, SQL
// model, or lifecycle ranking. Test streams stay below the retention bound.
type acknowledgementReference struct {
	events map[uint64]domain.ReleaseAcknowledgement
}

func (m *acknowledgementReference) deliver(a domain.ReleaseAcknowledgement) string {
	if old, exists := m.events[a.Sequence]; exists {
		if reflect.DeepEqual(old, a) {
			return "duplicate"
		}
		return "conflict"
	}
	m.events[a.Sequence] = a
	latest, _ := m.snapshot()
	if latest.Sequence == a.Sequence {
		return "accepted"
	}
	return "stale"
}

func (m *acknowledgementReference) snapshot() (latest, applied domain.ReleaseAcknowledgement) {
	ordered := make([]domain.ReleaseAcknowledgement, 0, len(m.events))
	for _, a := range m.events {
		ordered = append(ordered, a)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].TargetRevision != ordered[j].TargetRevision {
			return ordered[i].TargetRevision < ordered[j].TargetRevision
		}
		return ordered[i].Sequence < ordered[j].Sequence
	})
	for _, a := range ordered {
		latest = a
		if a.State == domain.ReleaseStateApplied {
			applied = a
		}
	}
	return latest, applied
}

func acknowledgementPermutations(n int) [][]int {
	var result [][]int
	var visit func([]int, []bool)
	visit = func(prefix []int, used []bool) {
		if len(prefix) == n {
			result = append(result, append([]int(nil), prefix...))
			return
		}
		for i := 0; i < n; i++ {
			if !used[i] {
				used[i] = true
				visit(append(prefix, i), used)
				used[i] = false
			}
		}
	}
	visit(nil, make([]bool, n))
	return result
}

func TestReleaseSessionReducerReferenceDeliveryPermutations(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "model")
	track := domain.ReleaseTrack{Namespace: nsRef("prod", "model"), Name: "runtime"}
	create := func() uint64 {
		r, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{Namespace: track.Namespace, Name: track.Name, Digest: "digest", Metadata: "{}"})
		if err != nil {
			t.Fatal(err)
		}
		return r.Version
	}
	lower, higher := create(), create()
	if _, _, err := st.ActivateConfigurationRelease(ctx, track, higher, nil); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		states []string
		// rollbackAt switches to a newer assigned target with a lower version.
		rollbackAt int
	}{
		{"normal_replayed_application", []string{"received", "prepared", "applied", "applied"}, -1},
		{"rejection_retry_replayed_application", []string{"rejected", "received", "prepared", "applied", "applied"}, -1},
		{"rollback_pending_preserves_applied", []string{"received", "applied", "received", "received"}, 2},
		{"rollback_rejection_preserves_applied", []string{"received", "applied", "rejected", "rejected"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for permutationID, order := range acknowledgementPermutations(len(tc.states)) {
				ref := domain.ReleaseSessionRef{Track: track, Identity: "client", ClientName: "api", InstanceID: "stable-instance", SessionID: fmt.Sprintf("%s-%d", tc.name, permutationID)}
				if err := st.RegisterReleaseSession(ctx, ref, false); err != nil {
					t.Fatal(err)
				}
				if err := st.ConnectReleaseSession(ctx, ref, "connection", true); err != nil {
					t.Fatal(err)
				}
				first, err := st.ResolveInstanceRelease(ctx, ref)
				if err != nil {
					t.Fatal(err)
				}
				second := first
				if tc.rollbackAt >= 0 {
					second, err = st.SetReleasePin(ctx, ref, lower, 0, domain.AuditEvent{})
					if err != nil {
						t.Fatal(err)
					}
				}
				events := make([]domain.ReleaseAcknowledgement, len(tc.states))
				for i, state := range tc.states {
					target := first
					if tc.rollbackAt >= 0 && i >= tc.rollbackAt {
						target = second
					}
					events[i] = domain.ReleaseAcknowledgement{Sequence: uint64(i + 1), TargetRevision: target.TargetRevision, ActivationRevision: target.ActivationRevision, ReleaseVersion: target.Release.Version, State: state, ConnectionID: "connection", ClientTimestamp: time.Unix(100-int64(i), 0).UTC()}
					if state == "rejected" {
						events[i].RejectionCategory = "validation"
						events[i].Diagnostic = "bounded diagnostic"
					}
					if state == "applied" {
						events[i].AppliedDivergent = true
						events[i].DivergentFieldCount = 2
					}
				}
				// The final element is an immutable replay, not a new event.
				events[len(events)-1] = events[len(events)-2]
				model := acknowledgementReference{events: make(map[uint64]domain.ReleaseAcknowledgement)}
				assertSnapshot := func(got domain.ReleaseAcknowledgement) {
					t.Helper()
					latest, applied := model.snapshot()
					if got.State != latest.State || got.Sequence != latest.Sequence || got.TargetRevision != latest.TargetRevision || got.ReleaseVersion != latest.ReleaseVersion || got.ActivationRevision != latest.ActivationRevision || got.RejectionCategory != latest.RejectionCategory || got.Diagnostic != latest.Diagnostic || got.AppliedDivergent != latest.AppliedDivergent || got.DivergentFieldCount != latest.DivergentFieldCount || !got.ClientTimestamp.Equal(latest.ClientTimestamp) || got.LastAppliedRevision != applied.TargetRevision || got.LastAppliedSequence != applied.Sequence || got.LastAppliedVersion != applied.ReleaseVersion {
						t.Fatalf("order %v: got %+v; model latest %+v applied %+v", order, got, latest, applied)
					}
				}
				for _, index := range order {
					event := events[index]
					want := model.deliver(event)
					result, err := st.ReduceReleaseSessionAcknowledgement(ctx, ref, event)
					if err != nil || result.Disposition != want {
						t.Fatalf("order %v event %d: disposition %s, want %s; %v", order, index, result.Disposition, want, err)
					}
					assertSnapshot(result.Effective)
				}
				// A retained sequence cannot be repurposed even when its target is old.
				conflict := events[0]
				conflict.Diagnostic = "changed immutable payload"
				if got := model.deliver(conflict); got != "conflict" {
					t.Fatal(got)
				}
				result, err := st.ReduceReleaseSessionAcknowledgement(ctx, ref, conflict)
				if err == nil || result.Disposition != "conflict" {
					t.Fatalf("conflicting replay accepted: %+v %v", result, err)
				}
				persisted, err := st.GetReleaseSessionAcknowledgement(ctx, ref)
				if err != nil {
					t.Fatal(err)
				}
				assertSnapshot(persisted)
			}
		})
	}
}
