package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"reflect"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Suhaibinator/kms/internal/domain"
)

var _ ReleaseStore = (*SQLStore)(nil)
var _ FirstReleaseStore = (*SQLStore)(nil)
var _ ApplicationReleaseStore = (*SQLStore)(nil)

func (s *SQLStore) CreateConfigurationRelease(ctx context.Context, release domain.ConfigurationRelease) (domain.ConfigurationRelease, error) {
	out, _, err := s.createConfigurationRelease(ctx, release, releaseCreateOptions{})
	return out, err
}

// CreateFirstConfigurationRelease atomically creates version one or aborts if
// the release stream is already established.
func (s *SQLStore) CreateFirstConfigurationRelease(ctx context.Context, release domain.ConfigurationRelease) (domain.ConfigurationRelease, error) {
	out, _, err := s.createConfigurationRelease(ctx, release, releaseCreateOptions{requireFirst: true})
	return out, err
}

type releaseCreateOptions struct {
	requireFirst bool
	application  *ApplicationReleaseCreate
}

// CreateLatestApplicationRelease performs the preview-state checks and
// version allocation together. It is the only release creation path that is
// idempotent against an identical latest canonical release.
func (s *SQLStore) CreateLatestApplicationRelease(ctx context.Context, in ApplicationReleaseCreate) (domain.ConfigurationRelease, bool, error) {
	return s.createConfigurationRelease(ctx, in.Release, releaseCreateOptions{application: &in})
}

func (s *SQLStore) createConfigurationRelease(ctx context.Context, release domain.ConfigurationRelease, options releaseCreateOptions) (domain.ConfigurationRelease, bool, error) {
	var out domain.ConfigurationRelease
	created := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		nsID, err := resolveNamespaceID(tx, release.Namespace)
		if err != nil {
			return err
		}
		for _, entry := range release.Entries {
			if entry.Ref.NS != release.Namespace {
				return domain.Errorf(domain.ErrInvalidArgument,
					"release entry %q must reference its home namespace %s", entry.Alias, release.Namespace)
			}
		}
		for index := range release.Entries {
			release.Entries[index].ResourceNamespaceID = nsID
		}
		if options.application != nil {
			if err := verifyApplicationReleaseState(tx, nsID, *options.application); err != nil {
				return err
			}
		}
		var maxVersion int64
		if err := tx.Model(&configurationReleaseModel{}).
			Where("namespace_id = ? AND name = ?", nsID, release.Name).
			Select("COALESCE(MAX(version_number), 0)").Scan(&maxVersion).Error; err != nil {
			return err
		}
		if options.application != nil && maxVersion != 0 {
			latest, err := getConfigurationRelease(tx, release.Namespace, release.Name, uint64(maxVersion))
			if err != nil {
				return err
			}
			if sameCanonicalRelease(latest, release) {
				var latestModel configurationReleaseModel
				if err := tx.Where("namespace_id = ? AND name = ? AND version_number = ?", nsID, release.Name, maxVersion).First(&latestModel).Error; err != nil {
					return err
				}
				if err := validateReleasePinsTx(tx, latestModel.ID); err != nil {
					return err
				}
				out = latest
				return nil
			}
			if uint64(maxVersion) != options.application.ExpectedLatestVersion {
				return applicationReleaseStale()
			}
		} else if options.application != nil && options.application.ExpectedLatestVersion != 0 {
			return applicationReleaseStale()
		}
		if options.requireFirst && maxVersion != 0 {
			return domain.Errorf(domain.ErrAborted, "configuration release %s/%s is already established", release.Namespace, release.Name)
		}
		now := nowUTC()
		m := configurationReleaseModel{
			NamespaceID: nsID, Name: release.Name, VersionNumber: maxVersion + 1,
			SchemaVersion: int64(release.SchemaVersion),
			Digest:        release.Digest, MetadataJSON: zeroOr(release.Metadata, "{}"),
			CreatedBy: release.CreatedBy, CreatedAt: fmtTime(now),
		}
		if err := tx.Omit(clause.Associations).Create(&m).Error; err != nil {
			if isUniqueErr(err) {
				if options.requireFirst {
					return domain.Errorf(domain.ErrAborted, "configuration release %s/%s is already established", release.Namespace, release.Name)
				}
				return domain.Errorf(domain.ErrAlreadyExists, "configuration release %s/%s version %d", release.Namespace, release.Name, maxVersion+1)
			}
			return err
		}
		for _, entry := range release.Entries {
			em := configurationReleaseEntryModel{
				ReleaseID: m.ID, Alias: entry.Alias, Kind: entry.Kind, ResourceNamespaceID: nsID,
				ResourceEnv: entry.Ref.NS.Env, ResourceApp: entry.Ref.NS.App, ResourceKey: entry.Ref.Key,
				ResourceVersion: int64(entry.Version), ContentType: entry.ContentType,
				MetadataJSON: zeroOr(entry.Metadata, "{}"), ParameterDigest: entry.ParameterDigest,
			}
			if err := tx.Omit(clause.Associations).Create(&em).Error; err != nil {
				if isUniqueErr(err) {
					return domain.Errorf(domain.ErrInvalidArgument, "duplicate release alias %q", entry.Alias)
				}
				return err
			}
		}
		if options.application != nil {
			if err := validateReleasePinsTx(tx, m.ID); err != nil {
				return err
			}
		}
		release.Version = uint64(m.VersionNumber)
		release.CreatedAt = now
		release.Metadata = m.MetadataJSON
		out = release
		created = true
		return nil
	})
	return out, created, err
}

func applicationReleaseStale() error {
	return domain.Errorf(domain.ErrAborted, "application release plan is stale; preview again")
}

