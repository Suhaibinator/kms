package httpserver

import (
	"net/http"

	"github.com/Suhaibinator/kms/internal/domain"
)

type applicationMigrationChangeDTO struct {
	Alias       string  `json:"alias"`
	FromAlias   string  `json:"from_alias"`
	Key         string  `json:"key"`
	Value       *string `json:"value"`
	ContentType string  `json:"content_type"`
	Version     uint64  `json:"version"`
}

type applicationMigrationRequestDTO struct {
	Environment                      string                            `json:"environment"`
	SchemaVersion                    uint64                            `json:"schema_version"`
	Contract                         []domain.ApplicationContractField `json:"contract"`
	Changes                          []applicationMigrationChangeDTO   `json:"changes"`
	MetadataJSON                     string                            `json:"metadata_json"`
	Execute                          bool                              `json:"execute"`
	PlanDigest                       string                            `json:"plan_digest"`
	ExpectedSourceVersion            *uint64                           `json:"expected_source_version"`
	ExpectedSourceActivationRevision *uint64                           `json:"expected_source_activation_revision"`
}

func (d applicationMigrationRequestDTO) toDomain(application string) domain.ApplicationReleaseMigrationInput {
	changes := make([]domain.ApplicationMigrationChange, 0, len(d.Changes))
	for _, c := range d.Changes {
		changes = append(changes, domain.ApplicationMigrationChange{
			Alias: c.Alias, FromAlias: c.FromAlias, Key: c.Key, Value: c.Value,
			ContentType: c.ContentType, Version: c.Version,
		})
	}
	return domain.ApplicationReleaseMigrationInput{
		Namespace:     domain.NamespaceRef{App: application, Env: d.Environment},
		SchemaVersion: d.SchemaVersion, Contract: d.Contract, Changes: changes,
		Metadata: d.MetadataJSON, Execute: d.Execute, PlanDigest: d.PlanDigest,
		ExpectedSourceVersion: d.ExpectedSourceVersion, ExpectedSourceActivationRevision: d.ExpectedSourceActivationRevision,
	}
}

func (s *server) handleApplicationSchemaMigration(w http.ResponseWriter, r *http.Request) {
	var body applicationMigrationRequestDTO
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	result, err := s.svc.MigrateApplicationRelease(r.Context(), principalFrom(r.Context()), body.toDomain(r.PathValue("application")))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toApplicationMigrationResultDTO(result))
}

type applicationMigrationEntryDTO struct {
	Alias       string `json:"alias"`
	Kind        string `json:"kind"`
	Key         string `json:"key"`
	FromVersion uint64 `json:"from_version"`
	ToVersion   uint64 `json:"to_version"`
	Source      string `json:"source"`
}

type applicationMigrationEnvironmentDTO struct {
	Environment   string `json:"environment"`
	ActiveVersion uint64 `json:"active_version"`
	SchemaVersion uint64 `json:"schema_version"`
}

type applicationMigrationResultDTO struct {
	PlanDigest               string                               `json:"plan_digest"`
	Valid                    bool                                 `json:"valid"`
	Executed                 bool                                 `json:"executed"`
	DefinitionChanged        bool                                 `json:"definition_changed"`
	ReleaseName              string                               `json:"release_name"`
	SourceVersion            uint64                               `json:"source_version"`
	SourceActivationRevision uint64                               `json:"source_activation_revision"`
	SchemaVersion            uint64                               `json:"schema_version"`
	Entries                  []applicationMigrationEntryDTO       `json:"entries"`
	Validation               []releaseValidationErrorDTO          `json:"validation"`
	AffectedEnvironments     []applicationMigrationEnvironmentDTO `json:"affected_environments"`
	Release                  *releaseDTO                          `json:"release,omitempty"`
	Activation               *shipActivationDTO                   `json:"activation,omitempty"`
}

func toApplicationMigrationResultDTO(r domain.ApplicationReleaseMigrationResult) applicationMigrationResultDTO {
	out := applicationMigrationResultDTO{
		PlanDigest: r.PlanDigest, Valid: r.Valid, Executed: r.Executed,
		DefinitionChanged: r.DefinitionChanged, ReleaseName: r.ReleaseName,
		SourceVersion: r.SourceVersion, SourceActivationRevision: r.SourceActivationRevision,
		SchemaVersion:        r.SchemaVersion,
		Entries:              make([]applicationMigrationEntryDTO, 0, len(r.Entries)),
		Validation:           releaseValidationErrorDTOs(r.Validation),
		AffectedEnvironments: make([]applicationMigrationEnvironmentDTO, 0, len(r.AffectedEnvironments)),
	}
	for _, entry := range r.Entries {
		out.Entries = append(out.Entries, applicationMigrationEntryDTO{
			Alias: entry.Alias, Kind: entry.Kind, Key: entry.Ref.Key,
			FromVersion: entry.FromVersion, ToVersion: entry.ToVersion, Source: entry.Source,
		})
	}
	for _, env := range r.AffectedEnvironments {
		out.AffectedEnvironments = append(out.AffectedEnvironments, applicationMigrationEnvironmentDTO{
			Environment: env.Environment, ActiveVersion: env.ActiveVersion, SchemaVersion: env.SchemaVersion,
		})
	}
	if r.Release != nil {
		release := toReleaseDTO(*r.Release)
		out.Release = &release
	}
	if r.Activation != nil {
		out.Activation = &shipActivationDTO{
			ActivationRevision: r.Activation.ActivationRevision,
			PreviousVersion:    r.Activation.PreviousVersion, Changed: r.Activation.Changed,
		}
	}
	return out
}
