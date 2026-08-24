package core

import (
	"context"
	"errors"
	"strconv"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
)

// CloneApplicationEnvironment creates (or attaches) a target environment for
// an application and seeds it from a source environment: parameters are
// copied as new versions unless the target key already exists; secrets are
// never copied and are reported as needs_value. Each item fails
// independently (boundedApplicationError), so a partial clone is inspectable.
func (s *Service) CloneApplicationEnvironment(ctx context.Context, pr Principal, in domain.CloneEnvironmentInput) (domain.CloneEnvironmentResult, error) {
	if err := s.requireAdmin(ctx, pr, "application.environment_clone", domain.ResourceApplication, in.Application); err != nil {
		return domain.CloneEnvironmentResult{}, err
	}
	if err := keyutil.ValidateApp(in.Application); err != nil {
		return domain.CloneEnvironmentResult{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	for _, env := range []string{in.SourceEnv, in.TargetEnv} {
		if err := keyutil.ValidateEnv(env); err != nil {
			return domain.CloneEnvironmentResult{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
		}
	}
	if in.SourceEnv == in.TargetEnv {
		return domain.CloneEnvironmentResult{}, domain.Errorf(domain.ErrInvalidArgument, "source and target environments must differ")
	}
	store, err := s.applicationStore()
	if err != nil {
		return domain.CloneEnvironmentResult{}, err
	}
	rs, err := s.releaseStore()
	if err != nil {
		return domain.CloneEnvironmentResult{}, err
	}
	app, err := store.GetApplication(ctx, in.Application)
	if err != nil {
		return domain.CloneEnvironmentResult{}, err
	}
	sourceNS := domain.NamespaceRef{Env: in.SourceEnv, App: app.Name}
	targetNS := domain.NamespaceRef{Env: in.TargetEnv, App: app.Name}
	source, err := s.store.GetNamespace(ctx, sourceNS)
	if err != nil {
		return domain.CloneEnvironmentResult{}, err
	}
	target, err := s.store.GetNamespace(ctx, targetNS)
	created := false
	if errors.Is(err, domain.ErrNotFound) {
		methods := in.AuthMethods
		if len(methods) == 0 {
			methods = source.AllowedAuthMethods
		}
		target, err = s.CreateNamespace(ctx, pr, targetNS, in.Description, methods)
		if err != nil {
			return domain.CloneEnvironmentResult{}, err
		}
		created = true
	} else if err != nil {
		return domain.CloneEnvironmentResult{}, err
	}

	rows, _, err := s.collectApplicationRows(ctx, []domain.Namespace{source, target})
	if err != nil {
		return domain.CloneEnvironmentResult{}, err
	}
	facts, err := s.loadEnvironmentReleaseFacts(ctx, rs, sourceNS, app.ReleaseName, false)
	if err != nil {
		return domain.CloneEnvironmentResult{}, err
	}
	var sourceActive *domain.ConfigurationRelease
	if facts.Active != nil {
		sourceActive = &facts.Active.Release
	}
	environments, err := store.ListApplicationNamespaces(ctx, app.Name)
	if err != nil {
		return domain.CloneEnvironmentResult{}, err
	}
	otherActive := map[string]domain.ConfigurationRelease{}
	for _, other := range environments {
		if other.Env == sourceNS.Env {
			continue
		}
		active, err := rs.GetActiveConfigurationRelease(ctx, other.NamespaceRef, app.ReleaseName)
		if err == nil {
			otherActive[other.Env] = active.Release
		} else if !errors.Is(err, domain.ErrNotFound) {
			return domain.CloneEnvironmentResult{}, err
		}
	}

	type cloneTarget struct{ alias, key, kind string }
	targets := make([]cloneTarget, 0)
	if len(app.Contract) > 0 {
		refs := resolveContractRefs(app, sourceNS.Env, sourceActive, facts.Latest, otherActive, rows)
		for _, field := range app.Contract {
			key := field.Alias
			if ref, ok := refs[field.Alias]; ok {
				key = ref.Key
			}
			targets = append(targets, cloneTarget{alias: field.Alias, key: key, kind: field.Kind})
		}
	} else {
		for _, row := range rows {
			if cell, ok := row.Cells[sourceNS.Env]; ok && cell.Present {
				targets = append(targets, cloneTarget{alias: row.Key, key: row.Key, kind: row.Kind})
			}
		}
	}

	result := domain.CloneEnvironmentResult{Namespace: target, NamespaceCreated: created, Items: make([]domain.CloneEnvironmentItem, 0, len(targets)), NeedsValue: []string{}}
	copied := 0
	for _, t := range targets {
		item := domain.CloneEnvironmentItem{Alias: t.alias, Key: t.key, Kind: t.kind}
		sourceCell, inSource := cellFor(rows, sourceNS.Env, t.kind, t.key)
		targetCell, inTarget := cellFor(rows, targetNS.Env, t.kind, t.key)
		if inSource {
			item.SourceVersion = sourceCell.Version
		}
		switch {
		case inTarget:
			item.Action, item.TargetVersion = domain.CloneItemExists, targetCell.Version
		case t.kind == domain.ReleaseEntrySecret:
			item.Action = domain.CloneItemNeedsValue
		case !inSource:
			item.Action = domain.CloneItemMissingInSource
		case !in.CopyValues:
			item.Action = domain.CloneItemNeedsValue
		default:
			version, _, err := s.PutParameter(ctx, pr, domain.Ref{NS: targetNS, Key: t.key}, sourceCell.Value, sourceCell.ContentType, "{}")
			if err != nil {
				item.Action, item.Error = domain.CloneItemError, boundedApplicationError(err)
			} else {
				item.Action, item.TargetVersion = domain.CloneItemCopied, version
				copied++
			}
		}
		if item.Action == domain.CloneItemNeedsValue {
			result.NeedsValue = append(result.NeedsValue, t.alias)
		}
		result.Items = append(result.Items, item)
	}
	s.auditRefWithNamespaceID(ctx, pr, "application.environment_clone", domain.ResourceApplication, domain.Ref{NS: targetNS, Key: app.Name}, target.ID, 0, "allow", map[string]string{
		"source_env": sourceNS.Env, "target_env": targetNS.Env, "namespace_created": strconv.FormatBool(created),
		"copied": strconv.Itoa(copied), "needs_value": strconv.Itoa(len(result.NeedsValue)),
	})
	return result, nil
}
