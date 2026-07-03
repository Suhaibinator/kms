package paramstore

import (
	"context"
	"errors"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

// EventType classifies a watch Event.
type EventType int

const (
	// EventPut is a parameter create/update or label move; Value holds the new
	// value.
	EventPut EventType = iota
	// EventDelete is a parameter deletion.
	EventDelete
	// EventSecretChange is a secret metadata change (no plaintext); the app
	// re-fetches via GetSecret if it cares.
	EventSecretChange
)

func (t EventType) String() string {
	switch t {
	case EventPut:
		return "put"
	case EventDelete:
		return "delete"
	case EventSecretChange:
		return "secret_change"
	default:
		return "unknown"
	}
}

// Event is delivered to Watch callbacks when a watched key changes.
type Event struct {
	Type EventType
	// Namespace is the "env/app" namespace of the changed resource.
	Namespace string
	// Key is the resource key, relative to Namespace.
	Key        string
	Value      string // populated for EventPut
	Version    uint64
	Revision   uint64
	ChangeType string // raw server change_type, useful for secret changes
}

// Path returns the "/env/app/key" display path of the event's resource.
func (e Event) Path() string { return "/" + e.Namespace + "/" + e.Key }

// defaultReconcileInterval is the safety-net full-sync poll cadence (plan 8.4).
const defaultReconcileInterval = 5 * time.Minute

type knownVal struct {
	value   string
	present bool
	// rev is the stream revision (or reconcile snapshot revision) the value was
	// last applied at. It fences stale writes: a reconcile read that raced a
	// newer live event must not regress the path (defect M2).
	rev uint64
}

type watcher struct {
	id  uint64
	ref ref // structured selector: namespace + key pattern
	// pattern is ref.display(), the display-path form used for matching against
	// concrete display paths and as the selector's identity in the set.
	pattern string
	fn      func(Event)
}

// subManager owns the Subscribe stream lifecycle for a Client: connect, send
// selector registration, apply events, ack heartbeats, reconnect with backoff,
// resume by revision, and reconcile periodically.
//
// Hot-reloading ParameterValues do not each register a per-key selector;
// instead every non-static value in a namespace shares one namespace-wide
// selector {namespace, "*"} on the stream, with a per-key handler dispatched
// from the events it delivers.
type subManager struct {
	client *Client

	mu sync.Mutex
	// dynNS holds the namespace-wide selectors ({ns, "*"}) contributed by
	// hot-reloading ParameterValues, keyed by display pattern. Add-only:
	// declarative values are never unregistered.
	dynNS map[string]ref
	// selectors is the union of dynNS and the watcher patterns actually sent on
	// the stream, keyed by display pattern.
	selectors     map[string]ref
	paramHandlers map[string][]func(newVal string, present bool) // display key -> handlers
	known         map[string]knownVal                            // display key -> value
	watchers      []*watcher
	nextWatcherID uint64
	started       bool
	// streamSelectors is the selector set sent on the current stream's Subscribe
	// request. A snapshot is authoritative for this set, so it scopes which
	// absent-but-known keys a snapshot may treat as deleted (defect M3).
	streamSelectors []ref

	lastRev atomic.Uint64

	stopCh           chan struct{}
	restartCh        chan struct{}
	restartRequested atomic.Bool
	stopOnce         sync.Once
	wg               sync.WaitGroup

	reconcileInterval time.Duration
}

func newSubManager(c *Client) *subManager {
	return &subManager{
		client:            c,
		dynNS:             make(map[string]ref),
		selectors:         make(map[string]ref),
		paramHandlers:     make(map[string][]func(string, bool)),
		known:             make(map[string]knownVal),
		stopCh:            make(chan struct{}),
		restartCh:         make(chan struct{}, 1),
		reconcileInterval: defaultReconcileInterval,
	}
}

// registerParam seeds a parameter's known value and registers a handler that is
// invoked whenever a new value arrives for its key, adding a namespace-wide
// selector for the key's namespace if one is not already present.
func (m *subManager) registerParam(r ref, initial string, handler func(newVal string, present bool)) {
	m.mu.Lock()
	nsSel := ref{ns: r.ns, key: "*"}
	m.dynNS[nsSel.display()] = nsSel
	disp := r.display()
	m.known[disp] = knownVal{value: initial, present: true}
	m.paramHandlers[disp] = append(m.paramHandlers[disp], handler)
	changed := m.recomputeSelectorsLocked()
	wasStarted := m.started
	m.ensureStartedLocked()
	m.mu.Unlock()

	if wasStarted && changed {
		m.signalRestart()
	}
}

// registerWatcher adds a namespace/pattern watcher.
func (m *subManager) registerWatcher(r ref, fn func(Event)) *watcher {
	m.mu.Lock()
	m.nextWatcherID++
	w := &watcher{id: m.nextWatcherID, ref: r, pattern: r.display(), fn: fn}
	m.watchers = append(m.watchers, w)
	changed := m.recomputeSelectorsLocked()
	wasStarted := m.started
	m.ensureStartedLocked()
	m.mu.Unlock()

	if wasStarted && changed {
		m.signalRestart()
	}
	return w
}

func (m *subManager) removeWatcher(w *watcher) {
	m.mu.Lock()
	idx := -1
	for i, x := range m.watchers {
		if x.id == w.id {
			idx = i
			break
		}
	}
	if idx < 0 {
		m.mu.Unlock()
		return
	}
	m.watchers = append(m.watchers[:idx], m.watchers[idx+1:]...)
	changed := m.recomputeSelectorsLocked()
	started := m.started
	m.mu.Unlock()

	if started && changed {
		m.signalRestart()
	}
}

// recomputeSelectorsLocked rebuilds the selector set as the union of the
// namespace-wide dynamic selectors and the live watcher patterns, reporting
// whether the set changed (and thus whether the stream must reconnect).
func (m *subManager) recomputeSelectorsLocked() bool {
	next := make(map[string]ref, len(m.dynNS)+len(m.watchers))
	for k, r := range m.dynNS {
		next[k] = r
	}
	for _, w := range m.watchers {
		next[w.pattern] = w.ref
	}
	if sameSelectorKeys(m.selectors, next) {
		return false
	}
	m.selectors = next
	return true
}

func sameSelectorKeys(a, b map[string]ref) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func (m *subManager) ensureStartedLocked() {
	if m.started {
		return
	}
	m.started = true
	m.wg.Add(2)
	go m.run()
	go m.reconcileLoop()
}

func (m *subManager) signalRestart() {
	select {
	case m.restartCh <- struct{}{}:
	default:
	}
}

func (m *subManager) drainRestart() {
	for {
		select {
		case <-m.restartCh:
		default:
			return
		}
	}
}

func (m *subManager) snapshotSelectors() []ref {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ref, 0, len(m.selectors))
	for _, r := range m.selectors {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].display() < out[j].display() })
	return out
}

