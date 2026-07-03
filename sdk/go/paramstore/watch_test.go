package paramstore

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/sdk/go/paramstore/paramstoretest"
)

const waitTimeout = 5 * time.Second

// TestDefaultHotReload proves the flipped default: a ParameterValue with no
// Static flag hot-reloads over the shared, namespace-wide subscription.
func TestDefaultHotReload(t *testing.T) {
	c, srv := newTestClient(t, Config{ClientName: "test-app"})
	srv.SetParameter(testNS, "rate", "100")

	pv := ParameterValue{Key: "rate"}
	if err := pv.Init(c); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if pv.Get() != "100" {
		t.Fatalf("initial Get = %q, want 100", pv.Get())
	}

	changes := make(chan [2]string, 8)
	pv.OnChange(func(old, new string) { changes <- [2]string{old, new} })

	sub, err := srv.WaitForSubscribe(waitTimeout)
	if err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}
	if sub.ClientName != "test-app" {
		t.Errorf("ClientName = %q, want test-app", sub.ClientName)
	}
	// Non-static values register one namespace-wide selector, not a per-key path.
	if !sub.HasSelector(testNS, "*") {
		t.Errorf("selectors = %v, want namespace-wide %s/*", sub.SelectorDisplays(), testNS)
	}
	if sub.LastSeenRevision != 0 {
		t.Errorf("first LastSeenRevision = %d, want 0", sub.LastSeenRevision)
	}

	sub.PushChange(5, testNS, "rate", "put", "200", 5)

	if !eventually(t, waitTimeout, func() bool { return pv.Get() == "200" }) {
		t.Fatalf("value did not hot-reload; Get = %q", pv.Get())
	}
	select {
	case ch := <-changes:
		if ch[0] != "100" || ch[1] != "200" {
			t.Errorf("OnChange got %v, want [100 200]", ch)
		}
	case <-time.After(waitTimeout):
		t.Fatal("OnChange callback not fired")
	}
}

// TestNamespaceWideSelectorShared proves that multiple non-static parameters in
// the same namespace share ONE namespace-wide selector (rather than one
// per-key), and that registering the second does not reconnect the stream.
func TestNamespaceWideSelectorShared(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetParameter(testNS, "a", "1")
	srv.SetParameter(testNS, "b", "2")

	a := ParameterValue{Key: "a"}
	b := ParameterValue{Key: "b"}
	if err := a.Init(c); err != nil {
		t.Fatalf("Init a: %v", err)
	}
	sub, err := srv.WaitForSubscribe(waitTimeout)
	if err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}
	// Second param, same namespace: selector set is unchanged, so no reconnect.
	if err := b.Init(c); err != nil {
		t.Fatalf("Init b: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if n := srv.SubscribeCount(); n != 1 {
		t.Errorf("SubscribeCount = %d, want 1 (namespace-wide selector is shared)", n)
	}
	if len(sub.Selectors) != 1 || !sub.HasSelector(testNS, "*") {
		t.Errorf("selectors = %v, want exactly [%s/*]", sub.SelectorDisplays(), testNS)
	}

	// A change to b flows over the shared selector.
	sub.PushChange(6, testNS, "b", "put", "22", 6)
	if !eventually(t, waitTimeout, func() bool { return b.Get() == "22" }) {
		t.Errorf("b did not hot-reload over shared selector; Get = %q", b.Get())
	}
}

// TestStaticParameterDoesNotSubscribe proves Static opts out of hot reload: the
// value resolves once and never opens a subscription.
func TestStaticParameterDoesNotSubscribe(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetParameter(testNS, "rate", "100")

	pv := ParameterValue{Key: "rate", Static: true}
	if err := pv.Init(c); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if pv.Get() != "100" {
		t.Fatalf("Get = %q, want 100", pv.Get())
	}
	time.Sleep(150 * time.Millisecond)
	if n := srv.SubscribeCount(); n != 0 {
		t.Errorf("SubscribeCount = %d, want 0 (static value must not subscribe)", n)
	}
	// A later server-side change is not reflected.
	srv.SetParameter(testNS, "rate", "200")
	time.Sleep(100 * time.Millisecond)
	if pv.Get() != "100" {
		t.Errorf("static Get = %q, want 100 (no hot reload)", pv.Get())
	}
}

