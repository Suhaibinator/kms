package core

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

func TestReleaseSubscriberNotifierCoalescesAndScopes(t *testing.T) {
	n := newReleaseSubscriberNotifier()
	ns := domain.NamespaceRef{Env: "prod", App: "app"}
	ch, cancel := n.Subscribe(ns, "runtime")
	other, cancelOther := n.Subscribe(ns, "batch")
	defer cancelOther()
	for i := 0; i < 5; i++ {
		n.Notify(ns, "runtime")
	}
	select {
	case <-ch:
	default:
		t.Fatal("expected a pending wakeup")
	}
	select {
	case <-ch:
		t.Fatal("burst must coalesce into one wakeup")
	default:
	}
	select {
	case <-other:
		t.Fatal("other release name must not be woken")
	default:
	}
	cancel()
	n.Notify(ns, "runtime")
	select {
	case <-ch:
		t.Fatal("cancelled subscription must not be woken")
	default:
	}
	if len(n.subs) != 1 {
		t.Fatalf("subscription map = %v", n.subs)
	}
}

func TestServiceNotifiesOnConnectionAckAndActivation(t *testing.T) {
	ctx := context.Background()
	st, err := storage.Open(filepath.Join(t.TempDir(), "kms.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ns := domain.NamespaceRef{Env: "prod", App: "app"}
	if _, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: ns, CreatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	ref := domain.Ref{NS: ns, Key: "config"}
	if _, _, err := st.PutParameter(ctx, ref, "1", "integer", "{}", "admin"); err != nil {
		t.Fatal(err)
	}
	svc := New(st, nil, "test")
	pr := adminPrincipal()
	wake, cancel := svc.SubscribeReleaseSubscribers(ns, "runtime")
	defer cancel()
	expectWake := func(step string) {
		t.Helper()
		select {
		case <-wake:
		case <-time.After(2 * time.Second):
			t.Fatalf("no wakeup after %s", step)
		}
	}
	release, err := svc.CreateConfigurationRelease(ctx, pr, domain.CreateConfigurationReleaseInput{Namespace: ns, Name: "runtime", Entries: []domain.ReleaseEntrySelector{{Alias: "config", Kind: domain.ReleaseEntryParameter, Ref: ref}}})
	if err != nil {
		t.Fatal(err)
	}
	active, _, err := svc.ActivateConfigurationRelease(ctx, pr, ns, "runtime", release.Version, nil)
	if err != nil {
		t.Fatal(err)
	}
	expectWake("activation")
	if err := svc.SetReleaseSubscriberConnected(ctx, ns, "runtime", "api", "i1", pr.Identity.Name, "conn-1", true); err != nil {
		t.Fatal(err)
	}
	expectWake("connection")
	if err := svc.AcknowledgeConfigurationRelease(ctx, pr, domain.ReleaseAcknowledgement{Namespace: ns, ReleaseName: "runtime", ReleaseVersion: release.Version, ActivationRevision: active.ActivationRevision, ClientName: "api", InstanceID: "i1", ConnectionID: "conn-1", State: domain.ReleaseStateApplied}); err != nil {
		t.Fatal(err)
	}
	expectWake("acknowledgement")
	snapshot, err := svc.GetReleaseRolloutSnapshot(ctx, pr, ns, "runtime")
	if err != nil || snapshot.CurrentRevision != active.ActivationRevision || snapshot.Summary.Total != 1 || snapshot.Summary.AppliedCurrent != 1 || len(snapshot.Subscribers) == 0 || snapshot.ServerTime.IsZero() {
		t.Fatalf("snapshot = %+v err=%v", snapshot, err)
	}
	if _, err := svc.GetReleaseRolloutSnapshot(ctx, clientPrincipal("c"), ns, "runtime"); err == nil {
		t.Fatal("snapshot must be admin-only")
	}
}
