// Package watch implements the change-fanout hub behind the WatchService. It
// tails the storage change log, delivers entries to matching, authorized
// subscribers in revision order, and maintains the live subscriber registry
// used by the admin API.
//
// The hub is trusted in-process infrastructure: it is the one transport-side
// component wired directly to storage.Store, because computing backlogs and
// tailing the change log require reads the service layer does not expose.
package watch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// Options tunes the hub. The zero value is not valid; use defaults via
// NewHub, which fills any unset field.
type Options struct {
	// HeartbeatInterval is the cadence the transport heartbeats subscribers and
	// the base unit for liveness expiry.
	HeartbeatInterval time.Duration
	// MissedHeartbeats is how many heartbeat intervals a subscriber may go
	// without acking before it is dropped.
	MissedHeartbeats int
	// SnapshotMaxReplay caps how many change-log entries a reconnecting
	// subscriber may replay before the hub falls back to a full snapshot.
	SnapshotMaxReplay uint64
	// PruneInterval is how often the change log is pruned.
	PruneInterval time.Duration
	// RetainDuration and RetainRows bound change-log retention at prune time.
	RetainDuration time.Duration
	RetainRows     int
	// ReleaseRetainDuration and ReleaseRetainVersions retain inactive immutable
	// releases. Active/previous and releases required by retained replay events
	// are independently protected by storage.
	ReleaseRetainDuration time.Duration
	ReleaseRetainVersions int
	// ReleaseSubscriberRetainDuration bounds disconnected lifecycle rows.
	ReleaseSubscriberRetainDuration time.Duration
	// SubscriberBuffer is the per-subscriber event channel depth. A subscriber
	// that lets it fill is dropped and expected to reconnect by revision.
	SubscriberBuffer int
	// DrainRetryInterval is how soon a transient change-log read failure is
	// retried without waiting for another write to wake the hub.
	DrainRetryInterval time.Duration

	// now overrides the clock (tests). nil uses time.Now.
	now func() time.Time
}

// Defaults.
const (
	defaultHeartbeatInterval               = 30 * time.Second
	defaultMissedHeartbeats                = 3
	defaultSnapshotMaxReplay               = 4096
	defaultPruneInterval                   = 5 * time.Minute
	defaultRetainDuration                  = 24 * time.Hour
	defaultRetainRows                      = 100000
	defaultReleaseRetainDuration           = 90 * 24 * time.Hour
	defaultReleaseRetainVersions           = 100
	defaultReleaseSubscriberRetainDuration = 30 * 24 * time.Hour
	defaultSubscriberBuffer                = 256
	defaultDrainRetryInterval              = time.Second
)

// dispatchBatch is how many change-log entries the dispatch loop reads per
// storage call while draining after a wake.
//
// It MUST stay at or below the storage layer's maximum page size (see
// storage.clampLimit, currently 1000): drain and replayEntries detect "log
// exhausted" via len(batch) < dispatchBatch, so a value above the store cap
// would make a full page look short and silently stop reading the tail. The
// static check below fails to compile if this invariant is ever broken.
const dispatchBatch = 512

// Compile-time guard for the dispatchBatch <= 1000 invariant documented above.
const _ = uint(1000 - dispatchBatch)

