package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// PutSecretInput describes a secret write (creation or new version).
type PutSecretInput struct {
	Ref         domain.Ref
	Value       []byte
	ContentType string
	Metadata    string
	// BindingKey is operator-owned key material used to bind this version. An
	// empty value creates an unbound version; it is never persisted or hashed.
	BindingKey string
	// GenerateToken independently mints or rotates the secret-level access
	// token, returned exactly once.
	GenerateToken bool
	// CreateOnly rejects the write when the secret already exists. Storage's
	// write expectation also rejects a concurrent creation atomically.
	CreateOnly bool
	ExpiresAt  int64 // unix ms, 0 = never
}

// PutSecretResult reports the write outcome.
type PutSecretResult struct {
	Version  uint64
	Revision uint64
	// AccessToken is non-empty only when GenerateToken was set. It is never
	// persisted or retrievable again.
	AccessToken string
}

// GetSecret decrypts and returns a secret value for an authorized machine
// caller. Per-secret access tokens, when set, are required — including for
// admins (the audited admin path is RevealSecret).
func (s *Service) GetSecret(ctx context.Context, pr Principal, ref domain.Ref, version uint64, label, secretToken, bindingKey string) (domain.SecretValue, error) {
	if err := validateRef(ref); err != nil {
		return domain.SecretValue{}, err
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpSecretRead, domain.ResourceSecret, ref)
	if err != nil {
		return domain.SecretValue{}, err
	}
	keyring, err := s.requireKeyring()
	if err != nil {
		return domain.SecretValue{}, err
	}

	rec, ver, err := s.store.GetSecretVersion(ctx, ref, version, label)
	if err != nil {
		return domain.SecretValue{}, err
	}
	// Both credentials are checked against the exact version before its state,
	// expiry, or ciphertext is inspected. This prevents callers missing either
	// credential from probing version state. Access-token failures remain a
	// generic authorization denial; all unusable binding-key material collapses
	// to the same generic decryption error.
	if ver.HasAccessToken && !tokenHashMatches(secretToken, rec.AccessTokenHash) {
		s.auditRefWithNamespaceID(ctx, pr, "secret.read", domain.ResourceSecret, ref, namespace.ID, ver.Version, "deny",
			map[string]string{"reason": "credential"})
		return domain.SecretValue{}, domain.Errorf(domain.ErrPermissionDenied, "access denied")
	}
	if ver.Bound {
		if err := testVersionBindingKey(keyring, ref, ver, bindingKey); err != nil {
			s.auditRefWithNamespaceID(ctx, pr, "secret.read", domain.ResourceSecret, ref, namespace.ID, ver.Version, "error", nil)
			return domain.SecretValue{}, err
		}
	}
	if err := s.checkVersionReadable(ver); err != nil {
		return domain.SecretValue{}, err
	}

	plaintext, err := s.decryptVersion(keyring, rec, ver, bindingKey)
	if err != nil {
		s.auditRefWithNamespaceID(ctx, pr, "secret.read", domain.ResourceSecret, ref, namespace.ID, ver.Version, "error", nil)
		s.log.Error("secret decryption failed", zap.String("ref", ref.String()), zap.Uint64("version", ver.Version), zap.String("kek_id", ver.KEKID))
		return domain.SecretValue{}, err
	}

	// Fail closed: a secret read that cannot be audited is not served.
	if err := s.auditRefStrictWithNamespaceID(ctx, pr, "secret.read", domain.ResourceSecret, ref, namespace.ID, ver.Version, "allow", nil); err != nil {
		crypto.Zero(plaintext)
		return domain.SecretValue{}, err
	}

	return domain.SecretValue{
		Ref:         ref,
		Version:     ver.Version,
		Value:       plaintext,
		ContentType: ver.ContentType,
		Metadata:    ver.Metadata,
		CreatedAt:   ver.CreatedAt,
	}, nil
}

