package httpserver

import (
	"net/http"

	"github.com/Suhaibinator/kms/internal/domain"
)

func (s *server) handleCreateRelease(w http.ResponseWriter, r *http.Request) {
	var body createReleaseDTO
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	release, err := s.svc.CreateConfigurationRelease(r.Context(), principalFrom(r.Context()), body.toDomain())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"release": toReleaseDTO(release)})
}

func (s *server) handleGetRelease(w http.ResponseWriter, r *http.Request) {
	version, err := parseVersion(r.URL.Query().Get("version"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	release, err := s.svc.GetConfigurationRelease(r.Context(), principalFrom(r.Context()),
		nsRefFromQuery(r), r.URL.Query().Get("name"), version)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"release": toReleaseDTO(release)})
}

func (s *server) handleGetActiveRelease(w http.ResponseWriter, r *http.Request) {
	active, err := s.svc.GetActiveConfigurationRelease(r.Context(), principalFrom(r.Context()),
		nsRefFromQuery(r), r.URL.Query().Get("name"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"release": toReleaseDTO(active.Release), "activation_revision": active.ActivationRevision,
		"previous_version": active.PreviousVersion,
	})
}

func (s *server) handleListReleases(w http.ResponseWriter, r *http.Request) {
	items, next, err := s.svc.ListConfigurationReleases(r.Context(), principalFrom(r.Context()),
		nsRefFromQuery(r), r.URL.Query().Get("name"), listPage(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]any{
			"release": toReleaseDTO(item.Release), "current": item.Current, "previous": item.Previous,
			"activation_revision": item.ActivationRevision,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"releases": out, "next_page_token": next})
}

func (s *server) handleValidateRelease(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Namespace namespaceRefDTO `json:"namespace"`
		Name      string          `json:"name"`
		Version   uint64          `json:"version"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	errorsOut, err := s.svc.ValidateConfigurationRelease(r.Context(), principalFrom(r.Context()),
		domain.NamespaceRef{Env: body.Namespace.Env, App: body.Namespace.App}, body.Name, body.Version)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := releaseValidationErrorDTOs(errorsOut)
	writeJSON(w, http.StatusOK, map[string]any{"valid": len(out) == 0, "errors": out})
}

func (s *server) handleActivateRelease(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Namespace              namespaceRefDTO `json:"namespace"`
		Name                   string          `json:"name"`
		Version                uint64          `json:"version"`
		ExpectedCurrentVersion *uint64         `json:"expected_current_version"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	active, changed, err := s.svc.ActivateConfigurationRelease(r.Context(), principalFrom(r.Context()),
		domain.NamespaceRef{Env: body.Namespace.Env, App: body.Namespace.App}, body.Name, body.Version,
		body.ExpectedCurrentVersion)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"release": toReleaseDTO(active.Release), "activation_revision": active.ActivationRevision,
		"previous_version": active.PreviousVersion, "changed": changed,
	})
}

func (s *server) handleCreateConfigurationSchema(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID           string `json:"id"`
		SchemaJSON   string `json:"schema_json"`
		MetadataJSON string `json:"metadata_json"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	schema, err := s.svc.CreateConfigurationSchema(r.Context(), principalFrom(r.Context()),
		body.ID, body.SchemaJSON, body.MetadataJSON)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"schema": toSchemaDTO(schema)})
}

func (s *server) handleListConfigurationSchemas(w http.ResponseWriter, r *http.Request) {
	items, next, err := s.svc.ListConfigurationSchemas(r.Context(), principalFrom(r.Context()),
		r.URL.Query().Get("id"), listPage(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := make([]schemaDTO, 0, len(items))
	for _, schema := range items {
		out = append(out, toSchemaDTO(schema))
	}
	writeJSON(w, http.StatusOK, map[string]any{"schemas": out, "next_page_token": next})
}

func (s *server) handleListReleaseSubscribers(w http.ResponseWriter, r *http.Request) {
	items, next, revision, err := s.svc.ListReleaseSubscribers(r.Context(), principalFrom(r.Context()),
		nsRefFromQuery(r), r.URL.Query().Get("name"), listPage(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := make([]releaseSubscriberDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toReleaseSubscriberDTO(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subscribers": out, "next_page_token": next, "current_revision": revision,
	})
}
