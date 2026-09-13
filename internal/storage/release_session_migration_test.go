package storage

import (
	"errors"
	"reflect"
	"testing"

	"gorm.io/gorm"
)

func TestReleaseBaseline4UpgradeAtomicPreservesSessions(t *testing.T) {
	st := newStore(t)
	if err := st.db.Migrator().DropTable(&releaseSessionEventModel{}, &releaseSessionModel{}); err != nil {
		t.Fatal(err)
	}
	objects, err := releaseBaselineSchema(4)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"table", "index"} {
		for _, object := range objects {
			if object.TableName == "release_sessions" && object.Type == kind {
				if err := st.db.Exec(object.SQL).Error; err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := st.db.Model(&schemaMigrationModel{}).Where("version = ?", schemaVersion).Update("version", 4).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.db.Exec(`INSERT INTO release_sessions (session_id,namespace_id,release_name,schema_version,client_name,instance_id,identity,server_timestamp,state,last_ack_sequence,last_applied_version,last_applied_revision) VALUES ('kept',1,'runtime',4,'api','one','client','2026-09-12T00:00:00Z','applied',9,4,153)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseBaseline4(st.db); err != nil {
		t.Fatal(err)
	}
	before, err := readBaselineSchema(st.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := upgradeReleaseSessionsWithVerifier(st.db, func(tx *gorm.DB) error {
		if err := verifyBaselineDB(tx); err != nil {
			t.Fatal(err)
		}
		return errors.New("injected verification failure")
	}); err == nil {
		t.Fatal("expected failure")
	}
	after, err := readBaselineSchema(st.db)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed migration changed physical schema")
	}
	if err := verifyReleaseBaseline4(st.db); err != nil {
		t.Fatal(err)
	}
	if err := upgradeSecretTokenSchema(st.db); err != nil {
		t.Fatal(err)
	}
	if err := verifyBaselineDB(st.db); err != nil {
		t.Fatal(err)
	}
	var row releaseSessionModel
	if err := st.db.First(&row, "session_id = ?", "kept").Error; err != nil {
		t.Fatal(err)
	}
	if row.State != "applied" || row.LastAckSequence != 9 || row.LastAppliedVersion != 4 || row.LastAppliedRevision != 153 {
		t.Fatalf("session evidence changed: %+v", row)
	}
	if row.LastAppliedSequence != 0 || row.PrunedAckSequence != 9 || row.Diagnostic != "" || row.ClientTimestamp != "" {
		t.Fatalf("migration invented metadata: %+v", row)
	}
}

func TestReleaseBaseline4RejectsUnexpectedSchema(t *testing.T) {
	st := newStore(t)
	if err := st.db.Model(&schemaMigrationModel{}).Where("version = ?", schemaVersion).Update("version", 4).Error; err != nil {
		t.Fatal(err)
	}
	before, err := readBaselineSchema(st.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := upgradeSecretTokenSchema(st.db); err == nil {
		t.Fatal("accepted stamp-only baseline downgrade")
	}
	after, err := readBaselineSchema(st.db)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected database was mutated")
	}
}