func (o Options) withDefaults() Options {
	if o.HeartbeatInterval <= 0 {
		o.HeartbeatInterval = defaultHeartbeatInterval
	}
	if o.MissedHeartbeats <= 0 {
		o.MissedHeartbeats = defaultMissedHeartbeats
	}
	if o.SnapshotMaxReplay == 0 {
		o.SnapshotMaxReplay = defaultSnapshotMaxReplay
	}
	if o.PruneInterval <= 0 {
		o.PruneInterval = defaultPruneInterval
	}
	if o.RetainDuration <= 0 {
		o.RetainDuration = defaultRetainDuration
	}
	if o.RetainRows <= 0 {
		o.RetainRows = defaultRetainRows
	}
	if o.ReleaseRetainDuration <= 0 {
		o.ReleaseRetainDuration = defaultReleaseRetainDuration
	}
	if o.ReleaseRetainVersions <= 0 {
		o.ReleaseRetainVersions = defaultReleaseRetainVersions
	}
	if o.ReleaseSubscriberRetainDuration <= 0 {
		o.ReleaseSubscriberRetainDuration = defaultReleaseSubscriberRetainDuration
	}
	if o.SubscriberBuffer <= 0 {
		o.SubscriberBuffer = defaultSubscriberBuffer
	}
	if o.DrainRetryInterval <= 0 {
		o.DrainRetryInterval = defaultDrainRetryInterval
	}
	if o.now == nil {
		o.now = func() time.Time { return time.Now().UTC() }
	}
	return o
}

// Hub fans change-log entries out to subscribers and owns the subscriber
// registry. It satisfies core.Hub. Construct with NewHub, then run the
// dispatch loop with Run.
type Hub struct {
	store storage.Store
	log   *zap.Logger
	opts  Options
	now   func() time.Time

	// wake is a 1-buffered coalescing signal that new entries may exist.
	wake chan struct{}

	// startedCh is closed once Run has captured its initial cursor and is about
	// to enter the dispatch loop. It lets callers (and tests) establish a
	// happens-before edge with the initial revision read.
	startedCh chan struct{}

	// cursor is the dispatch position, published for Stats. It is written only
	// by Run and drain (one goroutine) and read by any caller of Stats, so it
	// is atomic rather than guarded by mu: taking mu here would put the metrics
	// path on the same lock as fan-out.
	cursor atomic.Uint64
	// droppedStale and droppedSlow count subscribers the hub tore down, split
	// by which of the two failures caused it. They are never reset.
	droppedStale atomic.Uint64
	droppedSlow  atomic.Uint64

	mu          sync.Mutex
	subs        map[uint64]*Subscription
	releaseSubs map[uint64]*ReleaseSubscription
	nextID      uint64
}

// Stats is the hub's fan-out state: how many streams are connected, how far
// dispatch has got, how far the worst subscriber trails it, and how many
// subscribers have been torn down for each of the two reasons. It is a
// point-in-time copy of plain numbers — nothing that identifies a client,
// namespace, or identity — so it can be published as metrics unchanged.
type Stats struct {
	Subscribers        int
	ReleaseSubscribers int
	// LastDispatchedRevision is the dispatch cursor: the change-log revision
	// the hub has caught up to. At startup it is the revision the log was
	// already at, since earlier entries are history served as backlog.
	LastDispatchedRevision uint64
	// MaxLagRevisions is how far the furthest-behind subscriber trails the
	// cursor, measured from its last acked revision. It is 0 with no
	// subscribers, and 0 rather than negative if a subscriber has acked a
	// backlog revision the dispatch loop has not reached yet.
	MaxLagRevisions uint64
	// DroppedStale counts subscribers dropped for missing heartbeats.
	// DroppedSlow counts subscribers dropped for letting their buffer overflow,
	// whether during live dispatch or while their backlog was being computed.
	// Neither counts a client that disconnected on its own.
	DroppedStale uint64
	DroppedSlow  uint64
}

// Stats snapshots the hub for the metrics exporter. It follows the hub's
// locking discipline: registry sizes are read under mu, and the per-subscriber
// acks are read afterwards from a copied slice, so a subscription's own lock is
// never taken while the hub lock is held.
func (h *Hub) Stats() Stats {
	stats := Stats{
		LastDispatchedRevision: h.cursor.Load(),
		DroppedStale:           h.droppedStale.Load(),
		DroppedSlow:            h.droppedSlow.Load(),
	}

	h.mu.Lock()
	stats.Subscribers = len(h.subs)
	stats.ReleaseSubscribers = len(h.releaseSubs)
	subs := make([]*Subscription, 0, len(h.subs))
	for _, s := range h.subs {
		subs = append(subs, s)
	}
	h.mu.Unlock()

	if len(subs) == 0 {
		return stats
	}
	slowest := subs[0].lastAckedRevision()
	for _, s := range subs[1:] {
		if acked := s.lastAckedRevision(); acked < slowest {
			slowest = acked
		}
	}
	if stats.LastDispatchedRevision > slowest {
		stats.MaxLagRevisions = stats.LastDispatchedRevision - slowest
	}
	return stats
}

