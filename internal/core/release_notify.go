package core

import (
	"context"
	"sync"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
	"github.com/Suhaibinator/kms/internal/storage"
)

// maxRolloutSnapshotAcks bounds the acknowledgement rows one snapshot folds.
const maxRolloutSnapshotAcks = 1000

type releaseNotifyKey = domain.ReleaseTrack

// releaseSubscriberNotifier is an in-process fan-out of "the subscriber state
// of (namespace, release name) may have changed" wakeups. Each subscription is
// a coalescing channel: a burst of notifications collapses into one pending
// wakeup, so notifiers never block on slow consumers.
type releaseSubscriberNotifier struct {
	mu   sync.Mutex
	subs map[releaseNotifyKey]map[chan struct{}]struct{}
}

func newReleaseSubscriberNotifier() *releaseSubscriberNotifier {
	return &releaseSubscriberNotifier{subs: map[releaseNotifyKey]map[chan struct{}]struct{}{}}
}

// Subscribe returns a channel that receives at most one pending wakeup at a
// time and a cancel function that must be called when the consumer is done.
func (n *releaseSubscriberNotifier) Subscribe(track domain.ReleaseTrack) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	key := track
	n.mu.Lock()
	set := n.subs[key]
	if set == nil {
		set = map[chan struct{}]struct{}{}
		n.subs[key] = set
	}
	set[ch] = struct{}{}
	n.mu.Unlock()
	return ch, func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		if set, ok := n.subs[key]; ok {
			delete(set, ch)
			if len(set) == 0 {
				delete(n.subs, key)
			}
		}
	}
}

// Notify wakes every subscription of (ns, name) without blocking.
func (n *releaseSubscriberNotifier) Notify(track domain.ReleaseTrack) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for ch := range n.subs[track] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// SubscribeReleaseSubscribers wakes the returned channel whenever an
// acknowledgement, a connection change or an activation touches the release.
func (s *Service) SubscribeReleaseSubscribers(track domain.ReleaseTrack) (<-chan struct{}, func()) {
	return s.releaseNotify.Subscribe(track)
}

func (s *Service) notifyReleaseSubscribers(track domain.ReleaseTrack) {
	if s.releaseNotify != nil {
		s.releaseNotify.Notify(track)
	}
}

// GetReleaseRolloutSnapshot is one frame of the console's live rollout view:
// the folded rollout summary plus the raw subscriber rows for one release
// name. Admin-only, like ListReleaseSubscribers.
func (s *Service) GetReleaseRolloutSnapshot(ctx context.Context, pr Principal, track domain.ReleaseTrack) (domain.SubscriberStreamSnapshot, error) {
	ctx = withReleaseAuditTrack(ctx, track)
	ns, name := track.Namespace, track.Name
	if err := s.authorizeSubscriberInspection(ctx, pr, ns, name); err != nil {
		return domain.SubscriberStreamSnapshot{}, err
	}
	if err := validateReleaseAddress(ns, name); err != nil {
		return domain.SubscriberStreamSnapshot{}, err
	}
	if err := keyutil.ValidateNamespace(ns); err != nil {
		return domain.SubscriberStreamSnapshot{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	rs, err := s.releaseStore()
	if err != nil {
		return domain.SubscriberStreamSnapshot{}, err
	}
	acks, _, err := rs.ListReleaseAcknowledgements(ctx, domain.ReleaseFilter{Namespace: ns, Name: name, SchemaVersion: &track.SchemaVersion}, storage.ListPage{Limit: maxRolloutSnapshotAcks})
	if err != nil {
		return domain.SubscriberStreamSnapshot{}, err
	}
	var currentRevision uint64
	if active, err := rs.GetActiveConfigurationRelease(ctx, track); err == nil {
		currentRevision = active.ActivationRevision
	}
	now := s.now()
	summary, _ := computeRollout(acks, name, currentRevision, now)
	if acks == nil {
		acks = []domain.ReleaseAcknowledgement{}
	}
	return domain.SubscriberStreamSnapshot{Summary: summary, Subscribers: acks, CurrentRevision: currentRevision, ServerTime: now}, nil
}

// AuditReleaseStreamRejected records a live-stream request refused by the
// transport's concurrency caps so the deny is visible in the audit log.
func (s *Service) AuditReleaseStreamRejected(ctx context.Context, pr Principal, track domain.ReleaseTrack, reason string) {
	ctx = withReleaseAuditTrack(ctx, track)
	s.auditRef(ctx, pr, "configuration_release.subscribers_stream", domain.ResourceConfigurationRelease, domain.Ref{NS: track.Namespace, Key: track.Name}, 0, "deny", map[string]string{"reason": reason})
}
