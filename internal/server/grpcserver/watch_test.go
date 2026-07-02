package grpcserver

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
)

// eventStream is the common Recv surface of the bidi and server-streaming watch
// clients.
type eventStream interface {
	Recv() (*kmsv1.SubscribeEvent, error)
}

// recvMatching reads events until pred is satisfied, failing on stream error
// (including the stream-context deadline).
func recvMatching(t *testing.T, s eventStream, pred func(*kmsv1.SubscribeEvent) bool) *kmsv1.SubscribeEvent {
	t.Helper()
	for {
		ev, err := s.Recv()
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		if pred(ev) {
			return ev
		}
	}
}

func isSnapshot(e *kmsv1.SubscribeEvent) bool { return e.GetSnapshot() != nil }
func isParamChange(e *kmsv1.SubscribeEvent) bool {
	return e.GetChange() != nil
}
func isSecretChange(e *kmsv1.SubscribeEvent) bool { return e.GetSecretChange() != nil }
func isHeartbeat(e *kmsv1.SubscribeEvent) bool    { return e.GetHeartbeat() != nil }

func TestSubscribe_SnapshotThenLiveThenAck(t *testing.T) {
	env := newTestEnv(t, true)
	ctx, cancel := context.WithTimeout(adminCtx(), 5*time.Second)
	defer cancel()

	stream, err := env.watchClient().Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := stream.Send(&kmsv1.SubscribeRequest{
		ClientName: "app-1",
		Paths:      []string{"/cfg/*"},
	}); err != nil {
		t.Fatalf("send registration: %v", err)
	}

	// 1. Initial snapshot (empty store).
	snap := recvMatching(t, stream, isSnapshot)
	if len(snap.GetSnapshot().GetParameters()) != 0 {
		t.Fatalf("expected empty snapshot, got %+v", snap.GetSnapshot().GetParameters())
	}

	// 2. A write shows up as a live parameter change carrying the value.
	if _, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Path: "/cfg/db", Value: "postgres://x"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	change := recvMatching(t, stream, isParamChange)
	pc := change.GetChange()
	if pc.GetPath() != "/cfg/db" || pc.GetValue() != "postgres://x" || pc.GetChangeType() != domain.ChangePut {
		t.Fatalf("change = %+v", pc)
	}
	if change.GetRevision() == 0 {
		t.Fatal("change revision should be non-zero")
	}

	// 3. Ack the applied revision; the registry must reflect it.
	if err := stream.Send(&kmsv1.SubscribeRequest{AckedRevision: change.GetRevision()}); err != nil {
		t.Fatalf("send ack: %v", err)
	}
	waitForSubscriber(t, env, func(s *kmsv1.Subscriber) bool {
		return s.GetClientName() == "app-1" && s.GetLastAckedRevision() == change.GetRevision()
	})

	// 4. Heartbeats flow and carry the last-sent revision.
	hb := recvMatching(t, stream, isHeartbeat)
	if hb.GetRevision() != change.GetRevision() {
		t.Fatalf("heartbeat revision = %d, want %d", hb.GetRevision(), change.GetRevision())
	}
}

