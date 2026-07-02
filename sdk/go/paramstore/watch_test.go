package paramstore

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

const waitTimeout = 5 * time.Second

func TestDynamicParameterHotReload(t *testing.T) {
	c, srv := newTestClient(t, Config{ClientName: "test-app"})
	srv.SetParameter("/rate", "100")

	pv := ParameterValue{Key: "/rate", Dynamic: true}
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
	if !contains(sub.Paths, "/rate") {
		t.Errorf("subscription paths = %v, want to contain /rate", sub.Paths)
	}
	if sub.LastSeenRevision != 0 {
		t.Errorf("first LastSeenRevision = %d, want 0", sub.LastSeenRevision)
	}

	sub.PushChange(5, "/rate", "put", "200", 5)

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

func TestSnapshotNoSpuriousCallback(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetParameter("/rate", "100")

	pv := ParameterValue{Key: "/rate", Dynamic: true}
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
	sub.PushSnapshot(2, &kmsv1.Parameter{Path: "/rate", Value: "100", Version: 1})
	time.Sleep(150 * time.Millisecond)
	if n := atomic.LoadInt32(&fires); n != 0 {
		t.Errorf("OnChange fired %d times on no-op snapshot, want 0", n)
	}

	// Snapshot with a new value fires once.
	sub.PushSnapshot(3, &kmsv1.Parameter{Path: "/rate", Value: "300", Version: 2})
	if !eventually(t, waitTimeout, func() bool { return pv.Get() == "300" }) {
		t.Fatalf("snapshot value not applied; Get = %q", pv.Get())
	}
	if !eventually(t, waitTimeout, func() bool { return atomic.LoadInt32(&fires) == 1 }) {
		t.Errorf("OnChange fired %d times, want 1", atomic.LoadInt32(&fires))
	}
}

func TestHeartbeatAck(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetParameter("/rate", "1")
	pv := ParameterValue{Key: "/rate", Dynamic: true}
	if err := pv.Init(c); err != nil {
		t.Fatalf("Init: %v", err)
	}
	sub, err := srv.WaitForSubscribe(waitTimeout)
	if err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}

	sub.PushChange(4, "/rate", "put", "2", 4)
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
	srv.SetParameter("/rate", "1")
	pv := ParameterValue{Key: "/rate", Dynamic: true}
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
	sub1.PushChange(7, "/rate", "put", "70", 7)
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
	sub2.PushChange(8, "/rate", "put", "80", 8)
	if !eventually(t, waitTimeout, func() bool { return pv.Get() == "80" }) {
		t.Fatalf("post-reconnect change not applied; Get = %q", pv.Get())
	}
}

