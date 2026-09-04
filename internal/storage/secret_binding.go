package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"math"
	"slices"
	"time"

	"gorm.io/gorm"

	"github.com/Suhaibinator/kms/internal/domain"
)

const purgeBindingCohortAuditEvent = "secret.binding_cohort.purge"

// bindingCallbackRecord returns an isolated copy of a stored record. A crypto
// callback may zero or reuse its input buffers without mutating the row image
// storage subsequently validates and writes.
func bindingCallbackRecord(row secretVersionModel) SecretVersionRecord {
	rec := toSecretVersionRecord(row)
	rec.Ciphertext = bytes.Clone(rec.Ciphertext)
	rec.EncryptedDEK = bytes.Clone(rec.EncryptedDEK)
	rec.BindingKeySalt = bytes.Clone(rec.BindingKeySalt)
	rec.Nonce = bytes.Clone(rec.Nonce)
	return rec
}

func resolveBindingVersion(tx *gorm.DB, ref domain.Ref, requested uint64) (secretModel, secretVersionModel, error) {
	sec, err := (&SQLStore{}).findSecret(tx, ref)
	if err != nil {
		return secretModel{}, secretVersionModel{}, err
	}
	version := requested
	if version == 0 {
		var label secretLabelModel
		if err := tx.Where("secret_id = ? AND label = ?", sec.ID, domain.LabelCurrent).First(&label).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return secretModel{}, secretVersionModel{}, domain.Errorf(domain.ErrNotFound, "secret %s current version", ref)
			}
			return secretModel{}, secretVersionModel{}, err
		}
		if label.VersionNumber <= 0 {
			return secretModel{}, secretVersionModel{}, domain.Errorf(domain.ErrFailedPrecondition, "secret %s current label is invalid", ref)
		}
		version = uint64(label.VersionNumber)
	}
	if version > math.MaxInt64 {
		return secretModel{}, secretVersionModel{}, domain.Errorf(domain.ErrNotFound, "secret %s version %d", ref, version)
	}
	var row secretVersionModel
	if err := tx.Where("secret_id = ? AND version_number = ?", sec.ID, version).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return secretModel{}, secretVersionModel{}, domain.Errorf(domain.ErrNotFound, "secret %s version %d", ref, version)
		}
		return secretModel{}, secretVersionModel{}, err
	}
	return sec, row, nil
}

func validBoundWrapping(row secretVersionModel) bool {
	return row.State != domain.StateDestroyed &&
		i2b(row.Bound) &&
		row.WrapMode == domain.WrapModeBindingKey &&
		len(row.BindingKeySalt) > 0 &&
		len(row.EncryptedDEK) > 0 &&
		row.KEKID != ""
}

func validStandardWrapping(row secretVersionModel) bool {
	return row.State != domain.StateDestroyed &&
		!i2b(row.Bound) &&
		row.WrapMode == domain.WrapModeStandard &&
		len(row.BindingKeySalt) == 0 &&
		len(row.EncryptedDEK) > 0 &&
		row.KEKID != ""
}

func validateBindingWrapping(original secretVersionModel, got SecretBindingWrapping, targetBound bool) error {
	if got.KEKID == "" || got.KEKID != original.KEKID {
		return domain.Errorf(domain.ErrFailedPrecondition, "binding rewrap must preserve the version KEK")
	}
	if len(got.EncryptedDEK) == 0 {
		return domain.Errorf(domain.ErrFailedPrecondition, "binding rewrap returned an empty encrypted DEK")
	}
	if targetBound {
		if got.WrapMode != domain.WrapModeBindingKey || len(got.BindingKeySalt) == 0 {
			return domain.Errorf(domain.ErrFailedPrecondition, "binding rewrap returned invalid bound wrapping metadata")
		}
		return nil
	}
	if got.WrapMode != domain.WrapModeStandard || len(got.BindingKeySalt) != 0 {
		return domain.Errorf(domain.ErrFailedPrecondition, "binding rewrap returned invalid standard wrapping metadata")
	}
	return nil
}

