package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

func TestReleaseAcknowledgementUnavailableRejectsWithoutPersisting(t *testing.T) {
	for _, pruneChanges := range []bool{false, true} {
		t.Run(map[bool]string{false: "retained changelog", true: "pruned changelog"}[pruneChanges], func(t *testing.T) {
			st, tracks := trackFixture(t)
			ctx := context.Background()
			track := tracks[1]
			old := seedReleaseAcknowledgementActivation(t, st, track)
			previous := seedReleaseAcknowledgementActivation(t, st, track)
			current := seedReleaseAcknowledgementActivation(t, st, track)
			conn := domain.ReleaseSubscriberConnection{Namespace: track.Namespace, ReleaseName: track.Name, SchemaVersion: track.SchemaVersion, ClientName: "client", InstanceID: "instance", Identity: "identity", ConnectionID: "1", Connected: true, ServerTimestamp: time.Now()}
			if err := st.SetReleaseInstanceConnected(ctx, conn); err != nil {
				t.Fatal(err)
			}
			ack := domain.ReleaseAcknowledgement{Namespace: track.Namespace, ReleaseName: track.Name, SchemaVersion: track.SchemaVersion, ReleaseVersion: old.Release.Version, ActivationRevision: old.ActivationRevision, ClientName: conn.ClientName, InstanceID: conn.InstanceID, Identity: conn.Identity, ConnectionID: conn.ConnectionID, State: domain.ReleaseStateRejected, ServerTimestamp: time.Now()}
			if _, err := st.PruneConfigurationReleases(ctx, time.Nanosecond, 100); err != nil {
				t.Fatal(err)
			}
			if pruneChanges {
				if _, err := st.PruneChangeLog(ctx, time.Nanosecond, 0); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := st.GetConfigurationRelease(ctx, track, old.Release.Version); err != nil {
				t.Fatalf("retained release: %v", err)
			}
			var unavailable *domain.ReleaseAcknowledgementUnavailableError
			if err := st.UpsertReleaseAcknowledgement(ctx, ack); !errors.As(err, &unavailable) || !errors.Is(err, domain.ErrFailedPrecondition) {
				t.Fatalf("expired ACK: %v", err)
			}
			rows, _, err := st.ListReleaseAcknowledgements(ctx, domain.ReleaseFilter{Namespace: track.Namespace, Name: track.Name, SchemaVersion: &track.SchemaVersion}, ListPage{})
			if err != nil || len(rows) != 1 || rows[0].State != "" {
				t.Fatalf("unavailable ACK persisted: %+v %v", rows, err)
			}
			for _, active := range []domain.ActiveConfigurationRelease{previous, current} {
				ack.ReleaseVersion, ack.ActivationRevision = active.Release.Version, active.ActivationRevision
				if err := st.UpsertReleaseAcknowledgement(ctx, ack); err != nil {
					t.Fatalf("protected activation ACK: %v", err)
				}
			}
			if pruneChanges {
				// Keep one newer inactive release so retention can remove the
				// oldest manifest while preserving the active/previous pair.
				if _, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{Namespace: track.Namespace, Name: track.Name, SchemaVersion: track.SchemaVersion, Digest: "new-inactive"}); err != nil {
					t.Fatal(err)
				}
				if _, err := st.PruneConfigurationReleases(ctx, time.Nanosecond, 1); err != nil {
					t.Fatal(err)
				}
				if _, err := st.GetConfigurationRelease(ctx, track, old.Release.Version); !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("expired release remains: %v", err)
				}
			}
			// Recreate the transport registration after its disconnected state was
			// retained/pruned. Missing historical evidence still rejects explicitly.
			conn.Connected = false
			if err := st.SetReleaseInstanceConnected(ctx, conn); err != nil {
				t.Fatal(err)
			}
			if _, err := st.PruneReleaseAcknowledgements(ctx, time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			conn.Connected = true
			conn.ConnectionID = "2"
			if err := st.SetReleaseInstanceConnected(ctx, conn); err != nil {
				t.Fatal(err)
			}
			ack.ReleaseVersion, ack.ActivationRevision = old.Release.Version, old.ActivationRevision
			if err := st.UpsertReleaseAcknowledgement(ctx, ack); !errors.Is(err, domain.ErrAborted) {
				t.Fatalf("obsolete ownership accepted: %v", err)
			}
			ack.ConnectionID = conn.ConnectionID
			if err := st.UpsertReleaseAcknowledgement(ctx, ack); !errors.As(err, &unavailable) {
				t.Fatalf("offline expired ACK: %v", err)
			}
		})
	}
}

