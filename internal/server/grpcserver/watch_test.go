package grpcserver

import (
	"context"
	"slices"
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
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "app"})
	ctx, cancel := context.WithTimeout(adminCtx(), 5*time.Second)
	defer cancel()

	stream, err := env.watchClient().Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := stream.Send(&kmsv1.SubscribeRequest{
		ClientName: "app-1",
		Namespaces: []*kmsv1.NamespaceRef{pNS("prod", "app")},
	}); err != nil {
		t.Fatalf("send registration: %v", err)
	}

	// 1. Initial snapshot (empty store).
	snap := recvMatching(t, stream, isSnapshot)
	if len(snap.GetSnapshot().GetParameters()) != 0 {
		t.Fatalf("expected empty snapshot, got %+v", snap.GetSnapshot().GetParameters())
	}

	// 2. A write shows up as a live parameter change carrying the value.
	if _, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Ref: pRef("prod", "app", "db"), Value: "postgres://x"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	change := recvMatching(t, stream, isParamChange)
	pc := change.GetChange()
	if pc.GetRef().GetKey() != "db" || pc.GetRef().GetNamespace().GetApp() != "app" ||
		pc.GetValue() != "postgres://x" || pc.GetChangeType() != domain.ChangePut {
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
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "app"})
	ctx, cancel := context.WithTimeout(adminCtx(), 5*time.Second)
	defer cancel()

	stream, err := env.watchClient().Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := stream.Send(&kmsv1.SubscribeRequest{ClientName: "app", Namespaces: []*kmsv1.NamespaceRef{pNS("prod", "app")}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	recvMatching(t, stream, isSnapshot)

	// A secret change is injected with a poisoned value in the change log.
	env.store.injectSecretChange(ref("prod", "app", "db"), domain.ChangePut, 3)
	env.hub.Wake()

	ev := recvMatching(t, stream, isSecretChange)
	sc := ev.GetSecretChange()
	if sc.GetRef().GetKey() != "db" || sc.GetChangeType() != domain.ChangePut || sc.GetVersion() != 3 {
		t.Fatalf("secret change = %+v", sc)
	}
	// The event must be a metadata-only secret change: no parameter change (the
	// only field that could carry a value) is present.
	if ev.GetChange() != nil {
		t.Fatal("secret change must not be delivered as a ParameterChange with a value")
	}
}

// TestSubscribe_DeliversWholeNamespace proves the namespace-level model: once a
// client is admitted to a namespace it receives EVERY change in it, including
// keys it never "selected" — there is no per-key filtering on the stream.
func TestSubscribe_DeliversWholeNamespace(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "app"}, domain.AuthMethodToken)
	env.store.addPolicy(domain.Policy{
		Name:    "cfg",
		Subject: "client",
		Allow:   []domain.PolicyRule{{Operation: domain.OpParameterRead, Env: "prod", App: "app"}},
	})

	ctx, cancel := context.WithTimeout(clientCtx(), 5*time.Second)
	defer cancel()
	stream, err := env.watchClient().Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := stream.Send(&kmsv1.SubscribeRequest{ClientName: "c", Namespaces: []*kmsv1.NamespaceRef{pNS("prod", "app")}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	recvMatching(t, stream, isSnapshot)

	// Admin writes two keys in the namespace, in order.
	if _, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Ref: pRef("prod", "app", "priv/secret"), Value: "no"}); err != nil {
		t.Fatalf("put priv: %v", err)
	}
	if _, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Ref: pRef("prod", "app", "pub/ok"), Value: "yes"}); err != nil {
		t.Fatalf("put pub: %v", err)
	}

	// The client receives both keys, in write order — no key is filtered out.
	first := recvMatching(t, stream, isParamChange)
	if first.GetChange().GetRef().GetKey() != "priv/secret" {
		t.Fatalf("first change = %+v, want priv/secret (namespace-wide delivery)", first.GetChange())
	}
	second := recvMatching(t, stream, isParamChange)
	if second.GetChange().GetRef().GetKey() != "pub/ok" {
		t.Fatalf("second change = %+v, want pub/ok", second.GetChange())
	}
}

func TestSubscribe_ReplayOnReconnect(t *testing.T) {
	env := newTestEnv(t, true)
	// Seed two revisions before subscribing.
	if _, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Ref: pRef("prod", "app", "a"), Value: "1"}); err != nil {
		t.Fatalf("put a: %v", err)
	}
	putB, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Ref: pRef("prod", "app", "b"), Value: "2"})
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
		Namespaces:       []*kmsv1.NamespaceRef{pNS("prod", "app")},
		LastSeenRevision: 1,
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	change := recvMatching(t, stream, isParamChange)
	if change.GetChange().GetRef().GetKey() != "b" {
		t.Fatalf("replay = %+v, want key b", change.GetChange())
	}
	if change.GetRevision() != putB.GetRevision() {
		t.Fatalf("replay revision = %d, want %d", change.GetRevision(), putB.GetRevision())
	}
}

