package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"

	"github.com/Suhaibinator/kms/internal/domain"
	"gorm.io/gorm"
)

// MigrationResource identifies one candidate pin or edited parameter key.
type MigrationResource struct {
	Kind, Key string
	Version   uint64
	Write     bool
}

// MigrationSnapshot binds definition, environment and selected resource state.
// BaseDigest covers the definition and environment facts read before resolving
// resources. No secret bytes are read or returned.
type MigrationSnapshot struct {
	Digest        string
	BaseDigest    string
	ParameterNext map[string]uint64
}
type MigrationParameterWrite struct {
	Alias, Key, Value, ContentType string
	Version                        uint64
}
type ApplicationMigrationTransaction struct {
	SourceSchemaVersion              uint64
	ExpectedSourceVersion            uint64
	ExpectedSourceActivationRevision uint64
	Resources                        []MigrationResource
	Namespace                        domain.NamespaceRef
	Snapshot                         string
	Contract                         []domain.ApplicationContractField
	Release                          domain.ConfigurationRelease
	Writes                           []MigrationParameterWrite
	ExpectedActiveVersion            uint64
	Audit                            domain.AuditEvent
	ResourceAudits                   []domain.AuditEvent
}
type ApplicationMigrationStore interface {
	ApplicationMigrationSnapshot(context.Context, domain.ReleaseTrack, domain.ReleaseTrack, ...MigrationResource) (MigrationSnapshot, error)
	ApplyApplicationMigration(context.Context, ApplicationMigrationTransaction) (domain.ActiveConfigurationRelease, error)
}

func (s *SQLStore) ApplicationMigrationSnapshot(ctx context.Context, sourceTrack, targetTrack domain.ReleaseTrack, resources ...MigrationResource) (MigrationSnapshot, error) {
	var out MigrationSnapshot
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		out, err = migrationSnapshot(tx, sourceTrack, targetTrack, resources...)
		return err
	})
	return out, err
}

// migrationSnapshot reads only definition/environment facts and the referenced
// resource versions. It never scans parameter or release history.
func migrationSnapshot(tx *gorm.DB, sourceTrack, targetTrack domain.ReleaseTrack, resources ...MigrationResource) (MigrationSnapshot, error) {
	ns := sourceTrack.Namespace
	if targetTrack.Namespace != ns || targetTrack.Name != sourceTrack.Name {
		return MigrationSnapshot{}, domain.ErrInvalidArgument
	}
	h := sha256.New()
	hashQuery := func(q string, args ...any) error {
		var rows []map[string]any
		if err := tx.Raw(q, args...).Scan(&rows).Error; err != nil {
			return err
		}
		b, err := json.Marshal(rows, json.Deterministic(true))
		if err != nil {
			return err
		}
		_, err = h.Write(b)
		return err
	}
	if err := hashQuery(`SELECT * FROM applications WHERE name=?`, ns.App); err != nil {
		return MigrationSnapshot{}, err
	}
	if err := hashQuery(`SELECT * FROM namespaces WHERE app=? AND env=?`, ns.App, ns.Env); err != nil {
		return MigrationSnapshot{}, err
	}
	for _, track := range []domain.ReleaseTrack{sourceTrack, targetTrack} {
		if err := hashQuery(`SELECT * FROM configuration_schemas WHERE application_name=? AND release_name=? AND version_number=?`, ns.App, track.Name, track.SchemaVersion); err != nil {
			return MigrationSnapshot{}, err
		}
		if err := hashQuery(`SELECT * FROM schema_free_contracts WHERE application_name=? AND release_name=? AND ?=0`, ns.App, track.Name, track.SchemaVersion); err != nil {
			return MigrationSnapshot{}, err
		}
		if err := hashQuery(`SELECT r.* FROM configuration_release_labels r JOIN namespaces n ON n.id=r.namespace_id WHERE n.app=? AND n.env=? AND r.release_name=? AND r.schema_version=? ORDER BY r.label`, ns.App, ns.Env, track.Name, track.SchemaVersion); err != nil {
			return MigrationSnapshot{}, err
		}
		if err := hashQuery(`SELECT r.* FROM configuration_release_counters r JOIN namespaces n ON n.id=r.namespace_id WHERE n.app=? AND n.env=? AND r.release_name=? AND r.schema_version=?`, ns.App, ns.Env, track.Name, track.SchemaVersion); err != nil {
			return MigrationSnapshot{}, err
		}
	}
	out := MigrationSnapshot{BaseDigest: hex.EncodeToString(h.Sum(nil)), ParameterNext: map[string]uint64{}}
	for _, resource := range resources {
		if resource.Kind == domain.ReleaseEntryParameter {
			columns := "p.id,p.namespace_id,p.name"
			if resource.Write {
				columns = "p.*"
			}
			if err := hashQuery(`SELECT `+columns+` FROM parameters p JOIN namespaces n ON n.id=p.namespace_id WHERE n.app=? AND n.env=? AND p.name=?`, ns.App, ns.Env, resource.Key); err != nil {
				return out, err
			}
			if resource.Version > 0 {
				if err := hashQuery(`SELECT v.* FROM parameter_versions v JOIN parameters p ON p.id=v.parameter_id JOIN namespaces n ON n.id=p.namespace_id WHERE n.app=? AND n.env=? AND p.name=? AND v.version_number=?`, ns.App, ns.Env, resource.Key, resource.Version); err != nil {
					return out, err
				}
			}
			if resource.Write {
				if err := hashQuery(`SELECT l.* FROM parameter_labels l JOIN parameters p ON p.id=l.parameter_id JOIN namespaces n ON n.id=p.namespace_id WHERE n.app=? AND n.env=? AND p.name=? ORDER BY l.label`, ns.App, ns.Env, resource.Key); err != nil {
					return out, err
				}
				var next uint64
				if err := tx.Raw(`SELECT COALESCE(MAX(v.version_number),0)+1 FROM parameter_versions v JOIN parameters p ON p.id=v.parameter_id JOIN namespaces n ON n.id=p.namespace_id WHERE n.app=? AND n.env=? AND p.name=?`, ns.App, ns.Env, resource.Key).Scan(&next).Error; err != nil {
					return out, err
				}
				out.ParameterNext[resource.Key] = next
			}
		} else {
			if err := hashQuery(`SELECT s.id,s.namespace_id,s.name,v.id AS version_id,v.version_number,v.content_type,v.bound,v.wrap_mode,v.state,v.destroyed_at,v.expires_at,v.metadata_json FROM secrets s JOIN namespaces n ON n.id=s.namespace_id LEFT JOIN secret_versions v ON v.secret_id=s.id AND v.version_number=? WHERE n.app=? AND n.env=? AND s.name=?`, resource.Version, ns.App, ns.Env, resource.Key); err != nil {
				return out, err
			}
		}
	}
	b, err := json.Marshal(out.ParameterNext, json.Deterministic(true))
	if err != nil {
		return out, err
	}
	h.Write(b)
	out.Digest = hex.EncodeToString(h.Sum(nil))
	return out, nil
}