func TestSnapshotNoSpuriousCallback(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetParameter(testNS, "rate", "100")

	pv := ParameterValue{Key: "rate"}
	if err := pv.Init(c); err != nil {
		t.Fatalf("Init: %v", err)
	}
	var fires int32
	pv.OnChange(func(old, new string) { atomic.AddInt32(&fires, 1) })

	sub, err := srv.WaitForSubscribe(waitTimeout)
	if err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}

	// Snapshot repeating the current value must not fire OnChange.
	sub.PushSnapshot(2, paramstoretest.Param(testNS, "rate", "100", 1))
	time.Sleep(150 * time.Millisecond)
	if n := atomic.LoadInt32(&fires); n != 0 {
		t.Errorf("OnChange fired %d times on no-op snapshot, want 0", n)
	}

	// Snapshot with a new value fires once.
	sub.PushSnapshot(3, paramstoretest.Param(testNS, "rate", "300", 2))
	if !eventually(t, waitTimeout, func() bool { return pv.Get() == "300" }) {
		t.Fatalf("snapshot value not applied; Get = %q", pv.Get())
	}
	if !eventually(t, waitTimeout, func() bool { return atomic.LoadInt32(&fires) == 1 }) {
		t.Errorf("OnChange fired %d times, want 1", atomic.LoadInt32(&fires))
	}
}

func TestHeartbeatAck(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetParameter(testNS, "rate", "1")
	pv := ParameterValue{Key: "rate"}
	if err := pv.Init(c); err != nil {
		t.Fatalf("Init: %v", err)
	}
	sub, err := srv.WaitForSubscribe(waitTimeout)
	if err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}

	sub.PushChange(4, testNS, "rate", "put", "2", 4)
	if !eventually(t, waitTimeout, func() bool { return pv.Get() == "2" }) {
		t.Fatal("change not applied before heartbeat")
	}

	sub.SendHeartbeat(9)
	acked, err := sub.WaitAck(waitTimeout)
	if err != nil {
		t.Fatalf("WaitAck: %v", err)
	}
	if acked != 9 {
		t.Errorf("acked revision = %d, want 9", acked)
	}
}

func TestReconnectResumesFromLastRevision(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetParameter(testNS, "rate", "1")
	pv := ParameterValue{Key: "rate"}
	if err := pv.Init(c); err != nil {
		t.Fatalf("Init: %v", err)
	}

	sub1, err := srv.WaitForSubscribe(waitTimeout)
	if err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}
	if sub1.LastSeenRevision != 0 {
		t.Errorf("sub1 LastSeenRevision = %d, want 0", sub1.LastSeenRevision)
	}
	sub1.PushChange(7, testNS, "rate", "put", "70", 7)
	if !eventually(t, waitTimeout, func() bool { return pv.Get() == "70" }) {
		t.Fatal("change not applied")
	}

	// Drop the stream; the client must reconnect and resume from revision 7.
	sub1.Kill()
	sub2, err := srv.WaitForSubscribe(waitTimeout)
	if err != nil {
		t.Fatalf("reconnect WaitForSubscribe: %v", err)
	}
	if sub2.LastSeenRevision != 7 {
		t.Errorf("resumed LastSeenRevision = %d, want 7", sub2.LastSeenRevision)
	}
	sub2.PushChange(8, testNS, "rate", "put", "80", 8)
	if !eventually(t, waitTimeout, func() bool { return pv.Get() == "80" }) {
		t.Fatalf("post-reconnect change not applied; Get = %q", pv.Get())
	}
}

