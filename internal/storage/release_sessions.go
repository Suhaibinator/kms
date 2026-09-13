package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"strconv"
	"time"

	"github.com/Suhaibinator/kms/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Sessions are separate from legacy connections so old clients cannot inherit pins.
type releaseSessionModel struct {
	SessionID           string `gorm:"primaryKey"`
	NamespaceID         int64  `gorm:"not null;index"`
	ReleaseName         string `gorm:"not null"`
	SchemaVersion       uint64 `gorm:"not null"`
	ClientName          string `gorm:"not null"`
	InstanceID          string `gorm:"not null"`
	Identity            string `gorm:"not null"`
	PinVersion          uint64 `gorm:"not null;default:0"`
	PinRevision         uint64 `gorm:"not null;default:0"`
	PinnedBy            string
	PinnedAt            string
	Connected           int64 `gorm:"not null;default:0"`
	ConnectionID        string
	ServerTimestamp     string `gorm:"not null"`
	DisconnectedAt      string `gorm:"index"`
	State               string
	ReleaseVersion      uint64 `gorm:"not null;default:0"`
	TargetRevision      uint64 `gorm:"not null;default:0"`
	ActivationRevision  uint64 `gorm:"not null;default:0"`
	RejectionCategory   string
	AppliedDivergent    int64  `gorm:"not null;default:0"`
	DivergentFieldCount uint32 `gorm:"not null;default:0"`
	LastAppliedVersion  uint64 `gorm:"not null;default:0"`
	LastAckSequence     uint64
	LastAppliedRevision uint64
	LastAppliedSequence uint64
	PrunedAckSequence   uint64
	Diagnostic          string
	ClientTimestamp     string
}

// Keep the latest 256 event identities per session. Events below the persisted
// pruning watermark cannot regain authority after their fingerprint expires.
const releaseSessionEventRetention = 256

type releaseSessionEventModel struct {
	SessionID   string `gorm:"primaryKey"`
	Sequence    uint64 `gorm:"primaryKey;autoIncrement:false"`
	Fingerprint string `gorm:"not null"`
}

func (releaseSessionEventModel) TableName() string { return "release_session_events" }

func (releaseSessionModel) TableName() string { return "release_sessions" }

type releaseTargetDeliveryModel struct {
	CreatedAt          string `gorm:"not null"`
	SessionID          string `gorm:"primaryKey"`
	Revision           uint64 `gorm:"primaryKey;autoIncrement:false"`
	Version            uint64 `gorm:"not null"`
	ActivationRevision uint64 `gorm:"not null"`
}

func (releaseTargetDeliveryModel) TableName() string { return "release_target_deliveries" }

type ReleaseSessionStore interface {
	RegisterReleaseSession(context.Context, domain.ReleaseSessionRef, bool) error
	ResolveInstanceRelease(context.Context, domain.ReleaseSessionRef) (domain.InstanceReleaseTarget, error)
	SetReleasePin(context.Context, domain.ReleaseSessionRef, uint64, uint64, domain.AuditEvent) (domain.InstanceReleaseTarget, error)
	ConnectReleaseSession(context.Context, domain.ReleaseSessionRef, string, bool) error
	AcknowledgeReleaseSession(context.Context, domain.ReleaseSessionRef, domain.ReleaseAcknowledgement) error
	ReduceReleaseSessionAcknowledgement(context.Context, domain.ReleaseSessionRef, domain.ReleaseAcknowledgement) (domain.ReleaseAcknowledgementResult, error)
	GetReleaseSessionAcknowledgement(context.Context, domain.ReleaseSessionRef) (domain.ReleaseAcknowledgement, error)
}

func sessionTx(tx *gorm.DB, ref domain.ReleaseSessionRef) (releaseSessionModel, error) {
	nsID, err := resolveNamespaceID(tx, ref.Track.Namespace)
	if err != nil {
		return releaseSessionModel{}, err
	}
	var m releaseSessionModel
	err = tx.Where("session_id = ? AND namespace_id = ? AND release_name = ? AND schema_version = ? AND client_name = ? AND instance_id = ? AND identity = ?", ref.SessionID, nsID, ref.Track.Name, ref.Track.SchemaVersion, ref.ClientName, ref.InstanceID, ref.Identity).First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = domain.Errorf(domain.ErrFailedPrecondition, "release session is unavailable or expired; restart the client application")
	}
	return m, err
}