func verifyApplicationReleaseState(tx *gorm.DB, nsID int64, in ApplicationReleaseCreate) error {
	var app applicationModel
	if err := tx.Where("name = ?", in.Release.Namespace.App).First(&app).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return applicationReleaseStale()
		}
		return err
	}
	contract, err := contractJSON(in.Contract)
	if err != nil {
		return err
	}
	if app.ArchivedAt != nil || app.ReleaseName != in.Release.Name || uint64(app.SchemaVersion) != in.Release.SchemaVersion || app.ContractJSON != contract {
		return applicationReleaseStale()
	}
	if in.NamespaceID != nsID {
		return applicationReleaseStale()
	}
	var schema configurationSchemaModel
	if err := tx.Where("application_name = ? AND release_name = ? AND version_number = ?", app.Name, app.ReleaseName, app.SchemaVersion).First(&schema).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return applicationReleaseStale()
		}
		return err
	}
	if schema.Digest != in.SchemaDigest {
		return applicationReleaseStale()
	}
	var active configurationReleaseLabelModel
	err = tx.Where("namespace_id = ? AND release_name = ? AND label = ?", nsID, in.Release.Name, domain.LabelCurrent).First(&active).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		if in.ExpectedActiveVersion != 0 || in.ExpectedActivationRevision != 0 {
			return applicationReleaseStale()
		}
	case err != nil:
		return err
	default:
		if uint64(active.VersionNumber) != in.ExpectedActiveVersion || uint64(active.ActivationRevision) != in.ExpectedActivationRevision {
			return applicationReleaseStale()
		}
	}
	for _, pin := range in.CurrentPins {
		pinNamespaceID, err := resolveNamespaceID(tx, pin.Ref.NS)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrAborted) {
				return applicationReleaseStale()
			}
			return err
		}
		var current int64
		switch pin.Kind {
		case domain.ReleaseEntryParameter:
			err = tx.Raw(`SELECT l.version_number FROM parameters p
				JOIN parameter_labels l ON l.parameter_id = p.id AND l.label = ?
				WHERE p.namespace_id = ? AND p.name = ?`, domain.LabelCurrent, pinNamespaceID, pin.Ref.Key).Scan(&current).Error
		case domain.ReleaseEntrySecret:
			err = tx.Raw(`SELECT l.version_number FROM secrets s
				JOIN secret_labels l ON l.secret_id = s.id AND l.label = ?
				WHERE s.namespace_id = ? AND s.name = ?`, domain.LabelCurrent, pinNamespaceID, pin.Ref.Key).Scan(&current).Error
		default:
			return applicationReleaseStale()
		}
		if err != nil {
			return err
		}
		if uint64(current) != pin.Version {
			return applicationReleaseStale()
		}
	}
	return nil
}

func sameCanonicalRelease(a, b domain.ConfigurationRelease) bool {
	normalize := func(r domain.ConfigurationRelease) domain.ConfigurationRelease {
		r.Version, r.CreatedBy, r.CreatedAt = 0, "", time.Time{}
		r.Metadata = zeroOr(r.Metadata, "{}")
		r.Entries = append([]domain.ConfigurationReleaseEntry(nil), r.Entries...)
		for index := range r.Entries {
			r.Entries[index].Metadata = zeroOr(r.Entries[index].Metadata, "{}")
		}
		sort.Slice(r.Entries, func(i, j int) bool { return r.Entries[i].Alias < r.Entries[j].Alias })
		return r
	}
	return reflect.DeepEqual(normalize(a), normalize(b))
}

func (s *SQLStore) GetConfigurationRelease(ctx context.Context, ns domain.NamespaceRef, name string, version uint64) (domain.ConfigurationRelease, error) {
	var out domain.ConfigurationRelease
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		out, err = getConfigurationRelease(tx, ns, name, version)
		return err
	}, &sql.TxOptions{ReadOnly: true})
	return out, err
}

func getConfigurationRelease(db *gorm.DB, ns domain.NamespaceRef, name string, version uint64) (domain.ConfigurationRelease, error) {
	nsID, err := resolveNamespaceID(db, ns)
	if err != nil {
		return domain.ConfigurationRelease{}, err
	}
	var m configurationReleaseModel
	q := db.Where("namespace_id = ? AND name = ?", nsID, name)
	if version == 0 {
		q = q.Order("configuration_releases.version_number DESC")
	} else {
		q = q.Where("configuration_releases.version_number = ?", version)
	}
	if err := q.First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ConfigurationRelease{}, domain.Errorf(domain.ErrNotFound, "configuration release %s/%s version %d", ns, name, version)
		}
		return domain.ConfigurationRelease{}, err
	}
	return releaseFromModel(db, ns, m)
}

func releaseFromModel(db *gorm.DB, ns domain.NamespaceRef, m configurationReleaseModel) (domain.ConfigurationRelease, error) {
	var rows []configurationReleaseEntryModel
	if err := db.Where("release_id = ?", m.ID).Order("alias ASC").Find(&rows).Error; err != nil {
		return domain.ConfigurationRelease{}, err
	}
	return releaseFromModelAndEntries(ns, m, rows), nil
}

func releaseFromModelAndEntries(ns domain.NamespaceRef, m configurationReleaseModel, rows []configurationReleaseEntryModel) domain.ConfigurationRelease {
	entries := make([]domain.ConfigurationReleaseEntry, 0, len(rows))
	for _, e := range rows {
		entries = append(entries, domain.ConfigurationReleaseEntry{
			Alias: e.Alias, Kind: e.Kind,
			Ref:                 domain.Ref{NS: domain.NamespaceRef{Env: e.ResourceEnv, App: e.ResourceApp}, Key: e.ResourceKey},
			Version:             uint64(e.ResourceVersion),
			ResourceNamespaceID: e.ResourceNamespaceID,
			ContentType:         e.ContentType, Metadata: e.MetadataJSON,
			ParameterDigest: e.ParameterDigest,
		})
	}
	return domain.ConfigurationRelease{
		Namespace: ns, Name: m.Name, Version: uint64(m.VersionNumber),
		SchemaVersion: uint64(m.SchemaVersion), Entries: entries, Digest: m.Digest,
		Metadata: m.MetadataJSON, CreatedBy: m.CreatedBy, CreatedAt: parseTime(m.CreatedAt),
	}
}