// NewHub constructs a hub over store. logger may be nil.
func NewHub(store storage.Store, logger *zap.Logger, opts Options) *Hub {
	if logger == nil {
		logger = zap.NewNop()
	}
	opts = opts.withDefaults()
	return &Hub{
		store:       store,
		log:         logger,
		opts:        opts,
		now:         opts.now,
		wake:        make(chan struct{}, 1),
		startedCh:   make(chan struct{}),
		subs:        make(map[uint64]*Subscription),
		releaseSubs: make(map[uint64]*ReleaseSubscription),
	}
}

// Started returns a channel closed once the dispatch loop has captured its
// initial revision cursor and begun running.
func (h *Hub) Started() <-chan struct{} { return h.startedCh }

// HeartbeatInterval exposes the configured heartbeat cadence so the transport
// layer can align its heartbeat ticker with the hub's liveness window.
func (h *Hub) HeartbeatInterval() time.Duration { return h.opts.HeartbeatInterval }

// Wake signals the dispatch loop that new change-log entries may exist. It is
// non-blocking and coalescing: concurrent wakes collapse into one pass.
func (h *Hub) Wake() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// Subscribers returns a snapshot copy of the live registry.
func (h *Hub) Subscribers() []domain.Subscriber {
	h.mu.Lock()
	subs := make([]*Subscription, 0, len(h.subs))
	for _, s := range h.subs {
		subs = append(subs, s)
	}
	h.mu.Unlock()
	out := make([]domain.Subscriber, 0, len(subs))
	for _, s := range subs {
		out = append(out, s.describe())
	}
	return out
}

// Run drives dispatch, liveness, and pruning until ctx is cancelled. It reads
// the starting revision from the store; any entries already present are treated
// as history (subscribers receive them via backlog, not the live stream).
func (h *Hub) Run(ctx context.Context) error {
	var cursor uint64
	for {
		var err error
		cursor, err = h.store.CurrentRevision(ctx)
		if err == nil {
			h.cursor.Store(cursor)
			break
		}
		h.log.Error("watch hub: reading current revision at start", zap.Error(err))
		timer := time.NewTimer(h.opts.DrainRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}

	liveness := time.NewTicker(h.opts.HeartbeatInterval)
	defer liveness.Stop()
	prune := time.NewTicker(h.opts.PruneInterval)
	defer prune.Stop()

	close(h.startedCh)

	var retryTimer *time.Timer
	var retryCh <-chan time.Time
	scheduleRetry := func() {
		if retryTimer == nil {
			retryTimer = time.NewTimer(h.opts.DrainRetryInterval)
			retryCh = retryTimer.C
		}
	}
	stopRetry := func() {
		if retryTimer == nil {
			return
		}
		if !retryTimer.Stop() {
			select {
			case <-retryTimer.C:
			default:
			}
		}
		retryTimer = nil
		retryCh = nil
	}
	defer stopRetry()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-h.wake:
			var drainErr error
			cursor, drainErr = h.drain(ctx, cursor)
			if drainErr != nil {
				h.log.Error("watch hub: reading change log", zap.Uint64("since", cursor), zap.Error(drainErr))
				scheduleRetry()
			} else {
				stopRetry()
			}
		case <-retryCh:
			retryTimer = nil
			retryCh = nil
			var drainErr error
			cursor, drainErr = h.drain(ctx, cursor)
			if drainErr != nil {
				h.log.Error("watch hub: retrying change log read", zap.Uint64("since", cursor), zap.Error(drainErr))
				scheduleRetry()
			}
		case <-liveness.C:
			h.dropExpired()
		case <-prune.C:
			h.pruneChangeLog(ctx)
		}
	}
}