func affectedVersionsJSON(versions []uint64) (string, error) {
	b, err := json.Marshal(versions)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func appendSecretBindingChange(tx *gorm.DB, sec secretModel, ref domain.Ref, changeType string, anchor uint64, affected []uint64, createdAt time.Time) (uint64, error) {
	encoded, err := affectedVersionsJSON(affected)
	if err != nil {
		return 0, err
	}
	return appendChange(tx, &changeLogModel{
		ResourceType:         domain.ResourceSecret,
		NamespaceID:          sec.NamespaceID,
		Env:                  ref.NS.Env,
		App:                  ref.NS.App,
		Key:                  ref.Key,
		ChangeType:           changeType,
		VersionNumber:        int64(anchor),
		AffectedVersionsJSON: encoded,
		CreatedAt:            fmtTime(createdAt),
	})
}

func updateSecretBindingTimestamp(tx *gorm.DB, sec secretModel, at time.Time) error {
	updated := tx.Model(&secretModel{}).Where("id = ? AND namespace_id = ? AND name = ?", sec.ID, sec.NamespaceID, sec.Name).
		Update("updated_at", fmtTime(at))
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return domain.Errorf(domain.ErrAborted, "secret changed concurrently; retry")
	}
	return nil
}

func updateSecretWrapping(tx *gorm.DB, row secretVersionModel, wrapping SecretBindingWrapping, targetBound bool) error {
	updated := tx.Model(&secretVersionModel{}).
		Where("id = ? AND secret_id = ? AND version_number = ? AND state <> ? AND bound = ? AND kek_id = ?",
			row.ID, row.SecretID, row.VersionNumber, domain.StateDestroyed, row.Bound, row.KEKID).
		Updates(map[string]any{
			"bound":            b2i(targetBound),
			"encrypted_dek":    bytes.Clone(wrapping.EncryptedDEK),
			"kek_id":           wrapping.KEKID,
			"wrap_mode":        wrapping.WrapMode,
			"binding_key_salt": bytes.Clone(wrapping.BindingKeySalt),
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return domain.Errorf(domain.ErrAborted, "secret version changed concurrently; retry")
	}
	return nil
}

func (s *SQLStore) mutateExactSecretBinding(ctx context.Context, ref domain.Ref, version uint64, targetBound bool, rewrap SecretBindingRewrapFunc) (SecretBindingResult, error) {
	if rewrap == nil {
		return SecretBindingResult{}, domain.Errorf(domain.ErrInvalidArgument, "binding rewrap callback is required")
	}
	var result SecretBindingResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sec, row, err := resolveBindingVersion(tx, ref, version)
		if err != nil {
			return err
		}
		if row.State == domain.StateDestroyed {
			return domain.Errorf(domain.ErrFailedPrecondition, "secret %s version %d is destroyed", ref, row.VersionNumber)
		}
		if targetBound {
			if i2b(row.Bound) {
				return domain.Errorf(domain.ErrFailedPrecondition, "secret %s version %d is already bound", ref, row.VersionNumber)
			}
			if !validStandardWrapping(row) {
				return domain.Errorf(domain.ErrFailedPrecondition, "secret %s version %d has invalid wrapping metadata", ref, row.VersionNumber)
			}
		} else {
			if !i2b(row.Bound) {
				return domain.Errorf(domain.ErrFailedPrecondition, "secret %s version %d is not bound", ref, row.VersionNumber)
			}
			if !validBoundWrapping(row) {
				return domain.Errorf(domain.ErrFailedPrecondition, "secret %s version %d has invalid wrapping metadata", ref, row.VersionNumber)
			}
		}

		wrapping, err := rewrap(bindingCallbackRecord(row))
		if err != nil {
			return err
		}
		if err := validateBindingWrapping(row, wrapping, targetBound); err != nil {
			return err
		}
		if targetBound && bytes.Equal(wrapping.BindingKeySalt, row.BindingKeySalt) {
			return domain.Errorf(domain.ErrFailedPrecondition, "binding rewrap must use a fresh salt")
		}
		if err := updateSecretWrapping(tx, row, wrapping, targetBound); err != nil {
			return err
		}
		now := nowUTC()
		if err := updateSecretBindingTimestamp(tx, sec, now); err != nil {
			return err
		}
		affected := []uint64{uint64(row.VersionNumber)}
		changeType := domain.ChangeUnbind
		if targetBound {
			changeType = domain.ChangeBind
		}
		revision, err := appendSecretBindingChange(tx, sec, ref, changeType, affected[0], affected, now)
		if err != nil {
			return err
		}
		result = SecretBindingResult{AnchorVersion: affected[0], AffectedVersions: affected, Revision: revision}
		return nil
	})
	if err != nil {
		return SecretBindingResult{}, err
	}
	return result, nil
}

