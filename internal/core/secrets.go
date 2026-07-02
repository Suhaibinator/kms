package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/pathutil"
	"github.com/Suhaibinator/kms/internal/storage"
)

// PutSecretInput describes a secret write (creation or new version).
type PutSecretInput struct {
	Path        string
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
func (s *Service) GetSecret(ctx context.Context, pr Principal, path string, version uint64, label string) (domain.SecretValue, error) {
	path, err := pathutil.Normalize(path)
	if err != nil {
		return domain.SecretValue{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if err := s.authorize(ctx, pr, domain.OpSecretRead, domain.ResourceSecret, path); err != nil {
		return domain.SecretValue{}, err
	}
	keyring, err := s.requireKeyring()
	if err != nil {
		return domain.SecretValue{}, err
	}

	rec, ver, err := s.store.GetSecretVersion(ctx, path, version, label)
	if err != nil {
		return domain.SecretValue{}, err
	}
	// Per-secret token gate. Applied before the version-state check so a caller
	// without the token cannot probe version state (disabled/destroyed/expired).
	// The generic error covers missing and wrong tokens so callers cannot
	// distinguish them.
	//
	// For a client-bound secret the token is the decryption key itself, bound
	// per version (each version has its own HKDF salt and may have been written
	// under a different token after a rotation). The secret-level AccessTokenHash
	// only tracks the LATEST token, so comparing against it would wrongly reject
	// a valid token for an older version. We therefore require only that a token
	// be present here and let the crypto layer authenticate it against the
	// specific version — a wrong token fails as a generic decryption error, with
	// no additional information leaked.
	if rec.ClientBound {
		if pr.SecretToken == "" {
			s.auditOp(ctx, pr, "secret.read", domain.ResourceSecret, path, ver.Version, "deny",
				map[string]string{"reason": "token"})
			return domain.SecretValue{}, domain.Errorf(domain.ErrPermissionDenied, "access denied")
		}
	} else if len(rec.AccessTokenHash) > 0 && !tokenHashMatches(pr.SecretToken, rec.AccessTokenHash) {
		s.auditOp(ctx, pr, "secret.read", domain.ResourceSecret, path, ver.Version, "deny",
			map[string]string{"reason": "token"})
		return domain.SecretValue{}, domain.Errorf(domain.ErrPermissionDenied, "access denied")
	}
	if err := s.checkVersionReadable(ver); err != nil {
		return domain.SecretValue{}, err
	}

	plaintext, err := s.decryptVersion(keyring, rec, ver, pr.SecretToken)
	if err != nil {
		s.auditOp(ctx, pr, "secret.read", domain.ResourceSecret, path, ver.Version, "error", nil)
		s.log.Error("secret decryption failed", "path", path, "version", ver.Version, "kek_id", ver.KEKID)
		return domain.SecretValue{}, err
	}

	// Fail closed: a secret read that cannot be audited is not served.
	if err := s.auditStrict(ctx, s.buildEvent(pr, "secret.read", domain.ResourceSecret, path, ver.Version, "allow", nil)); err != nil {
		crypto.Zero(plaintext)
		return domain.SecretValue{}, err
	}

	return domain.SecretValue{
		Path:        path,
		Version:     ver.Version,
		Value:       plaintext,
		ContentType: rec.ContentType,
		Metadata:    ver.Metadata,
		CreatedAt:   ver.CreatedAt,
	}, nil
}

// RevealSecret is the audited admin path used by the frontend/CLI. It
// bypasses the per-secret token gate (break-glass) but can never decrypt
// client-bound secrets — the server lacks the key material by design.
func (s *Service) RevealSecret(ctx context.Context, pr Principal, path string, version uint64, label string) (domain.SecretValue, error) {
	path, err := pathutil.Normalize(path)
	if err != nil {
		return domain.SecretValue{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if !pr.IsAdmin() {
		s.auditOp(ctx, pr, "secret.reveal", domain.ResourceSecret, path, version, "deny", nil)
		return domain.SecretValue{}, domain.Errorf(domain.ErrPermissionDenied, "access denied")
	}
	keyring, err := s.requireKeyring()
	if err != nil {
		return domain.SecretValue{}, err
	}

	rec, ver, err := s.store.GetSecretVersion(ctx, path, version, label)
	if err != nil {
		return domain.SecretValue{}, err
	}
	if rec.ClientBound {
		return domain.SecretValue{}, domain.Errorf(domain.ErrFailedPrecondition,
			"client-bound secrets cannot be revealed: the server cannot decrypt them without the client token")
	}
	if err := s.checkVersionReadable(ver); err != nil {
		return domain.SecretValue{}, err
	}

	plaintext, err := s.decryptVersion(keyring, rec, ver, "")
	if err != nil {
		s.auditOp(ctx, pr, "secret.reveal", domain.ResourceSecret, path, ver.Version, "error", nil)
		s.log.Error("secret decryption failed", "path", path, "version", ver.Version, "kek_id", ver.KEKID)
		return domain.SecretValue{}, err
	}
	if err := s.auditStrict(ctx, s.buildEvent(pr, "secret.reveal", domain.ResourceSecret, path, ver.Version, "allow", nil)); err != nil {
		crypto.Zero(plaintext)
		return domain.SecretValue{}, err
	}

	return domain.SecretValue{
		Path:        path,
		Version:     ver.Version,
		Value:       plaintext,
		ContentType: rec.ContentType,
		Metadata:    ver.Metadata,
		CreatedAt:   ver.CreatedAt,
	}, nil
}

// PutSecret creates a secret or appends a new immutable version.
func (s *Service) PutSecret(ctx context.Context, pr Principal, in PutSecretInput) (PutSecretResult, error) {
	path, err := pathutil.Normalize(in.Path)
	if err != nil {
		return PutSecretResult{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
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
	if err := s.authorize(ctx, pr, domain.OpSecretWrite, domain.ResourceSecret, path); err != nil {
		return PutSecretResult{}, err
	}
	keyring, err := s.requireKeyring()
	if err != nil {
		return PutSecretResult{}, err
	}

	existing, err := s.store.GetSecretRecord(ctx, path)
	exists := err == nil
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return PutSecretResult{}, err
	}
	if exists && existing.ClientBound != in.ClientBound {
		return PutSecretResult{}, domain.Errorf(domain.ErrFailedPrecondition,
			"secret %s already exists with client_bound=%v; the mode of a secret cannot change", path, existing.ClientBound)
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
				s.auditOp(ctx, pr, "secret.write", domain.ResourceSecret, path, 0, "deny",
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

	kek := keyring.Active()
	value := in.Value
	version, revision, err := s.store.CreateSecretVersion(ctx, storage.CreateSecretParams{
		Path:            path,
		ContentType:     in.ContentType,
		Metadata:        metadata,
		CreatedBy:       pr.Identity.Name,
		ClientBound:     in.ClientBound,
		AccessTokenHash: newTokenHash,
		ExpiresAt:       unixMSToTime(in.ExpiresAt),
		Encrypt: func(version uint64) (storage.EncryptedPayload, error) {
			aad := crypto.BuildAAD(domain.ResourceSecret, path, version)
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
	s.auditOp(ctx, pr, "secret.write", domain.ResourceSecret, path, version, "allow", meta)
	s.getHub().Wake()

	return PutSecretResult{Version: version, Revision: revision, AccessToken: mintedToken}, nil
}

// ListSecrets lists secret metadata under a prefix, filtered by policy.
func (s *Service) ListSecrets(ctx context.Context, pr Principal, prefix string, page storage.ListPage) ([]domain.Secret, string, error) {
	prefix, err := normalizeListPrefix(prefix)
	if err != nil {
		return nil, "", err
	}
	// Secret list responses are metadata-only (never values), so either
	// secret:list or secret:read on an item exposes its metadata.
	filter, err := s.listFilter(ctx, pr, domain.ResourceSecret, domain.OpSecretList, prefix, domain.OpSecretList, domain.OpSecretRead)
	if err != nil {
		return nil, "", err
	}
	secrets, next, err := s.store.ListSecrets(ctx, prefix, page)
	if err != nil {
		return nil, "", err
	}
	out := secrets[:0]
	for _, sec := range secrets {
		if filter(sec.Path) {
			out = append(out, sec)
		}
	}
	return out, next, nil
}

// GetSecretInfo returns secret metadata and version history (no values).
func (s *Service) GetSecretInfo(ctx context.Context, pr Principal, path string) (domain.Secret, error) {
	path, err := pathutil.Normalize(path)
	if err != nil {
		return domain.Secret{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if err := s.authorize(ctx, pr, domain.OpSecretRead, domain.ResourceSecret, path); err != nil {
		return domain.Secret{}, err
	}
	return s.store.GetSecretInfo(ctx, path)
}

// DeleteSecret removes a secret and all versions (ciphertext included).
func (s *Service) DeleteSecret(ctx context.Context, pr Principal, path string) (uint64, error) {
	path, err := pathutil.Normalize(path)
	if err != nil {
		return 0, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if err := s.authorize(ctx, pr, domain.OpSecretDestroy, domain.ResourceSecret, path); err != nil {
		return 0, err
	}
	revision, err := s.store.DeleteSecret(ctx, path)
	if err != nil {
		return 0, err
	}
	s.auditOp(ctx, pr, "secret.delete", domain.ResourceSecret, path, 0, "allow", nil)
	s.getHub().Wake()
	return revision, nil
}

// DisableSecret disables (or re-enables) a version, or all versions when
// version is 0.
func (s *Service) DisableSecret(ctx context.Context, pr Principal, path string, version uint64, enable bool) (uint64, error) {
	path, err := pathutil.Normalize(path)
	if err != nil {
		return 0, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if err := s.authorize(ctx, pr, domain.OpSecretDisable, domain.ResourceSecret, path); err != nil {
		return 0, err
	}
	state := domain.StateDisabled
	eventType := "secret.disable"
	if enable {
		state = domain.StateEnabled
		eventType = "secret.enable"
	}
	revision, err := s.store.SetSecretVersionState(ctx, path, version, state)
	if err != nil {
		return 0, err
	}
	s.auditOp(ctx, pr, eventType, domain.ResourceSecret, path, version, "allow", nil)
	s.getHub().Wake()
	return revision, nil
}

// DestroySecretVersion irreversibly destroys one version's ciphertext.
func (s *Service) DestroySecretVersion(ctx context.Context, pr Principal, path string, version uint64) (uint64, error) {
	path, err := pathutil.Normalize(path)
	if err != nil {
		return 0, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if version == 0 {
		return 0, domain.Errorf(domain.ErrInvalidArgument, "version is required")
	}
	if err := s.authorize(ctx, pr, domain.OpSecretDestroy, domain.ResourceSecret, path); err != nil {
		return 0, err
	}
	revision, err := s.store.DestroySecretVersion(ctx, path, version)
	if err != nil {
		return 0, err
	}
	s.auditOp(ctx, pr, "secret.destroy", domain.ResourceSecret, path, version, "allow", nil)
	s.getHub().Wake()
	return revision, nil
}

// PromoteSecretVersion points "current" at the given version.
func (s *Service) PromoteSecretVersion(ctx context.Context, pr Principal, path string, version uint64) (current, previous, revision uint64, err error) {
	path, err = pathutil.Normalize(path)
	if err != nil {
		return 0, 0, 0, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	if version == 0 {
		return 0, 0, 0, domain.Errorf(domain.ErrInvalidArgument, "version is required")
	}
	if err := s.authorize(ctx, pr, domain.OpSecretPromote, domain.ResourceSecret, path); err != nil {
		return 0, 0, 0, err
	}
	current, previous, revision, err = s.store.PromoteSecretVersion(ctx, path, version)
	if err != nil {
		return 0, 0, 0, err
	}
	s.auditOp(ctx, pr, "secret.promote", domain.ResourceSecret, path, version, "allow", nil)
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
		return nil, fmt.Errorf("secret %s v%d: %w", rec.Path, ver.Version, domain.ErrDecryptFailed)
	}
	// Recompute the AAD from the row's authoritative identity (path + version)
	// rather than trusting the stored aad column. For a correctly stored row
	// this equals the stored value; for a row whose ciphertext/DEK/aad columns
	// were relocated to another secret (e.g. by direct DB tampering or a botched
	// migration) it no longer matches, so authentication fails loudly instead
	// of returning the wrong secret's plaintext.
	aad := crypto.BuildAAD(domain.ResourceSecret, rec.Path, ver.Version)
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
		return nil, domain.ErrDecryptFailed
	}
	return pt, nil
}