// RevealSecret is the audited admin break-glass path. It bypasses the
// independent access-token gate, but a bound version still requires its
// operator-owned binding key because the server cannot decrypt without it.
func (s *Service) RevealSecret(ctx context.Context, pr Principal, ref domain.Ref, version uint64, label, bindingKey string) (domain.SecretValue, error) {
	if err := validateRef(ref); err != nil {
		return domain.SecretValue{}, err
	}
	if !pr.IsAdmin() {
		s.auditRef(ctx, pr, "secret.reveal", domain.ResourceSecret, ref, version, "deny", nil)
		return domain.SecretValue{}, domain.Errorf(domain.ErrPermissionDenied, "access denied")
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpSecretRead, domain.ResourceSecret, ref)
	if err != nil {
		return domain.SecretValue{}, err
	}
	keyring, err := s.requireKeyring()
	if err != nil {
		return domain.SecretValue{}, err
	}

	rec, ver, err := s.store.GetSecretVersion(ctx, ref, version, label)
	if err != nil {
		return domain.SecretValue{}, err
	}
	if ver.Bound {
		if err := testVersionBindingKey(keyring, ref, ver, bindingKey); err != nil {
			s.auditRefWithNamespaceID(ctx, pr, "secret.reveal", domain.ResourceSecret, ref, namespace.ID, ver.Version, "error", nil)
			return domain.SecretValue{}, err
		}
	}
	if err := s.checkVersionReadable(ver); err != nil {
		return domain.SecretValue{}, err
	}

	plaintext, err := s.decryptVersion(keyring, rec, ver, bindingKey)
	if err != nil {
		s.auditRefWithNamespaceID(ctx, pr, "secret.reveal", domain.ResourceSecret, ref, namespace.ID, ver.Version, "error", nil)
		s.log.Error("secret decryption failed", zap.String("ref", ref.String()), zap.Uint64("version", ver.Version), zap.String("kek_id", ver.KEKID))
		return domain.SecretValue{}, err
	}
	if err := s.auditRefStrictWithNamespaceID(ctx, pr, "secret.reveal", domain.ResourceSecret, ref, namespace.ID, ver.Version, "allow", nil); err != nil {
		crypto.Zero(plaintext)
		return domain.SecretValue{}, err
	}

	return domain.SecretValue{
		Ref:         ref,
		Version:     ver.Version,
		Value:       plaintext,
		ContentType: ver.ContentType,
		Metadata:    ver.Metadata,
		CreatedAt:   ver.CreatedAt,
	}, nil
}

// PutSecret creates a secret or appends a new immutable version.
func (s *Service) PutSecret(ctx context.Context, pr Principal, in PutSecretInput) (PutSecretResult, error) {
	if err := validateRef(in.Ref); err != nil {
		return PutSecretResult{}, err
	}
	if len(in.Value) == 0 {
		return PutSecretResult{}, domain.Errorf(domain.ErrInvalidArgument, "secret value must not be empty")
	}
	if len(in.Value) > maxValueBytes {
		return PutSecretResult{}, domain.Errorf(domain.ErrInvalidArgument, "value exceeds %d bytes", maxValueBytes)
	}
	// Reject an expiry that is already in the past: it would create a version
	// that is unreadable from the moment it is written.
	if expiresAt := unixMSToTime(in.ExpiresAt); !expiresAt.IsZero() && !expiresAt.After(s.now()) {
		return PutSecretResult{}, domain.Errorf(domain.ErrInvalidArgument, "expires_at is in the past")
	}
	if in.ContentType == "" {
		in.ContentType = "application/octet-stream"
	}
	metadata, err := validateMetadataJSON(in.Metadata)
	if err != nil {
		return PutSecretResult{}, err
	}
	bound := in.BindingKey != ""
	if bound {
		if err := crypto.ValidateBindingKey(in.BindingKey); err != nil {
			return PutSecretResult{}, domain.Errorf(domain.ErrInvalidArgument, "binding_key must be valid UTF-8 and at least %d bytes", crypto.MinBindingKeyBytes)
		}
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpSecretWrite, domain.ResourceSecret, in.Ref)
	if err != nil {
		return PutSecretResult{}, err
	}

	existing, err := s.store.GetSecretRecord(ctx, in.Ref)
	exists := err == nil
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return PutSecretResult{}, err
	}
	if in.CreateOnly && exists {
		return PutSecretResult{}, domain.Errorf(
			domain.ErrAlreadyExists,
			"secret %s already exists; create a new version from the secret editor",
			in.Ref,
		)
	}
	expected := &storage.SecretWriteExpectation{Exists: exists}
	if exists {
		expected.ID = existing.ID
		expected.AccessTokenHash = append([]byte(nil), existing.AccessTokenHash...)
	}

	var newTokenHash []byte
	var mintedToken string
	if in.GenerateToken {
		mintedToken, newTokenHash, err = crypto.GenerateToken("kmss")
		if err != nil {
			return PutSecretResult{}, err
		}
	}

	// Keep the selected active KEK stable through the write in this process.
	// SQL storage independently checks key_metadata inside the transaction to
	// catch a rotation performed by another process.
	s.keyWriteMu.RLock()
	keyring, err := s.requireKeyring()
	if err != nil {
		s.keyWriteMu.RUnlock()
		return PutSecretResult{}, err
	}
	kek := keyring.Active()
	value := in.Value
	ref := in.Ref
	version, revision, err := s.store.CreateSecretVersion(ctx, storage.CreateSecretParams{
		Ref:             ref,
		ContentType:     in.ContentType,
		Metadata:        metadata,
		CreatedBy:       pr.Identity.Name,
		Bound:           bound,
		AccessTokenHash: newTokenHash,
		Expected:        expected,
		ExpiresAt:       unixMSToTime(in.ExpiresAt),
		Encrypt: func(version uint64) (storage.EncryptedPayload, error) {
			aad := crypto.BuildAAD(ref.NS.Env, ref.NS.App, ref.Key, version)
			var res crypto.EncryptResult
			var eerr error
			if bound {
				res, eerr = crypto.EncryptBindingKey(kek, value, aad, in.BindingKey)
			} else {
				res, eerr = crypto.Encrypt(kek, value, aad)
			}
			if eerr != nil {
				return storage.EncryptedPayload{}, eerr
			}
			return storage.EncryptedPayload{
				Ciphertext:     res.Ciphertext,
				EncryptedDEK:   res.EncryptedDEK,
				KEKID:          res.KEKID,
				WrapMode:       res.WrapMode,
				BindingKeySalt: res.BindingKeySalt,
				Algorithm:      res.Algorithm,
				Nonce:          res.Nonce,
				AAD:            res.AAD,
			}, nil
		},
	})
	s.keyWriteMu.RUnlock()
	if err != nil {
		return PutSecretResult{}, err
	}

	meta := map[string]string{}
	if bound {
		meta["bound"] = "true"
	}
	if in.GenerateToken {
		meta["token_minted"] = "true"
	}
	s.auditRefWithNamespaceID(ctx, pr, "secret.write", domain.ResourceSecret, ref, namespace.ID, version, "allow", meta)
	s.getHub().Wake()

	return PutSecretResult{Version: version, Revision: revision, AccessToken: mintedToken}, nil
}