func (s *SQLStore) GetActiveConfigurationRelease(ctx context.Context, ns domain.NamespaceRef, name string) (domain.ActiveConfigurationRelease, error) {
	var out domain.ActiveConfigurationRelease
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		nsID, err := resolveNamespaceID(tx, ns)
		if err != nil {
			return err
		}
		var cur configurationReleaseLabelModel
		if err := tx.Where("namespace_id = ? AND release_name = ? AND label = ?", nsID, name, domain.LabelCurrent).First(&cur).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.Errorf(domain.ErrNotFound, "active configuration release %s/%s", ns, name)
			}
			return err
		}
		rel, err := getConfigurationRelease(tx, ns, name, uint64(cur.VersionNumber))
		if err != nil {
			return err
		}
		var prev configurationReleaseLabelModel
		if err := tx.Where("namespace_id = ? AND release_name = ? AND label = ?", nsID, name, domain.LabelPrevious).First(&prev).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		out = domain.ActiveConfigurationRelease{Release: rel, ActivationRevision: uint64(cur.ActivationRevision), PreviousVersion: uint64(prev.VersionNumber)}
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	return out, err
}

func (s *SQLStore) CountConfigurationReleases(ctx context.Context, ns domain.NamespaceRef, name string) (uint64, error) {
	var count int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		nsID, err := resolveNamespaceID(tx, ns)
		if err != nil {
			return err
		}
		q := tx.Model(&configurationReleaseModel{}).Where("namespace_id = ?", nsID)
		if name != "" {
			q = q.Where("name = ?", name)
		}
		return q.Count(&count).Error
	})
	if err != nil {
		return 0, err
	}
	return uint64(count), nil
}

func (s *SQLStore) ListConfigurationReleases(ctx context.Context, ns domain.NamespaceRef, name string, page ListPage) ([]domain.ConfigurationReleaseSummary, string, error) {
	limit := clampLimit(page.Limit)
	after, err := decodeIntToken(page.Token)
	if err != nil {
		return nil, "", err
	}
	var out []domain.ConfigurationReleaseSummary
	next := ""
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		nsID, err := resolveNamespaceID(tx, ns)
		if err != nil {
			return err
		}
		q := tx.Where("namespace_id = ?", nsID)
		if name != "" {
			q = q.Where("name = ?", name)
		}
		if after > 0 {
			q = q.Where("id < ?", after)
		}
		var rows []configurationReleaseModel
		if err := q.Order("id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) > limit {
			rows = rows[:limit]
			next = encodeIntToken(rows[len(rows)-1].ID)
		}
		if len(rows) == 0 {
			out = []domain.ConfigurationReleaseSummary{}
			return nil
		}
		releaseNames := make([]string, 0, len(rows))
		releaseIDs := make([]int64, 0, len(rows))
		seenName := make(map[string]struct{}, len(rows))
		for _, row := range rows {
			releaseIDs = append(releaseIDs, row.ID)
			if _, ok := seenName[row.Name]; !ok {
				seenName[row.Name] = struct{}{}
				releaseNames = append(releaseNames, row.Name)
			}
		}
		var labels []configurationReleaseLabelModel
		if err := tx.Where("namespace_id = ? AND release_name IN ?", nsID, releaseNames).Find(&labels).Error; err != nil {
			return err
		}
		type lk struct{ name, label string }
		lm := map[lk]configurationReleaseLabelModel{}
		for _, l := range labels {
			lm[lk{l.ReleaseName, l.Label}] = l
		}
		conditions := make([]string, 0, len(rows))
		activationArgs := make([]any, 0, len(rows)*2+1)
		activationArgs = append(activationArgs, nsID)
		for _, row := range rows {
			conditions = append(conditions, "(release_name = ? AND version_number = ?)")
			activationArgs = append(activationArgs, row.Name, row.VersionNumber)
		}
		var activations []configurationReleaseActivationModel
		if err := tx.Where("namespace_id = ? AND ("+strings.Join(conditions, " OR ")+")", activationArgs...).Order("revision DESC").Find(&activations).Error; err != nil {
			return err
		}
		type ak struct {
			name    string
			version int64
		}
		latestActivation := make(map[ak]uint64, len(activations))
		for _, a := range activations {
			k := ak{a.ReleaseName, a.VersionNumber}
			if latestActivation[k] == 0 {
				latestActivation[k] = uint64(a.Revision)
			}
		}
		var entryRows []configurationReleaseEntryModel
		if err := tx.Where("release_id IN ?", releaseIDs).Order("release_id ASC, alias ASC").Find(&entryRows).Error; err != nil {
			return err
		}
		entriesByRelease := make(map[int64][]configurationReleaseEntryModel, len(rows))
		for _, entry := range entryRows {
			entriesByRelease[entry.ReleaseID] = append(entriesByRelease[entry.ReleaseID], entry)
		}
		out = make([]domain.ConfigurationReleaseSummary, 0, len(rows))
		for _, m := range rows {
			rel := releaseFromModelAndEntries(ns, m, entriesByRelease[m.ID])
			cur := lm[lk{m.Name, domain.LabelCurrent}]
			prev := lm[lk{m.Name, domain.LabelPrevious}]
			out = append(out, domain.ConfigurationReleaseSummary{Release: rel, Current: cur.VersionNumber == m.VersionNumber, Previous: prev.VersionNumber == m.VersionNumber, ActivationRevision: latestActivation[ak{m.Name, m.VersionNumber}]})
		}
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	return out, next, err
}

func (s *SQLStore) ConfigurationReleaseActivationExists(ctx context.Context, ns domain.NamespaceRef, name string, version, revision uint64) (bool, error) {
	exists := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		nsID, err := resolveNamespaceID(tx, ns)
		if err != nil {
			return err
		}
		var count int64
		if err := tx.Table("configuration_release_activations").Where("revision = ? AND namespace_id = ? AND release_name = ? AND version_number = ?", revision, nsID, name, version).Count(&count).Error; err != nil {
			return err
		}
		// The activation-history table is authoritative in the greenfield 0.3
		// baseline. Changelog and mutable label rows are not proof that this exact
		// activation occurred.
		exists = count == 1
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	return exists, err
}

