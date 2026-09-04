package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
)

var errBindingKeyRejected = domain.Errorf(domain.ErrPermissionDenied, "access denied")

func bindingSalt(key, marker byte, version uint64) []byte {
	salt := bytes.Repeat([]byte{marker}, crypto.BindingKeySaltSize)
	salt[0] = key
	binary.BigEndian.PutUint64(salt[len(salt)-8:], version)
	return salt
}

func putBindingVersion(t *testing.T, st *SQLStore, r domain.Ref, key byte, options ...func(*CreateSecretParams)) uint64 {
	t.Helper()
	p := CreateSecretParams{
		Ref:         r,
		ContentType: "application/x-test",
		Metadata:    `{"operator":"metadata"}`,
		CreatedBy:   "creator",
		Bound:       key != 0,
		Encrypt: func(version uint64) (EncryptedPayload, error) {
			payload := EncryptedPayload{
				Ciphertext:   []byte(fmt.Sprintf("ciphertext-%d", version)),
				EncryptedDEK: []byte(fmt.Sprintf("wrapped-dek-%d", version)),
				KEKID:        "kek-a",
				WrapMode:     domain.WrapModeStandard,
				Algorithm:    "AES-256-GCM",
				Nonce:        []byte(fmt.Sprintf("nonce-%d", version)),
				AAD:          fmt.Sprintf("aad-%d", version),
			}
			if key != 0 {
				payload.WrapMode = domain.WrapModeBindingKey
				payload.BindingKeySalt = bindingSalt(key, 'o', version)
			}
			return payload, nil
		},
	}
	for _, option := range options {
		option(&p)
	}
	version, _, err := st.CreateSecretVersion(context.Background(), p)
	if err != nil {
		t.Fatalf("CreateSecretVersion: %v", err)
	}
	return version
}

func bindingKeyTest(key byte) SecretBindingTestFunc {
	return func(rec SecretVersionRecord) error {
		if len(rec.BindingKeySalt) == 0 || rec.BindingKeySalt[0] != key {
			return errBindingKeyRejected
		}
		return nil
	}
}

func bindingRewrap(key byte, marker byte) SecretBindingRewrapFunc {
	return func(rec SecretVersionRecord) (SecretBindingWrapping, error) {
		return SecretBindingWrapping{
			EncryptedDEK:   []byte(fmt.Sprintf("rewrapped-%c-%d", key, rec.Version)),
			KEKID:          rec.KEKID,
			WrapMode:       domain.WrapModeBindingKey,
			BindingKeySalt: bindingSalt(key, marker, rec.Version),
		}, nil
	}
}

func standardRewrap(rec SecretVersionRecord) (SecretBindingWrapping, error) {
	return SecretBindingWrapping{
		EncryptedDEK: []byte(fmt.Sprintf("standard-%d", rec.Version)),
		KEKID:        rec.KEKID,
		WrapMode:     domain.WrapModeStandard,
	}, nil
}