// SecretVersionTransitionResult reports the new current version and its
// unchanged source, now labeled previous.
type SecretVersionTransitionResult struct {
	CurrentVersion  uint64
	PreviousVersion uint64
	Revision        uint64
}

// SecretBindingCohortResult reports a discovered or mutated contiguous cohort.
type SecretBindingCohortResult struct {
	AnchorVersion    uint64
	AffectedVersions []uint64
	Revision         uint64
}

// SecretVersionSetResult reports a previewed or purged version set.
type SecretVersionSetResult struct {
	AffectedVersions []uint64
	Revision         uint64
}

// BindSecret clones the current unbound version into one new bound version.
func (s *Service) BindSecret(ctx context.Context, pr Principal, ref domain.Ref, expectedCurrentVersion uint64, bindingKey string) (SecretVersionTransitionResult, error) {
	if err := validateRef(ref); err != nil {
		s.auditRef(ctx, pr, "secret.bind", domain.ResourceSecret, ref, expectedCurrentVersion, "error", nil)
		return SecretVersionTransitionResult{}, err
	}
	if err := validateNewBindingKeyArgument(bindingKey); err != nil {
		s.auditRef(ctx, pr, "secret.bind", domain.ResourceSecret, ref, expectedCurrentVersion, "error", nil)
		return SecretVersionTransitionResult{}, err
	}
	return s.transitionSecretProtection(ctx, pr, ref, expectedCurrentVersion, storage.SecretTransitionBind, "", bindingKey, "secret.bind")
}

// UnbindSecret clones the current bound version into one new unbound version.
func (s *Service) UnbindSecret(ctx context.Context, pr Principal, ref domain.Ref, expectedCurrentVersion uint64, bindingKey string) (SecretVersionTransitionResult, error) {
	if err := validateRef(ref); err != nil {
		s.auditRef(ctx, pr, "secret.unbind", domain.ResourceSecret, ref, expectedCurrentVersion, "error", nil)
		return SecretVersionTransitionResult{}, err
	}
	return s.transitionSecretProtection(ctx, pr, ref, expectedCurrentVersion, storage.SecretTransitionUnbind, bindingKey, "", "secret.unbind")
}

// PreviewSecretBindingCohort discovers the contiguous bound-version cohort
// around anchor without changing storage. anchor 0 resolves current.
func (s *Service) PreviewSecretBindingCohort(ctx context.Context, pr Principal, ref domain.Ref, anchor uint64, bindingKey string) (SecretBindingCohortResult, error) {
	if err := validateRef(ref); err != nil {
		s.auditRef(ctx, pr, "secret.binding_cohort.preview", domain.ResourceSecret, ref, anchor, "error", nil)
		return SecretBindingCohortResult{}, err
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpSecretBindingManage, domain.ResourceSecret, ref)
	if err != nil {
		return SecretBindingCohortResult{}, err
	}

	// A read lock keeps the keyring view stable while storage holds its read
	// transaction. Preview does not serialize unrelated puts.
	s.keyWriteMu.RLock()
	keyring, err := s.requireKeyring()
	if err != nil {
		s.keyWriteMu.RUnlock()
		s.auditRefWithNamespaceID(ctx, pr, "secret.binding_cohort.preview", domain.ResourceSecret, ref, namespace.ID, anchor, "error", nil)
		return SecretBindingCohortResult{}, err
	}
	result, err := s.store.PreviewSecretBindingCohort(ctx, ref, anchor, bindingTest(keyring, ref, bindingKey))
	s.keyWriteMu.RUnlock()
	if err != nil {
		s.auditRefWithNamespaceID(ctx, pr, "secret.binding_cohort.preview", domain.ResourceSecret, ref, namespace.ID, anchor, "error", nil)
		return SecretBindingCohortResult{}, sanitizeBindingMutationError(err)
	}

	if err := s.auditRefRequiredStrictWithNamespaceID(ctx, pr, "secret.binding_cohort.preview", domain.ResourceSecret, ref, namespace.ID, result.AnchorVersion, "allow", nil); err != nil {
		s.auditRefWithNamespaceID(ctx, pr, "secret.binding_cohort.preview", domain.ResourceSecret, ref, namespace.ID, result.AnchorVersion, "error", nil)
		return SecretBindingCohortResult{}, err
	}
	return secretBindingCohortResult(result), nil
}

