package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
)

func transitionPayload(bound bool, marker byte) func(SecretVersionRecord, uint64) (EncryptedPayload, error) {
	return func(_ SecretVersionRecord, version uint64) (EncryptedPayload, error) {
		payload := EncryptedPayload{
			Ciphertext:   []byte(fmt.Sprintf("fresh-ciphertext-%c-%d", marker, version)),
			EncryptedDEK: []byte(fmt.Sprintf("fresh-dek-%c-%d", marker, version)),
			KEKID:        "kek-a",
			WrapMode:     domain.WrapModeStandard,
			Algorithm:    "AES-256-GCM",
			Nonce:        []byte(fmt.Sprintf("fresh-nonce-%c-%d", marker, version)),
			AAD:          fmt.Sprintf("fresh-aad-%c-%d", marker, version),
		}
		if bound {
			payload.WrapMode = domain.WrapModeBindingKey
			payload.BindingKeySalt = bindingSalt(marker, 'n', version)
		}
		return payload, nil
	}
}

func TestTransitionSecretVersionClonesCurrentAndLeavesSourceImmutable(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "transition")
	expiresAt := time.Now().Add(-time.Hour).UTC()
	putBindingVersion(t, st, r, 0, func(p *CreateSecretParams) {
		p.AccessTokenHash = []byte("token-hash")
		p.ExpiresAt = expiresAt
	})
	if _, err := st.SetSecretVersionState(ctx, r, 1, domain.StateDisabled); err != nil {
		t.Fatalf("disable source: %v", err)
	}
	sourceBefore := rawSecretVersion(t, st, r, 1)

	result, err := st.TransitionSecretVersion(ctx, SecretVersionTransitionParams{
		Ref: r, ExpectedCurrentVersion: 1, Kind: SecretTransitionBind,
		CreatedBy: "operator", Encrypt: transitionPayload(true, 'B'),
		Audit: SecretBindingAudit{ActorIdentity: "operator"},
	})
	if err != nil {
		t.Fatalf("TransitionSecretVersion(bind): %v", err)
	}
	if result.CurrentVersion != 2 || result.PreviousVersion != 1 || result.Revision == 0 {
		t.Fatalf("transition result = %+v", result)
	}
	if got := rawSecretVersion(t, st, r, 1); !reflect.DeepEqual(bindingRowSnapshot(got), bindingRowSnapshot(sourceBefore)) {
		t.Fatal("source row changed during transition")
	}
	bound := rawSecretVersion(t, st, r, 2)
	if bound.Bound != 1 || bound.HasAccessToken != sourceBefore.HasAccessToken || bound.State != sourceBefore.State ||
		bound.ContentType != sourceBefore.ContentType || bound.MetadataJSON != sourceBefore.MetadataJSON ||
		!reflect.DeepEqual(bound.ExpiresAt, sourceBefore.ExpiresAt) || bound.CreatedBy != "operator" {
		t.Fatalf("new version did not preserve non-protection properties: %+v", bound)
	}
	if bytes.Equal(bound.Ciphertext, sourceBefore.Ciphertext) || bytes.Equal(bound.EncryptedDEK, sourceBefore.EncryptedDEK) ||
		bytes.Equal(bound.Nonce, sourceBefore.Nonce) || bound.AAD == sourceBefore.AAD || len(bound.BindingKeySalt) == 0 {
		t.Fatal("new version did not receive fresh cryptographic material")
	}
	labels, err := loadSecretLabels(st.db, bound.SecretID)
	if err != nil || labels[domain.LabelCurrent] != 2 || labels[domain.LabelPrevious] != 1 {
		t.Fatalf("labels = %v, err=%v", labels, err)
	}

	unbound, err := st.TransitionSecretVersion(ctx, SecretVersionTransitionParams{
		Ref: r, ExpectedCurrentVersion: 2, Kind: SecretTransitionUnbind,
		CreatedBy: "operator-2", Encrypt: transitionPayload(false, 'U'), Audit: SecretBindingAudit{ActorIdentity: "operator-2"},
	})
	if err != nil || unbound.CurrentVersion != 3 || unbound.PreviousVersion != 2 {
		t.Fatalf("TransitionSecretVersion(unbind) = %+v, err=%v", unbound, err)
	}
	if got := rawSecretVersion(t, st, r, 2); !reflect.DeepEqual(bindingRowSnapshot(got), bindingRowSnapshot(bound)) {
		t.Fatal("bound source row changed during unbind")
	}
	if row := rawSecretVersion(t, st, r, 3); row.Bound != 0 || row.WrapMode != domain.WrapModeStandard || len(row.BindingKeySalt) != 0 || row.State != domain.StateDisabled {
		t.Fatalf("unbound clone = %+v", row)
	}
}