func (s *SQLStore) RegisterReleaseSession(ctx context.Context, ref domain.ReleaseSessionRef, resume bool) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		nsID, err := resolveNamespaceID(tx, ref.Track.Namespace)
		if err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&releaseSessionModel{}).Where("session_id = ?", ref.SessionID).Count(&count).Error; err != nil {
			return err
		}
		if resume || count > 0 {
			_, err := sessionTx(tx, ref)
			return err
		}
		now := fmtTime(nowUTC())
		return tx.Create(&releaseSessionModel{SessionID: ref.SessionID, NamespaceID: nsID, ReleaseName: ref.Track.Name, SchemaVersion: ref.Track.SchemaVersion, ClientName: ref.ClientName, InstanceID: ref.InstanceID, Identity: ref.Identity, ServerTimestamp: now, DisconnectedAt: now}).Error
	})
}

func resolveInstanceTx(tx *gorm.DB, ref domain.ReleaseSessionRef, m releaseSessionModel) (domain.InstanceReleaseTarget, error) {
	out := domain.InstanceReleaseTarget{Pinned: m.PinVersion > 0, PinRevision: m.PinRevision, TargetRevision: m.PinRevision, PinnedBy: m.PinnedBy, PinnedAt: parseTime(m.PinnedAt)}
	version := m.PinVersion
	if version == 0 {
		var label configurationReleaseLabelModel
		err := tx.Where("namespace_id = ? AND release_name = ? AND schema_version = ? AND label = ?", m.NamespaceID, m.ReleaseName, m.SchemaVersion, domain.LabelCurrent).First(&label).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return out, err
		}
		if err == nil {
			version = uint64(label.VersionNumber)
			out.ActivationRevision = uint64(label.ActivationRevision)
			out.TargetRevision = max(out.TargetRevision, out.ActivationRevision)
		}
	}
	if version == 0 {
		return out, nil
	}
	rel, err := getConfigurationRelease(tx, ref.Track, version)
	if err != nil {
		return out, err
	}
	out.Release = rel
	d := releaseTargetDeliveryModel{CreatedAt: fmtTime(nowUTC()), SessionID: m.SessionID, Revision: out.TargetRevision, Version: version, ActivationRevision: out.ActivationRevision}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&d).Error; err != nil {
		return out, err
	}
	return out, nil
}
func (s *SQLStore) ResolveInstanceRelease(ctx context.Context, ref domain.ReleaseSessionRef) (out domain.InstanceReleaseTarget, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		m, e := sessionTx(tx, ref)
		if e != nil {
			return e
		}
		out, e = resolveInstanceTx(tx, ref, m)
		return e
	})
	return
}
func (s *SQLStore) SetReleasePin(ctx context.Context, ref domain.ReleaseSessionRef, version, expected uint64, audit domain.AuditEvent) (out domain.InstanceReleaseTarget, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		m, e := sessionTx(tx, ref)
		if e != nil {
			return e
		}
		if m.PinRevision != expected {
			return domain.Errorf(domain.ErrAborted, "instance pin changed; refresh before retrying")
		}
		if version > 0 && m.Connected == 0 {
			return domain.Errorf(domain.ErrFailedPrecondition, "pinning requires a connected client process")
		}
		if version > 0 {
			var rel configurationReleaseModel
			if e := tx.Where("namespace_id = ? AND name = ? AND schema_version = ? AND version_number = ?", m.NamespaceID, m.ReleaseName, m.SchemaVersion, version).First(&rel).Error; e != nil {
				return domain.Errorf(domain.ErrNotFound, "release not found")
			}
			if e := validateReleasePinsTx(tx, rel.ID); e != nil {
				return e
			}
		}
		if m.PinVersion == version {
			out, e = resolveInstanceTx(tx, ref, m)
			return e
		}
		rev, e := appendChange(tx, &changeLogModel{NamespaceID: m.NamespaceID, SchemaVersion: int64(m.SchemaVersion), ResourceType: domain.ResourceReleaseInstance, Env: ref.Track.Namespace.Env, App: ref.Track.Namespace.App, Key: m.ReleaseName, ChangeType: "target", VersionNumber: int64(version)})
		if e != nil {
			return e
		}
		m.PinVersion = version
		m.PinRevision = rev
		m.PinnedBy = audit.ActorIdentity
		m.PinnedAt = fmtTime(nowUTC())
		m.ServerTimestamp = m.PinnedAt
		if version == 0 {
			m.PinnedBy = ""
			m.PinnedAt = ""
		}
		if e := tx.Save(&m).Error; e != nil {
			return e
		}
		audit.ResourceNamespaceID = m.NamespaceID
		metadata, _ := json.Marshal(map[string]any{"session_id": m.SessionID, "instance_id": m.InstanceID, "client_name": m.ClientName, "identity": m.Identity, "schema_version": strconv.FormatUint(m.SchemaVersion, 10), "target_revision": strconv.FormatUint(rev, 10)})
		audit.Metadata = string(metadata)
		if e := appendAudit(tx, audit); e != nil {
			return e
		}
		out, e = resolveInstanceTx(tx, ref, m)
		return e
	})
	return
}
func (s *SQLStore) ConnectReleaseSession(ctx context.Context, ref domain.ReleaseSessionRef, connection string, connected bool) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		m, e := sessionTx(tx, ref)
		if e != nil {
			return e
		}
		if !connected && m.ConnectionID != connection {
			return nil
		}
		m.Connected = b2i(connected)
		m.ConnectionID = connection
		m.ServerTimestamp = fmtTime(nowUTC())
		m.DisconnectedAt = ""
		if !connected {
			m.DisconnectedAt = m.ServerTimestamp
		}
		return tx.Save(&m).Error
	})
}
func (s *SQLStore) AcknowledgeReleaseSession(ctx context.Context, ref domain.ReleaseSessionRef, ack domain.ReleaseAcknowledgement) error {
	_, err := s.ReduceReleaseSessionAcknowledgement(ctx, ref, ack)
	return err
}

