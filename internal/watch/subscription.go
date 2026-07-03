package watch

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
)

// allowFunc is the per-subscriber authorization predicate. It is held behind an
// atomic pointer so the transport layer can swap in a freshly re-authorized
// predicate (on heartbeat) without racing the dispatch loop that reads it.
type allowFunc func(resourceType string, ref domain.Ref) bool

func denyAll(string, domain.Ref) bool { return false }

// Registration is the immutable description of a watch subscription supplied by
// the transport layer. Selectors are already validated (namespace + key
// pattern) and Allowed is the per-subscriber authorization predicate built from
// the caller's policies (see core.Service.WatchAccessChecker).
type Registration struct {
	ClientName       string
	InstanceID       string
	Identity         string
	RemoteAddr       string
	Selectors        []domain.WatchSelector
	LastSeenRevision uint64
	// Allowed reports whether the subscriber may observe a change to
	// (resourceType, ref). A nil predicate denies everything.
	Allowed func(resourceType string, ref domain.Ref) bool
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

	// allowed is the live authorization predicate. It starts from
	// reg.Allowed and may be replaced via UpdateAllowed as the stream is
	// periodically re-authorized.
	allowed atomic.Pointer[allowFunc]

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

// offer is called by the dispatch loop for every entry that matches this
// subscriber's patterns and authorization. It returns false when the
// subscriber must be dropped (buffer overflow or already closed); the caller
// then closes it. It never blocks.
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
	selectors := make([]domain.WatchSelector, len(s.reg.Selectors))
	copy(selectors, s.reg.Selectors)
	return domain.Subscriber{
		ClientName:        s.reg.ClientName,
		InstanceID:        s.reg.InstanceID,
		Identity:          s.reg.Identity,
		Selectors:         selectors,
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

// allow evaluates the current authorization predicate for (resourceType, ref).
// A subscription with no predicate installed denies everything (fail closed).
func (s *Subscription) allow(resourceType string, ref domain.Ref) bool {
	fp := s.allowed.Load()
	if fp == nil {
		return false
	}
	return (*fp)(resourceType, ref)
}

// UpdateAllowed atomically swaps the authorization predicate. A nil predicate
// is treated as deny-all. It is safe to call concurrently with dispatch.
func (s *Subscription) UpdateAllowed(fn func(resourceType string, ref domain.Ref) bool) {
	f := allowFunc(denyAll)
	if fn != nil {
		f = fn
	}
	s.allowed.Store(&f)
}

// matches reports whether any registered selector covers the entry's ref.
func (s *Subscription) matches(ref domain.Ref) bool {
	return selectorMatchAny(s.reg.Selectors, ref)
}

// selectorMatch reports whether one selector covers ref: the namespaces must be
// identical and the ref's key must match the selector's key pattern (an empty
// or "*" pattern matches every key in the namespace).
func selectorMatch(sel domain.WatchSelector, ref domain.Ref) bool {
	return sel.NS == ref.NS && keyutil.MatchKey(sel.KeyPattern, ref.Key)
}

// selectorMatchAny reports whether any selector covers ref.
func selectorMatchAny(selectors []domain.WatchSelector, ref domain.Ref) bool {
	for _, sel := range selectors {
		if selectorMatch(sel, ref) {
			return true
		}
	}
	return false
}