func (s *SQLStore) ActivateConfigurationRelease(ctx context.Context, ns domain.NamespaceRef, name string, version uint64, expectedCurrent *uint64) (domain.ActiveConfigurationRelease, bool, error) {
	var out domain.ActiveConfigurationRelease
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		nsID, err := resolveNamespaceID(tx, ns)
		if err != nil {
			return err
		}
		var target configurationReleaseModel
		if err := tx.Where("namespace_id = ? AND name = ? AND version_number = ?", nsID, name, version).First(&target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.Errorf(domain.ErrNotFound, "configuration release %s/%s version %d", ns, name, version)
			}
			return err
		}
		var current configurationReleaseLabelModel
		err = tx.Where("namespace_id = ? AND release_name = ? AND label = ?", nsID, name, domain.LabelCurrent).First(&current).Error
		currentVersion := uint64(0)
		if err == nil {
			currentVersion = uint64(current.VersionNumber)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if expectedCurrent != nil && *expectedCurrent != currentVersion {
			return domain.Errorf(domain.ErrAborted, "configuration release current version is %d, expected %d", currentVersion, *expectedCurrent)
		}
		// Re-check every immutable resource pin in this write transaction, including
		// idempotent activations. A pin may have become unreadable after an earlier
		// preflight validation, and reporting success here would otherwise rely on
		// stale validation.
		if err := validateReleasePinsTx(tx, target.ID); err != nil {
			return err
		}
		if currentVersion == version {
			rel, err := releaseFromModel(tx, ns, target)
			if err != nil {
				return err
			}
			var prev configurationReleaseLabelModel
			if err := tx.Where("namespace_id = ? AND release_name = ? AND label = ?", nsID, name, domain.LabelPrevious).First(&prev).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			out = domain.ActiveConfigurationRelease{Release: rel, ActivationRevision: uint64(current.ActivationRevision), PreviousVersion: uint64(prev.VersionNumber)}
			return nil
		}
		rev, err := appendChange(tx, &changeLogModel{ResourceType: domain.ResourceConfigurationRelease, Env: ns.Env, App: ns.App, Key: name, ChangeType: "activate", VersionNumber: int64(version)})
		if err != nil {
			return err
		}
		if err := tx.Omit(clause.Associations).Create(&configurationReleaseActivationModel{
			Revision: int64(rev), NamespaceID: nsID, ReleaseName: name,
			VersionNumber: int64(version), ActivatedAt: fmtTime(time.Now()),
		}).Error; err != nil {
			return err
		}
		if currentVersion != 0 {
			prev := configurationReleaseLabelModel{NamespaceID: nsID, ReleaseName: name, Label: domain.LabelPrevious, VersionNumber: int64(currentVersion), ActivationRevision: current.ActivationRevision}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "namespace_id"}, {Name: "release_name"}, {Name: "label"}}, DoUpdates: clause.AssignmentColumns([]string{"version_number", "activation_revision"})}).Create(&prev).Error; err != nil {
				return err
			}
		}
		cur := configurationReleaseLabelModel{NamespaceID: nsID, ReleaseName: name, Label: domain.LabelCurrent, VersionNumber: int64(version), ActivationRevision: int64(rev)}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "namespace_id"}, {Name: "release_name"}, {Name: "label"}}, DoUpdates: clause.AssignmentColumns([]string{"version_number", "activation_revision"})}).Create(&cur).Error; err != nil {
			return err
		}
		rel, err := releaseFromModel(tx, ns, target)
		if err != nil {
			return err
		}
		out = domain.ActiveConfigurationRelease{Release: rel, ActivationRevision: rev, PreviousVersion: currentVersion}
		changed = true
		return nil
	})
	return out, changed, err
}

func validateReleasePinsTx(tx *gorm.DB, releaseID int64) error {
	type pinRow struct {
		Alias                     string
		Kind                      string
		PinnedContentType         string
		PinnedParameterDigest     string
		OwnerNamespaceID          int64
		OwnerEnv                  string
		OwnerApp                  string
		StoredResourceNamespaceID int64
		StoredResourceEnv         string
		StoredResourceApp         string
		ResourceNamespaceID       sql.NullInt64
		ParameterVersionID        sql.NullInt64
		ParameterValue            sql.NullString
		ParameterContentType      sql.NullString
		SecretVersionID           sql.NullInt64
		SecretState               sql.NullString
		SecretDestroyedAt         sql.NullString
		SecretExpiresAt           sql.NullString
		SecretContentType         sql.NullString
	}
	var pins []pinRow
	err := tx.Raw(`SELECT e.alias, e.kind,
			e.content_type AS pinned_content_type,
			e.parameter_digest AS pinned_parameter_digest,
			r.namespace_id AS owner_namespace_id,
			rn.env AS owner_env,
			rn.app AS owner_app,
			e.resource_namespace_id AS stored_resource_namespace_id,
			e.resource_env AS stored_resource_env,
			e.resource_app AS stored_resource_app,
			n.id AS resource_namespace_id,
			pv.id AS parameter_version_id,
			pv.value AS parameter_value,
			pv.content_type AS parameter_content_type,
			sv.id AS secret_version_id,
			sv.state AS secret_state,
			sv.destroyed_at AS secret_destroyed_at,
			sv.expires_at AS secret_expires_at,
			sv.content_type AS secret_content_type
		FROM configuration_release_entries e
		JOIN configuration_releases r ON r.id = e.release_id
		JOIN namespaces rn ON rn.id = r.namespace_id
		LEFT JOIN namespaces n
			ON n.id = e.resource_namespace_id
			AND n.env = e.resource_env AND n.app = e.resource_app
		LEFT JOIN parameters p
			ON e.kind = ? AND p.namespace_id = n.id AND p.name = e.resource_key
		LEFT JOIN parameter_versions pv
			ON pv.parameter_id = p.id AND pv.version_number = e.resource_version
		LEFT JOIN secrets s
			ON e.kind = ? AND s.namespace_id = n.id AND s.name = e.resource_key
		LEFT JOIN secret_versions sv
			ON sv.secret_id = s.id AND sv.version_number = e.resource_version
		WHERE e.release_id = ?
		ORDER BY e.alias`, domain.ReleaseEntryParameter, domain.ReleaseEntrySecret, releaseID).Scan(&pins).Error
	if err != nil {
		return err
	}
	for _, pin := range pins {
		if pin.StoredResourceNamespaceID != pin.OwnerNamespaceID ||
			pin.StoredResourceEnv != pin.OwnerEnv || pin.StoredResourceApp != pin.OwnerApp {
			return releasePinValidationError(pin.Alias, domain.ReleaseValidationUnreadable, "release entry resource namespace does not match its owner")
		}
		if !pin.ResourceNamespaceID.Valid {
			return releasePinValidationError(pin.Alias, domain.ReleaseValidationNotFound, "release entry references a missing or replaced namespace")
		}
		switch pin.Kind {
		case domain.ReleaseEntryParameter:
			if !pin.ParameterVersionID.Valid {
				return releasePinValidationError(pin.Alias, domain.ReleaseValidationNotFound, "release entry references a missing parameter version")
			}
			if !pin.ParameterContentType.Valid || pin.ParameterContentType.String != pin.PinnedContentType {
				return releasePinValidationError(pin.Alias, domain.ReleaseValidationContentType, "parameter content type does not match release pin")
			}
			if pin.PinnedParameterDigest != "" {
				digest := sha256.Sum256([]byte(pin.ParameterValue.String))
				if !pin.ParameterValue.Valid || hex.EncodeToString(digest[:]) != pin.PinnedParameterDigest {
					return releasePinValidationError(pin.Alias, domain.ReleaseValidationDigest, "parameter digest does not match release pin")
				}
			}
		case domain.ReleaseEntrySecret:
			if !pin.SecretVersionID.Valid {
				return releasePinValidationError(pin.Alias, domain.ReleaseValidationNotFound, "release entry references a missing secret version")
			}
			if !pin.SecretState.Valid || pin.SecretState.String != domain.StateEnabled || pin.SecretDestroyedAt.Valid {
				return releasePinValidationError(pin.Alias, domain.ReleaseValidationUnreadable, "secret version is not readable")
			}
			if pin.SecretExpiresAt.Valid {
				expiresAt := parseTime(pin.SecretExpiresAt.String)
				if expiresAt.IsZero() || time.Now().After(expiresAt) {
					return releasePinValidationError(pin.Alias, domain.ReleaseValidationUnreadable, "secret version is expired")
				}
			}
			if !pin.SecretContentType.Valid || pin.SecretContentType.String != pin.PinnedContentType {
				return releasePinValidationError(pin.Alias, domain.ReleaseValidationContentType, "secret content type does not match release pin")
			}
		default:
			return releasePinValidationError(pin.Alias, domain.ReleaseValidationUnreadable, "release entry kind is invalid")
		}
	}
	return nil
}