func sessionAcknowledgement(ref domain.ReleaseSessionRef, m releaseSessionModel) domain.ReleaseAcknowledgement {
	return domain.ReleaseAcknowledgement{Namespace: ref.Track.Namespace, ReleaseName: m.ReleaseName, SchemaVersion: m.SchemaVersion, SessionID: m.SessionID, ClientName: m.ClientName, InstanceID: m.InstanceID, Identity: m.Identity, ConnectionID: m.ConnectionID, Connected: m.Connected != 0, Sequence: m.LastAckSequence, State: m.State, ReleaseVersion: m.ReleaseVersion, TargetRevision: m.TargetRevision, ActivationRevision: m.ActivationRevision, LastAppliedVersion: m.LastAppliedVersion, LastAppliedRevision: m.LastAppliedRevision, LastAppliedSequence: m.LastAppliedSequence, RejectionCategory: m.RejectionCategory, Diagnostic: m.Diagnostic, AppliedDivergent: m.AppliedDivergent != 0, DivergentFieldCount: m.DivergentFieldCount, ClientTimestamp: parseTime(m.ClientTimestamp), ServerTimestamp: parseTime(m.ServerTimestamp), LiveTimestamp: parseTime(m.ServerTimestamp), PinVersion: m.PinVersion, PinRevision: m.PinRevision, PinnedBy: m.PinnedBy, PinnedAt: parseTime(m.PinnedAt)}
}

func (s *SQLStore) GetReleaseSessionAcknowledgement(ctx context.Context, ref domain.ReleaseSessionRef) (domain.ReleaseAcknowledgement, error) {
	m, err := sessionTx(s.db.WithContext(ctx), ref)
	return sessionAcknowledgement(ref, m), err
}