// RotateSecretBindingKey clones the current bound version under a new binding
// key. Historical versions continue requiring their old key.
func (s *Service) RotateSecretBindingKey(ctx context.Context, pr Principal, ref domain.Ref, expectedCurrentVersion uint64, bindingKey, newBindingKey string) (SecretVersionTransitionResult, error) {
	if err := validateRef(ref); err != nil {
		s.auditRef(ctx, pr, "secret.binding_key.rotate", domain.ResourceSecret, ref, expectedCurrentVersion, "error", nil)
		return SecretVersionTransitionResult{}, err
	}
	if err := validateNewBindingKeyArgument(newBindingKey); err != nil {
		s.auditRef(ctx, pr, "secret.binding_key.rotate", domain.ResourceSecret, ref, expectedCurrentVersion, "error", nil)
		return SecretVersionTransitionResult{}, err
	}
	return s.transitionSecretProtection(ctx, pr, ref, expectedCurrentVersion, storage.SecretTransitionRotate, bindingKey, newBindingKey, "secret.binding_key.rotate")
}

func (s *Service) transitionSecretProtection(ctx context.Context, pr Principal, ref domain.Ref, expectedCurrentVersion uint64, kind storage.SecretVersionTransitionKind, oldBindingKey, newBindingKey, eventType string) (SecretVersionTransitionResult, error) {
	if expectedCurrentVersion == 0 {
		s.auditRef(ctx, pr, eventType, domain.ResourceSecret, ref, expectedCurrentVersion, "error", nil)
		return SecretVersionTransitionResult{}, domain.Errorf(domain.ErrInvalidArgument, "expected_current_version is required")
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpSecretBindingManage, domain.ResourceSecret, ref)
	if err != nil {
		return SecretVersionTransitionResult{}, err
	}

	s.keyWriteMu.RLock()
	keyring, err := s.requireKeyring()
	if err != nil {
		s.keyWriteMu.RUnlock()
		s.auditRefWithNamespaceID(ctx, pr, eventType, domain.ResourceSecret, ref, namespace.ID, expectedCurrentVersion, "error", nil)
		return SecretVersionTransitionResult{}, err
	}
	activeKEK := keyring.Active()
	result, err := s.store.TransitionSecretVersion(ctx, storage.SecretVersionTransitionParams{
		Ref:                    ref,
		ExpectedCurrentVersion: expectedCurrentVersion,
		Kind:                   kind,
		CreatedBy:              pr.Identity.Name,
		Audit:                  secretBindingAudit(pr, s.now()),
		Encrypt: func(source storage.SecretVersionRecord, newVersion uint64) (storage.EncryptedPayload, error) {
			sourceKEK, err := keyring.Get(source.KEKID)
			if err != nil {
				return storage.EncryptedPayload{}, domain.ErrDecryptFailed
			}
			oldAAD := crypto.BuildAAD(ref.NS.Env, ref.NS.App, ref.Key, source.Version)
			plaintext, err := crypto.Decrypt(sourceKEK, crypto.DecryptInput{
				Ciphertext:     source.Ciphertext,
				EncryptedDEK:   source.EncryptedDEK,
				Nonce:          source.Nonce,
				AAD:            oldAAD,
				WrapMode:       source.WrapMode,
				BindingKeySalt: source.BindingKeySalt,
				BindingKey:     oldBindingKey,
			})
			if err != nil {
				return storage.EncryptedPayload{}, domain.ErrDecryptFailed
			}
			defer crypto.Zero(plaintext)
			// Prove the existing credential before reporting that the replacement
			// is unchanged. Otherwise a wrong credential repeated in both fields
			// bypasses the required old-key check and the normal audited failure path.
			if kind == storage.SecretTransitionRotate && oldBindingKey == newBindingKey {
				return storage.EncryptedPayload{}, domain.Errorf(domain.ErrInvalidArgument, "new binding key must differ from current binding key")
			}
			newAAD := crypto.BuildAAD(ref.NS.Env, ref.NS.App, ref.Key, newVersion)
			var encrypted crypto.EncryptResult
			if kind == storage.SecretTransitionUnbind {
				encrypted, err = crypto.Encrypt(activeKEK, plaintext, newAAD)
			} else {
				encrypted, err = crypto.EncryptBindingKey(activeKEK, plaintext, newAAD, newBindingKey)
			}
			if err != nil {
				return storage.EncryptedPayload{}, err
			}
			return encryptedPayload(encrypted), nil
		},
	})
	s.keyWriteMu.RUnlock()
	if err != nil {
		s.recordRequiredBindingAuditFailure(err)
		s.auditRefWithNamespaceID(ctx, pr, eventType, domain.ResourceSecret, ref, namespace.ID, expectedCurrentVersion, "error", nil)
		return SecretVersionTransitionResult{}, sanitizeBindingMutationError(err)
	}

	s.m().AuditEvent(eventType, DecisionAllow)
	s.getHub().Wake()
	return secretVersionTransitionResult(result), nil
}