// BindSecretVersion adds a binding-key wrapping layer to one exact version.
func (s *SQLStore) BindSecretVersion(ctx context.Context, ref domain.Ref, version uint64, rewrap SecretBindingRewrapFunc) (SecretBindingResult, error) {
	return s.mutateExactSecretBinding(ctx, ref, version, true, rewrap)
}

// UnbindSecretVersion removes a binding-key wrapping layer from one exact
// version after the callback has authenticated and opened it.
func (s *SQLStore) UnbindSecretVersion(ctx context.Context, ref domain.Ref, version uint64, rewrap SecretBindingRewrapFunc) (SecretBindingResult, error) {
	return s.mutateExactSecretBinding(ctx, ref, version, false, rewrap)
}

// discoverSecretBindingCohort validates the anchor first, then scans adjacent
// numeric versions without ever crossing a missing, destroyed, unbound,
// corrupt, or wrong-key boundary. Returned rows are sorted by version.
func discoverSecretBindingCohort(tx *gorm.DB, ref domain.Ref, requestedAnchor uint64, test SecretBindingTestFunc) (secretModel, uint64, []secretVersionModel, error) {
	sec, anchor, err := resolveBindingVersion(tx, ref, requestedAnchor)
	if err != nil {
		return secretModel{}, 0, nil, err
	}
	anchorVersion := uint64(anchor.VersionNumber)
	if anchor.State == domain.StateDestroyed {
		return secretModel{}, 0, nil, domain.Errorf(domain.ErrFailedPrecondition, "secret %s version %d is destroyed", ref, anchorVersion)
	}
	if !i2b(anchor.Bound) {
		return secretModel{}, 0, nil, domain.Errorf(domain.ErrFailedPrecondition, "secret %s version %d is not bound", ref, anchorVersion)
	}
	if !validBoundWrapping(anchor) {
		return secretModel{}, 0, nil, domain.Errorf(domain.ErrFailedPrecondition, "secret %s version %d has invalid wrapping metadata", ref, anchorVersion)
	}
	if err := test(bindingCallbackRecord(anchor)); err != nil {
		return secretModel{}, 0, nil, err
	}

	down := make([]secretVersionModel, 0)
	for version := anchor.VersionNumber - 1; version > 0; version-- {
		var row secretVersionModel
		err := tx.Where("secret_id = ? AND version_number = ?", sec.ID, version).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			break
		}
		if err != nil {
			return secretModel{}, 0, nil, err
		}
		if !validBoundWrapping(row) || test(bindingCallbackRecord(row)) != nil {
			break
		}
		down = append(down, row)
	}
	slices.Reverse(down)
	rows := append(down, anchor)
	for version := anchor.VersionNumber + 1; version > 0 && version <= math.MaxInt64; version++ {
		var row secretVersionModel
		err := tx.Where("secret_id = ? AND version_number = ?", sec.ID, version).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			break
		}
		if err != nil {
			return secretModel{}, 0, nil, err
		}
		if !validBoundWrapping(row) || test(bindingCallbackRecord(row)) != nil {
			break
		}
		rows = append(rows, row)
		if version == math.MaxInt64 {
			break
		}
	}
	return sec, anchorVersion, rows, nil
}

func bindingVersions(rows []secretVersionModel) []uint64 {
	versions := make([]uint64, len(rows))
	for i := range rows {
		versions[i] = uint64(rows[i].VersionNumber)
	}
	return versions
}

func validateSecretBindingGuard(guard SecretBindingCASGuard) error {
	if guard.ExpectedRevision == nil {
		if len(guard.ExpectedAffectedVersions) != 0 {
			return domain.Errorf(domain.ErrInvalidArgument, "expected revision and affected versions must be supplied together")
		}
		return nil
	}
	if len(guard.ExpectedAffectedVersions) == 0 {
		return domain.Errorf(domain.ErrInvalidArgument, "expected revision and affected versions must be supplied together")
	}
	var previous uint64
	for i, version := range guard.ExpectedAffectedVersions {
		if version == 0 || i > 0 && version <= previous {
			return domain.Errorf(domain.ErrInvalidArgument, "expected affected versions must be positive, sorted, and unique")
		}
		previous = version
	}
	return nil
}