// drain reads and dispatches change-log entries after cursor until the log is
// exhausted, returning the advanced cursor. On a transient error, any entries
// already dispatched remain reflected in the returned cursor so retry does not
// duplicate them.
func (h *Hub) drain(ctx context.Context, cursor uint64) (uint64, error) {
	for {
		entries, err := h.store.ListChangesSince(ctx, cursor, dispatchBatch)
		if err != nil {
			return cursor, err
		}
		if len(entries) == 0 {
			return cursor, nil
		}
		for _, e := range entries {
			h.dispatch(e)
			cursor = e.Revision
			h.cursor.Store(cursor)
		}
		if len(entries) < dispatchBatch {
			return cursor, nil
		}
	}
}

// dispatch delivers one entry to every live subscriber of the entry's
// namespace. Authorization is namespace-level and already enforced at subscribe
// time, so there is no per-event access check here. Slow subscribers are dropped.
func (h *Hub) dispatch(e domain.ChangeLogEntry) {
	h.mu.Lock()
	if e.ResourceType == domain.ResourceConfigurationRelease {
		subs := make([]*ReleaseSubscription, 0, len(h.releaseSubs))
		for _, s := range h.releaseSubs {
			subs = append(subs, s)
		}
		h.mu.Unlock()
		for _, s := range subs {
			if s.matches(e) {
				s.offer(e)
			}
		}
		return
	}
	subs := make([]*Subscription, 0, len(h.subs))
	for _, s := range h.subs {
		subs = append(subs, s)
	}
	h.mu.Unlock()

	for _, s := range subs {
		if !s.matches(e) {
			continue
		}
		if s.offer(e) {
			continue
		}
		// offer also reports false for a subscription that is already closed
		// (the client hung up between the registry copy and here), so only a
		// teardown this call actually performed counts as a slow subscriber.
		if s.close() {
			h.droppedSlow.Add(1)
		}
	}
}

// dropExpired removes subscribers that have not acked within the liveness
// window (HeartbeatInterval * MissedHeartbeats).
func (h *Hub) dropExpired() {
	deadline := h.now().Add(-h.opts.HeartbeatInterval * time.Duration(h.opts.MissedHeartbeats))
	h.mu.Lock()
	stale := make([]*Subscription, 0)
	for _, s := range h.subs {
		if s.lastHeartbeatTime().Before(deadline) {
			stale = append(stale, s)
		}
	}
	h.mu.Unlock()
	for _, s := range stale {
		h.log.Info("watch hub: dropping stale subscriber",
			zap.String("client", s.reg.ClientName), zap.String("instance", s.reg.InstanceID))
		if s.close() {
			h.droppedStale.Add(1)
		}
	}
}

// pruneChangeLog trims the change log per the retention policy.
func (h *Hub) pruneChangeLog(ctx context.Context) {
	n, err := h.store.PruneChangeLog(ctx, h.opts.RetainDuration, h.opts.RetainRows)
	if err != nil {
		h.log.Error("watch hub: pruning change log", zap.Error(err))
		return
	}
	if n > 0 {
		h.log.Info("watch hub: pruned change log", zap.Int("removed", n))
	}
	if rs, ok := h.store.(storage.ReleaseStore); ok {
		if n, err := rs.PruneConfigurationReleases(ctx, h.opts.ReleaseRetainDuration, h.opts.ReleaseRetainVersions); err != nil {
			h.log.Error("watch hub: pruning configuration releases", zap.Error(err))
		} else if n > 0 {
			h.log.Info("watch hub: pruned configuration releases", zap.Int("removed", n))
		}
		if n, err := rs.PruneReleaseAcknowledgements(ctx, h.now().Add(-h.opts.ReleaseSubscriberRetainDuration)); err != nil {
			h.log.Error("watch hub: pruning release subscriber states", zap.Error(err))
		} else if n > 0 {
			h.log.Info("watch hub: pruned release subscriber states", zap.Int("removed", n))
		}
	}
}

