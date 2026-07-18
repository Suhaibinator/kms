package paramstore

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
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

// Event is delivered to Watch callbacks for every change in a watched
// namespace.
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

const (
	// defaultReconcileInterval is the safety-net full-sync poll cadence (plan 8.4).
	defaultReconcileInterval = 5 * time.Minute
	maxReconcilePages        = 100
)

type knownVal struct {
	value   string
	present bool
	// rev is the stream revision (or reconcile snapshot revision) the value was
	// last applied at. It fences stale writes: a reconcile read that raced a
	// newer live event must not regress the key (defect M2).
	rev uint64
}

// watcher fires fn for every change in its namespace. The namespace is the unit
// of subscription; a watcher receives all changes in ns and filters (if it
// wants a subset) inside fn.
type watcher struct {
	id uint64
	ns namespaceRef
	fn func(Event)
}

// subManager owns the Subscribe stream lifecycle for a Client: connect, send
// the namespace subscription, apply events, ack heartbeats, reconnect with
// backoff, resume by revision, and reconcile periodically.
//
// The namespace (env, app) is the unit of subscription. Every non-static
// ParameterValue and every Watch share ONE subscription to the set of
// namespaces they reference (the client's own namespace plus any extra
// namespace a Watch targets). The server streams every change in those
// namespaces; the SDK routes each incoming change to the matching field by
// exact key and to every Watch on the change's namespace.
type subManager struct {
	client *Client

	mu sync.Mutex
	// namespaces is the set of namespaces sent on the Subscribe request, keyed
	// by "env/app". Add-only: a namespace, once subscribed, is never
	// unsubscribed for the client's lifetime.
	namespaces    map[string]namespaceRef
	paramHandlers map[string][]func(newVal string, present bool) // display key -> handlers
	known         map[string]knownVal                            // display key -> value
	watchers      []*watcher
	nextWatcherID uint64
	started       bool
	// streamNamespaces is the namespace set sent on the current stream's
	// Subscribe request. A snapshot is authoritative for this set, so it scopes
	// which absent-but-known keys a snapshot may treat as deleted (defect M3).
	streamNamespaces []namespaceRef

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
		namespaces:        make(map[string]namespaceRef),
		paramHandlers:     make(map[string][]func(string, bool)),
		known:             make(map[string]knownVal),
		stopCh:            make(chan struct{}),
		restartCh:         make(chan struct{}, 1),
		reconcileInterval: defaultReconcileInterval,
	}
}

// registerParam seeds a parameter's known value and registers a handler that is
// invoked whenever a new value arrives for its exact key, subscribing to the
// key's namespace if it is not already subscribed.
func (m *subManager) registerParam(r ref, initial string, handler func(newVal string, present bool)) {
	m.mu.Lock()
	changed := m.addNamespaceLocked(r.ns)
	disp := r.display()
	m.known[disp] = knownVal{value: initial, present: true}
	m.paramHandlers[disp] = append(m.paramHandlers[disp], handler)
	wasStarted := m.started
	m.ensureStartedLocked()
	m.mu.Unlock()

	if wasStarted && changed {
		m.signalRestart()
	}
}

// registerWatcher adds a whole-namespace watcher.
func (m *subManager) registerWatcher(ns namespaceRef, fn func(Event)) *watcher {
	m.mu.Lock()
	m.nextWatcherID++
	w := &watcher{id: m.nextWatcherID, ns: ns, fn: fn}
	m.watchers = append(m.watchers, w)
	changed := m.addNamespaceLocked(ns)
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
	defer m.mu.Unlock()
	for i, x := range m.watchers {
		if x.id == w.id {
			m.watchers = append(m.watchers[:i], m.watchers[i+1:]...)
			return
		}
	}
}