func TestIdempotentEventApplication(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetParameter(testNS, "rate", "1")
	pv := ParameterValue{Key: "rate"}
	if err := pv.Init(c); err != nil {
		t.Fatalf("Init: %v", err)
	}
	var fires int32
	pv.OnChange(func(old, new string) { atomic.AddInt32(&fires, 1) })

	sub, err := srv.WaitForSubscribe(waitTimeout)
	if err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}

	sub.PushChange(5, testNS, "rate", "put", "A", 5)
	if !eventually(t, waitTimeout, func() bool { return pv.Get() == "A" }) {
		t.Fatal("first change not applied")
	}
	// Duplicate/old revision must be ignored.
	sub.PushChange(5, testNS, "rate", "put", "B", 5)
	sub.PushChange(3, testNS, "rate", "put", "C", 3)
	time.Sleep(150 * time.Millisecond)
	if pv.Get() != "A" {
		t.Errorf("value = %q, want A (stale events ignored)", pv.Get())
	}
	// A newer revision applies.
	sub.PushChange(6, testNS, "rate", "put", "D", 6)
	if !eventually(t, waitTimeout, func() bool { return pv.Get() == "D" }) {
		t.Fatalf("newer change not applied; Get = %q", pv.Get())
	}
	if n := atomic.LoadInt32(&fires); n != 2 {
		t.Errorf("OnChange fired %d times, want 2 (A and D)", n)
	}
}

func TestEnvPinnedValueDoesNotReload(t *testing.T) {
	t.Setenv("PINNED", "env-value")
	c, srv := newTestClient(t, Config{})
	srv.SetParameter(testNS, "rate", "100")

	pv := ParameterValue{Key: "rate", EnvVar: "PINNED"}
	if err := pv.Init(c); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if pv.Get() != "env-value" {
		t.Fatalf("Get = %q, want env-value", pv.Get())
	}
	// An env-pinned value must not open a subscription.
	time.Sleep(150 * time.Millisecond)
	if n := srv.SubscribeCount(); n != 0 {
		t.Errorf("SubscribeCount = %d, want 0 (env-pinned value must not subscribe)", n)
	}
	// A log line should explain the pinning (without the value).
	logged := false
	for _, line := range c.logger.(*testLogger).all() {
		if strings.Contains(line, "pinned to env") {
			logged = true
			if strings.Contains(line, "env-value") {
				t.Errorf("log line leaked value: %q", line)
			}
		}
	}
	if !logged {
		t.Error("expected a log line about env pinning")
	}
}

