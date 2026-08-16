package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/keyutil"
	"github.com/Suhaibinator/kms/internal/storage"
)

func (s *Service) applicationStore() (storage.ApplicationStore, error) {
	store, ok := s.store.(storage.ApplicationStore)
	if !ok {
		return nil, domain.Errorf(domain.ErrFailedPrecondition, "application management is unavailable")
	}
	return store, nil
}

func normalizeApplication(app domain.Application) (domain.Application, error) {
	if err := keyutil.ValidateApp(app.Name); err != nil {
		return app, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	app.Description = strings.TrimSpace(app.Description)
	if app.ReleaseName == "" {
		app.ReleaseName = "runtime"
	}
	if err := keyutil.ValidateKey(app.ReleaseName); err != nil {
		return app, domain.Errorf(domain.ErrInvalidArgument, "invalid release name: %v", err)
	}
	if (app.SchemaID == "") != (app.SchemaVersion == 0) {
		return app, domain.Errorf(domain.ErrInvalidArgument, "schema_id and schema_version must be specified together")
	}
	seen := make(map[string]struct{}, len(app.Contract))
	for i := range app.Contract {
		field := &app.Contract[i]
		if len(field.Alias) == 0 || len(field.Alias) > maxReleaseAliasBytes || !releaseAliasRE.MatchString(field.Alias) {
			return app, domain.Errorf(domain.ErrInvalidArgument, "invalid contract alias %q", field.Alias)
		}
		if _, ok := seen[field.Alias]; ok {
			return app, domain.Errorf(domain.ErrInvalidArgument, "duplicate contract alias %q", field.Alias)
		}
		seen[field.Alias] = struct{}{}
		switch field.Kind {
		case domain.ReleaseEntryParameter:
			if !validParameterContentTypes[field.ContentType] {
				return app, domain.Errorf(domain.ErrInvalidArgument, "contract parameter %q has invalid content type %q", field.Alias, field.ContentType)
			}
		case domain.ReleaseEntrySecret:
			field.ContentType = ""
		default:
			return app, domain.Errorf(domain.ErrInvalidArgument, "contract alias %q has invalid kind %q", field.Alias, field.Kind)
		}
	}
	sort.Slice(app.Contract, func(i, j int) bool { return app.Contract[i].Alias < app.Contract[j].Alias })
	return app, nil
}

func (s *Service) validateApplicationSchema(ctx context.Context, app domain.Application) error {
	if app.SchemaID == "" {
		return nil
	}
	rs, err := s.releaseStore()
	if err != nil {
		return err
	}
	_, err = rs.GetConfigurationSchema(ctx, app.SchemaID, app.SchemaVersion)
	return err
}

func (s *Service) CreateApplication(ctx context.Context, pr Principal, app domain.Application) (domain.Application, error) {
	if err := s.requireAdmin(ctx, pr, "application.create", domain.ResourceApplication, app.Name); err != nil {
		return domain.Application{}, err
	}
	app, err := normalizeApplication(app)
	if err != nil {
		return domain.Application{}, err
	}
	if err := s.validateApplicationSchema(ctx, app); err != nil {
		return domain.Application{}, err
	}
	store, err := s.applicationStore()
	if err != nil {
		return domain.Application{}, err
	}
	app.CreatedBy, app.CreatedAt, app.UpdatedAt = pr.Identity.Name, s.now(), s.now()
	out, err := store.CreateApplication(ctx, app)
	if err == nil {
		s.auditName(ctx, pr, "application.create", domain.ResourceApplication, app.Name, "allow", nil)
	}
	return out, err
}

func (s *Service) UpdateApplication(ctx context.Context, pr Principal, app domain.Application) (domain.Application, error) {
	if err := s.requireAdmin(ctx, pr, "application.update", domain.ResourceApplication, app.Name); err != nil {
		return domain.Application{}, err
	}
	app, err := normalizeApplication(app)
	if err != nil {
		return domain.Application{}, err
	}
	if err := s.validateApplicationSchema(ctx, app); err != nil {
		return domain.Application{}, err
	}
	store, err := s.applicationStore()
	if err != nil {
		return domain.Application{}, err
	}
	out, err := store.UpdateApplication(ctx, app)
	if err == nil {
		s.auditName(ctx, pr, "application.update", domain.ResourceApplication, app.Name, "allow", nil)
	}
	return out, err
}

func (s *Service) GetApplication(ctx context.Context, pr Principal, name string) (domain.Application, error) {
	if err := s.requireAdmin(ctx, pr, "application.read", domain.ResourceApplication, name); err != nil {
		return domain.Application{}, err
	}
	if err := keyutil.ValidateApp(name); err != nil {
		return domain.Application{}, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
	}
	store, err := s.applicationStore()
	if err != nil {
		return domain.Application{}, err
	}
	return store.GetApplication(ctx, name)
}

func (s *Service) ListApplications(ctx context.Context, pr Principal, page storage.ListPage) ([]domain.Application, string, error) {
	if err := s.requireAdmin(ctx, pr, "application.list", domain.ResourceApplication, "applications"); err != nil {
		return nil, "", err
	}
	store, err := s.applicationStore()
	if err != nil {
		return nil, "", err
	}
	return store.ListApplications(ctx, page)
}

func (s *Service) DeleteApplication(ctx context.Context, pr Principal, name string) error {
	if err := s.requireAdmin(ctx, pr, "application.delete", domain.ResourceApplication, name); err != nil {
		return err
	}
	store, err := s.applicationStore()
	if err != nil {
		return err
	}
	if err := store.DeleteApplication(ctx, name); err != nil {
		return err
	}
	s.auditName(ctx, pr, "application.delete", domain.ResourceApplication, name, "allow", nil)
	return nil
}

func (s *Service) GetApplicationDashboard(ctx context.Context, pr Principal, name string) (domain.ApplicationDashboard, error) {
	app, err := s.GetApplication(ctx, pr, name)
	if err != nil {
		return domain.ApplicationDashboard{}, err
	}
	store, err := s.applicationStore()
	if err != nil {
		return domain.ApplicationDashboard{}, err
	}
	environments, err := store.ListApplicationNamespaces(ctx, name)
	if err != nil {
		return domain.ApplicationDashboard{}, err
	}
	rows := make(map[string]*domain.ApplicationConfigurationRow)
	for _, ns := range environments {
		var token string
		for {
			parameters, next, err := s.store.ListParameters(ctx, ns.NamespaceRef, "", storage.ListPage{Limit: 1000, Token: token})
			if err != nil {
				return domain.ApplicationDashboard{}, err
			}
			for _, parameter := range parameters {
				rowKey := domain.ResourceParameter + "\x00" + parameter.Ref.Key
				row := rows[rowKey]
				if row == nil {
					row = &domain.ApplicationConfigurationRow{Key: parameter.Ref.Key, Kind: domain.ResourceParameter, Cells: map[string]domain.ApplicationConfigurationCell{}}
					rows[rowKey] = row
				}
				row.Cells[ns.Env] = domain.ApplicationConfigurationCell{Environment: ns.Env, Present: true, Value: parameter.Value, ContentType: parameter.ContentType, Version: parameter.Version}
			}
			if next == "" {
				break
			}
			token = next
		}
		token = ""
		for {
			secrets, next, err := s.store.ListSecrets(ctx, ns.NamespaceRef, "", storage.ListPage{Limit: 1000, Token: token})
			if err != nil {
				return domain.ApplicationDashboard{}, err
			}
			for _, secret := range secrets {
				rowKey := domain.ResourceSecret + "\x00" + secret.Ref.Key
				row := rows[rowKey]
				if row == nil {
					row = &domain.ApplicationConfigurationRow{Key: secret.Ref.Key, Kind: domain.ResourceSecret, Cells: map[string]domain.ApplicationConfigurationCell{}}
					rows[rowKey] = row
				}
				row.Cells[ns.Env] = domain.ApplicationConfigurationCell{Environment: ns.Env, Present: true, ContentType: secret.ContentType, Version: secret.Labels[domain.LabelCurrent], ClientBound: secret.ClientBound, HasAccessToken: secret.HasAccessToken}
			}
			if next == "" {
				break
			}
			token = next
		}
	}
	ordered := make([]domain.ApplicationConfigurationRow, 0, len(rows))
	for _, row := range rows {
		ordered = append(ordered, *row)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Key == ordered[j].Key {
			return ordered[i].Kind < ordered[j].Kind
		}
		return ordered[i].Key < ordered[j].Key
	})
	return domain.ApplicationDashboard{Application: app, Environments: environments, Rows: ordered}, nil
}

