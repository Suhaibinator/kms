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
	"gorm.io/gorm/clause"

	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
)

const (
	bindSecretAuditEvent         = "secret.bind"
	unbindSecretAuditEvent       = "secret.unbind"
	rotateBindingKeyAuditEvent   = "secret.binding_key.rotate"
	purgeBindingCohortAuditEvent = "secret.binding_cohort.purge"
	purgeUnboundAuditEvent       = "secret.unbound_versions.purge"
)

type purgeCleanupPendingError struct {
	cause error
}

func (e *purgeCleanupPendingError) Error() string { return ErrPurgeCleanupPending.Error() }
func (e *purgeCleanupPendingError) Unwrap() error { return ErrPurgeCleanupPending }
func (e *purgeCleanupPendingError) Cause() error  { return e.cause }

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
		len(row.BindingKeySalt) == crypto.BindingKeySaltSize &&
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

func appendSecretBindingAllowAudit(tx *gorm.DB, eventType string, sec secretModel, ref domain.Ref, anchor uint64, affected []uint64, audit SecretBindingAudit, fallbackCreatedAt time.Time) error {
	metadata, err := affectedVersionsJSON(affected)
	if err != nil {
		return err
	}
	createdAt := audit.CreatedAt
	if createdAt.IsZero() {
		createdAt = fallbackCreatedAt
	}
	if err := appendAudit(tx, domain.AuditEvent{
		EventType:           eventType,
		ActorIdentity:       audit.ActorIdentity,
		ActorType:           audit.ActorType,
		ResourceType:        domain.ResourceSecret,
		ResourceNamespaceID: sec.NamespaceID,
		ResourceEnv:         ref.NS.Env,
		ResourceApp:         ref.NS.App,
		ResourceKey:         ref.Key,
		ResourceVersion:     anchor,
		Decision:            "allow",
		SourceIP:            audit.SourceIP,
		UserAgent:           audit.UserAgent,
		RequestID:           audit.RequestID,
		CreatedAt:           createdAt,
		Metadata:            `{"affected_versions":` + metadata + `}`,
	}); err != nil {
		return ErrRequiredAuditUnavailable
	}
	return nil
}

