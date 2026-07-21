package storage

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/Suhaibinator/kms/internal/domain"
)

// CreateNamespace stores a namespace. An empty AllowedAuthMethods set defaults
// to ["mtls"] — the strongest posture.
func (s *SQLStore) CreateNamespace(ctx context.Context, ns domain.Namespace) (domain.Namespace, error) {
	created := ns.CreatedAt
	if created.IsZero() {
		created = nowUTC()
	}
	m := namespaceModel{
		Env:                ns.Env,
		App:                ns.App,
		Description:        ns.Description,
		AllowedAuthMethods: marshalAuthMethods(ns.AllowedAuthMethods),
		CreatedBy:          ns.CreatedBy,
		CreatedAt:          fmtTime(created),
	}
	if err := s.db.WithContext(ctx).Create(&m).Error; err != nil {
		if isUniqueErr(err) {
			return domain.Namespace{}, domain.Errorf(domain.ErrAlreadyExists, "namespace %s", ns.NamespaceRef)
		}
		return domain.Namespace{}, err
	}
	return toNamespace(m), nil
}

// GetNamespace returns the namespace addressed by ref.
func (s *SQLStore) GetNamespace(ctx context.Context, ref domain.NamespaceRef) (domain.Namespace, error) {
	var m namespaceModel
	q := s.db.WithContext(ctx).Where("env = ? AND app = ?", ref.Env, ref.App)
	expectedID, bound := ExpectedNamespaceIncarnation(ctx, ref)
	if bound {
		q = q.Where("id = ?", expectedID)
	}
	if err := q.First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if bound {
				return domain.Namespace{}, domain.Errorf(domain.ErrAborted, "namespace %s changed during request; retry", ref)
			}
			return domain.Namespace{}, domain.Errorf(domain.ErrNotFound, "namespace %s", ref)
		}
		return domain.Namespace{}, err
	}
	return toNamespace(m), nil
}

// UpdateNamespace replaces a namespace's description and allowed auth methods
// (full replace of the method set). An empty method set defaults to ["mtls"].
func (s *SQLStore) UpdateNamespace(ctx context.Context, ref domain.NamespaceRef, description string, methods []domain.AuthMethod) (domain.Namespace, error) {
	db := s.db.WithContext(ctx)
	id, err := resolveNamespaceID(db, ref)
	if err != nil {
		return domain.Namespace{}, err
	}
	res := db.Model(&namespaceModel{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"description":          description,
			"allowed_auth_methods": marshalAuthMethods(methods),
		})
	if res.Error != nil {
		return domain.Namespace{}, res.Error
	}
	if res.RowsAffected == 0 {
		return domain.Namespace{}, domain.Errorf(domain.ErrAborted, "namespace %s changed during request; retry", ref)
	}
	return s.GetNamespace(ctx, ref)
}

// DeleteNamespace removes an empty namespace. Emptiness is verified inside the
// transaction: any remaining parameter, secret, or bound identity fails the
// delete with domain.ErrFailedPrecondition. A secrets store must not offer a
// recursive delete of live secrets.
func (s *SQLStore) DeleteNamespace(ctx context.Context, ref domain.NamespaceRef) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		id, err := resolveNamespaceID(tx, ref)
		if err != nil {
			return err
		}
		for _, check := range []struct {
			table, what string
		}{
			{"parameters", "parameters"},
			{"secrets", "secrets"},
			{"identities", "bound identities"},
			{"configuration_releases", "configuration releases"},
			{"configuration_release_labels", "configuration release labels"},
			{"release_subscriber_states", "configuration release subscriber states"},
			{"release_subscriber_connections", "configuration release subscriber connections"},
		} {
			var n int64
			if err := tx.Table(check.table).Where("namespace_id = ?", id).Count(&n).Error; err != nil {
				return err
			}
			if n > 0 {
				return domain.Errorf(domain.ErrFailedPrecondition,
					"namespace %s is not empty (%d %s)", ref, n, check.what)
			}
		}
		res := tx.Where("id = ?", id).Delete(&namespaceModel{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return domain.Errorf(domain.ErrNotFound, "namespace %s", ref)
		}
		return nil
	})
}

// ListNamespaces returns namespaces ordered by (env, app), each with its
// parameter and secret counts (cheap COUNT subqueries powering the dashboard).
func (s *SQLStore) ListNamespaces(ctx context.Context, page ListPage) ([]domain.Namespace, string, error) {
	limit := clampLimit(page.Limit)
	after, err := decodeToken(page.Token)
	if err != nil {
		return nil, "", err
	}
	// Flat row shape: namespace columns plus the two counts. A struct embedding
	// namespaceModel would be treated by GORM as a belongs-to association (the
	// embedded type has its own table/PK), not as flattened columns, so the
	// fields are listed explicitly here.
	type nsCountRow struct {
		ID                 int64
		Env                string
		App                string
		Description        string
		AllowedAuthMethods string
		CreatedBy          string
		CreatedAt          string
		ParameterCount     int64
		SecretCount        int64
	}
	q := s.db.WithContext(ctx).Table("namespaces AS n").
		Select("n.id, n.env, n.app, n.description, n.allowed_auth_methods, n.created_by, n.created_at, " +
			"(SELECT COUNT(*) FROM parameters p WHERE p.namespace_id = n.id) AS parameter_count, " +
			"(SELECT COUNT(*) FROM secrets s WHERE s.namespace_id = n.id) AS secret_count")
	if after != "" {
		// Keyset pagination over the (env, app) UNIQUE key, encoded as "env/app".
		env, app, _ := splitNamespaceToken(after)
		q = q.Where("(n.env > ?) OR (n.env = ? AND n.app > ?)", env, env, app)
	}
	var rows []nsCountRow
	if err := q.Order("n.env ASC, n.app ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = encodeToken(last.Env + "/" + last.App)
	}
	out := make([]domain.Namespace, 0, len(rows))
	for _, r := range rows {
		ns := toNamespace(namespaceModel{
			ID:                 r.ID,
			Env:                r.Env,
			App:                r.App,
			Description:        r.Description,
			AllowedAuthMethods: r.AllowedAuthMethods,
			CreatedBy:          r.CreatedBy,
			CreatedAt:          r.CreatedAt,
		})
		ns.ParameterCount = uint64(r.ParameterCount)
		ns.SecretCount = uint64(r.SecretCount)
		out = append(out, ns)
	}
	return out, next, nil
}

// splitNamespaceToken splits an "env/app" page token. app may not contain '/'
// (env/app are validated to [a-z0-9-]), so a single split is unambiguous.
func splitNamespaceToken(tok string) (env, app string, ok bool) {
	for i := 0; i < len(tok); i++ {
		if tok[i] == '/' {
			return tok[:i], tok[i+1:], true
		}
	}
	return tok, "", false
}

// NamespacePageToken returns the storage cursor immediately after ref. The
// service uses it when authorization filtering happens above storage so a
// response cursor never names a filtered-out namespace.
func NamespacePageToken(ref domain.NamespaceRef) string {
	return encodeToken(ref.String())
}