// ReleaseAcknowledgementFingerprint hashes immutable event content before
// redaction. Only the digest is retained; raw diagnostics are never persisted.
func ReleaseAcknowledgementFingerprint(ack domain.ReleaseAcknowledgement) string {
	// Transport generation and server timestamps change on replay; event content does not.
	payload, _ := json.Marshal(struct {
		Target, Version, Activation            uint64
		State, Category, Diagnostic, Timestamp string
		Divergent                              bool
		Fields                                 uint32
	}{ack.TargetRevision, ack.ReleaseVersion, ack.ActivationRevision, ack.State, ack.RejectionCategory, ack.Diagnostic, fmtTime(ack.ClientTimestamp), ack.AppliedDivergent, ack.DivergentFieldCount})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func (s *SQLStore) ReduceReleaseSessionAcknowledgement(ctx context.Context, ref domain.ReleaseSessionRef, ack domain.ReleaseAcknowledgement) (out domain.ReleaseAcknowledgementResult, err error) {
	out.Disposition = "unavailable"
	var previous domain.ReleaseAcknowledgement
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		m, e := sessionTx(tx, ref)
		if e != nil {
			return e
		}
		if m.Connected == 0 || m.ConnectionID != ack.ConnectionID {
			return domain.Errorf(domain.ErrAborted, "release session connection changed")
		}
		out.Effective = sessionAcknowledgement(ref, m)
		previous = out.Effective
		if ack.Sequence == 0 || sessionStateRank(ack.State) == 0 {
			return domain.Errorf(domain.ErrFailedPrecondition, "release acknowledgement requires a nonzero sequence and valid lifecycle state")
		}
		fingerprint := ack.PayloadFingerprint
		if fingerprint == "" {
			fingerprint = ReleaseAcknowledgementFingerprint(ack)
		}
		var retained releaseSessionEventModel
		e = tx.Where("session_id = ? AND sequence = ?", m.SessionID, ack.Sequence).First(&retained).Error
		if e == nil {
			if retained.Fingerprint != fingerprint {
				out.Disposition = "conflict"
				return domain.Errorf(domain.ErrFailedPrecondition, "release acknowledgement sequence has conflicting payload")
			}
			out.Disposition = "duplicate"
			m.ServerTimestamp = fmtTime(nowUTC())
			if e := tx.Save(&m).Error; e != nil {
				return e
			}
			out.Effective = sessionAcknowledgement(ref, m)
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if ack.Sequence <= m.PrunedAckSequence {
			out.Disposition = "stale"
			m.ServerTimestamp = fmtTime(nowUTC())
			if e := tx.Save(&m).Error; e != nil {
				return e
			}
			out.Effective = sessionAcknowledgement(ref, m)
			return nil
		}
		var d releaseTargetDeliveryModel
		e = tx.Where("session_id = ? AND revision = ?", m.SessionID, ack.TargetRevision).First(&d).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return &domain.ReleaseAcknowledgementUnavailableError{}
		}
		if e != nil {
			return e
		}
		if d.Version != ack.ReleaseVersion || d.ActivationRevision != ack.ActivationRevision {
			return domain.Errorf(domain.ErrFailedPrecondition, "acknowledgement does not match assigned target")
		}
		if e := tx.Create(&releaseSessionEventModel{SessionID: m.SessionID, Sequence: ack.Sequence, Fingerprint: fingerprint}).Error; e != nil {
			return e
		}
		sequence := ack.Sequence
		newer := ack.TargetRevision > m.TargetRevision || (ack.TargetRevision == m.TargetRevision && sequence > m.LastAckSequence)
		out.Disposition = "stale"
		if newer {
			out.Disposition = "accepted"
			m.LastAckSequence = sequence
			m.State = ack.State
			m.ReleaseVersion = ack.ReleaseVersion
			m.TargetRevision = ack.TargetRevision
			m.ActivationRevision = ack.ActivationRevision
			m.RejectionCategory = ack.RejectionCategory
			m.Diagnostic = ack.Diagnostic
			m.ClientTimestamp = fmtTime(ack.ClientTimestamp)
			m.AppliedDivergent = b2i(ack.AppliedDivergent)
			m.DivergentFieldCount = ack.DivergentFieldCount
		}
		if ack.State == domain.ReleaseStateApplied && (ack.TargetRevision > m.LastAppliedRevision || ack.TargetRevision == m.LastAppliedRevision && sequence > m.LastAppliedSequence) {
			m.LastAppliedRevision = ack.TargetRevision
			m.LastAppliedSequence = sequence
			m.LastAppliedVersion = ack.ReleaseVersion
		}
		var expired []releaseSessionEventModel
		if e := tx.Where("session_id = ?", m.SessionID).Order("sequence DESC").Offset(releaseSessionEventRetention).Find(&expired).Error; e != nil {
			return e
		}
		if len(expired) > 0 {
			m.PrunedAckSequence = max(m.PrunedAckSequence, expired[0].Sequence)
			if e := tx.Where("session_id = ? AND sequence <= ?", m.SessionID, m.PrunedAckSequence).Delete(&releaseSessionEventModel{}).Error; e != nil {
				return e
			}
		}
		m.ServerTimestamp = fmtTime(nowUTC())
		if e := tx.Save(&m).Error; e != nil {
			return e
		}
		out.Effective = sessionAcknowledgement(ref, m)
		return nil
	})
	if err != nil {
		if out.Disposition != "conflict" {
			out.Disposition = "unavailable"
		}
		out.Effective = previous
	}
	return out, err
}

// Retire only disconnected sessions. A resumed expired session is rejected.
func pruneReleaseSessions(tx *gorm.DB, before time.Time) error {
	if err := tx.Where("connected = 0 AND disconnected_at <> '' AND disconnected_at < ?", fmtTime(before)).Delete(&releaseSessionModel{}).Error; err != nil {
		return err
	}
	if err := tx.Exec("DELETE FROM release_target_deliveries WHERE session_id NOT IN (SELECT session_id FROM release_sessions)").Error; err != nil {
		return err
	}
	return tx.Exec("DELETE FROM release_session_events WHERE session_id NOT IN (SELECT session_id FROM release_sessions)").Error
}

func sessionStateRank(state string) int {
	switch state {
	case "received":
		return 1
	case "prepared":
		return 2
	case "applied":
		return 3
	case "rejected":
		return 4
	}
	return 0
}