func releasePinValidationError(alias, code, message string) error {
	return domain.NewReleaseValidationFailedError([]domain.ReleaseValidationError{{Alias: alias, Code: code, Message: message}})
}

func (s *SQLStore) CreateConfigurationSchema(ctx context.Context, schema domain.ConfigurationSchema) (domain.ConfigurationSchema, error) {
	var out domain.ConfigurationSchema
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		out, err = createConfigurationSchemaTx(tx, schema)
		return err
	})
	return out, err
}

func createConfigurationSchemaTx(tx *gorm.DB, schema domain.ConfigurationSchema) (domain.ConfigurationSchema, error) {
	var app applicationModel
	if err := tx.Where("name = ?", schema.Application).First(&app).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ConfigurationSchema{}, domain.Errorf(domain.ErrNotFound, "application %s", schema.Application)
		}
		return domain.ConfigurationSchema{}, err
	}
	if app.ArchivedAt != nil {
		return domain.ConfigurationSchema{}, domain.Errorf(domain.ErrFailedPrecondition, "application %s is archived", schema.Application)
	}
	if schema.ReleaseName != app.ReleaseName {
		return domain.ConfigurationSchema{}, domain.Errorf(domain.ErrFailedPrecondition,
			"application %s owns schemas under release %q", schema.Application, app.ReleaseName)
	}
	var max int64
	if err := tx.Model(&configurationSchemaModel{}).
		Where("application_name = ? AND release_name = ?", schema.Application, schema.ReleaseName).
		Select("COALESCE(MAX(version_number), 0)").Scan(&max).Error; err != nil {
		return domain.ConfigurationSchema{}, err
	}
	now := nowUTC()
	m := configurationSchemaModel{
		ApplicationName: schema.Application, ReleaseName: schema.ReleaseName,
		VersionNumber: max + 1, SchemaJSON: schema.Schema, Digest: schema.Digest,
		MetadataJSON: zeroOr(schema.Metadata, "{}"), CreatedBy: schema.CreatedBy, CreatedAt: fmtTime(now),
	}
	if err := tx.Omit(clause.Associations).Create(&m).Error; err != nil {
		if isUniqueErr(err) {
			return domain.ConfigurationSchema{}, domain.Errorf(domain.ErrAlreadyExists,
				"configuration schema %s/%s with this digest", schema.Application, schema.ReleaseName)
		}
		return domain.ConfigurationSchema{}, err
	}
	schema.Version = uint64(m.VersionNumber)
	schema.Metadata = m.MetadataJSON
	schema.CreatedAt = now
	return schema, nil
}

func (s *SQLStore) GetConfigurationSchema(ctx context.Context, application, releaseName string, version uint64) (domain.ConfigurationSchema, error) {
	var m configurationSchemaModel
	q := s.db.WithContext(ctx).Where("application_name = ? AND release_name = ?", application, releaseName)
	if version == 0 {
		q = q.Order("version_number DESC")
	} else {
		q = q.Where("version_number = ?", version)
	}
	if err := q.First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ConfigurationSchema{}, domain.Errorf(domain.ErrNotFound, "configuration schema %s/%s version %d", application, releaseName, version)
		}
		return domain.ConfigurationSchema{}, err
	}
	return schemaFromModel(m), nil
}

func schemaFromModel(m configurationSchemaModel) domain.ConfigurationSchema {
	return domain.ConfigurationSchema{Application: m.ApplicationName, ReleaseName: m.ReleaseName, Version: uint64(m.VersionNumber), Schema: m.SchemaJSON, Digest: m.Digest, Metadata: m.MetadataJSON, CreatedBy: m.CreatedBy, CreatedAt: parseTime(m.CreatedAt)}
}