func (s *Service) PutApplicationParameter(ctx context.Context, pr Principal, app, key, value, contentType, metadata string, environments []string) ([]domain.ApplicationParameterWriteResult, error) {
	if len(environments) == 0 {
		return nil, domain.Errorf(domain.ErrInvalidArgument, "at least one environment is required")
	}
	if _, err := s.GetApplication(ctx, pr, app); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(environments))
	results := make([]domain.ApplicationParameterWriteResult, 0, len(environments))
	for _, environment := range environments {
		if _, ok := seen[environment]; ok {
			continue
		}
		seen[environment] = struct{}{}
		if err := keyutil.ValidateEnv(environment); err != nil {
			return nil, domain.Errorf(domain.ErrInvalidArgument, "%v", err)
		}
		version, revision, err := s.PutParameter(ctx, pr, domain.Ref{NS: domain.NamespaceRef{Env: environment, App: app}, Key: key}, value, contentType, metadata)
		result := domain.ApplicationParameterWriteResult{Environment: environment, Version: version, Revision: revision}
		if err != nil {
			result.Error = boundedApplicationError(err)
		}
		results = append(results, result)
	}
	return results, nil
}

func boundedApplicationError(err error) string {
	if err == nil {
		return ""
	}
	for _, sentinel := range []error{domain.ErrInvalidArgument, domain.ErrNotFound, domain.ErrPermissionDenied, domain.ErrFailedPrecondition, domain.ErrAborted} {
		if errors.Is(err, sentinel) {
			return fmt.Sprintf("%v", err)
		}
	}
	return "write failed"
}
