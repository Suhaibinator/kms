package watch

import (
	"sync"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

// Registration is the immutable description of a watch subscription supplied by
// the transport layer. Namespaces are the (env, app) namespaces the stream
// watches; they are already validated and already authorized (core checks
// namespace-level access once, at subscribe time — see
// core.Service.AuthorizeSubscribe). The hub performs no per-event authorization:
// a subscriber receives every change in each of its namespaces.
type Registration struct {
	ClientName string
	InstanceID string
	Identity   string
	RemoteAddr string
	Namespaces []domain.NamespaceRef
	// NamespaceIDs binds each display name to the immutable row authorized by
	// core. Hub.Subscribe rejects missing/non-positive IDs, so all replay,
	// snapshot, and live delivery is fail-closed by construction.
	NamespaceIDs     map[domain.NamespaceRef]int64
	LastSeenRevision uint64
}

// Backlog is the initial state handed to a subscriber before live events flow.
// Exactly one of the two modes is populated, selected by IsSnapshot:
//   - snapshot: Snapshot holds the current parameter set; secrets never appear.
//   - replay:   Replay holds change-log entries with revision > LastSeenRevision.
//
// Revision is the consistent revision the backlog was taken at. Every event
// later delivered on Events() carries a revision strictly greater than it.
type Backlog struct {
	IsSnapshot bool
	Snapshot   []domain.Parameter
	Replay     []domain.ChangeLogEntry
	Revision   uint64
}

// Subscription is one live watch stream. It is created by Hub.Subscribe and
// consumed by the transport layer: read Backlog once, then range over Events()
// until Done() fires. Ack refreshes liveness; Close tears the subscription down
// (idempotent).
type Subscription struct {
	id  uint64
	hub *Hub
	reg Registration

	events chan domain.ChangeLogEntry
	done   chan struct{}

	bufferCap int

	closeOnce sync.Once

	mu sync.Mutex
	// ready flips true once the backlog is computed; before that, matching
	// entries are buffered in pending so none are missed and duplicates can be
	// suppressed against the backlog revision.
	ready   bool
	dropped bool
	pending []domain.ChangeLogEntry
	backlog Backlog

	// lastSent is the highest revision handed to the subscriber (via backlog or
	// Events). Dispatch suppresses anything at or below it.
	lastSent uint64

	connectedAt   time.Time
	lastHeartbeat time.Time
	lastAcked     uint64
}

// Events is the live event channel. Entries arrive in revision order, each
// strictly greater than Backlog().Revision. The channel is never closed; use
// Done to detect teardown.
func (s *Subscription) Events() <-chan domain.ChangeLogEntry { return s.events }

// Done is closed when the subscription is torn down (client disconnect, slow
// consumer, or liveness expiry).
func (s *Subscription) Done() <-chan struct{} { return s.done }

// Backlog returns the initial state computed at subscribe time.
func (s *Subscription) Backlog() Backlog {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.backlog
}

// Ack records the client's applied revision and refreshes the liveness clock.
func (s *Subscription) Ack(revision uint64) {
	s.mu.Lock()
	if revision > s.lastAcked {
		s.lastAcked = revision
	}
	s.lastHeartbeat = s.hub.now()
	s.mu.Unlock()
}

// Close tears the subscription down and removes it from the registry. Safe to
// call multiple times and from multiple goroutines.
func (s *Subscription) Close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.dropped = true
		s.mu.Unlock()
		close(s.done)
		s.hub.remove(s.id)
	})
}

// offer is called by the dispatch loop for every entry in one of this
// subscriber's namespaces. It returns false when the subscriber must be dropped
// (buffer overflow or already closed); the caller then closes it. It never
// blocks.
func (s *Subscription) offer(e domain.ChangeLogEntry) (alive bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dropped {
		return false
	}
	if !s.ready {
		// Backlog not computed yet: buffer so nothing is missed. An overflow
		// here means the subscriber fell behind before it even started.
		if len(s.pending) >= s.bufferCap {
			return false
		}
		s.pending = append(s.pending, e)
		return true
	}
	if e.Revision <= s.lastSent {
		return true // already covered by backlog or a prior send
	}
	select {
	case s.events <- e:
		s.lastSent = e.Revision
		return true
	default:
		return false // buffer full: slow subscriber
	}
}

// activate installs the computed backlog, flushes any entries buffered while it
// was computed (suppressing those already covered), and switches to live
// delivery. It returns false if the flush overflows the buffer.
func (s *Subscription) activate(bl Backlog) (alive bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dropped {
		return false
	}
	s.backlog = bl
	if bl.Revision > s.lastSent {
		s.lastSent = bl.Revision
	}
	pending := s.pending
	s.pending = nil
	s.ready = true
	for _, e := range pending {
		if e.Revision <= s.lastSent {
			continue
		}
		select {
		case s.events <- e:
			s.lastSent = e.Revision
		default:
			return false
		}
	}
	return true
}

// describe snapshots the subscriber's registry record.
func (s *Subscription) describe() domain.Subscriber {
	s.mu.Lock()
	defer s.mu.Unlock()
	namespaces := make([]domain.NamespaceRef, len(s.reg.Namespaces))
	copy(namespaces, s.reg.Namespaces)
	return domain.Subscriber{
		ClientName:        s.reg.ClientName,
		InstanceID:        s.reg.InstanceID,
		Identity:          s.reg.Identity,
		Namespaces:        namespaces,
		RemoteAddr:        s.reg.RemoteAddr,
		ConnectedAt:       s.connectedAt,
		LastHeartbeat:     s.lastHeartbeat,
		LastAckedRevision: s.lastAcked,
	}
}

// lastHeartbeatTime returns the liveness clock value.
func (s *Subscription) lastHeartbeatTime() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastHeartbeat
}

// matches reports whether the entry's namespace is one this subscriber watches.
func (s *Subscription) matches(entry domain.ChangeLogEntry) bool {
	if !namespaceMatchAny(s.reg.Namespaces, entry.Ref.NS) {
		return false
	}
	expectedID, bound := s.reg.NamespaceIDs[entry.Ref.NS]
	return bound && expectedID > 0 && entry.NamespaceID == expectedID
}

// namespaceMatchAny reports whether ns is in the subscribed set.
func namespaceMatchAny(namespaces []domain.NamespaceRef, ns domain.NamespaceRef) bool {
	for _, n := range namespaces {
		if n == ns {
			return true
		}
	}
	return false
}