func (s *SQLStore) ListConfigurationSchemas(ctx context.Context, application, releaseName string, page ListPage) ([]domain.ConfigurationSchema, string, error) {
	if application == "" && releaseName != "" {
		return nil, "", domain.Errorf(domain.ErrInvalidArgument, "application is required when release_name is set")
	}
	limit := clampLimit(page.Limit)
	cursor, err := decodeConfigurationSchemaCursor(page.Token)
	if err != nil {
		return nil, "", err
	}
	q := s.db.WithContext(ctx)
	if application != "" {
		q = q.Where("application_name = ?", application)
	}
	if releaseName != "" {
		q = q.Where("release_name = ?", releaseName)
	}
	if cursor.Application != "" {
		q = q.Where("application_name < ? OR (application_name = ? AND release_name < ?) OR (application_name = ? AND release_name = ? AND version_number < ?)",
			cursor.Application, cursor.Application, cursor.ReleaseName, cursor.Application, cursor.ReleaseName, cursor.Version)
	}
	var rows []configurationSchemaModel
	if err := q.Order("application_name DESC, release_name DESC, version_number DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next, err = encodeConfigurationSchemaCursor(configurationSchemaCursor{Application: last.ApplicationName, ReleaseName: last.ReleaseName, Version: last.VersionNumber})
		if err != nil {
			return nil, "", err
		}
	}
	out := make([]domain.ConfigurationSchema, len(rows))
	for i, m := range rows {
		out[i] = schemaFromModel(m)
	}
	return out, next, nil
}

type configurationSchemaCursor struct {
	Application string `json:"application"`
	ReleaseName string `json:"release_name"`
	Version     int64  `json:"version"`
}

func encodeConfigurationSchemaCursor(cursor configurationSchemaCursor) (string, error) {
	b, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return encodeToken(string(b)), nil
}

func decodeConfigurationSchemaCursor(token string) (configurationSchemaCursor, error) {
	raw, err := decodeToken(token)
	if err != nil || raw == "" {
		return configurationSchemaCursor{}, err
	}
	var cursor configurationSchemaCursor
	if err := json.Unmarshal([]byte(raw), &cursor); err != nil || cursor.Application == "" || cursor.ReleaseName == "" || cursor.Version <= 0 {
		return configurationSchemaCursor{}, domain.Errorf(domain.ErrInvalidArgument, "invalid page token")
	}
	return cursor, nil
}

func (s *SQLStore) UpsertReleaseAcknowledgement(ctx context.Context, ack domain.ReleaseAcknowledgement) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		nsID, err := resolveNamespaceID(tx, ack.Namespace)
		if err != nil {
			return err
		}
		// Bind the acknowledgement to the exact live stream generation. The
		// transaction shares SQLite's immediate writer lock with disconnects, so
		// either the acknowledgement commits first and the disconnect clears its
		// liveness, or the stale acknowledgement observes the disconnect/new
		// generation and aborts without resurrecting state.
		var connection releaseSubscriberConnectionModel
		err = tx.Where(
			"namespace_id = ? AND release_name = ? AND client_name = ? AND instance_id = ? AND identity = ?",
			nsID, ack.ReleaseName, ack.ClientName, ack.InstanceID, ack.Identity,
		).First(&connection).Error
		if errors.Is(err, gorm.ErrRecordNotFound) || err == nil && (connection.Connected == 0 || connection.ConnectionID != ack.ConnectionID) {
			return domain.Errorf(domain.ErrAborted, "release subscriber connection changed; retry on the active stream")
		}
		if err != nil {
			return err
		}

		m := releaseSubscriberStateModel{NamespaceID: nsID, ReleaseName: ack.ReleaseName, ClientName: ack.ClientName, InstanceID: ack.InstanceID, State: ack.State, Identity: ack.Identity, ReleaseVersion: int64(ack.ReleaseVersion), ActivationRevision: int64(ack.ActivationRevision), RejectionCategory: ack.RejectionCategory, Diagnostic: ack.Diagnostic, ClientTimestamp: fmtTime(ack.ClientTimestamp), ServerTimestamp: fmtTime(ack.ServerTimestamp), Connected: 1, AppliedDivergent: b2i(ack.AppliedDivergent), DivergentFieldCount: int64(ack.DivergentFieldCount)}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "namespace_id"}, {Name: "release_name"}, {Name: "client_name"}, {Name: "instance_id"}, {Name: "identity"}, {Name: "state"}},
			DoUpdates: clause.AssignmentColumns([]string{"release_version", "activation_revision", "rejection_category", "diagnostic", "client_timestamp", "server_timestamp", "connected", "disconnected_at", "applied_divergent", "divergent_field_count"}),
			Where:     clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "excluded.activation_revision > release_subscriber_states.activation_revision OR (excluded.activation_revision = release_subscriber_states.activation_revision AND excluded.server_timestamp >= release_subscriber_states.server_timestamp)"}}},
		}).Create(&m).Error
	})
}