func (m *subManager) getRev() uint64 { return m.lastRev.Load() }

func (m *subManager) advanceRev(rev uint64) {
	for {
		cur := m.lastRev.Load()
		if rev <= cur {
			return
		}
		if m.lastRev.CompareAndSwap(cur, rev) {
			return
		}
	}
}

// shouldApply implements idempotent, at-least-once event application: apply
// unversioned events, and versioned events strictly newer than the last one
// seen.
func (m *subManager) shouldApply(rev uint64) bool {
	return rev == 0 || rev > m.lastRev.Load()
}

func (m *subManager) stop() {
	m.stopOnce.Do(func() { close(m.stopCh) })
	m.wg.Wait()
}

func (m *subManager) run() {
	defer m.wg.Done()
	attempt := 0
	for {
		select {
		case <-m.stopCh:
			return
		default:
		}

		m.drainRestart()
		streamCtx, cancel := context.WithCancel(m.client.rootCtx)
		watchDone := make(chan struct{})
		go func() {
			select {
			case <-m.restartCh:
				m.restartRequested.Store(true)
				cancel()
			case <-m.stopCh:
				cancel()
			case <-watchDone:
			}
		}()

		err := m.runStream(streamCtx)
		close(watchDone)
		cancel()

		select {
		case <-m.stopCh:
			return
		default:
		}

		if m.restartRequested.Swap(false) {
			attempt = 0
			continue // path set changed: reconnect immediately with the union
		}

		d := backoffDelay(attempt)
		attempt++
		m.client.logf("paramstore: watch stream ended (%v); reconnecting in %s", err, d)
		t := time.NewTimer(d)
		select {
		case <-m.stopCh:
			t.Stop()
			return
		case <-t.C:
		}
	}
}