func TestIdempotentEventApplication(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetParameter("/rate", "1")
	pv := ParameterValue{Key: "/rate", Dynamic: true}
	if err := pv.Init(c); err != nil {
		t.Fatalf("Init: %v", err)
	}
	var fires int32
	pv.OnChange(func(old, new string) { atomic.AddInt32(&fires, 1) })

	sub, err := srv.WaitForSubscribe(waitTimeout)
	if err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}

	sub.PushChange(5, "/rate", "put", "A", 5)
	if !eventually(t, waitTimeout, func() bool { return pv.Get() == "A" }) {
		t.Fatal("first change not applied")
	}
	// Duplicate/old revision must be ignored.
	sub.PushChange(5, "/rate", "put", "B", 5)
	sub.PushChange(3, "/rate", "put", "C", 3)
	time.Sleep(150 * time.Millisecond)
	if pv.Get() != "A" {
		t.Errorf("value = %q, want A (stale events ignored)", pv.Get())
	}
	// A newer revision applies.
	sub.PushChange(6, "/rate", "put", "D", 6)
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
	srv.SetParameter("/rate", "100")

	pv := ParameterValue{Key: "/rate", EnvVar: "PINNED", Dynamic: true}
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
	stop, err := c.Watch(context.Background(), "/prod/payments/*", func(ev Event) { events <- ev })
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer stop()

	sub, err := srv.WaitForSubscribe(waitTimeout)
	if err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}
	if !contains(sub.Paths, "/prod/payments/*") {
		t.Errorf("paths = %v, want prefix pattern", sub.Paths)
	}

	sub.PushChange(2, "/prod/payments/rate", "put", "9", 2)
	select {
	case ev := <-events:
		if ev.Type != EventPut || ev.Path != "/prod/payments/rate" || ev.Value != "9" {
			t.Errorf("event = %+v", ev)
		}
	case <-time.After(waitTimeout):
		t.Fatal("watch event not delivered")
	}

	// A non-matching path must not deliver.
	sub.PushChange(3, "/prod/other/x", "put", "1", 3)
	select {
	case ev := <-events:
		t.Errorf("unexpected event for non-matching path: %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestWatchSecretChangeInvalidatesCache(t *testing.T) {
	c, srv := newTestClient(t, Config{CacheTTL: time.Minute})
	srv.SetSecret("/prod/db/pw", []byte("v1"))

	// Prime the cache.
	if s, _ := c.GetSecret(context.Background(), "/prod/db/pw"); s.StringValue() != "v1" {
		t.Fatalf("prime = %q", s.StringValue())
	}

	events := make(chan Event, 4)
	stop, err := c.Watch(context.Background(), "/prod/db/*", func(ev Event) { events <- ev })
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer stop()
	sub, err := srv.WaitForSubscribe(waitTimeout)
	if err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}

	// Rotate the secret on the server and announce it via metadata change.
	srv.SetSecret("/prod/db/pw", []byte("v2"))
	sub.PushSecretChange(4, "/prod/db/pw", "put", 2)

	select {
	case ev := <-events:
		if ev.Type != EventSecretChange || ev.Path != "/prod/db/pw" {
			t.Errorf("event = %+v", ev)
		}
	case <-time.After(waitTimeout):
		t.Fatal("secret change event not delivered")
	}

	// After invalidation, a fresh read should see the rotated value.
	if !eventually(t, waitTimeout, func() bool {
		s, _ := c.GetSecret(context.Background(), "/prod/db/pw")
		return s.StringValue() == "v2"
	}) {
		t.Error("secret cache not invalidated by secret_change event")
	}
}

func TestSlowCallbackDoesNotStallStream(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetParameter("/rate", "0")
	pv := ParameterValue{Key: "/rate", Dynamic: true}
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
	sub.PushChange(2, "/rate", "put", "1", 2)
	if !eventually(t, waitTimeout, func() bool { return started.Load() }) {
		t.Fatal("callback did not start")
	}
	// Even though the callback is stuck, further value updates must still apply.
	sub.PushChange(3, "/rate", "put", "2", 3)
	if !eventually(t, waitTimeout, func() bool { return pv.Get() == "2" }) {
		t.Errorf("value did not update while callback blocked; Get = %q", pv.Get())
	}
}

func TestReconcileAppliesMissedDrift(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetParameter("/rate", "1")
	srv.SetParameter("/prod/x/y", "old")

	pv := ParameterValue{Key: "/rate", Dynamic: true}
	if err := pv.Init(c); err != nil {
		t.Fatalf("Init: %v", err)
	}
	watchEvents := make(chan Event, 8)
	stop, err := c.Watch(context.Background(), "/prod/x/*", func(ev Event) { watchEvents <- ev })
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer stop()
	if _, err := srv.WaitForSubscribe(waitTimeout); err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}

	// Simulate events missed by the stream: change values server-side without
	// pushing any SubscribeEvent.
	srv.SetParameter("/rate", "999")
	srv.SetParameter("/prod/x/y", "new")

	// The periodic safety net reconciles both the exact dynamic parameter and
	// the prefix watcher.
	c.sub.reconcile()

	if pv.Get() != "999" {
		t.Errorf("reconcile did not apply drift to dynamic param; Get = %q", pv.Get())
	}
	select {
	case ev := <-watchEvents:
		if ev.Path != "/prod/x/y" || ev.Value != "new" {
			t.Errorf("reconcile watch event = %+v", ev)
		}
	case <-time.After(waitTimeout):
		t.Fatal("reconcile did not deliver watcher event for prefix drift")
	}
}