// remove deletes a subscription from the registry (idempotent).
func (h *Hub) remove(id uint64) {
	h.mu.Lock()
	delete(h.subs, id)
	h.mu.Unlock()
}

func (h *Hub) removeRelease(id uint64) {
	h.mu.Lock()
	delete(h.releaseSubs, id)
	h.mu.Unlock()
}

// Subscribe registers a new subscriber and computes its backlog. Registration
// happens before the backlog is read so no committed entry is missed; entries
// that race in during backlog computation are buffered and de-duplicated
// against the backlog revision.
func (h *Hub) Subscribe(ctx context.Context, reg Registration) (*Subscription, error) {
	// Registration is retained for the stream lifetime. Own its reference-typed
	// fields so a caller cannot mutate the authorized names/IDs after validation.
	reg.Namespaces = append([]domain.NamespaceRef(nil), reg.Namespaces...)
	ids := make(map[domain.NamespaceRef]int64, len(reg.NamespaceIDs))
	maps.Copy(ids, reg.NamespaceIDs)
	reg.NamespaceIDs = ids
	if len(reg.Namespaces) == 0 {
		return nil, domain.Errorf(domain.ErrInvalidArgument, "at least one namespace incarnation is required")
	}
	for _, ns := range reg.Namespaces {
		id, ok := reg.NamespaceIDs[ns]
		if !ok || id <= 0 {
			return nil, domain.Errorf(domain.ErrInvalidArgument, "namespace incarnation is required for %s", ns)
		}
		var err error
		ctx, err = storage.BindNamespaceIncarnation(ctx, ns, id)
		if err != nil {
			return nil, err
		}
		current, err := h.store.GetNamespace(ctx, ns)
		if err != nil {
			return nil, err
		}
		if current.ID != id {
			return nil, domain.Errorf(domain.ErrAborted, "namespace %s changed during subscribe; retry", ns)
		}
	}
	now := h.now()
	sub := &Subscription{
		hub:           h,
		reg:           reg,
		events:        make(chan domain.ChangeLogEntry, h.opts.SubscriberBuffer),
		done:          make(chan struct{}),
		bufferCap:     h.opts.SubscriberBuffer,
		connectedAt:   now,
		lastHeartbeat: now,
	}

	// Register first so the dispatch loop cannot skip entries committed while
	// the backlog is being computed.
	h.mu.Lock()
	h.nextID++
	sub.id = h.nextID
	h.subs[sub.id] = sub
	h.mu.Unlock()

	bl, err := h.computeBacklog(ctx, reg)
	if err != nil {
		sub.Close()
		return nil, err
	}
	if !sub.activate(bl) {
		// Overflowed while catching up during backlog computation. The client
		// should reconnect and resume by revision. It is the same failure the
		// dispatch loop counts — a buffer that filled faster than it drained —
		// so it belongs in the same counter.
		if sub.close() {
			h.droppedSlow.Add(1)
		}
		return nil, domain.Errorf(domain.ErrFailedPrecondition, "subscriber fell behind during subscribe")
	}
	return sub, nil
}