func (m *subManager) runStream(ctx context.Context) error {
	stream, err := m.client.watch.Subscribe(m.client.withAuth(ctx, ""))
	if err != nil {
		return err
	}
	sels := m.snapshotSelectors()
	m.mu.Lock()
	m.streamSelectors = sels
	m.mu.Unlock()
	protoSels := make([]*kmsv1.WatchSelector, len(sels))
	for i, s := range sels {
		protoSels[i] = s.selectorProto()
	}
	if err := stream.Send(&kmsv1.SubscribeRequest{
		ClientName:       m.client.clientName,
		Selectors:        protoSels,
		LastSeenRevision: m.getRev(),
	}); err != nil {
		return err
	}
	for {
		ev, err := stream.Recv()
		if err != nil {
			return err
		}
		if err := m.handleEvent(ev, stream); err != nil {
			return err
		}
	}
}

func (m *subManager) handleEvent(ev *kmsv1.SubscribeEvent, stream kmsv1.WatchService_SubscribeClient) error {
	rev := ev.GetRevision()
	switch e := ev.GetEvent().(type) {
	case *kmsv1.SubscribeEvent_Snapshot:
		m.applySnapshot(e.Snapshot, rev)
		m.advanceRev(rev)
	case *kmsv1.SubscribeEvent_Change:
		if m.shouldApply(rev) {
			m.applyChange(e.Change, rev)
		}
		m.advanceRev(rev)
	case *kmsv1.SubscribeEvent_SecretChange:
		if m.shouldApply(rev) {
			m.applySecretChange(e.SecretChange, rev)
		}
		m.advanceRev(rev)
	case *kmsv1.SubscribeEvent_Heartbeat:
		m.advanceRev(rev)
		// Respond to the heartbeat with our last-applied revision so the server
		// registry tracks propagation (plan 8.4).
		return stream.Send(&kmsv1.SubscribeRequest{AckedRevision: m.getRev()})
	}
	return nil
}

func (m *subManager) applySnapshot(s *kmsv1.Snapshot, rev uint64) {
	// A snapshot is authoritative for the paths this stream subscribed to: it
	// enumerates every currently-present parameter under those patterns. We apply
	// the present values, then treat any previously-known path that falls under
	// the subscribed patterns but is absent from the snapshot as deleted — a
	// parameter removed while we were disconnected past the replay window (defect
	// M3). Paths outside the subscribed scope are never touched.
	present := make(map[string]struct{}, len(s.GetParameters()))
	for _, p := range s.GetParameters() {
		disp := refFromProto(p.GetRef()).display()
		present[disp] = struct{}{}
		m.setValue(disp, p.GetValue(), true, p.GetVersion(), rev, false)
	}
	for _, path := range m.absentKnownPaths(present) {
		m.setValue(path, "", false, 0, rev, false)
	}
}

// absentKnownPaths returns the currently-present known keys that fall within
// this stream's subscribed selector scope but are missing from present. These
// were deleted while the snapshot was authoritative.
func (m *subManager) absentKnownPaths(present map[string]struct{}) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for path, kv := range m.known {
		if !kv.present {
			continue
		}
		if _, ok := present[path]; ok {
			continue
		}
		if !m.pathInScopeLocked(path) {
			continue
		}
		out = append(out, path)
	}
	return out
}

// pathInScopeLocked reports whether the display key matches any selector sent on
// the current stream's Subscribe request. Scoping to the request (rather than
// the live selector set) avoids treating a key registered after the request was
// sent — and thus legitimately absent from the resulting snapshot — as deleted.
func (m *subManager) pathInScopeLocked(path string) bool {
	for _, s := range m.streamSelectors {
		if matchPattern(s.display(), path) {
			return true
		}
	}
	return false
}

func (m *subManager) applyChange(c *kmsv1.ParameterChange, rev uint64) {
	disp := refFromProto(c.GetRef()).display()
	switch c.GetChangeType() {
	case "delete":
		m.setValue(disp, "", false, c.GetVersion(), rev, false)
	default: // put | label
		m.setValue(disp, c.GetValue(), true, c.GetVersion(), rev, false)
	}
}