func rawSecretVersion(t *testing.T, st *SQLStore, r domain.Ref, version uint64) secretVersionModel {
	t.Helper()
	sec, err := st.findSecret(st.db, r)
	if err != nil {
		t.Fatal(err)
	}
	var row secretVersionModel
	if err := st.db.Where("secret_id = ? AND version_number = ?", sec.ID, version).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func bindingRowSnapshot(row secretVersionModel) []any {
	return []any{
		row.ID, row.SecretID, row.VersionNumber, row.ContentType, row.Bound, row.HasAccessToken,
		string(row.Ciphertext), string(row.EncryptedDEK), row.KEKID, row.WrapMode,
		string(row.BindingKeySalt), row.Algorithm, string(row.Nonce), row.AAD, row.State,
		row.CreatedBy, row.CreatedAt, row.DestroyedAt, row.ExpiresAt, row.MetadataJSON,
	}
}

type forensicSecretMarkers struct {
	Ciphertext   []byte
	EncryptedDEK []byte
	Nonce        []byte
	Salt         []byte
	AAD          []byte
	Metadata     []byte
}

func newForensicSecretMarkers(tag string, version uint64) forensicSecretMarkers {
	marker := func(field string) []byte {
		return fmt.Appendf(nil, "|KMS-PURGE-LIVE-%s-%s-V%08d|", tag, field, version)
	}
	salt := bytes.Repeat([]byte{byte('K' + version)}, crypto.BindingKeySaltSize)
	salt[0] = 'B'
	return forensicSecretMarkers{
		Ciphertext:   marker("CIPHERTEXT"),
		EncryptedDEK: marker("ENCRYPTED-DEK"),
		Nonce:        marker("NONCE"),
		Salt:         salt,
		AAD:          marker("AAD"),
		Metadata:     marker("METADATA"),
	}
}

func putForensicBindingVersion(t *testing.T, st *SQLStore, r domain.Ref, markers forensicSecretMarkers) uint64 {
	t.Helper()
	version, _, err := st.CreateSecretVersion(context.Background(), CreateSecretParams{
		Ref: r, Bound: true, ContentType: "application/x-forensic-marker",
		Metadata: fmt.Sprintf(`{"marker":%q}`, string(bytes.Repeat(markers.Metadata, 384))), CreatedBy: "forensic-test",
		Encrypt: func(uint64) (EncryptedPayload, error) {
			return EncryptedPayload{
				Ciphertext: bytes.Repeat(markers.Ciphertext, 384), EncryptedDEK: bytes.Repeat(markers.EncryptedDEK, 384),
				KEKID: "kek-a", WrapMode: domain.WrapModeBindingKey, BindingKeySalt: bytes.Clone(markers.Salt),
				Algorithm: "AES-256-GCM", Nonce: bytes.Repeat(markers.Nonce, 384), AAD: string(bytes.Repeat(markers.AAD, 384)),
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("CreateSecretVersion forensic marker: %v", err)
	}
	return version
}

func readArtifact(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read SQLite artifact %s: %v", filepath.Base(path), err)
	}
	return b
}

func allForensicMarkers(markers ...forensicSecretMarkers) [][]byte {
	var out [][]byte
	for _, marker := range markers {
		out = append(out, marker.Ciphertext, marker.EncryptedDEK, marker.Nonce, marker.Salt, marker.AAD, marker.Metadata)
	}
	return out
}

func TestSecretBindingExactVersionLifecycle(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "exact")
	putBindingVersion(t, st, r, 0, func(p *CreateSecretParams) {
		p.ExpiresAt = time.Now().Add(time.Hour)
		p.AccessTokenHash = []byte("token-hash")
	})
	original := rawSecretVersion(t, st, r, 1)

	callbackFailure := errors.New("rewrap failed")
	revisionBefore, err := st.CurrentRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BindSecretVersion(ctx, r, 0, bindingKeyTest('B'), func(SecretVersionRecord) (SecretBindingWrapping, error) {
		return SecretBindingWrapping{}, callbackFailure
	}, SecretBindingAudit{}); !errors.Is(err, callbackFailure) {
		t.Fatalf("callback failure = %v", err)
	}
	if got := rawSecretVersion(t, st, r, 1); !reflect.DeepEqual(bindingRowSnapshot(got), bindingRowSnapshot(original)) {
		t.Fatal("failed bind changed the version")
	}
	if revision, _ := st.CurrentRevision(ctx); revision != revisionBefore {
		t.Fatalf("failed bind revision = %d, want %d", revision, revisionBefore)
	}

	if _, err := st.BindSecretVersion(ctx, r, 1, bindingKeyTest('B'), func(rec SecretVersionRecord) (SecretBindingWrapping, error) {
		return SecretBindingWrapping{EncryptedDEK: []byte("new"), KEKID: "different-kek", WrapMode: domain.WrapModeBindingKey, BindingKeySalt: []byte("fresh")}, nil
	}, SecretBindingAudit{}); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("changed KEK = %v", err)
	}

	auditContext := SecretBindingAudit{ActorIdentity: "binding-operator", ActorType: domain.IdentityKindClient, RequestID: "request-exact"}
	result, err := st.BindSecretVersion(ctx, r, 0, bindingKeyTest('B'), func(rec SecretVersionRecord) (SecretBindingWrapping, error) {
		// Callback-owned buffer changes must not leak into the stored payload.
		rec.Ciphertext[0] = 'X'
		rec.Nonce[0] = 'Y'
		return bindingRewrap('B', 'n')(rec)
	}, auditContext)
	if err != nil {
		t.Fatal(err)
	}
	if result.AnchorVersion != 1 || !slices.Equal(result.AffectedVersions, []uint64{1}) || result.Revision <= revisionBefore {
		t.Fatalf("bind result = %+v", result)
	}
	bound := rawSecretVersion(t, st, r, 1)
	if bound.Bound != 1 || bound.WrapMode != domain.WrapModeBindingKey || !bytes.Equal(bound.BindingKeySalt, bindingSalt('B', 'n', 1)) {
		t.Fatalf("bound wrapping = %+v", bound)
	}
	if !bytes.Equal(bound.Ciphertext, original.Ciphertext) || !bytes.Equal(bound.Nonce, original.Nonce) || bound.AAD != original.AAD || bound.Algorithm != original.Algorithm || bound.KEKID != original.KEKID {
		t.Fatal("bind rewrote immutable value-encryption fields")
	}

	unboundResult, err := st.UnbindSecretVersion(ctx, r, 0, standardRewrap, auditContext)
	if err != nil {
		t.Fatal(err)
	}
	if unboundResult.AnchorVersion != 1 || !slices.Equal(unboundResult.AffectedVersions, []uint64{1}) {
		t.Fatalf("unbind result = %+v", unboundResult)
	}
	unbound := rawSecretVersion(t, st, r, 1)
	if unbound.Bound != 0 || unbound.WrapMode != domain.WrapModeStandard || len(unbound.BindingKeySalt) != 0 {
		t.Fatalf("unbound wrapping = %+v", unbound)
	}
	if !bytes.Equal(unbound.Ciphertext, original.Ciphertext) || !bytes.Equal(unbound.Nonce, original.Nonce) || unbound.AAD != original.AAD || unbound.KEKID != original.KEKID {
		t.Fatal("unbind rewrote immutable value-encryption fields")
	}
	changes, err := st.ListChangesSince(ctx, revisionBefore, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 || changes[0].ChangeType != domain.ChangeBind || changes[1].ChangeType != domain.ChangeUnbind ||
		!slices.Equal(changes[0].AffectedVersions, []uint64{1}) || !slices.Equal(changes[1].AffectedVersions, []uint64{1}) {
		t.Fatalf("binding changes = %+v", changes)
	}
	for _, eventType := range []string{bindSecretAuditEvent, unbindSecretAuditEvent} {
		audits, _, err := st.ListAudit(ctx, domain.AuditFilter{EventType: eventType}, ListPage{})
		if err != nil || len(audits) != 1 {
			t.Fatalf("%s audits = %+v err=%v", eventType, audits, err)
		}
		if audit := audits[0]; audit.ActorIdentity != "binding-operator" || audit.ResourceVersion != 1 || audit.Decision != "allow" || audit.Metadata != `{"affected_versions":[1]}` {
			t.Fatalf("%s audit = %+v", eventType, audit)
		}
	}
}

func TestCreateAndBindRequireExactBindingSaltSize(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")

	for _, size := range []int{0, crypto.BindingKeySaltSize - 1, crypto.BindingKeySaltSize + 1} {
		r := ref("prod", "app", fmt.Sprintf("create-salt-%d", size))
		_, _, err := st.CreateSecretVersion(ctx, CreateSecretParams{
			Ref: r, Bound: true,
			Encrypt: func(version uint64) (EncryptedPayload, error) {
				payload, err := boundEncryptStub(nil)(version)
				payload.BindingKeySalt = bytes.Repeat([]byte{'x'}, size)
				return payload, err
			},
		})
		if !errors.Is(err, domain.ErrFailedPrecondition) {
			t.Fatalf("CreateSecretVersion salt length %d = %v, want failed precondition", size, err)
		}
		if _, err := st.GetSecretRecord(ctx, r); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("invalid create salt length %d left a secret: %v", size, err)
		}
	}

	r := ref("prod", "app", "bind-salt")
	putBindingVersion(t, st, r, 0)
	original := rawSecretVersion(t, st, r, 1)
	revision, err := st.CurrentRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{0, crypto.BindingKeySaltSize - 1, crypto.BindingKeySaltSize + 1} {
		_, err := st.BindSecretVersion(ctx, r, 1, bindingKeyTest('B'), func(rec SecretVersionRecord) (SecretBindingWrapping, error) {
			return SecretBindingWrapping{
				EncryptedDEK:   []byte("rewrapped"),
				KEKID:          rec.KEKID,
				WrapMode:       domain.WrapModeBindingKey,
				BindingKeySalt: bytes.Repeat([]byte{'x'}, size),
			}, nil
		}, SecretBindingAudit{})
		if !errors.Is(err, domain.ErrFailedPrecondition) {
			t.Fatalf("BindSecretVersion salt length %d = %v, want failed precondition", size, err)
		}
		if got := rawSecretVersion(t, st, r, 1); !reflect.DeepEqual(bindingRowSnapshot(got), bindingRowSnapshot(original)) {
			t.Fatalf("invalid bind salt length %d changed version", size)
		}
		if got, _ := st.CurrentRevision(ctx); got != revision {
			t.Fatalf("invalid bind salt length %d changed revision to %d, want %d", size, got, revision)
		}
	}
}

func TestBindSecretVersionRejectsImplicitCohortMergeAtomically(t *testing.T) {
	tests := []struct {
		name          string
		keys          []byte
		targetVersion uint64
	}{
		{name: "both sides", keys: []byte{'A', 0, 'A'}, targetVersion: 2},
		{name: "lower neighbor", keys: []byte{'A', 0}, targetVersion: 2},
		{name: "upper neighbor", keys: []byte{0, 'A'}, targetVersion: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newStore(t)
			ctx := context.Background()
			seedNS(t, st, "prod", "app")
			r := ref("prod", "app", "bind-merge")
			for _, key := range tc.keys {
				putBindingVersion(t, st, r, key)
			}
			before := make([]secretVersionModel, len(tc.keys))
			for i := range before {
				before[i] = rawSecretVersion(t, st, r, uint64(i+1))
			}
			revisionBefore, err := st.CurrentRevision(ctx)
			if err != nil {
				t.Fatal(err)
			}
			rewrapCalls := 0
			_, err = st.BindSecretVersion(ctx, r, tc.targetVersion, bindingKeyTest('A'), func(rec SecretVersionRecord) (SecretBindingWrapping, error) {
				rewrapCalls++
				return bindingRewrap('A', 'n')(rec)
			}, SecretBindingAudit{ActorIdentity: "operator"})
			if !errors.Is(err, domain.ErrFailedPrecondition) || err.Error() != "binding change would merge an adjacent cohort: failed precondition" {
				t.Fatalf("BindSecretVersion = %v, want sanitized merge failure", err)
			}
			if rewrapCalls != 0 {
				t.Fatalf("rewrap calls = %d, want 0", rewrapCalls)
			}
			for i, want := range before {
				if got := rawSecretVersion(t, st, r, uint64(i+1)); !reflect.DeepEqual(bindingRowSnapshot(got), bindingRowSnapshot(want)) {
					t.Fatalf("rejected bind changed version %d", i+1)
				}
			}
			if revision, _ := st.CurrentRevision(ctx); revision != revisionBefore {
				t.Fatalf("rejected bind revision = %d, want %d", revision, revisionBefore)
			}
			if changes, err := st.ListChangesSince(ctx, revisionBefore, 10); err != nil || len(changes) != 0 {
				t.Fatalf("rejected bind changes = %+v err=%v", changes, err)
			}
			if audits, _, err := st.ListAudit(ctx, domain.AuditFilter{EventType: bindSecretAuditEvent}, ListPage{}); err != nil || len(audits) != 0 {
				t.Fatalf("rejected bind allow audits = %+v err=%v", audits, err)
			}
		})
	}
}

