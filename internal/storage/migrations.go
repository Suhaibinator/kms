package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"

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
	Resources             []MigrationResource
	Namespace             domain.NamespaceRef
	Snapshot              string
	Contract              []domain.ApplicationContractField
	Release               domain.ConfigurationRelease
	Writes                []MigrationParameterWrite
	ExpectedActiveVersion uint64
	Audit                 domain.AuditEvent
}
type ApplicationMigrationStore interface {
	ApplicationMigrationSnapshot(context.Context, domain.NamespaceRef, ...MigrationResource) (MigrationSnapshot, error)
	ApplyApplicationMigration(context.Context, ApplicationMigrationTransaction) (domain.ActiveConfigurationRelease, error)
}

func (s *SQLStore) ApplicationMigrationSnapshot(ctx context.Context, ns domain.NamespaceRef, resources ...MigrationResource) (MigrationSnapshot, error) {
	var out MigrationSnapshot
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		out, err = migrationSnapshot(tx, ns, resources...)
		return err
	})
	return out, err
}

// migrationSnapshot reads only definition/environment facts and the referenced
// resource versions. It never scans parameter or release history.
func migrationSnapshot(tx *gorm.DB, ns domain.NamespaceRef, resources ...MigrationResource) (MigrationSnapshot, error) {
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
	queries := []string{
		`SELECT * FROM applications WHERE name=? ORDER BY id`,
		`SELECT * FROM namespaces WHERE app=? ORDER BY id`,
		`SELECT r.namespace_id,r.release_name,r.label,r.version_number,r.activation_revision,v.schema_version,v.digest FROM configuration_release_labels r JOIN namespaces n ON n.id=r.namespace_id JOIN configuration_releases v ON v.namespace_id=r.namespace_id AND v.name=r.release_name AND v.version_number=r.version_number WHERE n.app=? ORDER BY r.namespace_id,r.release_name,r.label`,
	}
	for _, q := range queries {
		if err := hashQuery(q, ns.App); err != nil {
			return MigrationSnapshot{}, err
		}
	}
	out := MigrationSnapshot{BaseDigest: hex.EncodeToString(h.Sum(nil)), ParameterNext: map[string]uint64{}}
	for _, resource := range resources {
		if resource.Kind == domain.ReleaseEntryParameter {
			if err := hashQuery(`SELECT p.* FROM parameters p JOIN namespaces n ON n.id=p.namespace_id WHERE n.app=? AND n.env=? AND p.name=?`, ns.App, ns.Env, resource.Key); err != nil {
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
		state, err := migrationSnapshot(tx, in.Namespace, in.Resources...)
		if err != nil {
			return err
		}
		if state.Digest != in.Snapshot {
			return applicationReleaseStale()
		}
		contract, err := contractJSON(in.Contract)
		if err != nil {
			return err
		}
		if err = tx.Model(&applicationModel{}).Where("name = ?", in.Namespace.App).Updates(map[string]any{
			"schema_version": in.Release.SchemaVersion,
			"contract_json":  contract,
			"updated_at":     fmtTime(nowUTC()),
		}).Error; err != nil {
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
		out, _, err = scoped.ActivateConfigurationRelease(ctx, in.Namespace, release.Name, release.Version, &in.ExpectedActiveVersion)
		if err != nil {
			return err
		}
		in.Audit.ResourceVersion = release.Version
		return appendAudit(tx, in.Audit)
	})
	if err != nil {
		return domain.ActiveConfigurationRelease{}, err
	}
	return out, nil
}