func TestSubscribe_SecretEventCarriesNoValue(t *testing.T) {
	env := newTestEnv(t, true)
	ctx, cancel := context.WithTimeout(adminCtx(), 5*time.Second)
	defer cancel()

	stream, err := env.watchClient().Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := stream.Send(&kmsv1.SubscribeRequest{ClientName: "app", Paths: []string{"/s/*"}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	recvMatching(t, stream, isSnapshot)

	// A secret change is injected with a poisoned value in the change log.
	env.store.injectSecretChange("/s/db", domain.ChangePut, 3)
	env.hub.Wake()

	ev := recvMatching(t, stream, isSecretChange)
	sc := ev.GetSecretChange()
	if sc.GetPath() != "/s/db" || sc.GetChangeType() != domain.ChangePut || sc.GetVersion() != 3 {
		t.Fatalf("secret change = %+v", sc)
	}
	// The event must be a metadata-only secret change: no parameter change (the
	// only field that could carry a value) is present.
	if ev.GetChange() != nil {
		t.Fatal("secret change must not be delivered as a ParameterChange with a value")
	}
}

func TestSubscribe_AuthorizationFiltersEvents(t *testing.T) {
	env := newTestEnv(t, true)
	// Grant the client read on /cfg/pub/* only.
	env.store.addPolicy(domain.Policy{
		Name:    "pub-read",
		Subject: "client",
		Allow:   []domain.PolicyRule{{Operation: domain.OpParameterRead, Path: "/cfg/pub/*"}},
	})

	ctx, cancel := context.WithTimeout(clientCtx(), 5*time.Second)
	defer cancel()
	stream, err := env.watchClient().Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := stream.Send(&kmsv1.SubscribeRequest{ClientName: "c", Paths: []string{"/cfg/*"}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	recvMatching(t, stream, isSnapshot)

	// Admin writes both a private and a public parameter.
	if _, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Path: "/cfg/priv/secret", Value: "no"}); err != nil {
		t.Fatalf("put priv: %v", err)
	}
	if _, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Path: "/cfg/pub/ok", Value: "yes"}); err != nil {
		t.Fatalf("put pub: %v", err)
	}

	// The first (and only permitted) change the client sees must be the public one.
	change := recvMatching(t, stream, isParamChange)
	if change.GetChange().GetPath() != "/cfg/pub/ok" {
		t.Fatalf("client received unauthorized path: %+v", change.GetChange())
	}
}

func TestSubscribe_ReplayOnReconnect(t *testing.T) {
	env := newTestEnv(t, true)
	// Seed two revisions before subscribing.
	if _, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Path: "/cfg/a", Value: "1"}); err != nil {
		t.Fatalf("put a: %v", err)
	}
	putB, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Path: "/cfg/b", Value: "2"})
	if err != nil {
		t.Fatalf("put b: %v", err)
	}

	ctx, cancel := context.WithTimeout(adminCtx(), 5*time.Second)
	defer cancel()
	stream, err := env.watchClient().Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	// Reconnect claiming we already applied revision 1: expect a replay (not a
	// snapshot) of the change at revision 2.
	if err := stream.Send(&kmsv1.SubscribeRequest{
		ClientName:       "app",
		Paths:            []string{"/cfg/*"},
		LastSeenRevision: 1,
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	change := recvMatching(t, stream, isParamChange)
	if change.GetChange().GetPath() != "/cfg/b" {
		t.Fatalf("replay = %+v, want /cfg/b", change.GetChange())
	}
	if change.GetRevision() != putB.GetRevision() {
		t.Fatalf("replay revision = %d, want %d", change.GetRevision(), putB.GetRevision())
	}
}

func TestWatchParameter_ServerStreaming(t *testing.T) {
	env := newTestEnv(t, true)
	ctx, cancel := context.WithTimeout(adminCtx(), 5*time.Second)
	defer cancel()

	stream, err := env.watchClient().WatchParameter(ctx, &kmsv1.WatchParameterRequest{Path: "/cfg/only"})
	if err != nil {
		t.Fatalf("watch parameter: %v", err)
	}
	recvMatching(t, stream, isSnapshot)

	// A write to a different path must not be delivered; the matching one must.
	if _, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Path: "/cfg/other", Value: "x"}); err != nil {
		t.Fatalf("put other: %v", err)
	}
	if _, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Path: "/cfg/only", Value: "y"}); err != nil {
		t.Fatalf("put only: %v", err)
	}
	change := recvMatching(t, stream, isParamChange)
	if change.GetChange().GetPath() != "/cfg/only" {
		t.Fatalf("watch parameter delivered %+v, want /cfg/only", change.GetChange())
	}
}

func TestWatchNamespace_ServerStreaming(t *testing.T) {
	env := newTestEnv(t, true)
	ctx, cancel := context.WithTimeout(adminCtx(), 5*time.Second)
	defer cancel()

	// A plain path is treated as its whole subtree.
	stream, err := env.watchClient().WatchNamespace(ctx, &kmsv1.WatchNamespaceRequest{PathPrefix: "/ns/app"})
	if err != nil {
		t.Fatalf("watch namespace: %v", err)
	}
	recvMatching(t, stream, isSnapshot)

	if _, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Path: "/ns/app/feature", Value: "on"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	change := recvMatching(t, stream, isParamChange)
	if change.GetChange().GetPath() != "/ns/app/feature" {
		t.Fatalf("watch namespace delivered %+v", change.GetChange())
	}
}