func cloneSecretBindingGuard(guard SecretBindingCASGuard) SecretBindingCASGuard {
	guard.ExpectedAffectedVersions = slices.Clone(guard.ExpectedAffectedVersions)
	if guard.ExpectedRevision != nil {
		revision := *guard.ExpectedRevision
		guard.ExpectedRevision = &revision
	}
	return guard
}

func checkSecretBindingGuard(guard SecretBindingCASGuard, revision uint64, affected []uint64) error {
	if guard.ExpectedRevision == nil {
		return nil
	}
	if *guard.ExpectedRevision != revision || !slices.Equal(guard.ExpectedAffectedVersions, affected) {
		return domain.Errorf(domain.ErrAborted, "secret binding cohort changed; preview and retry")
	}
	return nil
}

// PreviewSecretBindingCohort discovers a coherent cohort and global revision
// without mutating either secret or change log.
func (s *SQLStore) PreviewSecretBindingCohort(ctx context.Context, ref domain.Ref, anchor uint64, test SecretBindingTestFunc) (SecretBindingResult, error) {
	if test == nil {
		return SecretBindingResult{}, domain.Errorf(domain.ErrInvalidArgument, "binding test callback is required")
	}
	var result SecretBindingResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, resolvedAnchor, rows, err := discoverSecretBindingCohort(tx, ref, anchor, test)
		if err != nil {
			return err
		}
		revision, err := s.currentRevision(tx)
		if err != nil {
			return err
		}
		result = SecretBindingResult{
			AnchorVersion:    resolvedAnchor,
			AffectedVersions: bindingVersions(rows),
			Revision:         revision,
		}
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SecretBindingResult{}, err
	}
	return result, nil
}

// RotateSecretBindingKey rewraps every member of the rediscovered cohort in
// one transaction while preserving each version's KEK and value payload.
func (s *SQLStore) RotateSecretBindingKey(ctx context.Context, ref domain.Ref, anchor uint64, guard SecretBindingCASGuard, testOld SecretBindingTestFunc, rewrapNew SecretBindingRewrapFunc) (SecretBindingResult, error) {
	if testOld == nil || rewrapNew == nil {
		return SecretBindingResult{}, domain.Errorf(domain.ErrInvalidArgument, "binding test and rewrap callbacks are required")
	}
	guard = cloneSecretBindingGuard(guard)
	if err := validateSecretBindingGuard(guard); err != nil {
		return SecretBindingResult{}, err
	}
	var result SecretBindingResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sec, resolvedAnchor, rows, err := discoverSecretBindingCohort(tx, ref, anchor, testOld)
		if err != nil {
			return err
		}
		affected := bindingVersions(rows)
		currentRevision, err := s.currentRevision(tx)
		if err != nil {
			return err
		}
		if err := checkSecretBindingGuard(guard, currentRevision, affected); err != nil {
			return err
		}

		wrappings := make([]SecretBindingWrapping, len(rows))
		seenSalts := make(map[string]struct{}, len(rows))
		for i, row := range rows {
			wrapping, err := rewrapNew(bindingCallbackRecord(row))
			if err != nil {
				return err
			}
			if err := validateBindingWrapping(row, wrapping, true); err != nil {
				return err
			}
			if bytes.Equal(wrapping.BindingKeySalt, row.BindingKeySalt) {
				return domain.Errorf(domain.ErrFailedPrecondition, "binding rotation must use fresh salts")
			}
			saltKey := string(wrapping.BindingKeySalt)
			if _, duplicate := seenSalts[saltKey]; duplicate {
				return domain.Errorf(domain.ErrFailedPrecondition, "binding rotation must use independent salts")
			}
			seenSalts[saltKey] = struct{}{}
			wrappings[i] = SecretBindingWrapping{
				EncryptedDEK:   bytes.Clone(wrapping.EncryptedDEK),
				KEKID:          wrapping.KEKID,
				WrapMode:       wrapping.WrapMode,
				BindingKeySalt: bytes.Clone(wrapping.BindingKeySalt),
			}
		}
		for i, row := range rows {
			if err := updateSecretWrapping(tx, row, wrappings[i], true); err != nil {
				return err
			}
		}
		now := nowUTC()
		if err := updateSecretBindingTimestamp(tx, sec, now); err != nil {
			return err
		}
		revision, err := appendSecretBindingChange(tx, sec, ref, domain.ChangeRotateBindingKey, resolvedAnchor, affected, now)
		if err != nil {
			return err
		}
		result = SecretBindingResult{AnchorVersion: resolvedAnchor, AffectedVersions: affected, Revision: revision}
		return nil
	})
	if err != nil {
		return SecretBindingResult{}, err
	}
	return result, nil
}