func TestTransitionSecretVersionUsesCurrentLabelAndNextHighWaterVersion(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "transition-promoted-current")
	putBindingVersion(t, st, r, 0)
	putBindingVersion(t, st, r, 0)
	if current, previous, _, err := st.PromoteSecretVersion(ctx, r, 1); err != nil || current != 1 || previous != 2 {
		t.Fatalf("PromoteSecretVersion = current %d previous %d err=%v", current, previous, err)
	}
	sourceBefore := rawSecretVersion(t, st, r, 1)

	result, err := st.TransitionSecretVersion(ctx, SecretVersionTransitionParams{
		Ref: r, ExpectedCurrentVersion: 1, Kind: SecretTransitionBind,
		CreatedBy: "operator", Encrypt: transitionPayload(true, 'H'),
		Audit: SecretBindingAudit{ActorIdentity: "operator"},
	})
	if err != nil {
		t.Fatalf("TransitionSecretVersion: %v", err)
	}
	if result.CurrentVersion != 3 || result.PreviousVersion != 1 {
		t.Fatalf("transition result = %+v, want high-water v3 cloned from current-label v1", result)
	}
	if got := rawSecretVersion(t, st, r, 1); !reflect.DeepEqual(bindingRowSnapshot(got), bindingRowSnapshot(sourceBefore)) {
		t.Fatal("promoted source row changed during transition")
	}
	labels, err := loadSecretLabels(st.db, sourceBefore.SecretID)
	if err != nil || labels[domain.LabelCurrent] != 3 || labels[domain.LabelPrevious] != 1 {
		t.Fatalf("labels = %v, err=%v", labels, err)
	}
}

func TestTransitionSecretVersionGuardsModesAndRollsBackAuditFailure(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "transition-guards")
	putBindingVersion(t, st, r, 0)
	original := rawSecretVersion(t, st, r, 1)
	called := false
	if _, err := st.TransitionSecretVersion(ctx, SecretVersionTransitionParams{
		Ref: r, ExpectedCurrentVersion: 2, Kind: SecretTransitionBind,
		Encrypt: func(source SecretVersionRecord, version uint64) (EncryptedPayload, error) {
			called = true
			return transitionPayload(true, 'B')(source, version)
		},
	}); !errors.Is(err, domain.ErrAborted) || called {
		t.Fatalf("stale guard err=%v callback-called=%v", err, called)
	}
	if _, err := st.TransitionSecretVersion(ctx, SecretVersionTransitionParams{
		Ref: r, ExpectedCurrentVersion: 1, Kind: SecretTransitionUnbind, Encrypt: transitionPayload(false, 'U'),
	}); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("unbind unbound source err=%v", err)
	}
	if err := st.db.Exec(`CREATE TRIGGER reject_transition_audit BEFORE INSERT ON audit_events
		BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`).Error; err != nil {
		t.Fatalf("create audit trigger: %v", err)
	}
	if _, err := st.TransitionSecretVersion(ctx, SecretVersionTransitionParams{
		Ref: r, ExpectedCurrentVersion: 1, Kind: SecretTransitionBind,
		CreatedBy: "operator", Encrypt: transitionPayload(true, 'B'), Audit: SecretBindingAudit{ActorIdentity: "operator"},
	}); !errors.Is(err, ErrRequiredAuditUnavailable) {
		t.Fatalf("audit failure err=%v", err)
	}
	if got := rawSecretVersion(t, st, r, 1); !reflect.DeepEqual(bindingRowSnapshot(got), bindingRowSnapshot(original)) {
		t.Fatal("audit failure changed source")
	}
	var count int64
	if err := st.db.Model(&secretVersionModel{}).Where("secret_id = ?", original.SecretID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("audit failure left %d versions, err=%v", count, err)
	}
}