func TestWatchPattern(t *testing.T) {
	c, srv := newTestClient(t, Config{})

	events := make(chan Event, 16)
	stop, err := c.Watch(context.Background(), "payments/*", func(ev Event) { events <- ev })
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer stop()

	sub, err := srv.WaitForSubscribe(waitTimeout)
	if err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}
	if !sub.HasSelector(testNS, "payments/*") {
		t.Errorf("selectors = %v, want prefix pattern payments/*", sub.SelectorDisplays())
	}

	sub.PushChange(2, testNS, "payments/rate", "put", "9", 2)
	select {
	case ev := <-events:
		if ev.Type != EventPut || ev.Key != "payments/rate" || ev.Namespace != testNS || ev.Value != "9" {
			t.Errorf("event = %+v", ev)
		}
		if ev.Path() != "/prod/app/payments/rate" {
			t.Errorf("Path() = %q", ev.Path())
		}
	case <-time.After(waitTimeout):
		t.Fatal("watch event not delivered")
	}

	// A non-matching key must not deliver.
	sub.PushChange(3, testNS, "other/x", "put", "1", 3)
	select {
	case ev := <-events:
		t.Errorf("unexpected event for non-matching key: %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestWatchAbsolutePattern proves an absolute "/env/app/pattern" Watch reaches
// another namespace.
func TestWatchAbsolutePattern(t *testing.T) {
	c, srv := newTestClient(t, Config{})

	events := make(chan Event, 4)
	stop, err := c.Watch(context.Background(), "/staging/billing/*", func(ev Event) { events <- ev })
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer stop()

	sub, err := srv.WaitForSubscribe(waitTimeout)
	if err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}
	if !sub.HasSelector("staging/billing", "*") {
		t.Errorf("selectors = %v, want staging/billing *", sub.SelectorDisplays())
	}
	sub.PushChangePath(2, "/staging/billing/rate", "put", "5", 2)
	select {
	case ev := <-events:
		if ev.Namespace != "staging/billing" || ev.Key != "rate" || ev.Value != "5" {
			t.Errorf("event = %+v", ev)
		}
	case <-time.After(waitTimeout):
		t.Fatal("cross-namespace watch event not delivered")
	}
}

func TestWatchSecretChangeInvalidatesCache(t *testing.T) {
	c, srv := newTestClient(t, Config{CacheTTL: time.Minute})
	srv.SetSecret(testNS, "db/pw", []byte("v1"))

	// Prime the cache.
	if s, _ := c.GetSecret(context.Background(), "db/pw"); s.StringValue() != "v1" {
		t.Fatalf("prime = %q", s.StringValue())
	}

	events := make(chan Event, 4)
	stop, err := c.Watch(context.Background(), "db/*", func(ev Event) { events <- ev })
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer stop()
	sub, err := srv.WaitForSubscribe(waitTimeout)
	if err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}

	// Rotate the secret on the server and announce it via metadata change.
	srv.SetSecret(testNS, "db/pw", []byte("v2"))
	sub.PushSecretChange(4, testNS, "db/pw", "put", 2)

	select {
	case ev := <-events:
		if ev.Type != EventSecretChange || ev.Key != "db/pw" {
			t.Errorf("event = %+v", ev)
		}
	case <-time.After(waitTimeout):
		t.Fatal("secret change event not delivered")
	}

	// After invalidation, a fresh read should see the rotated value.
	if !eventually(t, waitTimeout, func() bool {
		s, _ := c.GetSecret(context.Background(), "db/pw")
		return s.StringValue() == "v2"
	}) {
		t.Error("secret cache not invalidated by secret_change event")
	}
}

func TestSlowCallbackDoesNotStallStream(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetParameter(testNS, "rate", "0")
	pv := ParameterValue{Key: "rate"}
	if err := pv.Init(c); err != nil {
		t.Fatalf("Init: %v", err)
	}

	block := make(chan struct{})
	var started atomic.Bool
	pv.OnChange(func(old, new string) {
		started.Store(true)
		<-block // block the callback goroutine indefinitely
	})
	defer close(block)

	sub, err := srv.WaitForSubscribe(waitTimeout)
	if err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}

	// First change triggers the (blocking) callback.
	sub.PushChange(2, testNS, "rate", "put", "1", 2)
	if !eventually(t, waitTimeout, func() bool { return started.Load() }) {
		t.Fatal("callback did not start")
	}
	// Even though the callback is stuck, further value updates must still apply.
	sub.PushChange(3, testNS, "rate", "put", "2", 3)
	if !eventually(t, waitTimeout, func() bool { return pv.Get() == "2" }) {
		t.Errorf("value did not update while callback blocked; Get = %q", pv.Get())
	}
}