func TestPreviewSecretBindingCohortBoundaries(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "cohorts")
	putBindingVersion(t, st, r, 'A') // 1: wrong-key lower boundary
	putBindingVersion(t, st, r, 'B') // 2: disabled but included
	if _, err := st.SetSecretVersionState(ctx, r, 2, domain.StateDisabled); err != nil {
		t.Fatal(err)
	}
	putBindingVersion(t, st, r, 'B', func(p *CreateSecretParams) { p.ExpiresAt = time.Now().Add(-time.Hour) }) // 3: expired but included
	putBindingVersion(t, st, r, 'B')                                                                           // 4
	putBindingVersion(t, st, r, 0)                                                                             // 5: unbound boundary
	putBindingVersion(t, st, r, 'B')                                                                           // 6: same key reused after boundary
	putBindingVersion(t, st, r, 'B')                                                                           // 7: current

	preview, err := st.PreviewSecretBindingCohort(ctx, r, 3, bindingKeyTest('B'))
	if err != nil {
		t.Fatal(err)
	}
	if preview.AnchorVersion != 3 || !slices.Equal(preview.AffectedVersions, []uint64{2, 3, 4}) {
		t.Fatalf("middle preview = %+v", preview)
	}
	current, err := st.PreviewSecretBindingCohort(ctx, r, 0, bindingKeyTest('B'))
	if err != nil {
		t.Fatal(err)
	}
	if current.AnchorVersion != 7 || !slices.Equal(current.AffectedVersions, []uint64{6, 7}) {
		t.Fatalf("current/reused-key preview = %+v", current)
	}

	if err := st.db.Where("secret_id = ? AND version_number = ?", rawSecretVersion(t, st, r, 7).SecretID, 6).Delete(&secretVersionModel{}).Error; err != nil {
		t.Fatal(err)
	}
	gap, err := st.PreviewSecretBindingCohort(ctx, r, 7, bindingKeyTest('B'))
	if err != nil || !slices.Equal(gap.AffectedVersions, []uint64{7}) {
		t.Fatalf("gap preview = %+v err=%v", gap, err)
	}

	putBindingVersion(t, st, r, 'C') // 8
	putBindingVersion(t, st, r, 'C') // 9 destroyed boundary
	if _, err := st.DestroySecretVersion(ctx, r, 9); err != nil {
		t.Fatal(err)
	}
	putBindingVersion(t, st, r, 'C') // 10
	destroyedBoundary, err := st.PreviewSecretBindingCohort(ctx, r, 10, bindingKeyTest('C'))
	if err != nil || !slices.Equal(destroyedBoundary.AffectedVersions, []uint64{10}) {
		t.Fatalf("destroyed boundary = %+v err=%v", destroyedBoundary, err)
	}

	putBindingVersion(t, st, r, 'D') // 11 corrupt boundary
	putBindingVersion(t, st, r, 'D') // 12
	row11 := rawSecretVersion(t, st, r, 11)
	if err := st.db.Model(&secretVersionModel{}).Where("id = ?", row11.ID).Update("encrypted_dek", nil).Error; err != nil {
		t.Fatal(err)
	}
	corruptBoundary, err := st.PreviewSecretBindingCohort(ctx, r, 12, bindingKeyTest('D'))
	if err != nil || !slices.Equal(corruptBoundary.AffectedVersions, []uint64{12}) {
		t.Fatalf("corrupt boundary = %+v err=%v", corruptBoundary, err)
	}

	calls := 0
	_, err = st.PreviewSecretBindingCohort(ctx, r, 3, func(rec SecretVersionRecord) error {
		calls++
		return errBindingKeyRejected
	})
	if !errors.Is(err, errBindingKeyRejected) || calls != 1 {
		t.Fatalf("wrong anchor key: calls=%d err=%v", calls, err)
	}
	if _, err := st.PreviewSecretBindingCohort(ctx, r, 5, bindingKeyTest('B')); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("unbound anchor = %v", err)
	}
	if _, err := st.PreviewSecretBindingCohort(ctx, r, 9, bindingKeyTest('C')); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("destroyed anchor = %v", err)
	}
}

func uint64Pointer(value uint64) *uint64 { return &value }

func TestRotateSecretBindingKeyCASRollbackAndStaleRows(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "rotate")
	for range 3 {
		putBindingVersion(t, st, r, 'B')
	}
	preview, err := st.PreviewSecretBindingCohort(ctx, r, 2, bindingKeyTest('B'))
	if err != nil {
		t.Fatal(err)
	}

	invalidGuards := []SecretBindingCASGuard{
		{ExpectedAffectedVersions: []uint64{1, 2, 3}},
		{ExpectedRevision: uint64Pointer(preview.Revision)},
		{ExpectedRevision: uint64Pointer(preview.Revision), ExpectedAffectedVersions: []uint64{2, 1}},
		{ExpectedRevision: uint64Pointer(preview.Revision), ExpectedAffectedVersions: []uint64{1, 1}},
		{ExpectedRevision: uint64Pointer(preview.Revision), ExpectedAffectedVersions: []uint64{0, 1}},
	}
	for _, guard := range invalidGuards {
		rewrapCalls := 0
		_, err := st.RotateSecretBindingKey(ctx, r, 2, guard, bindingKeyTest('B'), bindingKeyTest('C'), func(rec SecretVersionRecord) (SecretBindingWrapping, error) {
			rewrapCalls++
			return bindingRewrap('C', 'n')(rec)
		}, SecretBindingAudit{})
		if !errors.Is(err, domain.ErrInvalidArgument) || rewrapCalls != 0 {
			t.Fatalf("invalid guard %+v: calls=%d err=%v", guard, rewrapCalls, err)
		}
	}

	if _, _, err := st.PutParameter(ctx, ref("prod", "app", "revision-bump"), "x", "text/plain", "{}", "tester"); err != nil {
		t.Fatal(err)
	}
	rewrapCalls := 0
	_, err = st.RotateSecretBindingKey(ctx, r, 2, SecretBindingCASGuard{
		ExpectedRevision: uint64Pointer(preview.Revision), ExpectedAffectedVersions: preview.AffectedVersions,
	}, bindingKeyTest('B'), bindingKeyTest('C'), func(rec SecretVersionRecord) (SecretBindingWrapping, error) {
		rewrapCalls++
		return bindingRewrap('C', 'n')(rec)
	}, SecretBindingAudit{})
	if !errors.Is(err, domain.ErrAborted) || rewrapCalls != 0 {
		t.Fatalf("stale revision: calls=%d err=%v", rewrapCalls, err)
	}

	preview, err = st.PreviewSecretBindingCohort(ctx, r, 2, bindingKeyTest('B'))
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.RotateSecretBindingKey(ctx, r, 2, SecretBindingCASGuard{
		ExpectedRevision: uint64Pointer(preview.Revision), ExpectedAffectedVersions: []uint64{1, 2},
	}, bindingKeyTest('B'), bindingKeyTest('C'), func(rec SecretVersionRecord) (SecretBindingWrapping, error) {
		rewrapCalls++
		return bindingRewrap('C', 'n')(rec)
	}, SecretBindingAudit{})
	if !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("stale set = %v", err)
	}

	before := make([]secretVersionModel, 3)
	for i := range before {
		before[i] = rawSecretVersion(t, st, r, uint64(i+1))
	}
	callbackFailure := errors.New("rewrap version two")
	_, err = st.RotateSecretBindingKey(ctx, r, 2, SecretBindingCASGuard{
		ExpectedRevision: uint64Pointer(preview.Revision), ExpectedAffectedVersions: preview.AffectedVersions,
	}, bindingKeyTest('B'), bindingKeyTest('C'), func(rec SecretVersionRecord) (SecretBindingWrapping, error) {
		if rec.Version == 2 {
			return SecretBindingWrapping{}, callbackFailure
		}
		return bindingRewrap('C', 'n')(rec)
	}, SecretBindingAudit{})
	if !errors.Is(err, callbackFailure) {
		t.Fatalf("callback rollback = %v", err)
	}
	for i, want := range before {
		if got := rawSecretVersion(t, st, r, uint64(i+1)); !reflect.DeepEqual(bindingRowSnapshot(got), bindingRowSnapshot(want)) {
			t.Fatalf("callback failure changed version %d", i+1)
		}
	}
	if revision, _ := st.CurrentRevision(ctx); revision != preview.Revision {
		t.Fatalf("callback failure revision = %d, want %d", revision, preview.Revision)
	}

	result, err := st.RotateSecretBindingKey(ctx, r, 2, SecretBindingCASGuard{
		ExpectedRevision: uint64Pointer(preview.Revision), ExpectedAffectedVersions: preview.AffectedVersions,
	}, bindingKeyTest('B'), bindingKeyTest('C'), bindingRewrap('C', 'n'), SecretBindingAudit{})
	if err != nil {
		t.Fatal(err)
	}
	if result.AnchorVersion != 2 || !slices.Equal(result.AffectedVersions, []uint64{1, 2, 3}) {
		t.Fatalf("rotate result = %+v", result)
	}
	for i, old := range before {
		got := rawSecretVersion(t, st, r, uint64(i+1))
		if len(got.BindingKeySalt) == 0 || got.BindingKeySalt[0] != 'C' || got.KEKID != old.KEKID ||
			!bytes.Equal(got.Ciphertext, old.Ciphertext) || !bytes.Equal(got.Nonce, old.Nonce) || got.AAD != old.AAD {
			t.Fatalf("rotated version %d = %+v", i+1, got)
		}
	}

	previewC, err := st.PreviewSecretBindingCohort(ctx, r, 2, bindingKeyTest('C'))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.db.Exec(`CREATE TRIGGER ignore_binding_update BEFORE UPDATE OF encrypted_dek ON secret_versions
		WHEN OLD.version_number = 2 BEGIN SELECT RAISE(IGNORE); END`).Error; err != nil {
		t.Fatal(err)
	}
	beforeStale := []secretVersionModel{rawSecretVersion(t, st, r, 1), rawSecretVersion(t, st, r, 2), rawSecretVersion(t, st, r, 3)}
	_, err = st.RotateSecretBindingKey(ctx, r, 2, SecretBindingCASGuard{
		ExpectedRevision: uint64Pointer(previewC.Revision), ExpectedAffectedVersions: previewC.AffectedVersions,
	}, bindingKeyTest('C'), bindingKeyTest('D'), bindingRewrap('D', 'n'), SecretBindingAudit{})
	if !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("stale row = %v", err)
	}
	for i, want := range beforeStale {
		if got := rawSecretVersion(t, st, r, uint64(i+1)); !reflect.DeepEqual(bindingRowSnapshot(got), bindingRowSnapshot(want)) {
			t.Fatalf("stale row failure did not roll back version %d", i+1)
		}
	}
}

