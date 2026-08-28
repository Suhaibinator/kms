package httpserver

import (
	"io"
	"net/http"

	"github.com/Suhaibinator/kms/internal/domain"
)

type defaultsApplyEntryDTO struct {
	Alias          string `json:"alias"`
	Key            string `json:"key"`
	ContentType    string `json:"content_type"`
	Status         string `json:"status"`
	CurrentVersion uint64 `json:"current_version"`
	AppliedVersion uint64 `json:"applied_version"`
	Revision       uint64 `json:"revision"`
}

type defaultsApplyResultDTO struct {
	Profile           string                  `json:"profile"`
	SchemaSHA256      string                  `json:"schema_sha256"`
	ArtifactDigest    string                  `json:"artifact_digest"`
	PlanDigest        string                  `json:"plan_digest"`
	Entries           []defaultsApplyEntryDTO `json:"entries"`
	MissingSecrets    []string                `json:"missing_secrets"`
	Executed          bool                    `json:"executed"`
	DefinitionChanged bool                    `json:"definition_changed"`
	DefinitionUpdated bool                    `json:"definition_updated"`
}

func toDefaultsApplyResultDTO(result domain.DefaultsApplyResult) defaultsApplyResultDTO {
	entries := make([]defaultsApplyEntryDTO, 0, len(result.Entries))
	for _, entry := range result.Entries {
		entries = append(entries, defaultsApplyEntryDTO{
			Alias: entry.Alias, Key: entry.Key, ContentType: entry.ContentType,
			Status: entry.Status, CurrentVersion: entry.CurrentVersion,
			AppliedVersion: entry.AppliedVersion, Revision: entry.Revision,
		})
	}
	missing := result.MissingSecrets
	if missing == nil {
		missing = []string{}
	}
	return defaultsApplyResultDTO{
		Profile: result.Profile, SchemaSHA256: result.SchemaSHA256,
		ArtifactDigest: result.ArtifactDigest, PlanDigest: result.PlanDigest,
		Entries: entries, MissingSecrets: missing, Executed: result.Executed,
		DefinitionChanged: result.DefinitionChanged, DefinitionUpdated: result.DefinitionUpdated,
	}
}

func defaultsQueryBool(r *http.Request, name string) (bool, error) {
	value := r.URL.Query().Get(name)
	switch value {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, domain.Errorf(domain.ErrInvalidArgument, "%s must be true or false", name)
	}
}

// handleApplicationDefaults accepts the artifact as the exact raw request
// body. Query parameters select the existing namespace and preview/execute
// mode; values are never reflected in the response.
func (s *server) handleApplicationDefaults(w http.ResponseWriter, r *http.Request) {
	overwrite, err := defaultsQueryBool(r, "overwrite")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	execute, err := defaultsQueryBool(r, "execute")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	updateDefinition, err := defaultsQueryBool(r, "update_definition")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		s.writeError(w, r, domain.Errorf(domain.ErrInvalidArgument, "could not read defaults artifact"))
		return
	}
	if len(raw) > maxBodyBytes {
		s.writeError(w, r, domain.Errorf(domain.ErrInvalidArgument, "defaults artifact exceeds %d bytes", maxBodyBytes))
		return
	}
	result, err := s.svc.ApplyApplicationDefaults(r.Context(), principalFrom(r.Context()), domain.DefaultsApplyInput{
		Namespace: domain.NamespaceRef{Env: r.URL.Query().Get("env"), App: r.URL.Query().Get("app")},
		Artifact:  raw, Overwrite: overwrite, UpdateDefinition: updateDefinition, Execute: execute,
		PlanDigest: r.URL.Query().Get("plan_digest"),
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toDefaultsApplyResultDTO(result))
}