func TestReconcileAppliesMissedDrift(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetParameter(testNS, "rate", "1")
	srv.SetParameter(testNS, "x/y", "old")

	pv := ParameterValue{Key: "rate"}
	if err := pv.Init(c); err != nil {
		t.Fatalf("Init: %v", err)
	}
	watchEvents := make(chan Event, 8)
	stop, err := c.Watch(context.Background(), "x/*", func(ev Event) { watchEvents <- ev })
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer stop()
	if _, err := srv.WaitForSubscribe(waitTimeout); err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}

	// Simulate events missed by the stream: change values server-side without
	// pushing any SubscribeEvent.
	srv.SetParameter(testNS, "rate", "999")
	srv.SetParameter(testNS, "x/y", "new")

	// The periodic safety net reconciles both the exact hot-reload parameter and
	// the prefix watcher (via ListParameters on the namespace + key prefix).
	c.sub.reconcile()

	if pv.Get() != "999" {
		t.Errorf("reconcile did not apply drift to hot-reload param; Get = %q", pv.Get())
	}
	select {
	case ev := <-watchEvents:
		if ev.Key != "x/y" || ev.Value != "new" {
			t.Errorf("reconcile watch event = %+v", ev)
		}
	case <-time.After(waitTimeout):
		t.Fatal("reconcile did not deliver watcher event for prefix drift")
	}
}

func TestWatchStopViaContextCancel(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	ctx, cancel := context.WithCancel(context.Background())

	_, err := c.Watch(ctx, "sub/*", func(ev Event) {})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if _, err := srv.WaitForSubscribe(waitTimeout); err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}
	// Cancelling the context should unregister the watcher.
	cancel()
	if !eventually(t, waitTimeout, func() bool { return c.sub.watcherCount() == 0 }) {
		t.Error("watcher not removed after context cancel")
	}
}

// TestReconcileDoesNotRegressToStaleValue proves the M2 fix: a reconcile read
// that races a fresher live change must be dropped by the revision fence rather
// than regress the parameter to its stale snapshot value.
func TestReconcileDoesNotRegressToStaleValue(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetParameter(testNS, "rate", "V1")

	pv := ParameterValue{Key: "rate"}
	if err := pv.Init(c); err != nil {
		t.Fatalf("Init: %v", err)
	}

	var mu sync.Mutex
	var history [][2]string
	pv.OnChange(func(old, new string) {
		mu.Lock()
		history = append(history, [2]string{old, new})
		mu.Unlock()
	})

	sub, err := srv.WaitForSubscribe(waitTimeout)
	if err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}

	// While reconcile reads rate, a fresher change (V2 at revision 5) lands and
	// is fully applied. The server value is deliberately left at "V1", so the
	// reconcile fetch returns the now-stale value. The fence must drop it.
	var once sync.Once
	srv.SetGetParameterHook(func(display string) {
		if display != "/prod/app/rate" {
			return
		}
		once.Do(func() {
			sub.PushChangePath(5, "/prod/app/rate", "put", "V2", 5)
			if !eventually(t, waitTimeout, func() bool { return pv.Get() == "V2" }) {
				t.Errorf("newer event did not apply before reconcile read returned")
			}
		})
	})

	c.sub.reconcile()

	if pv.Get() != "V2" {
		t.Fatalf("reconcile regressed to stale value; Get = %q, want V2", pv.Get())
	}
	// Give any erroneous reconcile callback time to be dispatched.
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	for _, ch := range history {
		if ch[1] == "V1" {
			t.Errorf("stale reconcile write delivered OnChange back to V1: %v", history)
		}
	}
	if len(history) != 1 || history[0] != [2]string{"V1", "V2"} {
		t.Errorf("OnChange history = %v, want [[V1 V2]] only", history)
	}
}