func TestRotateSecretBindingKeyRejectsImplicitCohortMergeAtomically(t *testing.T) {
	tests := []struct {
		name string
		keys []byte
	}{
		{name: "both sides", keys: []byte{'A', 'B', 'B', 'A'}},
		{name: "lower neighbor", keys: []byte{'A', 'B', 'B'}},
		{name: "upper neighbor", keys: []byte{'B', 'B', 'A'}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newStore(t)
			ctx := context.Background()
			seedNS(t, st, "prod", "app")
			r := ref("prod", "app", "rotate-merge")
			for _, key := range tc.keys {
				putBindingVersion(t, st, r, key)
			}
			anchor := uint64(1)
			if tc.keys[0] == 'A' {
				anchor = 2
			}
			preview, err := st.PreviewSecretBindingCohort(ctx, r, anchor, bindingKeyTest('B'))
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "both sides" {
				newKeyTestCalls := 0
				_, err := st.RotateSecretBindingKey(ctx, r, anchor, SecretBindingCASGuard{
					ExpectedRevision: uint64Pointer(preview.Revision - 1), ExpectedAffectedVersions: preview.AffectedVersions,
				}, bindingKeyTest('B'), func(rec SecretVersionRecord) error {
					newKeyTestCalls++
					return bindingKeyTest('A')(rec)
				}, bindingRewrap('A', 'n'), SecretBindingAudit{})
				if !errors.Is(err, domain.ErrAborted) || newKeyTestCalls != 0 {
					t.Fatalf("stale guard ordering: new-key tests=%d err=%v", newKeyTestCalls, err)
				}
			}
			before := make([]secretVersionModel, len(tc.keys))
			for i := range before {
				before[i] = rawSecretVersion(t, st, r, uint64(i+1))
			}
			rewrapCalls := 0
			_, err = st.RotateSecretBindingKey(ctx, r, anchor, SecretBindingCASGuard{
				ExpectedRevision: uint64Pointer(preview.Revision), ExpectedAffectedVersions: preview.AffectedVersions,
			}, bindingKeyTest('B'), bindingKeyTest('A'), func(rec SecretVersionRecord) (SecretBindingWrapping, error) {
				rewrapCalls++
				return bindingRewrap('A', 'n')(rec)
			}, SecretBindingAudit{ActorIdentity: "operator"})
			if !errors.Is(err, domain.ErrFailedPrecondition) || err.Error() != "binding change would merge an adjacent cohort: failed precondition" {
				t.Fatalf("RotateSecretBindingKey = %v, want sanitized merge failure", err)
			}
			if rewrapCalls != 0 {
				t.Fatalf("rewrap calls = %d, want 0", rewrapCalls)
			}
			for i, want := range before {
				if got := rawSecretVersion(t, st, r, uint64(i+1)); !reflect.DeepEqual(bindingRowSnapshot(got), bindingRowSnapshot(want)) {
					t.Fatalf("rejected rotation changed version %d", i+1)
				}
			}
			if revision, _ := st.CurrentRevision(ctx); revision != preview.Revision {
				t.Fatalf("rejected rotation revision = %d, want %d", revision, preview.Revision)
			}
			if changes, err := st.ListChangesSince(ctx, preview.Revision, 10); err != nil || len(changes) != 0 {
				t.Fatalf("rejected rotation changes = %+v err=%v", changes, err)
			}
			if audits, _, err := st.ListAudit(ctx, domain.AuditFilter{EventType: rotateBindingKeyAuditEvent}, ListPage{}); err != nil || len(audits) != 0 {
				t.Fatalf("rejected rotation allow audits = %+v err=%v", audits, err)
			}
		})
	}
}

