package storage

import (
	"context"

	"github.com/Suhaibinator/kms/internal/domain"
)

// CreateNamespace stores a namespace.
func (s *SQLStore) CreateNamespace(ctx context.Context, ns domain.Namespace) (domain.Namespace, error) {
	created := ns.CreatedAt
	if created.IsZero() {
		created = nowUTC()
	}
	m := namespaceModel{
		Path:        ns.Path,
		Description: ns.Description,
		CreatedBy:   ns.CreatedBy,
		CreatedAt:   fmtTime(created),
	}
	if err := s.db.WithContext(ctx).Create(&m).Error; err != nil {
		if isUniqueErr(err) {
			return domain.Namespace{}, domain.Errorf(domain.ErrAlreadyExists, "namespace %s", ns.Path)
		}
		return domain.Namespace{}, err
	}
	return toNamespace(m), nil
}

// ListNamespaces returns namespaces ordered by path.
func (s *SQLStore) ListNamespaces(ctx context.Context, page ListPage) ([]domain.Namespace, string, error) {
	limit := clampLimit(page.Limit)
	after, err := decodeToken(page.Token)
	if err != nil {
		return nil, "", err
	}
	q := s.db.WithContext(ctx).Model(&namespaceModel{})
	if after != "" {
		q = q.Where("path > ?", after)
	}
	var rows []namespaceModel
	if err := q.Order("path ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = encodeToken(rows[len(rows)-1].Path)
	}
	out := make([]domain.Namespace, 0, len(rows))
	for _, m := range rows {
		out = append(out, toNamespace(m))
	}
	return out, next, nil
}