// TransitionSecretVersion clones the current source into a new immutable
// high-water version. The source row itself is never updated.
func (s *SQLStore) TransitionSecretVersion(ctx context.Context, p SecretVersionTransitionParams) (SecretVersionTransitionResult, error) {
	if p.ExpectedCurrentVersion == 0 {
		return SecretVersionTransitionResult{}, domain.Errorf(domain.ErrInvalidArgument, "expected current version is required")
	}
	if p.Encrypt == nil {
		return SecretVersionTransitionResult{}, domain.Errorf(domain.ErrInvalidArgument, "transition encryption callback is required")
	}
	targetBound := false
	changeType := ""
	auditEvent := ""
	switch p.Kind {
	case SecretTransitionBind:
		targetBound, changeType, auditEvent = true, domain.ChangeBind, bindSecretAuditEvent
	case SecretTransitionUnbind:
		changeType, auditEvent = domain.ChangeUnbind, unbindSecretAuditEvent
	case SecretTransitionRotate:
		targetBound, changeType, auditEvent = true, domain.ChangeRotateBindingKey, rotateBindingKeyAuditEvent
	default:
		return SecretVersionTransitionResult{}, domain.Errorf(domain.ErrInvalidArgument, "invalid secret protection transition")
	}

	var result SecretVersionTransitionResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sec, source, err := resolveBindingVersion(tx, p.Ref, 0)
		if err != nil {
			return err
		}
		if uint64(source.VersionNumber) != p.ExpectedCurrentVersion {
			return domain.Errorf(domain.ErrAborted, "secret %s current version changed; retry", p.Ref)
		}
		if source.State == domain.StateDestroyed || source.DestroyedAt != nil {
			return domain.Errorf(domain.ErrFailedPrecondition, "secret %s current version %d is destroyed", p.Ref, source.VersionNumber)
		}
		if p.Kind == SecretTransitionBind {
			if i2b(source.Bound) {
				return domain.Errorf(domain.ErrFailedPrecondition, "secret %s current version %d is already bound", p.Ref, source.VersionNumber)
			}
			if !validStandardWrapping(source) {
				return domain.Errorf(domain.ErrFailedPrecondition, "secret %s current version %d has invalid wrapping metadata", p.Ref, source.VersionNumber)
			}
		} else if !i2b(source.Bound) {
			return domain.Errorf(domain.ErrFailedPrecondition, "secret %s current version %d is not bound", p.Ref, source.VersionNumber)
		} else if !validBoundWrapping(source) {
			return domain.Errorf(domain.ErrFailedPrecondition, "secret %s current version %d has invalid wrapping metadata", p.Ref, source.VersionNumber)
		}

		var highWater secretVersionHighWaterModel
		if err := tx.Where("namespace_id = ? AND name = ?", sec.NamespaceID, sec.Name).First(&highWater).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.Errorf(domain.ErrFailedPrecondition, "secret %s version high-water mark is missing", p.Ref)
			}
			return err
		}
		if highWater.LastVersion == math.MaxInt64 {
			return domain.Errorf(domain.ErrFailedPrecondition, "secret %s version space exhausted", p.Ref)
		}
		newVersion := uint64(highWater.LastVersion + 1)
		payload, err := p.Encrypt(bindingCallbackRecord(source), newVersion)
		if err != nil {
			return err
		}
		if err := validateSecretPayloadWrapping(targetBound, payload); err != nil {
			return err
		}
		if len(payload.Ciphertext) == 0 || len(payload.EncryptedDEK) == 0 || len(payload.Nonce) == 0 || payload.AAD == "" || payload.Algorithm == "" {
			return domain.Errorf(domain.ErrFailedPrecondition, "transition encryption returned incomplete cryptographic material")
		}
		if bytes.Equal(payload.Ciphertext, source.Ciphertext) || bytes.Equal(payload.EncryptedDEK, source.EncryptedDEK) || bytes.Equal(payload.Nonce, source.Nonce) || payload.AAD == source.AAD {
			return domain.Errorf(domain.ErrFailedPrecondition, "transition encryption must use fresh cryptographic material and version-bound AAD")
		}
		if targetBound && bytes.Equal(payload.BindingKeySalt, source.BindingKeySalt) {
			return domain.Errorf(domain.ErrFailedPrecondition, "transition encryption must use a fresh binding salt")
		}
		var active keyMetadataModel
		if err := tx.Where("state = ?", domain.KeyStateActive).Order("created_at DESC, id DESC").First(&active).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.Errorf(domain.ErrFailedPrecondition, "no active KEK for secret transition")
			}
			return err
		}
		if payload.KEKID != active.ID {
			return domain.Errorf(domain.ErrFailedPrecondition, "active KEK changed during secret transition; retry")
		}

		now := nowUTC()
		row := secretVersionModel{
			SecretID:       sec.ID,
			VersionNumber:  int64(newVersion),
			ContentType:    source.ContentType,
			Bound:          b2i(targetBound),
			HasAccessToken: source.HasAccessToken,
			Ciphertext:     bytes.Clone(payload.Ciphertext),
			EncryptedDEK:   bytes.Clone(payload.EncryptedDEK),
			KEKID:          payload.KEKID,
			WrapMode:       payload.WrapMode,
			BindingKeySalt: bytes.Clone(payload.BindingKeySalt),
			Algorithm:      payload.Algorithm,
			Nonce:          bytes.Clone(payload.Nonce),
			AAD:            payload.AAD,
			State:          source.State,
			CreatedBy:      p.CreatedBy,
			CreatedAt:      fmtTime(now),
			ExpiresAt:      source.ExpiresAt,
			MetadataJSON:   source.MetadataJSON,
		}
		if err := tx.Omit(clause.Associations).Create(&row).Error; err != nil {
			return err
		}
		updatedHighWater := tx.Model(&secretVersionHighWaterModel{}).
			Where("namespace_id = ? AND name = ? AND last_version = ?", sec.NamespaceID, sec.Name, highWater.LastVersion).
			Update("last_version", int64(newVersion))
		if updatedHighWater.Error != nil {
			return updatedHighWater.Error
		}
		if updatedHighWater.RowsAffected != 1 {
			return domain.Errorf(domain.ErrAborted, "secret %s changed concurrently; retry", p.Ref)
		}
		if err := setSecretLabel(tx, sec.ID, domain.LabelCurrent, newVersion); err != nil {
			return err
		}
		if err := setSecretLabel(tx, sec.ID, domain.LabelPrevious, uint64(source.VersionNumber)); err != nil {
			return err
		}
		updatedSecret := tx.Model(&secretModel{}).
			Where("id = ? AND namespace_id = ? AND name = ?", sec.ID, sec.NamespaceID, sec.Name).
			Updates(map[string]any{"content_type": source.ContentType, "metadata_json": source.MetadataJSON, "updated_at": fmtTime(now)})
		if updatedSecret.Error != nil {
			return updatedSecret.Error
		}
		if updatedSecret.RowsAffected != 1 {
			return domain.Errorf(domain.ErrAborted, "secret %s changed concurrently; retry", p.Ref)
		}
		affected := []uint64{uint64(source.VersionNumber), newVersion}
		revision, err := appendSecretBindingChange(tx, sec, p.Ref, changeType, newVersion, affected, now)
		if err != nil {
			return err
		}
		if err := appendSecretBindingAllowAudit(tx, auditEvent, sec, p.Ref, newVersion, affected, p.Audit, now); err != nil {
			return err
		}
		result = SecretVersionTransitionResult{CurrentVersion: newVersion, PreviousVersion: uint64(source.VersionNumber), Revision: revision}
		return nil
	})
	if err != nil {
		return SecretVersionTransitionResult{}, err
	}
	return result, nil
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
	cohortSize := 1

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
		if cohortSize == MaxSecretBindingCohortVersions {
			return secretModel{}, 0, nil, domain.Errorf(domain.ErrResourceExhausted,
				"binding cohort exceeds the %d-version limit", MaxSecretBindingCohortVersions)
		}
		down = append(down, row)
		cohortSize++
	}
	slices.Reverse(down)
	rows := append(down, anchor)
	for version := anchor.VersionNumber + 1; version > 0; version++ {
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
		if cohortSize == MaxSecretBindingCohortVersions {
			return secretModel{}, 0, nil, domain.Errorf(domain.ErrResourceExhausted,
				"binding cohort exceeds the %d-version limit", MaxSecretBindingCohortVersions)
		}
		rows = append(rows, row)
		cohortSize++
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
	if guard.ExpectedRevision == 0 || len(guard.ExpectedAffectedVersions) == 0 {
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
	return guard
}

func checkSecretBindingGuard(guard SecretBindingCASGuard, revision uint64, affected []uint64) error {
	if guard.ExpectedRevision != revision || !slices.Equal(guard.ExpectedAffectedVersions, affected) {
		return domain.Errorf(domain.ErrAborted, "secret version set changed; preview and retry")
	}
	return nil
}

// waitForExclusivePoolConnection is called only while the purge's connection
// is already pinned and the pool is limited to that one connection. Existing
// checkouts are closed as they return because max-idle is zero; new checkouts
// cannot start. Seeing exactly one open/in-use connection therefore proves the
// caller owns the only live connection in this SQLStore.
func waitForExclusivePoolConnection(ctx context.Context, sqlDB *sql.DB) error {
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		stats := sqlDB.Stats()
		if stats.OpenConnections == 1 && stats.InUse == 1 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
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

func discoverSecretUnboundVersions(tx *gorm.DB, ref domain.Ref) (secretModel, uint64, []secretVersionModel, error) {
	sec, err := (&SQLStore{}).findSecret(tx, ref)
	if err != nil {
		return secretModel{}, 0, nil, err
	}
	var rows []secretVersionModel
	if err := tx.Where("secret_id = ? AND state <> ? AND bound = ?", sec.ID, domain.StateDestroyed, int64(0)).
		Order("version_number ASC").Find(&rows).Error; err != nil {
		return secretModel{}, 0, nil, err
	}
	if len(rows) == 0 {
		return secretModel{}, 0, nil, domain.Errorf(domain.ErrFailedPrecondition, "secret %s has no unbound versions to purge", ref)
	}
	return sec, uint64(rows[0].VersionNumber), rows, nil
}

// PreviewSecretUnboundVersions returns every non-destroyed unbound version,
// regardless of usability of its cryptographic material.
func (s *SQLStore) PreviewSecretUnboundVersions(ctx context.Context, ref domain.Ref) (SecretVersionSetResult, error) {
	var result SecretVersionSetResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, _, rows, err := discoverSecretUnboundVersions(tx, ref)
		if err != nil {
			return err
		}
		revision, err := s.currentRevision(tx)
		if err != nil {
			return err
		}
		result = SecretVersionSetResult{AffectedVersions: bindingVersions(rows), Revision: revision}
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SecretVersionSetResult{}, err
	}
	return result, nil
}

type purgeSecretVersionSelector func(tx *gorm.DB) (secretModel, uint64, []secretVersionModel, error)

// purgeSecretVersions is the common secure-erasure transaction and WAL scrub
// used by bound-cohort and unbound-set purge operations.
func (s *SQLStore) purgeSecretVersions(ctx context.Context, ref domain.Ref, guard SecretBindingCASGuard, selectRows purgeSecretVersionSelector, changeType, auditEvent string, audit SecretBindingPurgeAudit) (SecretBindingResult, error) {

	// Pin our connection first, then drain the rest of the database/sql pool.
	// Max-idle must be zero: otherwise database/sql can retain and hand out an
	// idle connection before enforcing max-open. With our checkout occupying the
	// sole allowed slot, new operations queue and existing checkouts are closed
	// as they return. External readers can still block TRUNCATE; that post-commit
	// condition is reported distinctly below.
	s.purgeMu.Lock()
	defer s.purgeMu.Unlock()
	sqlDB, err := s.db.DB()
	if err != nil {
		return SecretBindingResult{}, err
	}
	storeClosed := false
	defer func() {
		if storeClosed {
			return
		}
		// Restore max-open first so the configured max-idle value is not
		// immediately clamped back to the temporary limit.
		sqlDB.SetMaxOpenConns(s.poolMaxOpenConns)
		sqlDB.SetMaxIdleConns(s.poolMaxIdleConns)
	}()

	var result SecretBindingResult
	committed := false
	err = s.db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
		sqlDB.SetMaxIdleConns(0)
		sqlDB.SetMaxOpenConns(1)
		if err := waitForExclusivePoolConnection(ctx, sqlDB); err != nil {
			return err
		}
		if err := conn.Transaction(func(tx *gorm.DB) error {
			sec, resolvedAnchor, rows, err := selectRows(tx)
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
					Where("id = ? AND secret_id = ? AND version_number = ? AND state <> ? AND bound = ?",
						row.ID, row.SecretID, row.VersionNumber, domain.StateDestroyed, row.Bound).
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
			secretUpdates := map[string]any{"updated_at": fmtTime(now)}
			labels, err := loadSecretLabels(tx, sec.ID)
			if err != nil {
				return err
			}
			if current, ok := labels[domain.LabelCurrent]; ok && slices.Contains(affected, current) {
				// The secret row is a projection of current. Once current is a minimal
				// tombstone, it must not retain its operator-supplied metadata.
				secretUpdates["content_type"] = ""
				secretUpdates["metadata_json"] = ""
			}
			updatedSecret := tx.Model(&secretModel{}).
				Where("id = ? AND namespace_id = ? AND name = ?", sec.ID, sec.NamespaceID, sec.Name).
				Updates(secretUpdates)
			if updatedSecret.Error != nil {
				return updatedSecret.Error
			}
			if updatedSecret.RowsAffected != 1 {
				return domain.Errorf(domain.ErrAborted, "secret changed concurrently; retry")
			}
			revision, err := appendSecretBindingChange(tx, sec, ref, changeType, resolvedAnchor, affected, now)
			if err != nil {
				return err
			}
			if err := appendSecretBindingAllowAudit(tx, auditEvent, sec, ref, resolvedAnchor, affected, audit, now); err != nil {
				return err
			}
			result = SecretBindingResult{AnchorVersion: resolvedAnchor, AffectedVersions: affected, Revision: revision}
			return nil
		}); err != nil {
			return err
		}
		committed = true
		// Once committed, request cancellation must not skip best-effort physical
		// cleanup. SQLite's configured busy timeout still bounds external-reader
		// contention.
		if err := truncateWAL(conn.WithContext(context.WithoutCancel(ctx))); err != nil {
			// Mark database/sql closed while this exclusive connection is still
			// pinned. Closing only after GORM returned it to the pool would hand the
			// connection to one queued request before fail-closed retirement.
			_ = sqlDB.Close()
			storeClosed = true
			return &purgeCleanupPendingError{cause: err}
		}
		return nil
	})
	if err != nil {
		if committed {
			// Recoverable pre-purge frames may still exist. The callback retired
			// this store before releasing its exclusive connection so no queued or
			// subsequent request can mistake the logical commit for a full scrub.
			// A fresh Open retries TRUNCATE before serving.
			return result, err
		}
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
	return s.purgeSecretVersions(ctx, ref, guard, func(tx *gorm.DB) (secretModel, uint64, []secretVersionModel, error) {
		return discoverSecretBindingCohort(tx, ref, anchor, test)
	}, domain.ChangePurgeBindingCohort, purgeBindingCohortAuditEvent, audit)
}

// PurgeSecretUnboundVersions irreversibly tombstones the exact set returned by
// a prior preview. Both guards are mandatory.
func (s *SQLStore) PurgeSecretUnboundVersions(ctx context.Context, ref domain.Ref, expectedRevision uint64, expectedAffectedVersions []uint64, audit SecretBindingPurgeAudit) (SecretVersionSetResult, error) {
	guard := cloneSecretBindingGuard(SecretBindingCASGuard{
		ExpectedRevision:         expectedRevision,
		ExpectedAffectedVersions: expectedAffectedVersions,
	})
	if err := validateSecretBindingGuard(guard); err != nil {
		return SecretVersionSetResult{}, err
	}
	result, err := s.purgeSecretVersions(ctx, ref, guard, func(tx *gorm.DB) (secretModel, uint64, []secretVersionModel, error) {
		return discoverSecretUnboundVersions(tx, ref)
	}, domain.ChangePurgeUnbound, purgeUnboundAuditEvent, audit)
	return SecretVersionSetResult{AffectedVersions: slices.Clone(result.AffectedVersions), Revision: result.Revision}, err
}