// TestSnapshotResyncRevertsDeletedParamToDefault proves half of the M3 fix: a
// resync snapshot that omits a previously-known hot-reload parameter (deleted
// while disconnected) reverts the handle to its Default and fires OnChange.
func TestSnapshotResyncRevertsDeletedParamToDefault(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetParameter(testNS, "rate", "100")

	pv := ParameterValue{Key: "rate", Default: "def"}
	if err := pv.Init(c); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if pv.Get() != "100" {
		t.Fatalf("initial Get = %q, want 100", pv.Get())
	}
	changes := make(chan [2]string, 8)
	pv.OnChange(func(old, new string) { changes <- [2]string{old, new} })

	sub, err := srv.WaitForSubscribe(waitTimeout)
	if err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}

	// Reconnect past the replay window: the server sends an authoritative
	// snapshot of currently-present params under the subscribed namespace. rate
	// is absent because it was deleted while we were disconnected.
	sub.PushSnapshot(20) // empty snapshot within the subscribed scope

	if !eventually(t, waitTimeout, func() bool { return pv.Get() == "def" }) {
		t.Fatalf("deleted param did not revert to Default; Get = %q", pv.Get())
	}
	select {
	case ch := <-changes:
		if ch[0] != "100" || ch[1] != "def" {
			t.Errorf("OnChange = %v, want [100 def]", ch)
		}
	case <-time.After(waitTimeout):
		t.Fatal("OnChange not fired for snapshot deletion")
	}
}

// TestSnapshotOnlyDeletesInScopeAbsentPaths proves the M3 scope restriction: a
// snapshot reverts only known keys within the subscribed namespace that are
// absent from the snapshot; present keys are preserved.
func TestSnapshotOnlyDeletesInScopeAbsentPaths(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetParameter(testNS, "a", "a0")
	srv.SetParameter(testNS, "b", "b0")

	keep := ParameterValue{Key: "a", Default: "a-def"}
	gone := ParameterValue{Key: "b", Default: "b-def"}
	if err := keep.Init(c); err != nil {
		t.Fatalf("Init keep: %v", err)
	}
	if err := gone.Init(c); err != nil {
		t.Fatalf("Init gone: %v", err)
	}
	// Both share the one namespace-wide selector, so a single stream carries them.
	sub, err := srv.WaitForSubscribe(waitTimeout)
	if err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}

	// Snapshot keeps a present but omits b (deleted while disconnected). Both are
	// in the subscribed namespace, so b must revert while a stays.
	sub.PushSnapshot(30, paramstoretest.Param(testNS, "a", "a0", 1))

	if !eventually(t, waitTimeout, func() bool { return gone.Get() == "b-def" }) {
		t.Fatalf("in-scope absent param did not revert; Get = %q, want b-def", gone.Get())
	}
	if keep.Get() != "a0" {
		t.Errorf("present param was disturbed; Get = %q, want a0", keep.Get())
	}
}

// TestReconcileNotFoundRevertsToDefault proves the other half of M3: when the
// periodic reconcile fetch returns NotFound for a known hot-reload parameter,
// the handle reverts to Default.
func TestReconcileNotFoundRevertsToDefault(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetParameter(testNS, "rate", "100")

	pv := ParameterValue{Key: "rate", Default: "def"}
	if err := pv.Init(c); err != nil {
		t.Fatalf("Init: %v", err)
	}
	changes := make(chan [2]string, 8)
	pv.OnChange(func(old, new string) { changes <- [2]string{old, new} })
	if _, err := srv.WaitForSubscribe(waitTimeout); err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}

	// The parameter is deleted server-side and the stream missed the event.
	srv.RemoveParameter(testNS, "rate")
	c.sub.reconcile()

	if !eventually(t, waitTimeout, func() bool { return pv.Get() == "def" }) {
		t.Fatalf("reconcile did not revert deleted param to Default; Get = %q", pv.Get())
	}
	select {
	case ch := <-changes:
		if ch[0] != "100" || ch[1] != "def" {
			t.Errorf("OnChange = %v, want [100 def]", ch)
		}
	case <-time.After(waitTimeout):
		t.Fatal("OnChange not fired for reconcile deletion")
	}
}