func (s *SQLStore) ListReleaseAcknowledgements(ctx context.Context, ns domain.NamespaceRef, name string, page ListPage) ([]domain.ReleaseAcknowledgement, string, error) {
	limit := clampLimit(page.Limit)
	cursor, err := decodeReleaseAcknowledgementCursor(page.Token)
	if err != nil {
		return nil, "", err
	}
	type acknowledgementRow struct {
		ReleaseName         string
		ReleaseVersion      int64
		ActivationRevision  int64
		ClientName          string
		InstanceID          string
		Identity            string
		State               string
		RejectionCategory   string
		Diagnostic          string
		ClientTimestamp     string
		ServerTimestamp     string
		Connected           int64
		AppliedDivergent    int64
		DivergentFieldCount int64
	}
	query := `WITH subscriber_rows AS (
		SELECT s.release_name, s.release_version, s.activation_revision,
			s.client_name, s.instance_id, s.identity, s.state,
			s.rejection_category, s.diagnostic, s.client_timestamp,
			s.server_timestamp, COALESCE(c.connected, s.connected) AS connected,
			s.applied_divergent, s.divergent_field_count
		FROM release_subscriber_states s
		LEFT JOIN release_subscriber_connections c
			ON c.namespace_id = s.namespace_id
			AND c.release_name = s.release_name
			AND c.client_name = s.client_name
			AND c.instance_id = s.instance_id
			AND c.identity = s.identity
		WHERE s.namespace_id = ? AND (? = '' OR s.release_name = ?)
		UNION ALL
		SELECT c.release_name, 0, 0, c.client_name, c.instance_id,
			c.identity, '', '', '', '', c.server_timestamp, c.connected, 0, 0
		FROM release_subscriber_connections c
		WHERE c.namespace_id = ? AND (? = '' OR c.release_name = ?)
			AND NOT EXISTS (
				SELECT 1 FROM release_subscriber_states s
				WHERE s.namespace_id = c.namespace_id
					AND s.release_name = c.release_name
					AND s.client_name = c.client_name
					AND s.instance_id = c.instance_id
					AND s.identity = c.identity
			)
	)
	SELECT * FROM subscriber_rows`
	var out []domain.ReleaseAcknowledgement
	next := ""
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		nsID, err := resolveNamespaceID(tx, ns)
		if err != nil {
			return err
		}
		queryText := query
		args := []any{nsID, name, name, nsID, name, name}
		if cursor.ServerTimestamp != "" {
			queryText += ` WHERE server_timestamp < ? OR
			(server_timestamp = ? AND (release_name, client_name, instance_id, identity, state) > (?, ?, ?, ?, ?))`
			args = append(args, cursor.ServerTimestamp, cursor.ServerTimestamp, cursor.ReleaseName, cursor.ClientName, cursor.InstanceID, cursor.Identity, cursor.State)
		}
		queryText += ` ORDER BY server_timestamp DESC, release_name ASC, client_name ASC, instance_id ASC, identity ASC, state ASC LIMIT ?`
		args = append(args, limit+1)
		var rows []acknowledgementRow
		if err := tx.Raw(queryText, args...).Scan(&rows).Error; err != nil {
			return err
		}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
		}
		out = make([]domain.ReleaseAcknowledgement, 0, len(rows))
		for _, row := range rows {
			out = append(out, domain.ReleaseAcknowledgement{Namespace: ns, ReleaseName: row.ReleaseName, ReleaseVersion: uint64(row.ReleaseVersion), ActivationRevision: uint64(row.ActivationRevision), ClientName: row.ClientName, InstanceID: row.InstanceID, Identity: row.Identity, State: row.State, RejectionCategory: row.RejectionCategory, Diagnostic: row.Diagnostic, ClientTimestamp: parseTime(row.ClientTimestamp), ServerTimestamp: parseTime(row.ServerTimestamp), Connected: i2b(row.Connected), AppliedDivergent: i2b(row.AppliedDivergent), DivergentFieldCount: uint32(row.DivergentFieldCount)})
		}
		if hasMore {
			last := rows[len(rows)-1]
			next, err = encodeReleaseAcknowledgementCursor(releaseAcknowledgementCursor{ServerTimestamp: last.ServerTimestamp, ReleaseName: last.ReleaseName, ClientName: last.ClientName, InstanceID: last.InstanceID, Identity: last.Identity, State: last.State})
			return err
		}
		return nil
	}, &sql.TxOptions{ReadOnly: true})
	return out, next, err
}

type releaseAcknowledgementCursor struct {
	Version         int    `json:"v"`
	ServerTimestamp string `json:"server_timestamp"`
	ReleaseName     string `json:"release_name"`
	ClientName      string `json:"client_name"`
	InstanceID      string `json:"instance_id"`
	Identity        string `json:"identity"`
	State           string `json:"state"`
}

func encodeReleaseAcknowledgementCursor(cursor releaseAcknowledgementCursor) (string, error) {
	cursor.Version = 2
	b, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return encodeToken(string(b)), nil
}

func decodeReleaseAcknowledgementCursor(token string) (releaseAcknowledgementCursor, error) {
	raw, err := decodeToken(token)
	if err != nil || raw == "" {
		return releaseAcknowledgementCursor{}, err
	}
	var cursor releaseAcknowledgementCursor
	if err := json.Unmarshal([]byte(raw), &cursor); err != nil || cursor.Version != 2 || cursor.ServerTimestamp == "" {
		return releaseAcknowledgementCursor{}, domain.Errorf(domain.ErrInvalidArgument, "invalid page token")
	}
	return cursor, nil
}

func (s *SQLStore) SetReleaseInstanceConnected(ctx context.Context, connection domain.ReleaseSubscriberConnection) error {
	at := fmtTime(connection.ServerTimestamp)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		nsID, err := resolveNamespaceID(tx, connection.Namespace)
		if err != nil {
			return err
		}
		stateScope := tx.Model(&releaseSubscriberStateModel{}).Where("namespace_id = ? AND release_name = ? AND client_name = ? AND instance_id = ? AND identity = ?", nsID, connection.ReleaseName, connection.ClientName, connection.InstanceID, connection.Identity)
		if connection.Connected {
			model := releaseSubscriberConnectionModel{NamespaceID: nsID, ReleaseName: connection.ReleaseName, ClientName: connection.ClientName, InstanceID: connection.InstanceID, Identity: connection.Identity, ConnectionID: connection.ConnectionID, Connected: 1, ConnectedAt: at, ServerTimestamp: at}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "namespace_id"}, {Name: "release_name"}, {Name: "client_name"}, {Name: "instance_id"}, {Name: "identity"}}, DoUpdates: clause.AssignmentColumns([]string{"connection_id", "connected", "connected_at", "disconnected_at", "server_timestamp"})}).Create(&model).Error; err != nil {
				return err
			}
			return stateScope.Updates(map[string]any{"connected": 1, "disconnected_at": nil}).Error
		}
		res := tx.Model(&releaseSubscriberConnectionModel{}).Where("namespace_id = ? AND release_name = ? AND client_name = ? AND instance_id = ? AND identity = ? AND connection_id = ?", nsID, connection.ReleaseName, connection.ClientName, connection.InstanceID, connection.Identity, connection.ConnectionID).Updates(map[string]any{"connected": 0, "disconnected_at": at, "server_timestamp": at})
		if res.Error != nil || res.RowsAffected == 0 {
			return res.Error
		}
		return stateScope.Updates(map[string]any{"connected": 0, "disconnected_at": at}).Error
	})
}