func TestTransitionSecretVersionRejectsInconsistentSourceWrapping(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name        string
		bound       byte
		corruption  map[string]any
		kind        SecretVersionTransitionKind
		targetBound bool
	}{
		{
			name:  "bind source marked unbound with binding-key wrapping",
			bound: 0,
			corruption: map[string]any{
				"wrap_mode": domain.WrapModeBindingKey, "binding_key_salt": bindingSalt('x', 'i', 1),
			},
			kind: SecretTransitionBind, targetBound: true,
		},
		{
			name:  "unbind source marked bound with standard wrapping",
			bound: 'B',
			corruption: map[string]any{
				"wrap_mode": domain.WrapModeStandard, "binding_key_salt": nil,
			},
			kind: SecretTransitionUnbind,
		},
		{
			name:  "rotation source marked bound with standard wrapping",
			bound: 'B',
			corruption: map[string]any{
				"wrap_mode": domain.WrapModeStandard, "binding_key_salt": nil,
			},
			kind: SecretTransitionRotate, targetBound: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := newStore(t)
			seedNS(t, st, "prod", "app")
			r := ref("prod", "app", "inconsistent-source")
			putBindingVersion(t, st, r, tc.bound)
			source := rawSecretVersion(t, st, r, 1)
			if err := st.db.Model(&secretVersionModel{}).Where("id = ?", source.ID).Updates(tc.corruption).Error; err != nil {
				t.Fatalf("corrupt source wrapping: %v", err)
			}
			source = rawSecretVersion(t, st, r, 1)
			beforeRevision, err := st.CurrentRevision(ctx)
			if err != nil {
				t.Fatal(err)
			}
			called := false
			_, err = st.TransitionSecretVersion(ctx, SecretVersionTransitionParams{
				Ref: r, ExpectedCurrentVersion: 1, Kind: tc.kind,
				Encrypt: func(source SecretVersionRecord, version uint64) (EncryptedPayload, error) {
					called = true
					return transitionPayload(tc.targetBound, 'N')(source, version)
				},
			})
			if !errors.Is(err, domain.ErrFailedPrecondition) || !strings.Contains(err.Error(), "invalid wrapping metadata") {
				t.Fatalf("transition error = %v, want invalid-wrapping failed precondition", err)
			}
			if called {
				t.Fatal("inconsistent source reached encryption callback")
			}
			if got := rawSecretVersion(t, st, r, 1); !reflect.DeepEqual(bindingRowSnapshot(got), bindingRowSnapshot(source)) {
				t.Fatal("rejected transition changed its source")
			}
			var count int64
			if err := st.db.Model(&secretVersionModel{}).Where("secret_id = ?", source.SecretID).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("rejected transition left %d versions, err=%v", count, err)
			}
			if revision, err := st.CurrentRevision(ctx); err != nil || revision != beforeRevision {
				t.Fatalf("rejected transition revision=%d err=%v, want %d", revision, err, beforeRevision)
			}
		})
	}
}

func TestTransitionSecretVersionRejectsMissingAndDestroyedCurrent(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	called := false
	encrypt := func(source SecretVersionRecord, version uint64) (EncryptedPayload, error) {
		called = true
		return transitionPayload(true, 'B')(source, version)
	}
	missing := ref("prod", "app", "missing-transition")
	if _, err := st.TransitionSecretVersion(ctx, SecretVersionTransitionParams{
		Ref: missing, ExpectedCurrentVersion: 1, Kind: SecretTransitionBind, Encrypt: encrypt,
	}); !errors.Is(err, domain.ErrNotFound) || called {
		t.Fatalf("missing current transition err=%v callback-called=%v", err, called)
	}

	r := ref("prod", "app", "destroyed-transition")
	putBindingVersion(t, st, r, 0)
	if _, err := st.DestroySecretVersion(ctx, r, 1); err != nil {
		t.Fatalf("destroy current: %v", err)
	}
	before := rawSecretVersion(t, st, r, 1)
	beforeRevision, err := st.CurrentRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	called = false
	if _, err := st.TransitionSecretVersion(ctx, SecretVersionTransitionParams{
		Ref: r, ExpectedCurrentVersion: 1, Kind: SecretTransitionBind, Encrypt: encrypt,
	}); !errors.Is(err, domain.ErrFailedPrecondition) || called {
		t.Fatalf("destroyed current transition err=%v callback-called=%v", err, called)
	}
	if got := rawSecretVersion(t, st, r, 1); !reflect.DeepEqual(bindingRowSnapshot(got), bindingRowSnapshot(before)) {
		t.Fatal("rejected destroyed-current transition changed its source")
	}
	if revision, err := st.CurrentRevision(ctx); err != nil || revision != beforeRevision {
		t.Fatalf("rejected destroyed-current revision=%d err=%v, want %d", revision, err, beforeRevision)
	}
}