func (m *subManager) applySecretChange(c *kmsv1.SecretMetadataChange, rev uint64) {
	r := refFromProto(c.GetRef())
	m.client.cache.invalidateSecret(r.display())
	ev := Event{
		Type:       EventSecretChange,
		Namespace:  r.ns.String(),
		Key:        r.key,
		Version:    c.GetVersion(),
		Revision:   rev,
		ChangeType: c.GetChangeType(),
	}
	for _, w := range m.matchingWatchers(r.display()) {
		m.fireWatcher(w, ev)
	}
}

// setValue applies value for path, updating known state, parameter handlers,
// the read cache, and matching watchers — but only when the value actually
// changed, which keeps event delivery idempotent for callers.
//
// rev fences stale writes. For a live event (reconcile=false) rev is the stream
// revision and the write applies only if it is strictly newer than the path's
// last-applied revision (rev==0 is unversioned/best-effort and always applies).
// For a reconcile read (reconcile=true) rev is the snapshot revision captured
// before the fetch; the write applies only if no newer live event has advanced
// the path past that snapshot — otherwise the reconcile read is stale and must
// be dropped so it cannot regress a fresher value (defect M2).
func (m *subManager) setValue(path, value string, present bool, version, rev uint64, reconcile bool) {
	m.mu.Lock()
	prev, had := m.known[path]
	if had && !revAllowsWrite(prev.rev, rev, reconcile) {
		m.mu.Unlock()
		return
	}
	newRev := rev
	if prev.rev > newRev {
		newRev = prev.rev
	}
	var changed bool
	if present {
		changed = !had || prev.value != value || !prev.present
		m.known[path] = knownVal{value: value, present: true, rev: newRev}
	} else {
		changed = had && prev.present
		delete(m.known, path)
	}
	handlers := make([]func(string, bool), len(m.paramHandlers[path]))
	copy(handlers, m.paramHandlers[path])
	watchers := m.matchingWatchersLocked(path)
	m.mu.Unlock()

	if !changed {
		return
	}

	m.client.cache.invalidateParam(path)
	for _, h := range handlers {
		h(value, present)
	}

	evType := EventPut
	if !present {
		evType = EventDelete
	}
	r := refOf(path)
	ev := Event{Type: evType, Namespace: r.ns.String(), Key: r.key, Value: value, Version: version, Revision: rev}
	for _, w := range watchers {
		m.fireWatcher(w, ev)
	}
}

// revAllowsWrite is the stale-write fence for setValue. See setValue for the
// live-vs-reconcile semantics.
func revAllowsWrite(prevRev, rev uint64, reconcile bool) bool {
	if reconcile {
		// Authoritative only if no newer live event advanced the path past the
		// snapshot revision we captured before reading.
		return prevRev <= rev
	}
	// A live event must be strictly newer than what we've already applied.
	return rev == 0 || rev > prevRev
}

func (m *subManager) watcherCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.watchers)
}

func (m *subManager) matchingWatchers(path string) []*watcher {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.matchingWatchersLocked(path)
}

func (m *subManager) matchingWatchersLocked(path string) []*watcher {
	var out []*watcher
	for _, w := range m.watchers {
		if matchPattern(w.pattern, path) {
			out = append(out, w)
		}
	}
	return out
}

// fireWatcher dispatches a watch callback on the client's shared callback
// goroutine so a slow or panicking handler cannot stall the stream.
func (m *subManager) fireWatcher(w *watcher, ev Event) {
	fn := w.fn
	m.client.enqueueCallback(ev.Path(), func() { fn(ev) })
}

func (m *subManager) reconcileLoop() {
	defer m.wg.Done()
	t := time.NewTicker(m.reconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-t.C:
			m.reconcile()
		}
	}
}