// PurgeSecretBindingCohort irreversibly destroys the contiguous cohort around
// anchor. It is admin-only regardless of delegated secret:destroy policy.
func (s *Service) PurgeSecretBindingCohort(ctx context.Context, pr Principal, ref domain.Ref, anchor uint64, bindingKey string, expectedRevision uint64, expectedAffected []uint64) (SecretBindingCohortResult, error) {
	if err := validateRef(ref); err != nil {
		s.auditRef(ctx, pr, "secret.binding_cohort.purge", domain.ResourceSecret, ref, anchor, "error", nil)
		return SecretBindingCohortResult{}, err
	}
	// Reject non-admin callers before namespace lookup or cohort discovery so
	// delegated destroy policy cannot become a binding-key oracle.
	if !pr.IsAdmin() {
		s.auditRef(ctx, pr, "secret.binding_cohort.purge", domain.ResourceSecret, ref, anchor, "deny", nil)
		return SecretBindingCohortResult{}, domain.Errorf(domain.ErrPermissionDenied, "access denied")
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpSecretDestroy, domain.ResourceSecret, ref)
	if err != nil {
		return SecretBindingCohortResult{}, err
	}

	s.keyWriteMu.Lock()
	keyring, err := s.requireKeyring()
	if err != nil {
		s.keyWriteMu.Unlock()
		s.auditRefWithNamespaceID(ctx, pr, "secret.binding_cohort.purge", domain.ResourceSecret, ref, namespace.ID, anchor, "error", nil)
		return SecretBindingCohortResult{}, err
	}
	result, err := s.store.PurgeSecretBindingCohort(ctx, ref, anchor, storage.SecretBindingCASGuard{
		ExpectedRevision:         expectedRevision,
		ExpectedAffectedVersions: expectedAffected,
	}, bindingTest(keyring, ref, bindingKey), secretBindingAudit(pr, s.now()))
	s.keyWriteMu.Unlock()
	if errors.Is(err, storage.ErrPurgeCleanupPending) {
		// The logical purge, change-log row, and single allow audit already
		// committed. Wake consumers, preserve the committed result, and surface
		// the distinct cleanup state without appending a misleading error audit.
		s.m().AuditEvent("secret.binding_cohort.purge", DecisionAllow)
		s.getHub().Wake()
		return secretBindingCohortResult(result), storage.ErrPurgeCleanupPending
	}
	if err != nil {
		s.recordRequiredBindingAuditFailure(err)
		s.auditRefWithNamespaceID(ctx, pr, "secret.binding_cohort.purge", domain.ResourceSecret, ref, namespace.ID, anchor, "error", nil)
		return SecretBindingCohortResult{}, sanitizeBindingMutationError(err)
	}

	// Storage inserted the single allow audit atomically with the tombstones and
	// change-log entry. Appending a second allow row here would break that unit.
	s.m().AuditEvent("secret.binding_cohort.purge", DecisionAllow)
	s.getHub().Wake()
	return secretBindingCohortResult(result), nil
}

// PreviewSecretUnboundVersions returns every non-destroyed unbound version.
// It is admin-only even when destroy permission has been delegated.
func (s *Service) PreviewSecretUnboundVersions(ctx context.Context, pr Principal, ref domain.Ref) (SecretVersionSetResult, error) {
	if err := validateRef(ref); err != nil {
		s.auditRef(ctx, pr, "secret.unbound_versions.preview", domain.ResourceSecret, ref, 0, "error", nil)
		return SecretVersionSetResult{}, err
	}
	if !pr.IsAdmin() {
		s.auditRef(ctx, pr, "secret.unbound_versions.preview", domain.ResourceSecret, ref, 0, "deny", nil)
		return SecretVersionSetResult{}, domain.Errorf(domain.ErrPermissionDenied, "access denied")
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpSecretDestroy, domain.ResourceSecret, ref)
	if err != nil {
		return SecretVersionSetResult{}, err
	}
	result, err := s.store.PreviewSecretUnboundVersions(ctx, ref)
	if err != nil {
		s.auditRefWithNamespaceID(ctx, pr, "secret.unbound_versions.preview", domain.ResourceSecret, ref, namespace.ID, 0, "error", nil)
		return SecretVersionSetResult{}, err
	}
	if err := s.auditRefRequiredStrictWithNamespaceID(ctx, pr, "secret.unbound_versions.preview", domain.ResourceSecret, ref, namespace.ID, 0, "allow", nil); err != nil {
		s.auditRefWithNamespaceID(ctx, pr, "secret.unbound_versions.preview", domain.ResourceSecret, ref, namespace.ID, 0, "error", nil)
		return SecretVersionSetResult{}, err
	}
	return secretVersionSetResult(result), nil
}