func TestTransitionSecretVersionAbortsAfterConcurrentPutAdvancesCurrent(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "transition-concurrent-put")
	putBindingVersion(t, st, r, 0)
	sourceBefore := rawSecretVersion(t, st, r, 1)

	writerLocked := make(chan struct{})
	releaseWriter := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(releaseWriter)
		}
	}()
	type putOutcome struct {
		version  uint64
		revision uint64
		err      error
	}
	putDone := make(chan putOutcome, 1)
	go func() {
		version, revision, err := st.CreateSecretVersion(ctx, CreateSecretParams{
			Ref: r, ContentType: "text/plain", Metadata: `{"writer":"concurrent"}`, CreatedBy: "writer",
			Encrypt: func(version uint64) (EncryptedPayload, error) {
				close(writerLocked)
				<-releaseWriter
				return transitionPayload(false, 'P')(SecretVersionRecord{}, version)
			},
		})
		putDone <- putOutcome{version: version, revision: revision, err: err}
	}()
	<-writerLocked

	transitionCallbackCalled := false
	transitionDone := make(chan error, 1)
	go func() {
		_, err := st.TransitionSecretVersion(ctx, SecretVersionTransitionParams{
			Ref: r, ExpectedCurrentVersion: 1, Kind: SecretTransitionBind, CreatedBy: "operator",
			Encrypt: func(source SecretVersionRecord, version uint64) (EncryptedPayload, error) {
				transitionCallbackCalled = true
				return transitionPayload(true, 'B')(source, version)
			},
			Audit: SecretBindingAudit{ActorIdentity: "operator"},
		})
		transitionDone <- err
	}()

	sqlDB, err := st.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for sqlDB.Stats().InUse < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if sqlDB.Stats().InUse < 2 {
		close(releaseWriter)
		released = true
		<-putDone
		<-transitionDone
		t.Fatal("transition did not contend with the in-flight put")
	}
	close(releaseWriter)
	released = true
	put := <-putDone
	if put.err != nil || put.version != 2 || put.revision == 0 {
		t.Fatalf("concurrent put = %+v", put)
	}
	if err := <-transitionDone; !errors.Is(err, domain.ErrAborted) {
		t.Fatalf("transition err=%v, want stale-current abort", err)
	}
	if transitionCallbackCalled {
		t.Fatal("stale transition invoked its encryption callback")
	}
	if got := rawSecretVersion(t, st, r, 1); !reflect.DeepEqual(bindingRowSnapshot(got), bindingRowSnapshot(sourceBefore)) {
		t.Fatal("concurrent put or aborted transition changed the source row")
	}
	var versionCount int64
	if err := st.db.Model(&secretVersionModel{}).Where("secret_id = ?", sourceBefore.SecretID).Count(&versionCount).Error; err != nil || versionCount != 2 {
		t.Fatalf("version count=%d err=%v, want exactly put-created v2", versionCount, err)
	}
	_, current, err := st.GetSecretVersion(ctx, r, 0, "")
	if err != nil || current.Version != 2 || current.Bound {
		t.Fatalf("current after race=%+v err=%v", current, err)
	}
	var auditCount int64
	if err := st.db.Model(&auditEventModel{}).Count(&auditCount).Error; err != nil || auditCount != 0 {
		t.Fatalf("aborted transition audit count=%d err=%v", auditCount, err)
	}
}

