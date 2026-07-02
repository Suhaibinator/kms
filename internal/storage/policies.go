package storage

import (
	"context"
	"encoding/json"

	"github.com/Suhaibinator/kms/internal/domain"
)

// rulesJSON is the on-disk shape of policies.rules_json.
type rulesJSON struct {
	Allow []domain.PolicyRule `json:"allow"`
	Deny  []domain.PolicyRule `json:"deny"`
}

func marshalRules(p domain.Policy) (string, error) {
	b, err := json.Marshal(rulesJSON{Allow: p.Allow, Deny: p.Deny})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func policyFromModel(m policyModel) (domain.Policy, error) {
	var r rulesJSON
	if m.RulesJSON != "" {
		if err := json.Unmarshal([]byte(m.RulesJSON), &r); err != nil {
			return domain.Policy{}, err
		}
	}
	return domain.Policy{
		Name:      m.Name,
		Subject:   m.Subject,
		Allow:     r.Allow,
		Deny:      r.Deny,
		CreatedAt: parseTime(m.CreatedAt),
		UpdatedAt: parseTime(m.UpdatedAt),
	}, nil
}

// CreatePolicy stores a new policy.
func (s *SQLStore) CreatePolicy(ctx context.Context, p domain.Policy) (domain.Policy, error) {
	rules, err := marshalRules(p)
	if err != nil {
		return domain.Policy{}, domain.Errorf(domain.ErrInvalidArgument, "marshal policy rules: %v", err)
	}
	now := fmtTime(nowUTC())
	m := policyModel{Name: p.Name, Subject: p.Subject, RulesJSON: rules, CreatedAt: now, UpdatedAt: now}
	if err := s.db.WithContext(ctx).Create(&m).Error; err != nil {
		if isUniqueErr(err) {
			return domain.Policy{}, domain.Errorf(domain.ErrAlreadyExists, "policy %s", p.Name)
		}
		return domain.Policy{}, err
	}
	return policyFromModel(m)
}

// UpdatePolicy replaces an existing policy's subject and rules.
func (s *SQLStore) UpdatePolicy(ctx context.Context, p domain.Policy) (domain.Policy, error) {
	rules, err := marshalRules(p)
	if err != nil {
		return domain.Policy{}, domain.Errorf(domain.ErrInvalidArgument, "marshal policy rules: %v", err)
	}
	now := fmtTime(nowUTC())
	res := s.db.WithContext(ctx).Model(&policyModel{}).Where("name = ?", p.Name).Updates(map[string]any{
		"subject":    p.Subject,
		"rules_json": rules,
		"updated_at": now,
	})
	if res.Error != nil {
		return domain.Policy{}, res.Error
	}
	if res.RowsAffected == 0 {
		return domain.Policy{}, domain.Errorf(domain.ErrNotFound, "policy %s", p.Name)
	}
	var m policyModel
	if err := s.db.WithContext(ctx).Where("name = ?", p.Name).First(&m).Error; err != nil {
		return domain.Policy{}, err
	}
	return policyFromModel(m)
}

// DeletePolicy removes a policy by name.
func (s *SQLStore) DeletePolicy(ctx context.Context, name string) error {
	res := s.db.WithContext(ctx).Where("name = ?", name).Delete(&policyModel{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return domain.Errorf(domain.ErrNotFound, "policy %s", name)
	}
	return nil
}

// ListPolicies returns policies ordered by name.
func (s *SQLStore) ListPolicies(ctx context.Context, page ListPage) ([]domain.Policy, string, error) {
	limit := clampLimit(page.Limit)
	after, err := decodeToken(page.Token)
	if err != nil {
		return nil, "", err
	}
	q := s.db.WithContext(ctx).Model(&policyModel{})
	if after != "" {
		q = q.Where("name > ?", after)
	}
	var rows []policyModel
	if err := q.Order("name ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = encodeToken(rows[len(rows)-1].Name)
	}
	out := make([]domain.Policy, 0, len(rows))
	for _, m := range rows {
		p, err := policyFromModel(m)
		if err != nil {
			return nil, "", err
		}
		out = append(out, p)
	}
	return out, next, nil
}

// PoliciesForSubject returns all policies whose subject is the given identity
// name or "*".
func (s *SQLStore) PoliciesForSubject(ctx context.Context, subject string) ([]domain.Policy, error) {
	var rows []policyModel
	if err := s.db.WithContext(ctx).Where("subject = ? OR subject = ?", subject, "*").
		Order("name ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]domain.Policy, 0, len(rows))
	for _, m := range rows {
		p, err := policyFromModel(m)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}