// reconcile is the periodic safety net (plan 8.4.8): fetch current values for
// registered parameters and apply any drift missed by the event stream.
func (m *subManager) reconcile() {
	m.mu.Lock()
	paramPaths := make([]string, 0, len(m.paramHandlers))
	for p := range m.paramHandlers {
		paramPaths = append(paramPaths, p)
	}
	prefixSels := make([]ref, 0)
	for _, w := range m.watchers {
		if strings.HasSuffix(w.ref.key, "*") {
			prefixSels = append(prefixSels, w.ref)
		}
	}
	m.mu.Unlock()

	// Capture the snapshot revision before issuing any read. Every value we fetch
	// reflects the store at least as of this revision; a live event that lands
	// with a higher revision while we read is authoritative and must win over our
	// (now stale) reads. setValue enforces that with reconcile=true (defect M2).
	snapRev := m.getRev()

	ctx, cancel := context.WithTimeout(m.client.rootCtx, m.client.timeout)
	defer cancel()

	for _, p := range paramPaths {
		val, err := m.client.fetchParameter(ctx, refOf(p), getOptions{})
		if err != nil {
			// A hot-reloading parameter the store no longer has was deleted while
			// the stream missed the event: treat it as a deletion (defect M3). Any
			// other error (unavailable, timeout) keeps the last-known value.
			if errors.Is(err, ErrNotFound) {
				m.setValue(p, "", false, 0, snapRev, true)
			}
			continue
		}
		m.setValue(p, val, true, 0, snapRev, true)
	}

	for _, sel := range prefixSels {
		m.reconcileSelector(ctx, sel, snapRev)
	}
}

// reconcileSelector lists the parameters under a wildcard watcher's namespace
// and key prefix and applies any drift the stream missed.
func (m *subManager) reconcileSelector(ctx context.Context, sel ref, snapRev uint64) {
	keyPrefix := strings.TrimSuffix(sel.key, "*")
	pageToken := ""
	for i := 0; i < 100; i++ { // bounded to avoid runaway loops
		cctx, cancel := m.client.callCtx(ctx, "")
		resp, err := m.client.params.ListParameters(cctx, &kmsv1.ListParametersRequest{
			Namespace: sel.ns.proto(),
			KeyPrefix: keyPrefix,
			PageToken: pageToken,
		})
		cancel()
		if err != nil {
			return
		}
		for _, p := range resp.GetParameters() {
			m.setValue(refFromProto(p.GetRef()).display(), p.GetValue(), true, p.GetVersion(), snapRev, true)
		}
		pageToken = resp.GetNextPageToken()
		if pageToken == "" {
			return
		}
	}
}

// Watch subscribes to a key pattern and invokes fn for every change. pattern is
// relative to the client namespace — an exact key ("rate-limit"), a prefix
// pattern ("billing/*"), or "*" for the whole namespace — or an absolute
// "/env/app/pattern" display path for another namespace. The returned stop
// function unregisters the watcher; it is also called automatically if ctx is
// cancelled.
func (c *Client) Watch(ctx context.Context, pattern string, fn func(Event)) (stop func(), err error) {
	if fn == nil {
		return nil, errors.New("paramstore: Watch requires a non-nil callback")
	}
	if pattern == "" {
		return nil, errors.New("paramstore: Watch requires a non-empty pattern")
	}
	r, err := c.resolveRef(ctx, pattern)
	if err != nil {
		return nil, err
	}
	w := c.subs().registerWatcher(r, fn)
	// done is closed by stop() so the ctx-watcher goroutine below always exits,
	// even when ctx is non-cancelable (e.g. context.Background()) and the caller
	// unregisters via stop() rather than context cancellation (defect L3).
	done := make(chan struct{})
	var once sync.Once
	stop = func() {
		once.Do(func() {
			close(done)
			c.subs().removeWatcher(w)
		})
	}
	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				stop()
			case <-done:
			case <-c.closed:
			}
		}()
	}
	return stop, nil
}

// backoffDelay implements exponential backoff with full jitter (base 1s, cap
// 60s), per plan 8.4.5.
func backoffDelay(attempt int) time.Duration {
	const base = time.Second
	const maxDelay = 60 * time.Second
	d := maxDelay
	if attempt < 6 { // 1s << 6 = 64s > cap, so clamp beyond
		if shifted := base << uint(attempt); shifted < maxDelay {
			d = shifted
		}
	}
	// Full jitter: uniform in (0, d], with a small floor to avoid a hot loop.
	j := time.Duration(rand.Int64N(int64(d)))
	if j < 10*time.Millisecond {
		j = 10 * time.Millisecond
	}
	return j
}

// matchPattern reports whether a subscription pattern matches a concrete path.
// A pattern ending in "*" is a prefix match; otherwise it is an exact match.
func matchPattern(pattern, path string) bool {
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(path, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == path
}