func TestPreviewAndPurgeSecretUnboundVersionsExactSet(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "purge-unbound")
	putBindingVersion(t, st, r, 0)
	putBindingVersion(t, st, r, 'B')
	putBindingVersion(t, st, r, 0, func(p *CreateSecretParams) { p.ExpiresAt = time.Now().Add(-time.Hour) })
	putBindingVersion(t, st, r, 0)
	if _, err := st.DestroySecretVersion(ctx, r, 4); err != nil {
		t.Fatalf("destroy v4: %v", err)
	}
	putBindingVersion(t, st, r, 0)
	if _, err := st.SetSecretVersionState(ctx, r, 1, domain.StateDisabled); err != nil {
		t.Fatalf("disable v1: %v", err)
	}
	// Corrupt v3 deliberately: selection is based only on immutable mode/state.
	if err := st.db.Model(&secretVersionModel{}).Where("secret_id = ? AND version_number = ?", rawSecretVersion(t, st, r, 3).SecretID, 3).
		Updates(map[string]any{"ciphertext": nil, "encrypted_dek": nil, "nonce": nil}).Error; err != nil {
		t.Fatalf("corrupt v3: %v", err)
	}

	preview, err := st.PreviewSecretUnboundVersions(ctx, r)
	if err != nil || !slices.Equal(preview.AffectedVersions, []uint64{1, 3, 5}) {
		t.Fatalf("preview = %+v, err=%v", preview, err)
	}
	boundBefore := rawSecretVersion(t, st, r, 2)
	labelsBefore, err := loadSecretLabels(st.db, boundBefore.SecretID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.PutParameter(ctx, ref("prod", "app", "revision-bump"), "x", "text/plain", "{}", "tester"); err != nil {
		t.Fatalf("revision bump: %v", err)
	}
	if _, err := st.PurgeSecretUnboundVersions(ctx, r, preview.Revision, preview.AffectedVersions, SecretBindingPurgeAudit{}); !errors.Is(err, domain.ErrAborted) ||
		!strings.Contains(err.Error(), "version set changed") || strings.Contains(err.Error(), "binding cohort") {
		t.Fatalf("stale purge err=%v, want neutral version-set abort", err)
	}
	preview, err = st.PreviewSecretUnboundVersions(ctx, r)
	if err != nil {
		t.Fatalf("refresh preview: %v", err)
	}
	result, err := st.PurgeSecretUnboundVersions(ctx, r, preview.Revision, preview.AffectedVersions, SecretBindingPurgeAudit{ActorIdentity: "admin"})
	if err != nil || !slices.Equal(result.AffectedVersions, []uint64{1, 3, 5}) {
		t.Fatalf("purge = %+v, err=%v", result, err)
	}
	for _, version := range result.AffectedVersions {
		row := rawSecretVersion(t, st, r, version)
		if row.State != domain.StateDestroyed || row.ContentType != "" || row.HasAccessToken != 0 || len(row.Ciphertext) != 0 ||
			len(row.EncryptedDEK) != 0 || row.KEKID != "" || row.WrapMode != "" || len(row.BindingKeySalt) != 0 ||
			row.Algorithm != "" || len(row.Nonce) != 0 || row.AAD != "" || row.ExpiresAt != nil || row.MetadataJSON != "" {
			t.Fatalf("v%d tombstone retained protected data: %+v", version, row)
		}
	}
	if got := rawSecretVersion(t, st, r, 2); !reflect.DeepEqual(bindingRowSnapshot(got), bindingRowSnapshot(boundBefore)) {
		t.Fatal("bound version changed during unbound purge")
	}
	labelsAfter, err := loadSecretLabels(st.db, boundBefore.SecretID)
	if err != nil || !reflect.DeepEqual(labelsAfter, labelsBefore) {
		t.Fatalf("labels changed: before=%v after=%v err=%v", labelsBefore, labelsAfter, err)
	}
	var projected secretModel
	if err := st.db.First(&projected, boundBefore.SecretID).Error; err != nil || projected.ContentType != "" || projected.MetadataJSON != "" {
		t.Fatalf("purged current retained projection: %+v err=%v", projected, err)
	}
	if _, err := st.PreviewSecretUnboundVersions(ctx, r); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("empty preview err=%v", err)
	}
}

func TestPurgeSecretUnboundVersionsAuditFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	seedNS(t, st, "prod", "app")
	r := ref("prod", "app", "purge-unbound-audit-rollback")
	putBindingVersion(t, st, r, 0)
	putBindingVersion(t, st, r, 'B')
	putBindingVersion(t, st, r, 0)
	preview, err := st.PreviewSecretUnboundVersions(ctx, r)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	before := map[uint64]secretVersionModel{
		1: rawSecretVersion(t, st, r, 1),
		2: rawSecretVersion(t, st, r, 2),
		3: rawSecretVersion(t, st, r, 3),
	}
	if err := st.db.Exec(`CREATE TRIGGER reject_unbound_purge_audit BEFORE INSERT ON audit_events
		BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := st.PurgeSecretUnboundVersions(ctx, r, preview.Revision, preview.AffectedVersions, SecretBindingPurgeAudit{ActorIdentity: "admin"}); !errors.Is(err, ErrRequiredAuditUnavailable) {
		t.Fatalf("purge audit failure=%v, want ErrRequiredAuditUnavailable", err)
	}
	for version, want := range before {
		if got := rawSecretVersion(t, st, r, version); !reflect.DeepEqual(bindingRowSnapshot(got), bindingRowSnapshot(want)) {
			t.Fatalf("audit failure changed version %d", version)
		}
	}
	if revision, err := st.CurrentRevision(ctx); err != nil || revision != preview.Revision {
		t.Fatalf("audit failure revision=%d err=%v, want %d", revision, err, preview.Revision)
	}
	var auditCount int64
	if err := st.db.Model(&auditEventModel{}).Count(&auditCount).Error; err != nil || auditCount != 0 {
		t.Fatalf("audit failure left %d audit rows, err=%v", auditCount, err)
	}
}