func TestWatchStopViaContextCancel(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	ctx, cancel := context.WithCancel(context.Background())

	_, err := c.Watch(ctx, "/prod/*", func(ev Event) {})
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
	srv.SetParameter("/rate", "V1")

	pv := ParameterValue{Key: "/rate", Dynamic: true}
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

	// While reconcile reads /rate, a fresher change (V2 at revision 5) lands and
	// is fully applied. The server value is deliberately left at "V1", so the
	// reconcile fetch returns the now-stale value. The fence must drop it.
	var once sync.Once
	srv.SetGetParameterHook(func(path string) {
		if path != "/rate" {
			return
		}
		once.Do(func() {
			sub.PushChange(5, "/rate", "put", "V2", 5)
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
// resync snapshot that omits a previously-known dynamic parameter (deleted while
// disconnected) reverts the handle to its Default and fires OnChange.
func TestSnapshotResyncRevertsDeletedParamToDefault(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetParameter("/rate", "100")

	pv := ParameterValue{Key: "/rate", Dynamic: true, Default: "def"}
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
	// snapshot of currently-present params for the subscribed paths. /rate is
	// absent because it was deleted while we were disconnected.
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

// TestSnapshotOnlyDeletesInScopeAbsentPaths proves the M3 scope restriction:
// among known paths, a snapshot reverts only those that fall within the
// subscribed pattern set and are absent from the snapshot; present paths are
// preserved.
func TestSnapshotOnlyDeletesInScopeAbsentPaths(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetParameter("/a", "a0")
	srv.SetParameter("/b", "b0")

	keep := ParameterValue{Key: "/a", Dynamic: true, Default: "a-def"}
	gone := ParameterValue{Key: "/b", Dynamic: true, Default: "b-def"}
	if err := keep.Init(c); err != nil {
		t.Fatalf("Init keep: %v", err)
	}
	if err := gone.Init(c); err != nil {
		t.Fatalf("Init gone: %v", err)
	}
	// Registering the second dynamic param reconnects with a larger path set, so
	// wait for the stream that carries both before pushing the snapshot.
	sub, err := srv.WaitForSubscribe(waitTimeout)
	if err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}
	for !contains(sub.Paths, "/a") || !contains(sub.Paths, "/b") {
		sub, err = srv.WaitForSubscribe(waitTimeout)
		if err != nil {
			t.Fatalf("WaitForSubscribe: %v", err)
		}
	}

	// Snapshot keeps /a present but omits /b (deleted while disconnected). Both
	// are in the subscribed scope, so /b must revert while /a stays.
	sub.PushSnapshot(30, &kmsv1.Parameter{Path: "/a", Value: "a0", Version: 1})

	if !eventually(t, waitTimeout, func() bool { return gone.Get() == "b-def" }) {
		t.Fatalf("in-scope absent param did not revert; Get = %q, want b-def", gone.Get())
	}
	if keep.Get() != "a0" {
		t.Errorf("present param was disturbed; Get = %q, want a0", keep.Get())
	}
}

// TestReconcileNotFoundRevertsToDefault proves the other half of M3: when the
// periodic reconcile fetch returns NotFound for a known dynamic parameter, the
// handle reverts to Default.
func TestReconcileNotFoundRevertsToDefault(t *testing.T) {
	c, srv := newTestClient(t, Config{})
	srv.SetParameter("/rate", "100")

	pv := ParameterValue{Key: "/rate", Dynamic: true, Default: "def"}
	if err := pv.Init(c); err != nil {
		t.Fatalf("Init: %v", err)
	}
	changes := make(chan [2]string, 8)
	pv.OnChange(func(old, new string) { changes <- [2]string{old, new} })
	if _, err := srv.WaitForSubscribe(waitTimeout); err != nil {
		t.Fatalf("WaitForSubscribe: %v", err)
	}

	// The parameter is deleted server-side and the stream missed the event.
	srv.RemoveParameter("/rate")
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
	const pattern = "/leak/*"

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

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