func (s *SQLStore) ResetReleaseInstanceConnections(ctx context.Context, at time.Time) error {
	stamp := fmtTime(at)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&releaseSubscriberConnectionModel{}).Where("connected = 1").Updates(map[string]any{"connected": 0, "disconnected_at": stamp, "server_timestamp": stamp}).Error; err != nil {
			return err
		}
		return tx.Model(&releaseSubscriberStateModel{}).Where("connected = 1").Updates(map[string]any{"connected": 0, "disconnected_at": stamp}).Error
	})
}

func (s *SQLStore) FindProtectedReleaseReference(ctx context.Context, ref domain.Ref, kind string, version uint64) (ReleaseReference, error) {
	return findProtectedReleaseReference(s.db.WithContext(ctx), ref, kind, version)
}

func findProtectedReleaseReference(db *gorm.DB, ref domain.Ref, kind string, version uint64) (ReleaseReference, error) {
	resourceNamespaceID, err := resolveNamespaceID(db, ref.NS)
	if err != nil {
		return ReleaseReference{}, err
	}
	q := db.Table("configuration_release_entries e").Select("n.env,n.app,r.name AS release_name,r.version_number,e.alias").Joins("JOIN configuration_releases r ON r.id=e.release_id").Joins("JOIN namespaces n ON n.id=r.namespace_id").Joins("JOIN configuration_release_labels l ON l.namespace_id=r.namespace_id AND l.release_name=r.name AND l.version_number=r.version_number AND l.label IN (?,?)", domain.LabelCurrent, domain.LabelPrevious).Where("e.kind=? AND e.resource_key=? AND e.resource_namespace_id=? AND e.resource_env=? AND e.resource_app=?", kind, ref.Key, resourceNamespaceID, ref.NS.Env, ref.NS.App)
	if version > 0 {
		q = q.Where("e.resource_version=?", version)
	}
	var row struct {
		Env, App, ReleaseName, Alias string
		VersionNumber                int64
	}
	res := q.Order("CASE l.label WHEN 'current' THEN 0 ELSE 1 END").Limit(1).Scan(&row)
	if res.Error != nil {
		return ReleaseReference{}, res.Error
	}
	if res.RowsAffected == 0 {
		return ReleaseReference{}, domain.ErrNotFound
	}
	return ReleaseReference{Namespace: domain.NamespaceRef{Env: row.Env, App: row.App}, ReleaseName: row.ReleaseName, ReleaseVersion: uint64(row.VersionNumber), Alias: row.Alias}, nil
}

func rejectProtectedReleaseReference(db *gorm.DB, ref domain.Ref, kind string, version uint64) error {
	reference, err := findProtectedReleaseReference(db, ref, kind, version)
	if err == nil {
		return protectedError(reference)
	}
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	return err
}

func protectedError(rr ReleaseReference) error {
	return domain.Errorf(domain.ErrFailedPrecondition, "resource is referenced by configuration release %s/%s version %d alias %q", rr.Namespace, rr.ReleaseName, rr.ReleaseVersion, rr.Alias)
}

func (s *SQLStore) PruneConfigurationReleases(ctx context.Context, retainDuration time.Duration, retainVersions int) (int, error) {
	if retainDuration <= 0 || retainVersions <= 0 {
		return 0, nil
	}
	cutoff := fmtTime(time.Now().Add(-retainDuration))
	var deleted int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Activation identities outlive the shorter generic changelog retention so
		// reconnecting clients can retry acknowledgements. Bound them to the same
		// retention window as immutable release history, while always preserving
		// the current/previous label activations.
		if err := tx.Exec(`DELETE FROM configuration_release_activations
			WHERE activated_at < ?
			AND NOT EXISTS (
				SELECT 1 FROM configuration_release_labels l
				WHERE l.namespace_id=configuration_release_activations.namespace_id
				AND l.release_name=configuration_release_activations.release_name
				AND l.activation_revision=configuration_release_activations.revision
			)`, cutoff).Error; err != nil {
			return err
		}
		res := tx.Exec(`DELETE FROM configuration_releases
			WHERE created_at < ?
			AND id NOT IN (SELECT r.id FROM configuration_releases r JOIN configuration_release_labels l ON l.namespace_id=r.namespace_id AND l.release_name=r.name AND l.version_number=r.version_number)
			AND id NOT IN (
				SELECT id FROM (
					SELECT r.id, ROW_NUMBER() OVER (PARTITION BY r.namespace_id,r.name ORDER BY r.version_number DESC) AS rn
					FROM configuration_releases r
					WHERE NOT EXISTS (
						SELECT 1 FROM configuration_release_labels l
						WHERE l.namespace_id=r.namespace_id AND l.release_name=r.name AND l.version_number=r.version_number
					)
				) WHERE rn <= ?
			)
			AND NOT EXISTS (SELECT 1 FROM configuration_release_activations a WHERE a.namespace_id=configuration_releases.namespace_id AND a.release_name=configuration_releases.name AND a.version_number=configuration_releases.version_number)
			AND NOT EXISTS (SELECT 1 FROM change_log c WHERE c.resource_type=? AND c.namespace_id=configuration_releases.namespace_id AND c.key=configuration_releases.name AND c.version_number=configuration_releases.version_number AND c.change_type='activate')`, cutoff, retainVersions, domain.ResourceConfigurationRelease)
		deleted = res.RowsAffected
		return res.Error
	})
	return int(deleted), err
}

func (s *SQLStore) PruneReleaseAcknowledgements(ctx context.Context, disconnectedBefore time.Time) (int, error) {
	removed := int64(0)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Where("connected = 0 AND disconnected_at IS NOT NULL AND disconnected_at < ?", fmtTime(disconnectedBefore)).Delete(&releaseSubscriberStateModel{})
		if res.Error != nil {
			return res.Error
		}
		removed += res.RowsAffected
		res = tx.Where("connected = 0 AND disconnected_at IS NOT NULL AND disconnected_at < ?", fmtTime(disconnectedBefore)).Delete(&releaseSubscriberConnectionModel{})
		removed += res.RowsAffected
		return res.Error
	})
	return int(removed), err
}
