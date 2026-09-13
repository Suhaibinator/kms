package storage

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
)

func TestReadReleaseProjectionCompleteBeyondPageLimit(t *testing.T) {
	st, ref, ack := reducerFixture(t)
	ctx := context.Background()
	ack.State, ack.Sequence = "applied", 1
	if _, err := st.ReduceReleaseSessionAcknowledgement(ctx, ref, ack); err != nil {
		t.Fatal(err)
	}
	model, err := sessionTx(st.db, ref)
	if err != nil {
		t.Fatal(err)
	}
	var sessions []releaseSessionModel
	for i := 0; i < 1005; i++ {
		copy := model
		copy.SessionID = fmt.Sprintf("instance-%04d", i)
		sessions = append(sessions, copy)
	}
	if err := st.db.CreateInBatches(sessions, 50).Error; err != nil {
		t.Fatal(err)
	}
	filter := domain.ReleaseFilter{Namespace: ref.Track.Namespace, Name: ref.Track.Name}
	rows, active, err := st.ReadReleaseProjection(ctx, filter)
	if err != nil || len(rows) != 1006 {
		t.Fatalf("snapshot rows=%d err=%v", len(rows), err)
	}
	if active[ref.Track].ActivationRevision != ack.ActivationRevision || active[ref.Track].Release.Version != ack.ReleaseVersion {
		t.Fatalf("target snapshot: %+v", active)
	}
	for _, row := range rows {
		if row.DesiredRevision != active[ref.Track].ActivationRevision || row.DesiredVersion != active[ref.Track].Release.Version {
			t.Fatalf("mixed desired target: %+v", row)
		}
	}
	// A corrupt/dangling target is an error, never an empty desired revision.
	if err := st.db.Where("namespace_id = ?", model.NamespaceID).Delete(&configurationReleaseModel{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ReadReleaseProjection(ctx, filter); err == nil {
		t.Fatal("dangling fleet target accepted")
	}
}

func TestReadReleaseProjectionPreservesActiveReleaseDetails(t *testing.T) {
	st, ref, _ := reducerFixture(t)
	ctx := context.Background()
	next, err := st.CreateConfigurationRelease(ctx, domain.ConfigurationRelease{Namespace: ref.Track.Namespace, Name: ref.Track.Name, Digest: "next", Metadata: "{}", CreatedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.ActivateConfigurationRelease(ctx, ref.Track, next.Version, nil); err != nil {
		t.Fatal(err)
	}
	want, err := st.GetActiveConfigurationRelease(ctx, ref.Track)
	if err != nil {
		t.Fatal(err)
	}
	if want.PreviousVersion == 0 {
		t.Fatal("fixture needs rollback history")
	}
	_, active, err := st.ReadReleaseProjection(ctx, domain.ReleaseFilter{Namespace: ref.Track.Namespace, Name: ref.Track.Name})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(active[ref.Track], want) {
		t.Fatalf("projection lost active metadata: got %+v want %+v", active[ref.Track], want)
	}
}
