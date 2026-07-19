package watch

import (
	"context"
	"sync"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// ReleaseRegistration is the immutable scope of one release stream.
type ReleaseRegistration struct {
	Namespace        domain.NamespaceRef
	Name             string
	ClientName       string
	InstanceID       string
	Identity         string
	LastSeenRevision uint64
}

type ReleaseEvent struct {
	Release   domain.ConfigurationRelease
	Namespace domain.NamespaceRef
	Name      string
	Version   uint64
	Revision  uint64
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
	id        uint64
	hub       *Hub
	reg       ReleaseRegistration
	events    chan ReleaseEvent
	done      chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	ready     bool
	closed    bool
	pending   *domain.ChangeLogEntry
	backlog   ReleaseBacklog
	lastSent  uint64
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
	return e.ResourceType == domain.ResourceConfigurationRelease && e.Ref.NS == s.reg.Namespace && e.Ref.Key == s.reg.Name
}

func (s *ReleaseSubscription) offer(e domain.ChangeLogEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || e.Revision <= s.lastSent {
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
	ev := ReleaseEvent{Namespace: e.Ref.NS, Name: e.Ref.Key, Version: e.Version, Revision: e.Revision}
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
	sub := &ReleaseSubscription{hub: h, reg: reg, events: make(chan ReleaseEvent, 1), done: make(chan struct{})}
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
				if e.ResourceType != domain.ResourceConfigurationRelease || e.Ref.NS != reg.Namespace || e.Ref.Key != reg.Name {
					continue
				}
				rel, err := rs.GetConfigurationRelease(ctx, reg.Namespace, reg.Name, e.Version)
				if err != nil {
					return ReleaseBacklog{}, err
				}
				events = append(events, ReleaseEvent{Release: rel, Namespace: reg.Namespace, Name: reg.Name, Version: e.Version, Revision: e.Revision})
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
				return releaseSnapshotBacklog(ctx, rs, reg)
			}
			return ReleaseBacklog{Events: events, Revision: current}, nil
		}
	}
	return releaseSnapshotBacklog(ctx, rs, reg)
}

func releaseSnapshotBacklog(ctx context.Context, rs storage.ReleaseStore, reg ReleaseRegistration) (ReleaseBacklog, error) {
	active, err := rs.GetActiveConfigurationRelease(ctx, reg.Namespace, reg.Name)
	if err != nil {
		return ReleaseBacklog{}, err
	}
	return ReleaseBacklog{IsSnapshot: true, Events: []ReleaseEvent{{Release: active.Release, Namespace: reg.Namespace, Name: reg.Name, Version: active.Release.Version, Revision: active.ActivationRevision}}, Revision: active.ActivationRevision}, nil
}