// PurgeSecretUnboundVersions securely tombstones exactly the set returned by
// a prior preview. It intentionally bypasses release-reference safeguards.
func (s *Service) PurgeSecretUnboundVersions(ctx context.Context, pr Principal, ref domain.Ref, expectedRevision uint64, expectedAffected []uint64) (SecretVersionSetResult, error) {
	if err := validateRef(ref); err != nil {
		s.auditRef(ctx, pr, "secret.unbound_versions.purge", domain.ResourceSecret, ref, 0, "error", nil)
		return SecretVersionSetResult{}, err
	}
	if !pr.IsAdmin() {
		s.auditRef(ctx, pr, "secret.unbound_versions.purge", domain.ResourceSecret, ref, 0, "deny", nil)
		return SecretVersionSetResult{}, domain.Errorf(domain.ErrPermissionDenied, "access denied")
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpSecretDestroy, domain.ResourceSecret, ref)
	if err != nil {
		return SecretVersionSetResult{}, err
	}

	s.keyWriteMu.Lock()
	result, err := s.store.PurgeSecretUnboundVersions(ctx, ref, expectedRevision, expectedAffected, secretBindingAudit(pr, s.now()))
	s.keyWriteMu.Unlock()
	if errors.Is(err, storage.ErrPurgeCleanupPending) {
		s.m().AuditEvent("secret.unbound_versions.purge", DecisionAllow)
		s.getHub().Wake()
		return secretVersionSetResult(result), storage.ErrPurgeCleanupPending
	}
	if err != nil {
		s.recordRequiredBindingAuditFailure(err)
		s.auditRefWithNamespaceID(ctx, pr, "secret.unbound_versions.purge", domain.ResourceSecret, ref, namespace.ID, 0, "error", nil)
		return SecretVersionSetResult{}, sanitizeBindingMutationError(err)
	}
	s.m().AuditEvent("secret.unbound_versions.purge", DecisionAllow)
	s.getHub().Wake()
	return secretVersionSetResult(result), nil
}

func secretVersionTransitionResult(result storage.SecretVersionTransitionResult) SecretVersionTransitionResult {
	return SecretVersionTransitionResult{
		CurrentVersion:  result.CurrentVersion,
		PreviousVersion: result.PreviousVersion,
		Revision:        result.Revision,
	}
}

func secretVersionSetResult(result storage.SecretVersionSetResult) SecretVersionSetResult {
	return SecretVersionSetResult{AffectedVersions: append([]uint64(nil), result.AffectedVersions...), Revision: result.Revision}
}

func secretBindingCohortResult(result storage.SecretBindingResult) SecretBindingCohortResult {
	return SecretBindingCohortResult{
		AnchorVersion:    result.AnchorVersion,
		AffectedVersions: append([]uint64(nil), result.AffectedVersions...),
		Revision:         result.Revision,
	}
}

func secretBindingAudit(pr Principal, createdAt time.Time) storage.SecretBindingAudit {
	return storage.SecretBindingAudit{
		ActorIdentity: pr.Identity.Name,
		ActorType:     pr.Identity.Kind,
		SourceIP:      pr.RemoteAddr,
		UserAgent:     pr.UserAgent,
		RequestID:     pr.RequestID,
		CreatedAt:     createdAt,
	}
}

func (s *Service) recordRequiredBindingAuditFailure(err error) {
	if errors.Is(err, storage.ErrRequiredAuditUnavailable) {
		s.m().AuditWriteFailed()
	}
}

func validateNewBindingKeyArgument(bindingKey string) error {
	if err := crypto.ValidateBindingKey(bindingKey); err != nil {
		return domain.Errorf(domain.ErrInvalidArgument, "binding_key must be valid UTF-8 and at least %d bytes", crypto.MinBindingKeyBytes)
	}
	return nil
}

func sanitizeBindingMutationError(err error) error {
	if errors.Is(err, domain.ErrDecryptFailed) {
		return domain.ErrDecryptFailed
	}
	if errors.Is(err, storage.ErrRequiredAuditUnavailable) {
		return domain.Errorf(domain.ErrFailedPrecondition, "audit unavailable")
	}
	return err
}

func bindingTest(keyring *crypto.Keyring, ref domain.Ref, bindingKey string) storage.SecretBindingTestFunc {
	return func(ver storage.SecretVersionRecord) error {
		return testVersionBindingKey(keyring, ref, ver, bindingKey)
	}
}