func TestRotateSecretBindingKeyAllowsDifferentKeyAndReuseBeyondBoundaries(t *testing.T) {
	t.Run("different key does not merge", func(t *testing.T) {
		st := newStore(t)
		ctx := context.Background()
		seedNS(t, st, "prod", "app")
		r := ref("prod", "app", "rotate-different")
		for _, key := range []byte{'A', 'B', 'B', 'A'} {
			putBindingVersion(t, st, r, key)
		}
		preview, err := st.PreviewSecretBindingCohort(ctx, r, 2, bindingKeyTest('B'))
		if err != nil {
			t.Fatal(err)
		}
		result, err := st.RotateSecretBindingKey(ctx, r, 2, SecretBindingCASGuard{
			ExpectedRevision: uint64Pointer(preview.Revision), ExpectedAffectedVersions: preview.AffectedVersions,
		}, bindingKeyTest('B'), bindingKeyTest('C'), bindingRewrap('C', 'n'), SecretBindingAudit{ActorIdentity: "operator"})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(result.AffectedVersions, []uint64{2, 3}) {
			t.Fatalf("rotation result = %+v", result)
		}
		for _, version := range []uint64{2, 3} {
			if row := rawSecretVersion(t, st, r, version); row.BindingKeySalt[0] != 'C' {
				t.Fatalf("version %d was not rotated to C", version)
			}
		}
	})

	boundaries := []struct {
		name    string
		prepare func(*testing.T, *SQLStore, domain.Ref) SecretBindingTestFunc
	}{
		{
			name: "unbound",
			prepare: func(t *testing.T, st *SQLStore, r domain.Ref) SecretBindingTestFunc {
				putBindingVersion(t, st, r, 0)
				return bindingKeyTest('A')
			},
		},
		{
			name: "missing",
			prepare: func(t *testing.T, st *SQLStore, r domain.Ref) SecretBindingTestFunc {
				putBindingVersion(t, st, r, 0)
				row := rawSecretVersion(t, st, r, 2)
				if err := st.db.Delete(&row).Error; err != nil {
					t.Fatal(err)
				}
				return bindingKeyTest('A')
			},
		},
		{
			name: "destroyed",
			prepare: func(t *testing.T, st *SQLStore, r domain.Ref) SecretBindingTestFunc {
				putBindingVersion(t, st, r, 'A')
				if _, err := st.DestroySecretVersion(context.Background(), r, 2); err != nil {
					t.Fatal(err)
				}
				return bindingKeyTest('A')
			},
		},
		{
			name: "cryptographically corrupt",
			prepare: func(t *testing.T, st *SQLStore, r domain.Ref) SecretBindingTestFunc {
				putBindingVersion(t, st, r, 'A')
				row := rawSecretVersion(t, st, r, 2)
				if err := st.db.Model(&secretVersionModel{}).Where("id = ?", row.ID).Update("encrypted_dek", []byte("corrupt")).Error; err != nil {
					t.Fatal(err)
				}
				return func(rec SecretVersionRecord) error {
					if bytes.Equal(rec.EncryptedDEK, []byte("corrupt")) {
						return errBindingKeyRejected
					}
					return bindingKeyTest('A')(rec)
				}
			},
		},
		{
			name: "different key",
			prepare: func(t *testing.T, st *SQLStore, r domain.Ref) SecretBindingTestFunc {
				putBindingVersion(t, st, r, 'C')
				return bindingKeyTest('A')
			},
		},
	}
	for _, tc := range boundaries {
		t.Run(tc.name, func(t *testing.T) {
			st := newStore(t)
			ctx := context.Background()
			seedNS(t, st, "prod", "app")
			r := ref("prod", "app", "rotate-boundary")
			putBindingVersion(t, st, r, 'A')
			testNew := tc.prepare(t, st, r)
			putBindingVersion(t, st, r, 'B')
			preview, err := st.PreviewSecretBindingCohort(ctx, r, 3, bindingKeyTest('B'))
			if err != nil {
				t.Fatal(err)
			}
			result, err := st.RotateSecretBindingKey(ctx, r, 3, SecretBindingCASGuard{
				ExpectedRevision: uint64Pointer(preview.Revision), ExpectedAffectedVersions: preview.AffectedVersions,
			}, bindingKeyTest('B'), testNew, bindingRewrap('A', 'n'), SecretBindingAudit{ActorIdentity: "operator"})
			if err != nil {
				t.Fatalf("rotation across %s boundary: %v", tc.name, err)
			}
			if !slices.Equal(result.AffectedVersions, []uint64{3}) {
				t.Fatalf("rotation result = %+v", result)
			}
			cohort, err := st.PreviewSecretBindingCohort(ctx, r, 3, testNew)
			if err != nil || !slices.Equal(cohort.AffectedVersions, []uint64{3}) {
				t.Fatalf("rotation crossed %s boundary: %+v err=%v", tc.name, cohort, err)
			}
		})
	}
}

func TestRotateRejectsInvalidOrReusedSaltsAcrossWholeCohort(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "rotate-salt-validation")
	for range 3 {
		putBindingVersion(t, st, r, 'B')
	}
	preview, err := st.PreviewSecretBindingCohort(ctx, r, 2, bindingKeyTest('B'))
	if err != nil {
		t.Fatal(err)
	}
	original := []secretVersionModel{
		rawSecretVersion(t, st, r, 1),
		rawSecretVersion(t, st, r, 2),
		rawSecretVersion(t, st, r, 3),
	}

	cases := []struct {
		name   string
		rewrap SecretBindingRewrapFunc
	}{
		{
			name: "short",
			rewrap: func(rec SecretVersionRecord) (SecretBindingWrapping, error) {
				return SecretBindingWrapping{EncryptedDEK: []byte("new"), KEKID: rec.KEKID, WrapMode: domain.WrapModeBindingKey, BindingKeySalt: bytes.Repeat([]byte{'s'}, crypto.BindingKeySaltSize-1)}, nil
			},
		},
		{
			name: "long",
			rewrap: func(rec SecretVersionRecord) (SecretBindingWrapping, error) {
				return SecretBindingWrapping{EncryptedDEK: []byte("new"), KEKID: rec.KEKID, WrapMode: domain.WrapModeBindingKey, BindingKeySalt: bytes.Repeat([]byte{'l'}, crypto.BindingKeySaltSize+1)}, nil
			},
		},
		{
			name: "another versions old salt",
			rewrap: func(rec SecretVersionRecord) (SecretBindingWrapping, error) {
				salt := bindingSalt('C', 'n', rec.Version)
				if rec.Version == 1 {
					salt = bytes.Clone(original[1].BindingKeySalt)
				}
				return SecretBindingWrapping{EncryptedDEK: []byte("new"), KEKID: rec.KEKID, WrapMode: domain.WrapModeBindingKey, BindingKeySalt: salt}, nil
			},
		},
		{
			name: "duplicate new salt",
			rewrap: func(rec SecretVersionRecord) (SecretBindingWrapping, error) {
				return SecretBindingWrapping{EncryptedDEK: []byte("new"), KEKID: rec.KEKID, WrapMode: domain.WrapModeBindingKey, BindingKeySalt: bindingSalt('C', 'n', 99)}, nil
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := st.RotateSecretBindingKey(ctx, r, 2, SecretBindingCASGuard{
				ExpectedRevision: uint64Pointer(preview.Revision), ExpectedAffectedVersions: preview.AffectedVersions,
			}, bindingKeyTest('B'), bindingKeyTest('C'), tc.rewrap, SecretBindingAudit{})
			if !errors.Is(err, domain.ErrFailedPrecondition) {
				t.Fatalf("RotateSecretBindingKey = %v, want failed precondition", err)
			}
			for i, want := range original {
				if got := rawSecretVersion(t, st, r, uint64(i+1)); !reflect.DeepEqual(bindingRowSnapshot(got), bindingRowSnapshot(want)) {
					t.Fatalf("failed rotation changed version %d", i+1)
				}
			}
			if got, _ := st.CurrentRevision(ctx); got != preview.Revision {
				t.Fatalf("failed rotation revision = %d, want %d", got, preview.Revision)
			}
		})
	}
}