// PurgeSecretBindingCohort irreversibly replaces each selected row with a
// minimal tombstone. It intentionally does not consult release references.
func (s *SQLStore) PurgeSecretBindingCohort(ctx context.Context, ref domain.Ref, anchor uint64, guard SecretBindingCASGuard, test SecretBindingTestFunc, audit SecretBindingPurgeAudit) (SecretBindingResult, error) {
	if test == nil {
		return SecretBindingResult{}, domain.Errorf(domain.ErrInvalidArgument, "binding test callback is required")
	}
	guard = cloneSecretBindingGuard(guard)
	if err := validateSecretBindingGuard(guard); err != nil {
		return SecretBindingResult{}, err
	}
	var result SecretBindingResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sec, resolvedAnchor, rows, err := discoverSecretBindingCohort(tx, ref, anchor, test)
		if err != nil {
			return err
		}
		affected := bindingVersions(rows)
		currentRevision, err := s.currentRevision(tx)
		if err != nil {
			return err
		}
		if err := checkSecretBindingGuard(guard, currentRevision, affected); err != nil {
			return err
		}

		now := nowUTC()
		for _, row := range rows {
			updated := tx.Model(&secretVersionModel{}).
				Where("id = ? AND secret_id = ? AND version_number = ? AND state <> ? AND bound = ? AND kek_id = ?",
					row.ID, row.SecretID, row.VersionNumber, domain.StateDestroyed, row.Bound, row.KEKID).
				Updates(map[string]any{
					"content_type":     "",
					"bound":            int64(0),
					"has_access_token": int64(0),
					"ciphertext":       nil,
					"encrypted_dek":    nil,
					"kek_id":           "",
					"wrap_mode":        "",
					"binding_key_salt": nil,
					"algorithm":        "",
					"nonce":            nil,
					"aad":              "",
					"state":            domain.StateDestroyed,
					"destroyed_at":     fmtTime(now),
					"expires_at":       nil,
					"metadata_json":    "",
				})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return domain.Errorf(domain.ErrAborted, "secret version changed concurrently; retry")
			}
		}
		if err := updateSecretBindingTimestamp(tx, sec, now); err != nil {
			return err
		}
		revision, err := appendSecretBindingChange(tx, sec, ref, domain.ChangePurgeBindingCohort, resolvedAnchor, affected, now)
		if err != nil {
			return err
		}
		auditCreatedAt := audit.CreatedAt
		if auditCreatedAt.IsZero() {
			auditCreatedAt = now
		}
		metadata, err := affectedVersionsJSON(affected)
		if err != nil {
			return err
		}
		if err := appendAudit(tx, domain.AuditEvent{
			EventType:           purgeBindingCohortAuditEvent,
			ActorIdentity:       audit.ActorIdentity,
			ActorType:           audit.ActorType,
			ResourceType:        domain.ResourceSecret,
			ResourceNamespaceID: sec.NamespaceID,
			ResourceEnv:         ref.NS.Env,
			ResourceApp:         ref.NS.App,
			ResourceKey:         ref.Key,
			ResourceVersion:     resolvedAnchor,
			Decision:            "allow",
			SourceIP:            audit.SourceIP,
			UserAgent:           audit.UserAgent,
			RequestID:           audit.RequestID,
			CreatedAt:           auditCreatedAt,
			Metadata:            `{"affected_versions":` + metadata + `}`,
		}); err != nil {
			return err
		}
		result = SecretBindingResult{AnchorVersion: resolvedAnchor, AffectedVersions: affected, Revision: revision}
		return nil
	})
	if err != nil {
		return SecretBindingResult{}, err
	}
	return result, nil
}