func testVersionBindingKey(keyring *crypto.Keyring, ref domain.Ref, ver storage.SecretVersionRecord, bindingKey string) error {
	if crypto.ValidateBindingKey(bindingKey) != nil {
		return domain.ErrDecryptFailed
	}
	kek, err := keyring.Get(ver.KEKID)
	if err != nil {
		return domain.ErrDecryptFailed
	}
	aad := crypto.BuildAAD(ref.NS.Env, ref.NS.App, ref.Key, ver.Version)
	if err := crypto.TestBindingKeyDEK(kek, ver.EncryptedDEK, ver.BindingKeySalt, aad, bindingKey); err != nil {
		return domain.ErrDecryptFailed
	}
	return nil
}

func encryptedPayload(result crypto.EncryptResult) storage.EncryptedPayload {
	return storage.EncryptedPayload{
		Ciphertext:     result.Ciphertext,
		EncryptedDEK:   result.EncryptedDEK,
		KEKID:          result.KEKID,
		WrapMode:       result.WrapMode,
		BindingKeySalt: result.BindingKeySalt,
		Algorithm:      result.Algorithm,
		Nonce:          result.Nonce,
		AAD:            result.AAD,
	}
}

// ListSecrets lists secret metadata in a namespace under a key prefix, filtered
// by policy.
func (s *Service) ListSecrets(ctx context.Context, pr Principal, ns domain.NamespaceRef, keyPrefix string, page storage.ListPage) ([]domain.Secret, string, error) {
	if err := validateListScope(ns, keyPrefix); err != nil {
		return nil, "", err
	}
	// Secret list responses are metadata-only (never values), so either
	// secret:list or secret:read on an item exposes its metadata.
	ctx, _, filter, err := s.listFilter(ctx, pr, domain.ResourceSecret, domain.OpSecretList, ns, domain.OpSecretList, domain.OpSecretRead)
	if err != nil {
		return nil, "", err
	}
	secrets, next, err := s.store.ListSecrets(ctx, ns, keyPrefix, page)
	if err != nil {
		return nil, "", err
	}
	out := secrets[:0]
	for _, sec := range secrets {
		if filter(sec.Ref) {
			out = append(out, sec)
		}
	}
	return out, next, nil
}

// GetSecretInfo returns secret metadata and version history (no values).
func (s *Service) GetSecretInfo(ctx context.Context, pr Principal, ref domain.Ref) (domain.Secret, error) {
	if err := validateRef(ref); err != nil {
		return domain.Secret{}, err
	}
	ctx, _, err := s.authorize(ctx, pr, domain.OpSecretRead, domain.ResourceSecret, ref)
	if err != nil {
		return domain.Secret{}, err
	}
	return s.store.GetSecretInfo(ctx, ref)
}

// GetSecretVersionInfo returns metadata for one version selected by number or label (no values).
func (s *Service) GetSecretVersionInfo(ctx context.Context, pr Principal, ref domain.Ref, version uint64, label string) (domain.Secret, error) {
	if err := validateRef(ref); err != nil {
		return domain.Secret{}, err
	}
	if (version == 0 && label == "") || (version != 0 && label != "") {
		return domain.Secret{}, domain.Errorf(domain.ErrInvalidArgument, "exactly one secret version or label is required")
	}
	ctx, _, err := s.authorize(ctx, pr, domain.OpSecretRead, domain.ResourceSecret, ref)
	if err != nil {
		return domain.Secret{}, err
	}
	return s.store.GetSecretVersionInfo(ctx, ref, version, label)
}

// DeleteSecret removes a secret and all versions (ciphertext included).
func (s *Service) DeleteSecret(ctx context.Context, pr Principal, ref domain.Ref) (uint64, error) {
	if err := validateRef(ref); err != nil {
		return 0, err
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpSecretDestroy, domain.ResourceSecret, ref)
	if err != nil {
		return 0, err
	}
	revision, err := s.store.DeleteSecret(ctx, ref)
	if err != nil {
		if errors.Is(err, domain.ErrFailedPrecondition) {
			s.auditProtectedReleaseReference(ctx, pr, ref, namespace.ID, domain.ReleaseEntrySecret, 0, "delete")
		}
		return 0, err
	}
	s.auditRefWithNamespaceID(ctx, pr, "secret.delete", domain.ResourceSecret, ref, namespace.ID, 0, "allow", nil)
	s.getHub().Wake()
	return revision, nil
}

// DisableSecret disables (or re-enables) a version, or all versions when
// version is 0.
func (s *Service) DisableSecret(ctx context.Context, pr Principal, ref domain.Ref, version uint64, enable bool) (uint64, error) {
	if err := validateRef(ref); err != nil {
		return 0, err
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpSecretDisable, domain.ResourceSecret, ref)
	if err != nil {
		return 0, err
	}
	state := domain.StateDisabled
	eventType := "secret.disable"
	if enable {
		state = domain.StateEnabled
		eventType = "secret.enable"
	}
	revision, err := s.store.SetSecretVersionState(ctx, ref, version, state)
	if err != nil {
		return 0, err
	}
	s.auditRefWithNamespaceID(ctx, pr, eventType, domain.ResourceSecret, ref, namespace.ID, version, "allow", nil)
	s.getHub().Wake()
	return revision, nil
}