// addNamespaceLocked adds ns to the subscription set, reporting whether it was
// newly added (and thus whether the stream must reconnect to include it).
func (m *subManager) addNamespaceLocked(ns namespaceRef) bool {
	k := ns.String()
	if _, ok := m.namespaces[k]; ok {
		return false
	}
	m.namespaces[k] = ns
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

func (m *subManager) snapshotNamespaces() []namespaceRef {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]namespaceRef, 0, len(m.namespaces))
	for _, n := range m.namespaces {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
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
			continue // namespace set changed: reconnect immediately with the union
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
	nss := m.snapshotNamespaces()
	m.mu.Lock()
	m.streamNamespaces = nss
	m.mu.Unlock()
	protoNS := make([]*kmsv1.NamespaceRef, len(nss))
	for i, n := range nss {
		protoNS[i] = n.proto()
	}
	if err := stream.Send(&kmsv1.SubscribeRequest{
		ClientName:       m.client.clientName,
		Namespaces:       protoNS,
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
	// A snapshot is authoritative for the namespaces this stream subscribed to:
	// it enumerates every currently-present parameter in those namespaces. We
	// apply the present values, then treat any previously-known key in a
	// subscribed namespace that is absent from the snapshot as deleted — a
	// parameter removed while we were disconnected past the replay window (defect
	// M3). Keys in other namespaces are never touched.
	// Secret changes carry metadata only. A full snapshot cannot replay any
	// secret events that were pruned, so invalidate every tokenless cached
	// secret in the authoritative stream namespaces before continuing.
	m.mu.Lock()
	streamNamespaces := append([]namespaceRef(nil), m.streamNamespaces...)
	m.mu.Unlock()
	m.client.cache.invalidateSecretsInNamespaces(streamNamespaces)

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

// absentKnownPaths returns the currently-present known keys in this stream's
// subscribed namespaces that are missing from present. These were deleted while
// the snapshot was authoritative.
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

// pathInScopeLocked reports whether the display key belongs to a namespace sent
// on the current stream's Subscribe request. Scoping to the request (rather than
// the live namespace set) avoids treating a key in a namespace subscribed after
// the request was sent — and thus legitimately absent from the resulting
// snapshot — as deleted.
func (m *subManager) pathInScopeLocked(path string) bool {
	ns := refOf(path).ns
	for _, s := range m.streamNamespaces {
		if s == ns {
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
	for _, w := range m.matchingWatchers(r.ns) {
		m.fireWatcher(w, ev)
	}
}

// setValue applies value for path, updating known state, parameter handlers,
// the read cache, and the namespace's watchers — but only when the value
// actually changed, which keeps event delivery idempotent for callers.
//
// rev fences stale writes. For a live event (reconcile=false) rev is the stream
// revision and the write applies only if it is strictly newer than the key's
// last-applied revision (rev==0 is unversioned/best-effort and always applies).
// For a reconcile read (reconcile=true) rev is the snapshot revision captured
// before the fetch; the write applies only if no newer live event has advanced
// the key past that snapshot — otherwise the reconcile read is stale and must
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
		// Preserve a revisioned tombstone. Removing the entry would discard the
		// stale-write fence and let a reconcile read captured before this delete
		// resurrect the old value.
		m.known[path] = knownVal{present: false, rev: newRev}
	}
	handlers := make([]func(string, bool), len(m.paramHandlers[path]))
	copy(handlers, m.paramHandlers[path])
	r := refOf(path)
	watchers := m.matchingWatchersLocked(r.ns)
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
	ev := Event{Type: evType, Namespace: r.ns.String(), Key: r.key, Value: value, Version: version, Revision: rev}
	for _, w := range watchers {
		m.fireWatcher(w, ev)
	}
}

// revAllowsWrite is the stale-write fence for setValue. See setValue for the
// live-vs-reconcile semantics.
func revAllowsWrite(prevRev, rev uint64, reconcile bool) bool {
	if reconcile {
		// Authoritative only if no newer live event advanced the key past the
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

func (m *subManager) matchingWatchers(ns namespaceRef) []*watcher {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.matchingWatchersLocked(ns)
}

func (m *subManager) matchingWatchersLocked(ns namespaceRef) []*watcher {
	var out []*watcher
	for _, w := range m.watchers {
		if w.ns == ns {
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

// reconcile is the periodic safety net (plan 8.4.8): list every subscribed
// namespace in full and apply any drift the event stream missed, including
// deletions of previously-known hot-reload parameters.
func (m *subManager) reconcile() {
	m.mu.Lock()
	nsList := make([]namespaceRef, 0, len(m.namespaces))
	for _, n := range m.namespaces {
		nsList = append(nsList, n)
	}
	paramPaths := make([]string, 0, len(m.paramHandlers))
	for p := range m.paramHandlers {
		paramPaths = append(paramPaths, p)
	}
	m.mu.Unlock()

	// Capture the snapshot revision before issuing any read. Every value we fetch
	// reflects the store at least as of this revision; a live event that lands
	// with a higher revision while we read is authoritative and must win over our
	// (now stale) reads. setValue enforces that with reconcile=true (defect M2).
	snapRev := m.getRev()

	ctx, cancel := context.WithTimeout(m.client.rootCtx, m.client.timeout)
	defer cancel()

	present := make(map[string]struct{})
	listed := make(map[string]bool) // namespace string -> fully enumerated
	for _, ns := range nsList {
		listed[ns.String()] = m.reconcileNamespace(ctx, ns, snapRev, present)
	}

	// A registered hot-reload parameter absent from its (fully listed) namespace
	// was deleted while the stream missed the event: revert it (defect M3). If
	// the namespace could not be listed, keep the last-known value.
	for _, p := range paramPaths {
		if _, ok := present[p]; ok {
			continue
		}
		if !listed[refOf(p).ns.String()] {
			continue
		}
		m.setValue(p, "", false, 0, snapRev, true)
	}
}

// reconcileNamespace lists every parameter in ns (empty key prefix) and applies
// any drift the stream missed, recording each present key in present. It reports
// whether the namespace was fully enumerated (false on any list error, so the
// caller does not mistake a fetch failure for a deletion).
func (m *subManager) reconcileNamespace(ctx context.Context, ns namespaceRef, snapRev uint64, present map[string]struct{}) bool {
	pageToken := ""
	for i := 0; i < maxReconcilePages; i++ { // bounded to avoid runaway loops
		cctx, cancel := m.client.callCtx(ctx, "")
		resp, err := m.client.params.ListParameters(cctx, &kmsv1.ListParametersRequest{
			Namespace: ns.proto(),
			PageToken: pageToken,
		})
		cancel()
		if err != nil {
			return false
		}
		for _, p := range resp.GetParameters() {
			disp := refFromProto(p.GetRef()).display()
			present[disp] = struct{}{}
			m.setValue(disp, p.GetValue(), true, p.GetVersion(), snapRev, true)
		}
		pageToken = resp.GetNextPageToken()
		if pageToken == "" {
			return true
		}
	}
	// A non-empty page token means the namespace was only partially listed.
	// Report it as incomplete so the caller cannot mistake keys beyond the cap
	// for deletions and revert their hot-reloaded values.
	return false
}

// Watch subscribes to the client's whole namespace and invokes fn for every
// change in it — there is no key pattern. An app that only cares about a subset
// filters inside fn by its own convention (e.g.
// strings.HasPrefix(ev.Key, "db/")). The returned stop function unregisters the
// watcher; it is also called automatically if ctx is cancelled. Watch requires
// the client to have a namespace (Config.Namespace or a namespace-bound
// identity); otherwise it returns ErrNoNamespace.
func (c *Client) Watch(ctx context.Context, fn func(Event)) (stop func(), err error) {
	ns, bound, err := c.resolveNamespace(ctx)
	if err != nil {
		return nil, err
	}
	if !bound {
		return nil, fmt.Errorf("%w: Watch needs a namespace (set Config.Namespace or bind the identity)", ErrNoNamespace)
	}
	return c.watchNamespace(ctx, ns, fn)
}

// WatchNamespace watches a specific namespace ("env/app") the client is
// authorized for, invoking fn for every change in it. Like Watch it takes no key
// pattern. Use it to observe a namespace other than the client's own.
func (c *Client) WatchNamespace(ctx context.Context, namespace string, fn func(Event)) (stop func(), err error) {
	ns, err := parseNamespace(namespace)
	if err != nil {
		return nil, err
	}
	return c.watchNamespace(ctx, ns, fn)
}

func (c *Client) watchNamespace(ctx context.Context, ns namespaceRef, fn func(Event)) (func(), error) {
	if fn == nil {
		return nil, errors.New("paramstore: Watch requires a non-nil callback")
	}
	w := c.subs().registerWatcher(ns, fn)
	// done is closed by stop() so the ctx-watcher goroutine below always exits,
	// even when ctx is non-cancelable (e.g. context.Background()) and the caller
	// unregisters via stop() rather than context cancellation (defect L3).
	done := make(chan struct{})
	var once sync.Once
	stop := func() {
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