func TestReleaseAcknowledgementKnownMismatchedActivationStillRejects(t *testing.T) {
	st, tracks := trackFixture(t)
	ctx := context.Background()
	wanted, foreign := tracks[0], tracks[1]
	current := seedReleaseAcknowledgementActivation(t, st, wanted)
	other := seedReleaseAcknowledgementActivation(t, st, foreign)
	conn := domain.ReleaseSubscriberConnection{Namespace: wanted.Namespace, ReleaseName: wanted.Name, ClientName: "c", InstanceID: "i", Identity: "identity", ConnectionID: "1", Connected: true, ServerTimestamp: time.Now()}
	if err := st.SetReleaseInstanceConnected(ctx, conn); err != nil {
		t.Fatal(err)
	}
	ack := domain.ReleaseAcknowledgement{Namespace: wanted.Namespace, ReleaseName: wanted.Name, ReleaseVersion: current.Release.Version, ActivationRevision: other.ActivationRevision, ClientName: conn.ClientName, InstanceID: conn.InstanceID, Identity: conn.Identity, ConnectionID: conn.ConnectionID, State: domain.ReleaseStateReceived, ServerTimestamp: time.Now()}
	assertMismatch := func() {
		t.Helper()
		var unavailable *domain.ReleaseAcknowledgementUnavailableError
		err := st.UpsertReleaseAcknowledgement(ctx, ack)
		if !errors.Is(err, domain.ErrFailedPrecondition) || errors.As(err, &unavailable) {
			t.Fatalf("forged foreign activation: %v", err)
		}
	}
	assertMismatch()
	seedReleaseAcknowledgementActivation(t, st, foreign)
	seedReleaseAcknowledgementActivation(t, st, foreign)
	if _, err := st.PruneConfigurationReleases(ctx, time.Nanosecond, 100); err != nil {
		t.Fatal(err)
	}
	// The changelog may disprove a mismatch after the activation table was
	// pruned, but is never used to accept an ACK for an expired activation.
	assertMismatch()
	ack.ActivationRevision = current.ActivationRevision
	ack.ReleaseVersion++
	assertMismatch()
}

func TestUnavailableReleaseAcknowledgementRemainsNamespaceFenced(t *testing.T) {
	st, tracks := trackFixture(t)
	ctx := context.Background()
	track := tracks[0]
	ns, err := st.GetNamespace(ctx, track.Namespace)
	if err != nil {
		t.Fatal(err)
	}
	oldCtx, err := BindNamespaceIncarnation(ctx, track.Namespace, ns.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteNamespace(ctx, track.Namespace); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateNamespace(ctx, domain.Namespace{NamespaceRef: track.Namespace}); err != nil {
		t.Fatal(err)
	}
	conn := domain.ReleaseSubscriberConnection{Namespace: track.Namespace, ReleaseName: track.Name, ClientName: "c", InstanceID: "i", Identity: "identity", ConnectionID: "same", Connected: true, ServerTimestamp: time.Now()}
	if err := st.SetReleaseInstanceConnected(ctx, conn); err != nil {
		t.Fatal(err)
	}
	ack := domain.ReleaseAcknowledgement{Namespace: track.Namespace, ReleaseName: track.Name, ReleaseVersion: 1, ActivationRevision: 1, ClientName: conn.ClientName, InstanceID: conn.InstanceID, Identity: conn.Identity, ConnectionID: conn.ConnectionID, State: domain.ReleaseStateReceived, ServerTimestamp: time.Now()}
	var unavailable *domain.ReleaseAcknowledgementUnavailableError
	if err := st.UpsertReleaseAcknowledgement(oldCtx, ack); !errors.Is(err, domain.ErrAborted) || errors.As(err, &unavailable) {
		t.Fatalf("old incarnation must fail before unavailable handling: %v", err)
	}
}