// TestWatchStopEndsCtxWatcherGoroutine proves the L3 fix: stop() ends the
// per-watch context-watcher goroutine even when the caller passed a
// non-cancelable context, so it does not leak until Client.Close.
func TestWatchStopEndsCtxWatcherGoroutine(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	const pattern = "leak/*"

	// Start the subscription with one watcher and let startup goroutines settle
	// so the baseline count excludes stream churn.
	first, err := c.Watch(context.Background(), pattern, func(Event) {})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if _, err := srv.WaitForSubscribe(waitTimeout); err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	runtime.GC()
	base := runtime.NumGoroutine()

	// Reusing the same pattern avoids subscription restarts, so the goroutine
	// delta is exactly the per-watch ctx-watcher goroutines.
	const n = 40
	stops := []func(){first}
	for i := 0; i < n; i++ {
		stop, err := c.Watch(context.Background(), pattern, func(Event) {})
		if err != nil {
			t.Fatalf("Watch %d: %v", i, err)
		}
		stops = append(stops, stop)
	}
	if got := runtime.NumGoroutine(); got < base+n/2 {
		t.Fatalf("expected the ctx-watcher goroutines to be running; base=%d now=%d n=%d", base, got, n)
	}

	for _, s := range stops {
		s()
	}

	if !eventually(t, waitTimeout, func() bool {
		runtime.GC()
		return runtime.NumGoroutine() <= base+2
	}) {
		t.Errorf("ctx-watcher goroutines leaked after stop(): base=%d now=%d", base, runtime.NumGoroutine())
	}
}

// TestMatchPatternBaseKey pins the prefix-matches-base-key semantics of
// matchPattern: a "prefix/*" pattern must match both "/env/app/prefix" and
// everything beneath it, mirroring the server's keyutil.MatchKey.
func TestMatchPatternBaseKey(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"/prod/app/billing/*", "/prod/app/billing", true},        // base key (the fix)
		{"/prod/app/billing/*", "/prod/app/billing/stripe", true}, // child
		{"/prod/app/billing/*", "/prod/app/billing/x/y", true},    // deep child
		{"/prod/app/billing/*", "/prod/app/billingx", false},      // sibling, not under
		{"/prod/app/billing/*", "/prod/app/other", false},
		{"/prod/app/*", "/prod/app/anything", true},         // whole namespace
		{"/prod/app/*", "/prod/app2/anything", false},       // namespace boundary respected
		{"/prod/app/billing", "/prod/app/billing", true},    // exact
		{"/prod/app/billing", "/prod/app/billing/x", false}, // exact is not a prefix
	}
	for _, c := range cases {
		if got := matchPattern(c.pattern, c.path); got != c.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

// TestReconcilePrefix pins the server list-prefix derivation: "billing/*" must
// list with prefix "billing" (base + children), not "billing/" (matches nothing).
func TestReconcilePrefix(t *testing.T) {
	cases := map[string]string{
		"billing/*": "billing",
		"a/b/*":     "a/b",
		"*":         "",
		"":          "",
		"billing":   "billing",
	}
	for pattern, want := range cases {
		if got := reconcilePrefix(pattern); got != want {
			t.Errorf("reconcilePrefix(%q) = %q, want %q", pattern, got, want)
		}
	}
}

// TestWatchPrefixMatchesBaseKey proves a "prefix/*" watcher fires for the base
// key "prefix" itself. The server hub delivers base-key events to a prefix
// subscriber (keyutil.MatchKey), so the SDK's local dispatch must not drop them.
func TestWatchPrefixMatchesBaseKey(t *testing.T) {
	c, srv := newTestClient(t, Config{})

	events := make(chan Event, 4)
	stop, err := c.Watch(context.Background(), "billing/*", func(ev Event) { events <- ev })
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer stop()

	sub, err := srv.WaitForSubscribe(waitTimeout)
	if err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}
	// The base key "billing" (no child segment) is under "billing/*" per server
	// semantics; the callback must fire.
	sub.PushChange(2, testNS, "billing", "put", "5", 2)
	select {
	case ev := <-events:
		if ev.Key != "billing" || ev.Value != "5" {
			t.Errorf("base-key event = %+v, want key=billing value=5", ev)
		}
	case <-time.After(waitTimeout):
		t.Fatal("base-key event not delivered to billing/* watcher")
	}
}
