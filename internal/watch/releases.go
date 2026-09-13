package watch

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// ReleaseRegistration is the immutable scope of one release stream.
type ReleaseRegistration struct {
	RemoteAddr       string
	Namespace        domain.NamespaceRef
	NamespaceID      int64
	Name             string
	SchemaVersion    uint64
	ClientName       string
	InstanceID       string
	SessionID        string
	Identity         string
	LastSeenRevision uint64
}

type ReleaseEvent struct {
	Release       domain.ConfigurationRelease
	Namespace     domain.NamespaceRef
	Name          string
	SchemaVersion uint64
	NamespaceID   int64
	Version       uint64
	Revision      uint64
}

type ReleaseBacklog struct {
	IsSnapshot bool
	Events     []ReleaseEvent
	Revision   uint64
}

// ReleaseSubscription is a release-only stream. Its live queue is a
// replace-latest slot: if the consumer is slow it may skip intermediate
// activations, but it is never permanently dropped and is eventually offered
// the latest active release.
type ReleaseSubscription struct {
	id              uint64
	hub             *Hub
	reg             ReleaseRegistration
	events          chan ReleaseEvent
	done            chan struct{}
	closeOnce       sync.Once
	mu              sync.Mutex
	ready           bool
	closed          bool
	pending         *domain.ChangeLogEntry
	backlog         ReleaseBacklog
	lastSent        uint64
	connectedAt     time.Time
	acknowledgement domain.ReleaseAcknowledgement
}

// RecordEffectiveAcknowledgement publishes the authoritative persisted snapshot.
// This registry deliberately has no acknowledgement ordering/reduction rules.
func (s *ReleaseSubscription) RecordEffectiveAcknowledgement(a domain.ReleaseAcknowledgement) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.Namespace == s.reg.Namespace && a.ReleaseName == s.reg.Name && a.SchemaVersion == s.reg.SchemaVersion && a.SessionID == s.reg.SessionID {
		s.acknowledgement = a
	}
}

func (s *ReleaseSubscription) describe() domain.Subscriber {
	s.mu.Lock()
	defer s.mu.Unlock()
	return domain.Subscriber{
		ClientName: s.reg.ClientName, InstanceID: s.reg.InstanceID, SessionID: s.reg.SessionID, Identity: s.reg.Identity,
		Namespaces: []domain.NamespaceRef{s.reg.Namespace}, RemoteAddr: s.reg.RemoteAddr, ConnectedAt: s.connectedAt,
		ReleaseName: s.reg.Name, SchemaVersion: s.reg.SchemaVersion, ReleaseState: s.acknowledgement.State,
		ReleaseVersion: s.acknowledgement.ReleaseVersion, ReleaseRevision: s.acknowledgement.ActivationRevision,
	}
}

func (s *ReleaseSubscription) Backlog() ReleaseBacklog {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.backlog
}
func (s *ReleaseSubscription) Events() <-chan ReleaseEvent { return s.events }
func (s *ReleaseSubscription) Done() <-chan struct{}       { return s.done }
func (s *ReleaseSubscription) Close() {
	s.closeOnce.Do(func() { s.mu.Lock(); s.closed = true; s.mu.Unlock(); close(s.done); s.hub.removeRelease(s.id) })
}
func (s *ReleaseSubscription) matches(e domain.ChangeLogEntry) bool {
	return e.ResourceType == domain.ResourceConfigurationRelease && e.Ref.NS == s.reg.Namespace && e.Ref.Key == s.reg.Name && e.SchemaVersion == s.reg.SchemaVersion &&
		s.reg.NamespaceID > 0 && e.NamespaceID == s.reg.NamespaceID
}

func (s *ReleaseSubscription) offer(e domain.ChangeLogEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.matches(e) || e.Revision <= s.lastSent {
		return
	}
	if !s.ready {
		copy := e
		s.pending = &copy
		return
	}
	s.deliverLocked(e)
}

func (s *ReleaseSubscription) deliverLocked(e domain.ChangeLogEntry) {
	ev := ReleaseEvent{Namespace: e.Ref.NS, Name: e.Ref.Key, Version: e.Version, Revision: e.Revision, SchemaVersion: e.SchemaVersion, NamespaceID: e.NamespaceID}
	select {
	case s.events <- ev:
	default:
		select {
		case <-s.events:
		default:
		}
		s.events <- ev
	}
	s.lastSent = e.Revision
}

func (s *ReleaseSubscription) activate(bl ReleaseBacklog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.backlog = bl
	s.lastSent = bl.Revision
	s.ready = true
	if s.pending != nil && s.pending.Revision > s.lastSent {
		s.deliverLocked(*s.pending)
	}
	s.pending = nil
}