// computeBacklog decides between replaying change-log entries and sending a
// full snapshot, per the retention/replay policy, and returns the chosen
// backlog scoped to the subscriber's namespaces. Namespace-level authorization
// is enforced at subscribe time, so no per-item filtering happens here.
func (h *Hub) computeBacklog(ctx context.Context, reg Registration) (Backlog, error) {
	current, err := h.store.CurrentRevision(ctx)
	if err != nil {
		return Backlog{}, err
	}

	if h.canReplay(ctx, reg.LastSeenRevision, current) {
		replay, complete, err := h.replayEntries(ctx, reg, current)
		if err != nil {
			return Backlog{}, err
		}
		if complete {
			return Backlog{IsSnapshot: false, Replay: replay, Revision: current}, nil
		}
		// A prune committed between canReplay and the replay read, creating a
		// gap at the tail we were about to replay. Fall through to a snapshot
		// rather than silently skipping the pruned changes.
	}

	params, snapRev, err := h.store.SnapshotParameters(ctx, reg.Namespaces)
	if err != nil {
		return Backlog{}, err
	}
	return Backlog{IsSnapshot: true, Snapshot: params, Revision: snapRev}, nil
}

// canReplay reports whether the reconnecting subscriber's last-seen revision is
// recent enough to replay from the change log without a gap.
func (h *Hub) canReplay(ctx context.Context, lastSeen, current uint64) bool {
	if lastSeen == 0 || lastSeen > current {
		return false
	}
	if current-lastSeen > h.opts.SnapshotMaxReplay {
		return false
	}
	oldest, err := h.store.OldestRetainedRevision(ctx)
	if err != nil {
		h.log.Error("watch hub: reading oldest retained revision", zap.Error(err))
		return false
	}
	// The log must contain an unbroken tail starting at or before lastSeen+1.
	return oldest != 0 && oldest <= lastSeen+1
}

// replayEntries reads change-log entries in (lastSeen, current], filtered to
// the subscriber's namespaces. complete is false when a gap is detected at the
// tail (a prune raced this subscribe), signaling the caller to fall back to a
// snapshot rather than replaying a discontinuous log.
func (h *Hub) replayEntries(ctx context.Context, reg Registration, current uint64) (entries []domain.ChangeLogEntry, complete bool, err error) {
	var out []domain.ChangeLogEntry
	cursor := reg.LastSeenRevision
	for cursor < current {
		batch, err := h.store.ListChangesSince(ctx, cursor, dispatchBatch)
		if err != nil {
			return nil, false, err
		}
		if len(batch) == 0 {
			// cursor is still behind the captured current revision, so an empty
			// tail means pruning raced the paginated read.
			return nil, false, nil
		}
		for _, e := range batch {
			if e.Revision > current {
				// Committed after our consistent point; leave it for the live
				// stream so it is not delivered twice.
				break
			}
			// AUTOINCREMENT revisions are contiguous until pruning. Check every
			// page boundary, not just the first result, because a prune may race
			// after one or more full pages have already been read.
			if e.Revision != cursor+1 {
				return nil, false, nil
			}
			cursor = e.Revision
			if e.ResourceType == domain.ResourceConfigurationRelease {
				continue
			}
			if !namespaceMatchAny(reg.Namespaces, e.Ref.NS) {
				continue
			}
			expectedID, bound := reg.NamespaceIDs[e.Ref.NS]
			if !bound || expectedID <= 0 {
				return nil, false, domain.Errorf(domain.ErrAborted, "namespace incarnation binding was lost for %s", e.Ref.NS)
			}
			// Legacy rows predate immutable namespace IDs. A replay containing
			// one cannot be safely attributed, so force a current snapshot.
			if e.NamespaceID == 0 {
				return nil, false, nil
			}
			if e.NamespaceID != expectedID {
				continue
			}
			out = append(out, e)
		}
		if cursor >= current {
			return out, true, nil
		}
		if len(batch) < dispatchBatch {
			return nil, false, nil
		}
	}
	return out, true, nil
}

// randomHex returns n bytes of hex-encoded randomness for instance IDs. It is
// exported through InstanceSuffix for the transport layer.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// rand.Read never fails on supported platforms; fall back to time.
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))[:n*2]
	}
	return hex.EncodeToString(b)
}

// InstanceSuffix returns a short random hex suffix for building instance IDs.
func InstanceSuffix() string { return randomHex(4) }