func TestSubscribe_InvalidNamespaceRejected(t *testing.T) {
	env := newTestEnv(t, true)
	ctx, cancel := context.WithTimeout(adminCtx(), 3*time.Second)
	defer cancel()
	stream, err := env.watchClient().Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	// An empty namespace is invalid.
	if err := stream.Send(&kmsv1.SubscribeRequest{ClientName: "app", Namespaces: []*kmsv1.NamespaceRef{pNS("", "")}}); err != nil {
		t.Fatalf("send: %v", err)
	}
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
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "app"}, domain.AuthMethodToken)
	env.store.addPolicy(domain.Policy{
		Name:    "cfg-read",
		Subject: "client",
		Allow:   []domain.PolicyRule{{Operation: domain.OpParameterRead, Env: "prod", App: "app"}},
	})

	ctx, cancel := context.WithTimeout(clientCtx(), 5*time.Second)
	defer cancel()
	stream, err := env.watchClient().Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := stream.Send(&kmsv1.SubscribeRequest{ClientName: "c", Namespaces: []*kmsv1.NamespaceRef{pNS("prod", "app")}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	recvMatching(t, stream, isSnapshot)

	// Confirm the client is receiving events while authorized.
	if _, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Ref: pRef("prod", "app", "a"), Value: "1"}); err != nil {
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

// TestSubscribe_PolicyRevocationClosesLiveStream pins that a mid-stream
// revocation of a client's EXPLICIT namespace grant tears the live stream down
// on the next heartbeat re-authorization — consistent with identity-disable and
// method-tightening. (A home-bound subscriber keeps its implicit grant and is
// unaffected; see TestSubscribe_HomeNamespaceGrantAllows.)
func TestSubscribe_PolicyRevocationClosesLiveStream(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "app"}, domain.AuthMethodToken)
	// "client" is UNBOUND — it reaches prod/app only through this explicit grant,
	// so clearing it genuinely revokes its access.
	env.store.addPolicy(domain.Policy{
		Name:    "cfg-read",
		Subject: "client",
		Allow:   []domain.PolicyRule{{Operation: domain.OpParameterRead, Env: "prod", App: "app"}},
	})

	ctx, cancel := context.WithTimeout(clientCtx(), 5*time.Second)
	defer cancel()
	stream, err := env.watchClient().Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := stream.Send(&kmsv1.SubscribeRequest{ClientName: "c", Namespaces: []*kmsv1.NamespaceRef{pNS("prod", "app")}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	recvMatching(t, stream, isSnapshot)
	if _, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Ref: pRef("prod", "app", "a"), Value: "1"}); err != nil {
		t.Fatalf("put a: %v", err)
	}
	recvMatching(t, stream, isParamChange)

	// Revoke the client's read grant. The next heartbeat's re-authorization must
	// fail and close the stream with PermissionDenied.
	env.store.clearPolicies()
	closeErr := drainUntilErr(stream)
	if codeOf(closeErr) != codes.PermissionDenied {
		t.Fatalf("stream close code = %v, want PermissionDenied", codeOf(closeErr))
	}
}

// TestSubscribe_HomeGrantSurvivesPolicyChange is the counterpart to
// TestSubscribe_PolicyRevocationClosesLiveStream: a subscriber bound to its home
// namespace holds an implicit grant that is not policy-derived, so clearing all
// policies mid-stream leaves several heartbeats of re-authorization untouched and
// the stream keeps delivering.
func TestSubscribe_HomeGrantSurvivesPolicyChange(t *testing.T) {
	env := newTestEnv(t, true)
	ns := domain.NamespaceRef{Env: "prod", App: "home"}
	env.store.addNamespace(ns, domain.AuthMethodToken)
	env.store.addIdentity("homeclient", domain.IdentityKindClient, "home-token", &ns)

	ctx, cancel := context.WithTimeout(authCtx("home-token"), 5*time.Second)
	defer cancel()
	stream, err := env.watchClient().Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := stream.Send(&kmsv1.SubscribeRequest{ClientName: "home", Namespaces: []*kmsv1.NamespaceRef{pNS("prod", "home")}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	recvMatching(t, stream, isSnapshot)
	if _, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Ref: pRef("prod", "home", "a"), Value: "1"}); err != nil {
		t.Fatalf("put a: %v", err)
	}
	recvMatching(t, stream, isParamChange)

	// Clear all policies and let several heartbeats re-authorize; the implicit home
	// grant persists, so the stream stays open and still delivers.
	env.store.clearPolicies()
	time.Sleep(4 * env.hub.HeartbeatInterval())

	if _, err := env.param().PutParameter(adminCtx(), &kmsv1.PutParameterRequest{Ref: pRef("prod", "home", "b"), Value: "2"}); err != nil {
		t.Fatalf("put b: %v", err)
	}
	change := recvMatching(t, stream, isParamChange)
	if change.GetChange().GetRef().GetKey() != "b" {
		t.Fatalf("change = %+v, want key b (home grant unaffected by policy change)", change.GetChange())
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
			if slices.ContainsFunc(resp.GetSubscribers(), pred) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("subscriber condition not met within timeout")
}