// ApplyApplicationMigration rechecks all dependencies and commits the shared
// definition, parameter versions, release, activation and success audit together.
func (s *SQLStore) ApplyApplicationMigration(ctx context.Context, in ApplicationMigrationTransaction) (domain.ActiveConfigurationRelease, error) {
	var out domain.ActiveConfigurationRelease
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state, err := migrationSnapshot(tx, domain.ReleaseTrack{Namespace: in.Namespace, Name: in.Release.Name, SchemaVersion: in.SourceSchemaVersion}, in.Release.Track(), in.Resources...)
		if err != nil {
			return err
		}
		if state.Digest != in.Snapshot {
			return applicationReleaseStale()
		}
		nsID, err := resolveNamespaceID(tx, in.Namespace)
		if err != nil {
			return err
		}
		var source configurationReleaseLabelModel
		err = tx.Where("namespace_id = ? AND release_name = ? AND schema_version = ? AND label = ?", nsID, in.Release.Name, in.SourceSchemaVersion, domain.LabelCurrent).First(&source).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if uint64(source.VersionNumber) != in.ExpectedSourceVersion || uint64(source.ActivationRevision) != in.ExpectedSourceActivationRevision {
			return applicationReleaseStale()
		}
		if _, err := adoptSchemaContractTx(tx, in.Namespace.App, in.Release.Name, in.Release.SchemaVersion, in.Contract); err != nil {
			return err
		}
		for _, w := range in.Writes {
			v, _, err := putParameterTx(tx, domain.Ref{NS: in.Namespace, Key: w.Key}, w.Value, w.ContentType, "{}", in.Release.CreatedBy, fmtTime(nowUTC()))
			if err != nil {
				return err
			}
			if v != w.Version {
				return applicationReleaseStale()
			}
		}
		// GORM nested transactions use savepoints on this same transaction/connection;
		// neither release creation nor activation can commit independently.
		scoped := &SQLStore{db: tx}
		release, err := scoped.CreateConfigurationRelease(ctx, in.Release)
		if err != nil {
			return err
		}
		out, _, err = scoped.ActivateConfigurationRelease(ctx, release.Track(), release.Version, &in.ExpectedActiveVersion)
		if err != nil {
			return err
		}
		for _, event := range in.ResourceAudits {
			if event.ResourceType == domain.ResourceConfigurationRelease {
				event.ResourceVersion = release.Version
			}
			if err := appendAudit(tx, event); err != nil {
				return err
			}
		}
		in.Audit.ResourceVersion = release.Version
		return appendAudit(tx, in.Audit)
	})
	if err != nil {
		return domain.ActiveConfigurationRelease{}, err
	}
	return out, nil
}