// DestroySecretVersion irreversibly destroys one version's ciphertext.
func (s *Service) DestroySecretVersion(ctx context.Context, pr Principal, ref domain.Ref, version uint64) (uint64, error) {
	if err := validateRef(ref); err != nil {
		return 0, err
	}
	if version == 0 {
		return 0, domain.Errorf(domain.ErrInvalidArgument, "version is required")
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpSecretDestroy, domain.ResourceSecret, ref)
	if err != nil {
		return 0, err
	}
	revision, err := s.store.DestroySecretVersion(ctx, ref, version)
	if err != nil {
		if errors.Is(err, domain.ErrFailedPrecondition) {
			s.auditProtectedReleaseReference(ctx, pr, ref, namespace.ID, domain.ReleaseEntrySecret, version, "destroy")
		}
		return 0, err
	}
	s.auditRefWithNamespaceID(ctx, pr, "secret.destroy", domain.ResourceSecret, ref, namespace.ID, version, "allow", nil)
	s.getHub().Wake()
	return revision, nil
}

// PromoteSecretVersion points "current" at the given version.
func (s *Service) PromoteSecretVersion(ctx context.Context, pr Principal, ref domain.Ref, version uint64) (current, previous, revision uint64, err error) {
	if err := validateRef(ref); err != nil {
		return 0, 0, 0, err
	}
	if version == 0 {
		return 0, 0, 0, domain.Errorf(domain.ErrInvalidArgument, "version is required")
	}
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpSecretPromote, domain.ResourceSecret, ref)
	if err != nil {
		return 0, 0, 0, err
	}
	current, previous, revision, err = s.store.PromoteSecretVersion(ctx, ref, version)
	if err != nil {
		return 0, 0, 0, err
	}
	s.auditRefWithNamespaceID(ctx, pr, "secret.promote", domain.ResourceSecret, ref, namespace.ID, version, "allow", nil)
	s.getHub().Wake()
	return current, previous, revision, nil
}

// checkVersionReadable rejects disabled, destroyed, and expired versions.
func (s *Service) checkVersionReadable(ver storage.SecretVersionRecord) error {
	// State and tombstone time are independently persisted. Treat any
	// contradictory row as destroyed instead of allowing a corrupt "enabled"
	// state to resurrect tombstoned secret material.
	if !ver.DestroyedAt.IsZero() {
		return domain.Errorf(domain.ErrFailedPrecondition, "secret version %d is destroyed", ver.Version)
	}
	switch ver.State {
	case domain.StateEnabled:
	case domain.StateDisabled:
		return domain.Errorf(domain.ErrFailedPrecondition, "secret version %d is disabled", ver.Version)
	case domain.StateDestroyed:
		return domain.Errorf(domain.ErrFailedPrecondition, "secret version %d is destroyed", ver.Version)
	default:
		return domain.Errorf(domain.ErrFailedPrecondition, "secret version %d is unavailable", ver.Version)
	}
	if !ver.ExpiresAt.IsZero() && s.now().After(ver.ExpiresAt) {
		return domain.Errorf(domain.ErrFailedPrecondition, "secret version %d is expired", ver.Version)
	}
	return nil
}

// decryptVersion recovers plaintext for a stored version.
func (s *Service) decryptVersion(keyring *crypto.Keyring, rec storage.SecretRecord, ver storage.SecretVersionRecord, bindingKey string) ([]byte, error) {
	kek, err := keyring.Get(ver.KEKID)
	if err != nil {
		s.m().DecryptFailed()
		return nil, fmt.Errorf("secret %s v%d: %w", rec.Ref, ver.Version, domain.ErrDecryptFailed)
	}
	// Recompute the AAD from the row's authoritative identity (env/app/key +
	// version) rather than trusting the stored aad column. For a correctly stored
	// row this equals the stored value; for a row whose ciphertext/DEK/aad columns
	// were relocated to another secret (e.g. by direct DB tampering or a botched
	// migration) it no longer matches, so authentication fails loudly instead of
	// returning the wrong secret's plaintext.
	aad := crypto.BuildAAD(rec.Ref.NS.Env, rec.Ref.NS.App, rec.Ref.Key, ver.Version)
	pt, err := crypto.Decrypt(kek, crypto.DecryptInput{
		Ciphertext:     ver.Ciphertext,
		EncryptedDEK:   ver.EncryptedDEK,
		Nonce:          ver.Nonce,
		AAD:            aad,
		WrapMode:       ver.WrapMode,
		BindingKeySalt: ver.BindingKeySalt,
		BindingKey:     bindingKey,
	})
	if err != nil {
		// Deliberately generic: credential, ciphertext, and key failures are
		// indistinguishable to the caller.
		s.m().DecryptFailed()
		return nil, domain.ErrDecryptFailed
	}
	return pt, nil
}