func (h *Hub) SubscribeRelease(ctx context.Context, reg ReleaseRegistration) (*ReleaseSubscription, error) {
	rs, ok := h.store.(storage.ReleaseStore)
	if !ok {
		return nil, domain.Errorf(domain.ErrNotReady, "configuration release storage is unavailable")
	}
	if reg.NamespaceID <= 0 {
		return nil, domain.Errorf(domain.ErrInvalidArgument, "namespace incarnation is required for %s", reg.Namespace)
	}
	var err error
	ctx, err = storage.BindNamespaceIncarnation(ctx, reg.Namespace, reg.NamespaceID)
	if err != nil {
		return nil, err
	}
	current, err := h.store.GetNamespace(ctx, reg.Namespace)
	if err != nil {
		return nil, err
	}
	if current.ID != reg.NamespaceID {
		return nil, domain.Errorf(domain.ErrAborted, "namespace %s changed during subscribe; retry", reg.Namespace)
	}
	if reg.SchemaVersion != 0 {
		if _, err := rs.GetConfigurationSchema(ctx, reg.Namespace.App, reg.Name, reg.SchemaVersion); err != nil {
			return nil, err
		}
	}
	sub := &ReleaseSubscription{hub: h, reg: reg, connectedAt: h.now(), events: make(chan ReleaseEvent, 1), done: make(chan struct{})}
	h.mu.Lock()
	h.nextID++
	sub.id = h.nextID
	h.releaseSubs[sub.id] = sub
	h.mu.Unlock()
	bl, err := h.computeReleaseBacklog(ctx, rs, reg)
	if err != nil {
		sub.Close()
		return nil, err
	}
	sub.activate(bl)
	return sub, nil
}

func (h *Hub) computeReleaseBacklog(ctx context.Context, rs storage.ReleaseStore, reg ReleaseRegistration) (ReleaseBacklog, error) {
	current, err := h.store.CurrentRevision(ctx)
	if err != nil {
		return ReleaseBacklog{}, err
	}
	canReplay := h.canReplay(ctx, reg.LastSeenRevision, current)
	if canReplay {
		cursor := reg.LastSeenRevision
		events := []ReleaseEvent{}
		for cursor < current {
			batch, err := h.store.ListChangesSince(ctx, cursor, dispatchBatch)
			if err != nil {
				return ReleaseBacklog{}, err
			}
			if len(batch) == 0 {
				canReplay = false
				break
			}
			for _, e := range batch {
				if e.Revision > current {
					break
				}
				if e.Revision != cursor+1 {
					canReplay = false
					break
				}
				cursor = e.Revision
				if e.ResourceType != domain.ResourceConfigurationRelease || e.Ref.NS != reg.Namespace || e.Ref.Key != reg.Name || e.SchemaVersion != reg.SchemaVersion {
					continue
				}
				if e.NamespaceID == 0 {
					canReplay = false
					break
				}
				if e.NamespaceID != reg.NamespaceID {
					continue
				}
				rel, err := rs.GetConfigurationRelease(ctx, reg.Track(), e.Version)
				if errors.Is(err, domain.ErrNotFound) {
					canReplay = false
					break
				}
				if err != nil {
					return ReleaseBacklog{}, err
				}
				events = append(events, ReleaseEvent{Release: rel, Namespace: reg.Namespace, Name: reg.Name, Version: e.Version, Revision: e.Revision, SchemaVersion: e.SchemaVersion, NamespaceID: e.NamespaceID})
			}
			if !canReplay {
				break
			}
			if cursor >= current {
				break
			}
			if len(batch) < dispatchBatch {
				canReplay = false
				break
			}
		}
		if canReplay {
			// A release stream always establishes the authoritative active
			// candidate. Global revisions may have advanced only because of
			// unrelated resources; an empty filtered replay must not leave a
			// reconnecting subscriber waiting indefinitely for another activation.
			if len(events) == 0 {
				return releaseSnapshotBacklog(ctx, rs, reg, current)
			}
			return ReleaseBacklog{Events: events, Revision: current}, nil
		}
	}
	return releaseSnapshotBacklog(ctx, rs, reg, current)
}

func releaseSnapshotBacklog(ctx context.Context, rs storage.ReleaseStore, reg ReleaseRegistration, current uint64) (ReleaseBacklog, error) {
	active, err := rs.GetActiveConfigurationRelease(ctx, reg.Track())
	if errors.Is(err, domain.ErrNotFound) {
		return ReleaseBacklog{IsSnapshot: true, Revision: current}, nil
	}
	if err != nil {
		return ReleaseBacklog{}, err
	}
	// The snapshot event retains the activation identity needed for ACKs. The
	// stream cursor also includes unrelated changes already scanned. The active
	// read may observe an activation committed after CurrentRevision was read.
	return ReleaseBacklog{IsSnapshot: true, Events: []ReleaseEvent{{Release: active.Release, Namespace: reg.Namespace, Name: reg.Name, Version: active.Release.Version, Revision: active.ActivationRevision, SchemaVersion: reg.SchemaVersion, NamespaceID: reg.NamespaceID}}, Revision: max(current, active.ActivationRevision)}, nil
}

func (r ReleaseRegistration) Track() domain.ReleaseTrack {
	return domain.ReleaseTrack{Namespace: r.Namespace, Name: r.Name, SchemaVersion: r.SchemaVersion}
}
