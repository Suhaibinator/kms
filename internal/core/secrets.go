package core

import (
	"context"
	"errors"
	"fmt"

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
	// ClientBound selects the double-wrapped mode. Must match the existing
	// secret's mode on updates.
	ClientBound bool
	// GenerateToken mints a fresh per-secret access token, returned exactly
	// once. Required when creating a client-bound secret; on an existing
	// client-bound secret it rotates the token (old token must be supplied
	// with the request).
	GenerateToken bool
	ExpiresAt     int64 // unix ms, 0 = never
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
func (s *Service) GetSecret(ctx context.Context, pr Principal, ref domain.Ref, version uint64, label string) (domain.SecretValue, error) {
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
	// Per-secret token gate. Applied before the version-state check so a caller
	// without the token cannot probe version state (disabled/destroyed/expired).
	// The generic error covers missing and wrong tokens so callers cannot
	// distinguish them.
	//
	// Protection presence is pinned to the exact version. This prevents adding
	// a token to a later version from retroactively making older immutable
	// versions unreadable. The token hash remains secret-scoped, so rotating an
	// existing standard-secret token replaces the credential for every version
	// that was created as token-protected.
	//
	// For a client-bound secret the token is the decryption key itself, bound
	// per version (each version has its own HKDF salt and may have been written
	// under a different token after a rotation). The secret-level AccessTokenHash
	// only tracks the LATEST token, so comparing against it would wrongly reject
	// a valid token for an older version. We therefore require only that a token
	// be present here and let the crypto layer authenticate it against the
	// specific version — a wrong token fails as a generic decryption error, with
	// no additional information leaked.
	if ver.ClientBound {
		if pr.SecretToken == "" {
			s.auditRefWithNamespaceID(ctx, pr, "secret.read", domain.ResourceSecret, ref, namespace.ID, ver.Version, "deny",
				map[string]string{"reason": "token"})
			return domain.SecretValue{}, domain.Errorf(domain.ErrPermissionDenied, "access denied")
		}
	} else if ver.HasAccessToken && !tokenHashMatches(pr.SecretToken, rec.AccessTokenHash) {
		s.auditRefWithNamespaceID(ctx, pr, "secret.read", domain.ResourceSecret, ref, namespace.ID, ver.Version, "deny",
			map[string]string{"reason": "token"})
		return domain.SecretValue{}, domain.Errorf(domain.ErrPermissionDenied, "access denied")
	}
	if err := s.checkVersionReadable(ver); err != nil {
		return domain.SecretValue{}, err
	}

	plaintext, err := s.decryptVersion(keyring, rec, ver, pr.SecretToken)
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

// RevealSecret is the audited admin path used by the frontend/CLI. It
// bypasses the per-secret token gate (break-glass) but can never decrypt
// client-bound secrets — the server lacks the key material by design.
func (s *Service) RevealSecret(ctx context.Context, pr Principal, ref domain.Ref, version uint64, label string) (domain.SecretValue, error) {
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
	if ver.ClientBound {
		return domain.SecretValue{}, domain.Errorf(domain.ErrFailedPrecondition,
			"client-bound secrets cannot be revealed: the server cannot decrypt them without the client token")
	}
	if err := s.checkVersionReadable(ver); err != nil {
		return domain.SecretValue{}, err
	}

	plaintext, err := s.decryptVersion(keyring, rec, ver, "")
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
	ctx, namespace, err := s.authorize(ctx, pr, domain.OpSecretWrite, domain.ResourceSecret, in.Ref)
	if err != nil {
		return PutSecretResult{}, err
	}
	keyring, err := s.requireKeyring()
	if err != nil {
		return PutSecretResult{}, err
	}

	existing, err := s.store.GetSecretRecord(ctx, in.Ref)
	exists := err == nil
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return PutSecretResult{}, err
	}
	if exists && existing.ClientBound != in.ClientBound {
		return PutSecretResult{}, domain.Errorf(domain.ErrFailedPrecondition,
			"secret %s already exists with client_bound=%v; the mode of a secret cannot change", in.Ref, existing.ClientBound)
	}
	expected := &storage.SecretWriteExpectation{Exists: exists}
	if exists {
		expected.ID = existing.ID
		expected.AccessTokenHash = append([]byte(nil), existing.AccessTokenHash...)
	}

	// Resolve token handling.
	var (
		newTokenHash []byte // stored on the secret row when non-nil
		mintedToken  string // returned once to the caller
		encToken     string // key material for client-bound encryption
	)
	if in.ClientBound {
		switch {
		case exists:
			// Writing a new version requires proving possession of the
			// current token — it is the encryption key share.
			if !tokenHashMatches(pr.SecretToken, existing.AccessTokenHash) {
				s.auditRefWithNamespaceID(ctx, pr, "secret.write", domain.ResourceSecret, in.Ref, namespace.ID, 0, "deny",
					map[string]string{"reason": "token"})
				return PutSecretResult{}, domain.Errorf(domain.ErrPermissionDenied, "access denied")
			}
			encToken = pr.SecretToken
			if in.GenerateToken { // token rotation with the new version
				mintedToken, newTokenHash, err = crypto.GenerateToken("kmss")
				if err != nil {
					return PutSecretResult{}, err
				}
				encToken = mintedToken
			}
		case in.GenerateToken:
			mintedToken, newTokenHash, err = crypto.GenerateToken("kmss")
			if err != nil {
				return PutSecretResult{}, err
			}
			encToken = mintedToken
		default:
			return PutSecretResult{}, domain.Errorf(domain.ErrInvalidArgument,
				"creating a client-bound secret requires generate_access_token; the returned token is the only key share and is shown exactly once")
		}
	} else if in.GenerateToken {
		mintedToken, newTokenHash, err = crypto.GenerateToken("kmss")
		if err != nil {
			return PutSecretResult{}, err
		}
	}

	// Keep the selected active KEK stable through the write in this process.
	// SQL storage independently checks key_metadata inside the transaction to
	// catch a rotation performed by another process.
	s.keyWriteMu.RLock()
	kek := keyring.Active()
	value := in.Value
	ref := in.Ref
	version, revision, err := s.store.CreateSecretVersion(ctx, storage.CreateSecretParams{
		Ref:             ref,
		ContentType:     in.ContentType,
		Metadata:        metadata,
		CreatedBy:       pr.Identity.Name,
		ClientBound:     in.ClientBound,
		AccessTokenHash: newTokenHash,
		Expected:        expected,
		ExpiresAt:       unixMSToTime(in.ExpiresAt),
		Encrypt: func(version uint64) (storage.EncryptedPayload, error) {
			aad := crypto.BuildAAD(ref.NS.Env, ref.NS.App, ref.Key, version)
			var res crypto.EncryptResult
			var eerr error
			if in.ClientBound {
				res, eerr = crypto.EncryptClientBound(kek, value, aad, encToken)
			} else {
				res, eerr = crypto.Encrypt(kek, value, aad)
			}
			if eerr != nil {
				return storage.EncryptedPayload{}, eerr
			}
			return storage.EncryptedPayload{
				Ciphertext:    res.Ciphertext,
				EncryptedDEK:  res.EncryptedDEK,
				KEKID:         res.KEKID,
				WrapMode:      res.WrapMode,
				ClientKeySalt: res.ClientKeySalt,
				Algorithm:     res.Algorithm,
				Nonce:         res.Nonce,
				AAD:           res.AAD,
			}, nil
		},
	})
	s.keyWriteMu.RUnlock()
	if err != nil {
		return PutSecretResult{}, err
	}

	meta := map[string]string{}
	if in.ClientBound {
		meta["client_bound"] = "true"
	}
	if in.GenerateToken {
		meta["token_minted"] = "true"
	}
	s.auditRefWithNamespaceID(ctx, pr, "secret.write", domain.ResourceSecret, ref, namespace.ID, version, "allow", meta)
	s.getHub().Wake()

	return PutSecretResult{Version: version, Revision: revision, AccessToken: mintedToken}, nil
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
func (s *Service) decryptVersion(keyring *crypto.Keyring, rec storage.SecretRecord, ver storage.SecretVersionRecord, clientToken string) ([]byte, error) {
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
		Ciphertext:    ver.Ciphertext,
		EncryptedDEK:  ver.EncryptedDEK,
		Nonce:         ver.Nonce,
		AAD:           aad,
		WrapMode:      ver.WrapMode,
		ClientKeySalt: ver.ClientKeySalt,
		ClientToken:   clientToken,
	})
	if err != nil {
		// Deliberately generic: token, ciphertext, and key failures are
		// indistinguishable to the caller.
		s.m().DecryptFailed()
		return nil, domain.ErrDecryptFailed
	}
	return pt, nil
}