func TestSubscribe_InvalidPatternRejected(t *testing.T) {
	env := newTestEnv(t, true)
	ctx, cancel := context.WithTimeout(adminCtx(), 3*time.Second)
	defer cancel()
	stream, err := env.watchClient().Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := stream.Send(&kmsv1.SubscribeRequest{ClientName: "app", Paths: []string{"no-leading-slash"}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	// The invalid pattern must terminate the stream with InvalidArgument.
	_, err = stream.Recv()
	if codeOf(err) != codes.InvalidArgument {
		t.Fatalf("recv err code = %v, want InvalidArgument", codeOf(err))
	}
}

// drainUntilErr reads events until the stream returns an error, which it
// returns.
func drainUntilErr(s eventStream) error {
	for {
		if _, err := s.Recv(); err != nil {
			return err
		}
	}
}

func TestSubscribe_ReauthRevocationClosesStream(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addPolicy(domain.Policy{
		Name:    "cfg-read",
		Subject: "client",
		Allow:   []domain.PolicyRule{{Operation: domain.OpParameterRead, Path: "/cfg/*"}},
	})

	ctx, cancel := context.WithTimeout(clientCtx(), 5*time.Second)
	defer cancel()
	stream, err := env.watchClient().Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := stream.Send(&kmsv1.SubscribeRequest{ClientName: "c", Paths: []string{"/cfg/*"}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	recvMatching(t, stream, isSnapshot)

	// Confirm the client is receiving events while authorized.
	if _, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Path: "/cfg/a", Value: "1"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	recvMatching(t, stream, isParamChange)

	// Revoke the identity. The next heartbeat's re-authorization must fail and
	// close the stream with Unauthenticated.
	if err := env.store.SetIdentityDisabled(context.Background(), "client", true); err != nil {
		t.Fatalf("disable identity: %v", err)
	}
	closeErr := drainUntilErr(stream)
	if codeOf(closeErr) != codes.Unauthenticated {
		t.Fatalf("stream close code = %v, want Unauthenticated", codeOf(closeErr))
	}
}

func TestSubscribe_ReauthPolicyChangeStopsEvents(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addPolicy(domain.Policy{
		Name:    "cfg-read",
		Subject: "client",
		Allow:   []domain.PolicyRule{{Operation: domain.OpParameterRead, Path: "/cfg/*"}},
	})

	ctx, cancel := context.WithTimeout(clientCtx(), 5*time.Second)
	defer cancel()
	stream, err := env.watchClient().Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := stream.Send(&kmsv1.SubscribeRequest{ClientName: "c", Paths: []string{"/cfg/*"}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	recvMatching(t, stream, isSnapshot)
	if _, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Path: "/cfg/a", Value: "1"}); err != nil {
		t.Fatalf("put a: %v", err)
	}
	recvMatching(t, stream, isParamChange)

	// Remove the client's read policy. The identity stays valid, so the stream
	// stays open, but re-authorization on the next heartbeats swaps in a
	// deny-all predicate.
	env.store.clearPolicies()
	// Give a few heartbeats to apply the refreshed predicate.
	time.Sleep(4 * env.hub.HeartbeatInterval())

	// A write to the now-denied path must not reach the client.
	if _, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Path: "/cfg/b", Value: "2"}); err != nil {
		t.Fatalf("put b: %v", err)
	}

	// Read across several heartbeats; no parameter change may appear.
	heartbeats := 0
	for heartbeats < 4 {
		ev, err := stream.Recv()
		if err != nil {
			t.Fatalf("unexpected stream error: %v", err)
		}
		if ev.GetHeartbeat() != nil {
			heartbeats++
		}
		if pc := ev.GetChange(); pc != nil {
			t.Fatalf("event delivered after authorization revoked: %+v", pc)
		}
	}
}

// waitForSubscriber polls ListSubscribers until pred matches a subscriber, or
// fails after a timeout.
func waitForSubscriber(t *testing.T, env *testEnv, pred func(*kmsv1.Subscriber) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := env.admin().ListSubscribers(adminCtx(), &kmsv1.ListSubscribersRequest{})
		if err == nil {
			for _, s := range resp.GetSubscribers() {
				if pred(s) {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("subscriber condition not met within timeout")
}