func TestPurgeSecretBindingCohortTombstonesAndBypassesRelease(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	ns := seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "purge")
	putBindingVersion(t, st, r, 'A')
	putBindingVersion(t, st, r, 'B', func(p *CreateSecretParams) { p.AccessTokenHash = []byte("hash") })
	putBindingVersion(t, st, r, 'B', func(p *CreateSecretParams) { p.ExpiresAt = time.Now().Add(-time.Hour) })
	if _, err := st.SetSecretVersionState(ctx, r, 2, domain.StateDisabled); err != nil {
		t.Fatal(err)
	}
	secret, err := st.findSecret(st.db, r)
	if err != nil {
		t.Fatal(err)
	}
	labelsBefore, err := loadSecretLabels(st.db, secret.ID)
	if err != nil {
		t.Fatal(err)
	}
	release := configurationReleaseModel{
		NamespaceID: ns.ID, Name: "runtime", VersionNumber: 1, Digest: "digest", MetadataJSON: "{}", CreatedBy: "admin", CreatedAt: fmtTime(nowUTC()),
	}
	if err := st.db.Omit(clause.Associations).Create(&release).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.db.Omit(clause.Associations).Create(&configurationReleaseLabelModel{
		NamespaceID: ns.ID, ReleaseName: release.Name, Label: domain.LabelCurrent, VersionNumber: release.VersionNumber,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.db.Omit(clause.Associations).Create(&configurationReleaseEntryModel{
		ReleaseID: release.ID, Alias: "secret", Kind: domain.ReleaseEntrySecret,
		ResourceNamespaceID: ns.ID, ResourceEnv: r.NS.Env, ResourceApp: r.NS.App,
		ResourceKey: r.Key, ResourceVersion: 2, ContentType: "application/x-test", MetadataJSON: "{}",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := st.DestroySecretVersion(ctx, r, 2); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("ordinary destroy did not observe release guard: %v", err)
	}

	preview, err := st.PreviewSecretBindingCohort(ctx, r, 0, bindingKeyTest('B'))
	if err != nil || !slices.Equal(preview.AffectedVersions, []uint64{2, 3}) {
		t.Fatalf("preview = %+v err=%v", preview, err)
	}
	var auditBefore, changesBefore int64
	if err := st.db.Model(&auditEventModel{}).Count(&auditBefore).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.db.Model(&changeLogModel{}).Count(&changesBefore).Error; err != nil {
		t.Fatal(err)
	}
	purged, err := st.PurgeSecretBindingCohort(ctx, r, 0, SecretBindingCASGuard{
		ExpectedRevision: uint64Pointer(preview.Revision), ExpectedAffectedVersions: preview.AffectedVersions,
	}, bindingKeyTest('B'), SecretBindingPurgeAudit{
		ActorIdentity: "admin", ActorType: domain.IdentityKindAdmin, SourceIP: "127.0.0.1", UserAgent: "test", RequestID: "request-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if purged.AnchorVersion != 3 || !slices.Equal(purged.AffectedVersions, []uint64{2, 3}) {
		t.Fatalf("purge result = %+v", purged)
	}
	for _, version := range []uint64{2, 3} {
		row := rawSecretVersion(t, st, r, version)
		if row.State != domain.StateDestroyed || row.DestroyedAt == nil || row.Bound != 0 || row.HasAccessToken != 0 ||
			len(row.Ciphertext) != 0 || len(row.EncryptedDEK) != 0 || len(row.Nonce) != 0 || len(row.BindingKeySalt) != 0 ||
			row.KEKID != "" || row.WrapMode != "" || row.Algorithm != "" || row.AAD != "" || row.ExpiresAt != nil ||
			row.ContentType != "" || row.MetadataJSON != "" || row.CreatedBy != "creator" || row.CreatedAt == "" {
			t.Fatalf("version %d tombstone retained recoverable data: %+v", version, row)
		}
	}
	if row := rawSecretVersion(t, st, r, 1); row.State == domain.StateDestroyed || len(row.Ciphertext) == 0 {
		t.Fatalf("outside-cohort version changed: %+v", row)
	}
	labelsAfter, err := loadSecretLabels(st.db, secret.ID)
	if err != nil || !reflect.DeepEqual(labelsAfter, labelsBefore) {
		t.Fatalf("labels after purge = %v, want %v (err=%v)", labelsAfter, labelsBefore, err)
	}
	projected, err := st.findSecret(st.db, r)
	if err != nil {
		t.Fatal(err)
	}
	if projected.ContentType != "" || projected.MetadataJSON != "" {
		t.Fatalf("purged current retained secret projection: content_type=%q metadata=%q", projected.ContentType, projected.MetadataJSON)
	}
	_, current, err := st.GetSecretVersion(ctx, r, 0, "")
	if err != nil || current.Version != 3 || current.State != domain.StateDestroyed {
		t.Fatalf("current tombstone = %+v err=%v", current, err)
	}
	var releaseEntryCount int64
	if err := st.db.Model(&configurationReleaseEntryModel{}).Where("release_id = ?", release.ID).Count(&releaseEntryCount).Error; err != nil || releaseEntryCount != 1 {
		t.Fatalf("release pin after purge: count=%d err=%v", releaseEntryCount, err)
	}
	var auditAfter, changesAfter int64
	_ = st.db.Model(&auditEventModel{}).Count(&auditAfter).Error
	_ = st.db.Model(&changeLogModel{}).Count(&changesAfter).Error
	if auditAfter != auditBefore+1 || changesAfter != changesBefore+1 {
		t.Fatalf("transactional records: audit %d->%d changes %d->%d", auditBefore, auditAfter, changesBefore, changesAfter)
	}
	changes, err := st.ListChangesSince(ctx, preview.Revision, 10)
	if err != nil || len(changes) != 1 || changes[0].ChangeType != domain.ChangePurgeBindingCohort || !slices.Equal(changes[0].AffectedVersions, []uint64{2, 3}) {
		t.Fatalf("purge change = %+v err=%v", changes, err)
	}
	audits, _, err := st.ListAudit(ctx, domain.AuditFilter{EventType: purgeBindingCohortAuditEvent}, ListPage{})
	if err != nil || len(audits) != 1 {
		t.Fatalf("purge audit = %+v err=%v", audits, err)
	}
	if audit := audits[0]; audit.ActorIdentity != "admin" || audit.ResourceNamespaceID != ns.ID || audit.ResourceVersion != 3 ||
		audit.Decision != "allow" || audit.Metadata != `{"affected_versions":[2,3]}` {
		t.Fatalf("purge audit = %+v", audit)
	}
	var highWater secretVersionHighWaterModel
	if err := st.db.Where("namespace_id = ? AND name = ?", ns.ID, r.Key).First(&highWater).Error; err != nil || highWater.LastVersion != 3 {
		t.Fatalf("high water = %+v err=%v", highWater, err)
	}
}

func TestPurgeNonCurrentCohortPreservesCurrentProjection(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "purge-non-current")
	putBindingVersion(t, st, r, 'A')
	putBindingVersion(t, st, r, 'B')
	putBindingVersion(t, st, r, 'A', func(p *CreateSecretParams) {
		p.ContentType = "application/current"
		p.Metadata = `{"current":true}`
	})

	if _, err := st.PurgeSecretBindingCohort(ctx, r, 2, SecretBindingCASGuard{}, bindingKeyTest('B'), SecretBindingPurgeAudit{ActorIdentity: "admin"}); err != nil {
		t.Fatal(err)
	}
	projected, err := st.findSecret(st.db, r)
	if err != nil {
		t.Fatal(err)
	}
	if projected.ContentType != "application/current" || projected.MetadataJSON != `{"current":true}` {
		t.Fatalf("non-current purge changed current projection: content_type=%q metadata=%q", projected.ContentType, projected.MetadataJSON)
	}
}

func TestPurgeScrubsLiveSQLiteArtifacts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kms.db")
	st, err := OpenWithOptions(path, Options{BusyTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "physical-purge")
	mainMarkers := newForensicSecretMarkers("MAIN", 1)
	walMarkers := newForensicSecretMarkers("WAL", 2)
	putForensicBindingVersion(t, st, r, mainMarkers)
	if err := st.db.WithContext(ctx).Connection(func(conn *gorm.DB) error { return truncateWAL(conn) }); err != nil {
		t.Fatalf("stage main database marker: %v", err)
	}
	putForensicBindingVersion(t, st, r, walMarkers)

	mainBefore := readArtifact(t, path)
	walBefore := readArtifact(t, path+"-wal")
	for _, marker := range allForensicMarkers(mainMarkers) {
		if !bytes.Contains(mainBefore, marker) {
			t.Fatalf("main database did not contain staged %d-byte marker", len(marker))
		}
	}
	for _, marker := range allForensicMarkers(walMarkers) {
		if !bytes.Contains(walBefore, marker) {
			t.Fatalf("WAL did not contain staged %d-byte marker", len(marker))
		}
	}

	result, err := st.PurgeSecretBindingCohort(ctx, r, 1, SecretBindingCASGuard{}, bindingKeyTest('B'), SecretBindingPurgeAudit{ActorIdentity: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.AffectedVersions, []uint64{1, 2}) {
		t.Fatalf("purged versions = %v", result.AffectedVersions)
	}

	artifacts := map[string][]byte{
		"main": readArtifact(t, path),
		"wal":  readArtifact(t, path+"-wal"),
		"shm":  readArtifact(t, path+"-shm"),
	}
	for artifact, contents := range artifacts {
		for _, marker := range allForensicMarkers(mainMarkers, walMarkers) {
			if bytes.Contains(contents, marker) {
				t.Fatalf("%s artifact retained a purged %d-byte marker", artifact, len(marker))
			}
		}
	}
	if info, err := os.Stat(path + "-wal"); err == nil && info.Size() != 0 {
		t.Fatalf("successful purge left WAL size %d, want 0", info.Size())
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestPurgeReportsCommittedCleanupPendingWhenExternalReaderBlocksCheckpoint(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kms.db")
	st, err := OpenWithOptions(path, Options{BusyTimeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "pending-cleanup")
	putBindingVersion(t, st, r, 'B')

	reader, err := OpenWithOptions(path, Options{BusyTimeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	readerDB, err := reader.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	readTx, err := readerDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var visibleRows int
	if err := readTx.QueryRowContext(ctx, "SELECT COUNT(*) FROM secret_versions").Scan(&visibleRows); err != nil || visibleRows != 1 {
		_ = readTx.Rollback()
		t.Fatalf("establish external read snapshot: rows=%d err=%v", visibleRows, err)
	}

	result, purgeErr := st.PurgeSecretBindingCohort(ctx, r, 1, SecretBindingCASGuard{}, bindingKeyTest('B'), SecretBindingPurgeAudit{ActorIdentity: "admin"})
	if !errors.Is(purgeErr, ErrPurgeCleanupPending) {
		_ = readTx.Rollback()
		t.Fatalf("purge error = %v, want ErrPurgeCleanupPending", purgeErr)
	}
	if purgeErr.Error() != ErrPurgeCleanupPending.Error() {
		_ = readTx.Rollback()
		t.Fatalf("cleanup-pending error leaked details: %q", purgeErr)
	}
	if result.AnchorVersion != 1 || !slices.Equal(result.AffectedVersions, []uint64{1}) || result.Revision == 0 {
		_ = readTx.Rollback()
		t.Fatalf("committed purge did not return its result: %+v", result)
	}
	if err := st.Ping(ctx); err == nil {
		_ = readTx.Rollback()
		t.Fatal("cleanup-pending store continued serving after WAL scrub failure")
	}
	if blocked, err := OpenWithOptions(path, Options{BusyTimeout: 10 * time.Millisecond}); err == nil {
		_ = blocked.Close()
		_ = readTx.Rollback()
		t.Fatal("store opened while an external reader prevented startup WAL cleanup")
	}

	if err := readTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	// Reopening performs the startup TRUNCATE checkpoint, recovering a crash or
	// response failure between logical commit and physical cleanup.
	reopened, err := OpenWithOptions(path, Options{BusyTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("startup cleanup after reader release: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	row := rawSecretVersion(t, reopened, r, 1)
	if row.State != domain.StateDestroyed || len(row.Ciphertext) != 0 {
		t.Fatalf("cleanup-pending purge did not logically commit: %+v", row)
	}
	var auditCount int64
	if err := reopened.db.Model(&auditEventModel{}).Where("event_type = ?", purgeBindingCohortAuditEvent).Count(&auditCount).Error; err != nil || auditCount != 1 {
		t.Fatalf("cleanup-pending purge audit count=%d err=%v", auditCount, err)
	}
	if info, err := os.Stat(path + "-wal"); err == nil && info.Size() != 0 {
		t.Fatalf("startup cleanup left WAL size %d, want 0", info.Size())
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestPurgeQuiescesPrimedInProcessPoolAndRestoresPolicy(t *testing.T) {
	ctx := context.Background()
	st := newStoreWithOptions(t, Options{BusyTimeout: 20 * time.Millisecond})
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "in-process-reader")
	putBindingVersion(t, st, r, 'B')
	sqlDB, err := st.db.DB()
	if err != nil {
		t.Fatal(err)
	}

	// Keep one connection checked out with an old read snapshot and return a
	// second to the idle pool. SetMaxOpenConns(1) alone would let purge take the
	// idle connection without waiting for the reader because database/sql checks
	// the idle list before enforcing max-open.
	idleConn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	readerConn, err := sqlDB.Conn(ctx)
	if err != nil {
		_ = idleConn.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readerConn.Close() })
	readTx, err := readerConn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		_ = idleConn.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readTx.Rollback() })
	var visibleRows int
	if err := readTx.QueryRowContext(ctx, "SELECT COUNT(*) FROM secret_versions").Scan(&visibleRows); err != nil || visibleRows != 1 {
		_ = idleConn.Close()
		t.Fatalf("establish read snapshot: rows=%d err=%v", visibleRows, err)
	}
	if err := idleConn.Close(); err != nil {
		t.Fatal(err)
	}
	if stats := sqlDB.Stats(); stats.InUse != 1 || stats.Idle != 1 || stats.OpenConnections != 2 {
		t.Fatalf("primed pool stats = %+v, want one active reader and one idle connection", stats)
	}

	type purgeResult struct {
		result SecretBindingResult
		err    error
	}
	done := make(chan purgeResult, 1)
	go func() {
		result, err := st.PurgeSecretBindingCohort(ctx, r, 1, SecretBindingCASGuard{}, bindingKeyTest('B'), SecretBindingPurgeAudit{ActorIdentity: "admin"})
		done <- purgeResult{result: result, err: err}
	}()

	deadline := time.Now().Add(time.Second)
	for {
		stats := sqlDB.Stats()
		if stats.MaxOpenConnections == 1 && stats.OpenConnections == 2 && stats.InUse == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("purge never pinned its connection while the in-process reader remained active")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case got := <-done:
		t.Fatalf("purge returned before in-process reader drained: result=%+v err=%v", got.result, got.err)
	case <-time.After(4 * 20 * time.Millisecond):
	}
	if err := readTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := readerConn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || !slices.Equal(got.result.AffectedVersions, []uint64{1}) {
			t.Fatalf("purge after reader drain: result=%+v err=%v", got.result, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("purge did not finish after in-process reader drained")
	}

	if got := sqlDB.Stats().MaxOpenConnections; got != sqlStoreMaxOpenConns {
		t.Fatalf("max-open after purge = %d, want configured %d", got, sqlStoreMaxOpenConns)
	}
	first, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := sqlDB.Conn(ctx)
	if err != nil {
		_ = first.Close()
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		_ = second.Close()
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if stats := sqlDB.Stats(); stats.Idle != sqlStoreMaxIdleConns {
		t.Fatalf("idle connections after purge = %d, want configured %d (stats=%+v)", stats.Idle, sqlStoreMaxIdleConns, stats)
	}
}

func TestPurgeCleanupPendingRejectsQueuedWorkBeforeConnectionHandoff(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kms.db")
	st, err := OpenWithOptions(path, Options{BusyTimeout: 250 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "queued-work")
	putBindingVersion(t, st, r, 'B')

	reader, err := OpenWithOptions(path, Options{BusyTimeout: 250 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	readerDB, err := reader.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	readTx, err := readerDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readTx.Rollback() })
	var visibleRows int
	if err := readTx.QueryRowContext(ctx, "SELECT COUNT(*) FROM secret_versions").Scan(&visibleRows); err != nil || visibleRows != 1 {
		t.Fatalf("establish external read snapshot: rows=%d err=%v", visibleRows, err)
	}

	type purgeResult struct {
		result SecretBindingResult
		err    error
	}
	purgeDone := make(chan purgeResult, 1)
	go func() {
		result, err := st.PurgeSecretBindingCohort(ctx, r, 1, SecretBindingCASGuard{}, bindingKeyTest('B'), SecretBindingPurgeAudit{ActorIdentity: "admin"})
		purgeDone <- purgeResult{result: result, err: err}
	}()

	sqlDB, err := st.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		stats := sqlDB.Stats()
		if stats.MaxOpenConnections == 1 && stats.InUse == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("purge never pinned its exclusive connection")
		}
		time.Sleep(time.Millisecond)
	}

	waitBefore := sqlDB.Stats().WaitCount
	queuedDone := make(chan error, 1)
	queuedRef := ref("prod", "app", "must-not-commit")
	go func() {
		_, _, err := st.PutParameter(ctx, queuedRef, "value", "text/plain", "{}", "tester")
		queuedDone <- err
	}()
	deadline = time.Now().Add(time.Second)
	for sqlDB.Stats().WaitCount == waitBefore && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if sqlDB.Stats().WaitCount == waitBefore {
		t.Fatal("concurrent write never queued behind purge")
	}

	gotPurge := <-purgeDone
	if !errors.Is(gotPurge.err, ErrPurgeCleanupPending) || gotPurge.result.Revision == 0 {
		t.Fatalf("purge result = %+v err=%v, want committed cleanup-pending", gotPurge.result, gotPurge.err)
	}
	select {
	case err := <-queuedDone:
		if err == nil {
			t.Fatal("queued write was served after cleanup-pending retirement")
		}
	case <-time.After(time.Second):
		t.Fatal("queued write was not released when cleanup-pending retired the store")
	}

	if err := readTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWithOptions(path, Options{BusyTimeout: 250 * time.Millisecond})
	if err != nil {
		t.Fatalf("reopen after external reader release: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if _, err := reopened.GetParameter(ctx, queuedRef, 0, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("queued write persisted after fail-closed purge: %v", err)
	}
	if row := rawSecretVersion(t, reopened, r, 1); row.State != domain.StateDestroyed {
		t.Fatalf("purge did not commit before cleanup-pending: %+v", row)
	}
}

func TestPurgeSecretBindingCohortAuditFailureRollsBack(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "purge-rollback")
	putBindingVersion(t, st, r, 'B')
	putBindingVersion(t, st, r, 'B')
	preview, err := st.PreviewSecretBindingCohort(ctx, r, 1, bindingKeyTest('B'))
	if err != nil {
		t.Fatal(err)
	}
	before := []secretVersionModel{rawSecretVersion(t, st, r, 1), rawSecretVersion(t, st, r, 2)}
	if err := st.db.Exec(`CREATE TRIGGER reject_purge_audit BEFORE INSERT ON audit_events
		BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`).Error; err != nil {
		t.Fatal(err)
	}
	_, err = st.PurgeSecretBindingCohort(ctx, r, 1, SecretBindingCASGuard{
		ExpectedRevision: uint64Pointer(preview.Revision), ExpectedAffectedVersions: preview.AffectedVersions,
	}, bindingKeyTest('B'), SecretBindingPurgeAudit{ActorIdentity: "admin"})
	if !errors.Is(err, ErrRequiredAuditUnavailable) {
		t.Fatalf("purge audit failure = %v, want ErrRequiredAuditUnavailable", err)
	}
	for i, want := range before {
		if got := rawSecretVersion(t, st, r, uint64(i+1)); !reflect.DeepEqual(bindingRowSnapshot(got), bindingRowSnapshot(want)) {
			t.Fatalf("audit failure did not roll back version %d", i+1)
		}
	}
	if revision, _ := st.CurrentRevision(ctx); revision != preview.Revision {
		t.Fatalf("audit failure revision = %d, want %d", revision, preview.Revision)
	}
}

func TestBindingMutationAuditFailureRollsBack(t *testing.T) {
	tests := []struct {
		name  string
		bound bool
		run   func(context.Context, *SQLStore, domain.Ref) error
	}{
		{
			name: "bind",
			run: func(ctx context.Context, st *SQLStore, ref domain.Ref) error {
				_, err := st.BindSecretVersion(ctx, ref, 1, bindingKeyTest('B'), bindingRewrap('B', 'n'), SecretBindingAudit{ActorIdentity: "operator"})
				return err
			},
		},
		{
			name: "unbind", bound: true,
			run: func(ctx context.Context, st *SQLStore, ref domain.Ref) error {
				_, err := st.UnbindSecretVersion(ctx, ref, 1, standardRewrap, SecretBindingAudit{ActorIdentity: "operator"})
				return err
			},
		},
		{
			name: "rotate", bound: true,
			run: func(ctx context.Context, st *SQLStore, ref domain.Ref) error {
				_, err := st.RotateSecretBindingKey(ctx, ref, 1, SecretBindingCASGuard{}, bindingKeyTest('B'), bindingKeyTest('C'), bindingRewrap('C', 'n'), SecretBindingAudit{ActorIdentity: "operator"})
				return err
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newStore(t)
			ctx := context.Background()
			seedNS(t, st, "prod", "app")
			r := ref("prod", "app", "audit-rollback-"+tc.name)
			key := byte(0)
			if tc.bound {
				key = 'B'
			}
			putBindingVersion(t, st, r, key)
			before := rawSecretVersion(t, st, r, 1)
			beforeRevision, err := st.CurrentRevision(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var changesBefore, auditsBefore int64
			if err := st.db.Model(&changeLogModel{}).Count(&changesBefore).Error; err != nil {
				t.Fatal(err)
			}
			if err := st.db.Model(&auditEventModel{}).Count(&auditsBefore).Error; err != nil {
				t.Fatal(err)
			}
			if err := st.db.Exec(`CREATE TRIGGER reject_binding_mutation_audit BEFORE INSERT ON audit_events
				BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`).Error; err != nil {
				t.Fatal(err)
			}

			if err := tc.run(ctx, st, r); !errors.Is(err, ErrRequiredAuditUnavailable) {
				t.Fatalf("mutation audit failure = %v, want ErrRequiredAuditUnavailable", err)
			}
			if got := rawSecretVersion(t, st, r, 1); !reflect.DeepEqual(bindingRowSnapshot(got), bindingRowSnapshot(before)) {
				t.Fatal("audit failure did not roll back wrapping")
			}
			if revision, _ := st.CurrentRevision(ctx); revision != beforeRevision {
				t.Fatalf("audit failure revision = %d, want %d", revision, beforeRevision)
			}
			var changesAfter, auditsAfter int64
			_ = st.db.Model(&changeLogModel{}).Count(&changesAfter).Error
			_ = st.db.Model(&auditEventModel{}).Count(&auditsAfter).Error
			if changesAfter != changesBefore || auditsAfter != auditsBefore {
				t.Fatalf("audit failure changed durable records: changes %d->%d audits %d->%d", changesBefore, changesAfter, auditsBefore, auditsAfter)
			}
		})
	}
}

func TestPurgeSecretBindingCohortCASValidation(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "purge-cas")
	putBindingVersion(t, st, r, 'B')
	putBindingVersion(t, st, r, 'B')
	preview, err := st.PreviewSecretBindingCohort(ctx, r, 1, bindingKeyTest('B'))
	if err != nil {
		t.Fatal(err)
	}
	before := []secretVersionModel{rawSecretVersion(t, st, r, 1), rawSecretVersion(t, st, r, 2)}

	for _, guard := range []SecretBindingCASGuard{
		{ExpectedRevision: uint64Pointer(preview.Revision)},
		{ExpectedAffectedVersions: preview.AffectedVersions},
	} {
		if _, err := st.PurgeSecretBindingCohort(ctx, r, 1, guard, bindingKeyTest('B'), SecretBindingPurgeAudit{ActorIdentity: "admin"}); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("half purge guard %+v = %v", guard, err)
		}
	}

	if _, _, err := st.PutParameter(ctx, ref("prod", "app", "purge-revision-bump"), "x", "text/plain", "{}", "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PurgeSecretBindingCohort(ctx, r, 1, SecretBindingCASGuard{
		ExpectedRevision: uint64Pointer(preview.Revision), ExpectedAffectedVersions: preview.AffectedVersions,
	}, bindingKeyTest('B'), SecretBindingPurgeAudit{ActorIdentity: "admin"}); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("stale purge revision = %v", err)
	}
	preview, err = st.PreviewSecretBindingCohort(ctx, r, 1, bindingKeyTest('B'))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PurgeSecretBindingCohort(ctx, r, 1, SecretBindingCASGuard{
		ExpectedRevision: uint64Pointer(preview.Revision), ExpectedAffectedVersions: []uint64{1},
	}, bindingKeyTest('B'), SecretBindingPurgeAudit{ActorIdentity: "admin"}); !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("stale purge set = %v", err)
	}
	for i, want := range before {
		if got := rawSecretVersion(t, st, r, uint64(i+1)); !reflect.DeepEqual(bindingRowSnapshot(got), bindingRowSnapshot(want)) {
			t.Fatalf("failed purge CAS changed version %d", i+1)
		}
	}
	var auditCount int64
	if err := st.db.Model(&auditEventModel{}).Count(&auditCount).Error; err != nil || auditCount != 0 {
		t.Fatalf("failed purge CAS audit count=%d err=%v", auditCount, err)
	}
}

func TestPurgePreservesVersionHighWaterAcrossDeleteAndRecreate(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "purge-high-water")
	putBindingVersion(t, st, r, 'B')
	putBindingVersion(t, st, r, 'B')
	if _, err := st.PurgeSecretBindingCohort(ctx, r, 1, SecretBindingCASGuard{}, bindingKeyTest('B'), SecretBindingPurgeAudit{ActorIdentity: "admin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteSecret(ctx, r); err != nil {
		t.Fatal(err)
	}
	if got := putBindingVersion(t, st, r, 0); got != 3 {
		t.Fatalf("recreated version = %d, want 3", got)
	}
}

func TestChangeLogHasAffectedVersionsBaselineColumn(t *testing.T) {
	st := newStore(t)
	if !st.db.Migrator().HasColumn(&changeLogModel{}, "affected_versions_json") {
		t.Fatal("change_log lacks affected_versions_json")
	}
}
