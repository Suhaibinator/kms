package httpserver

import (
	"net/http"
	"strconv"

	"github.com/Suhaibinator/kms/internal/domain"
)

func (s *server) handleCreateRelease(w http.ResponseWriter, r *http.Request) {
	var body createReleaseDTO
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	if body.SchemaVersion == nil {
		s.writeError(w, r, invalidArg("schema_version is required"))
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
	schemaVersion, err := parseSchemaVersion(r, true)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	release, err := s.svc.GetConfigurationRelease(r.Context(), principalFrom(r.Context()),
		domain.ReleaseTrack{Namespace: nsRefFromQuery(r), Name: r.URL.Query().Get("name"), SchemaVersion: *schemaVersion}, version)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"release": toReleaseDTO(release)})
}

func (s *server) handleGetActiveRelease(w http.ResponseWriter, r *http.Request) {
	schemaVersion, err := parseSchemaVersion(r, true)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	active, err := s.svc.GetActiveConfigurationRelease(r.Context(), principalFrom(r.Context()),
		domain.ReleaseTrack{Namespace: nsRefFromQuery(r), Name: r.URL.Query().Get("name"), SchemaVersion: *schemaVersion})
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
	schemaVersion, err := parseSchemaVersion(r, false)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items, next, err := s.svc.ListConfigurationReleases(r.Context(), principalFrom(r.Context()),
		domain.ReleaseFilter{Namespace: nsRefFromQuery(r), Name: r.URL.Query().Get("name"), SchemaVersion: schemaVersion}, listPage(r))
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

// Discovery shares release-list authorization, not the admin schema registry.
func (s *server) handleListReleaseSchemaVersions(w http.ResponseWriter, r *http.Request) {
	versions, next, err := s.svc.ListReleaseSchemaVersions(r.Context(), principalFrom(r.Context()), nsRefFromQuery(r), r.URL.Query().Get("name"), listPage(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema_versions": versions, "next_page_token": next})
}

func (s *server) handleValidateRelease(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Namespace     namespaceRefDTO `json:"namespace"`
		Name          string          `json:"name"`
		Version       uint64          `json:"version"`
		SchemaVersion *uint64         `json:"schema_version"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	if body.SchemaVersion == nil {
		s.writeError(w, r, invalidArg("schema_version is required"))
		return
	}
	errorsOut, err := s.svc.ValidateConfigurationRelease(r.Context(), principalFrom(r.Context()),
		domain.ReleaseTrack{Namespace: domain.NamespaceRef{Env: body.Namespace.Env, App: body.Namespace.App}, Name: body.Name, SchemaVersion: *body.SchemaVersion}, body.Version)
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
		SchemaVersion          *uint64         `json:"schema_version"`
		ExpectedCurrentVersion *uint64         `json:"expected_current_version"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	if body.SchemaVersion == nil {
		s.writeError(w, r, invalidArg("schema_version is required"))
		return
	}
	active, changed, err := s.svc.ActivateConfigurationRelease(r.Context(), principalFrom(r.Context()),
		domain.ReleaseTrack{Namespace: domain.NamespaceRef{Env: body.Namespace.Env, App: body.Namespace.App}, Name: body.Name, SchemaVersion: *body.SchemaVersion}, body.Version,
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
		Application  string `json:"application"`
		SchemaJSON   string `json:"schema_json"`
		MetadataJSON string `json:"metadata_json"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	schema, err := s.svc.CreateConfigurationSchema(r.Context(), principalFrom(r.Context()),
		body.Application, body.SchemaJSON, body.MetadataJSON)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"schema": toSchemaDTO(schema)})
}

func (s *server) handleListConfigurationSchemas(w http.ResponseWriter, r *http.Request) {
	items, next, err := s.svc.ListConfigurationSchemas(r.Context(), principalFrom(r.Context()),
		r.URL.Query().Get("application"), r.URL.Query().Get("release_name"), listPage(r))
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
	schemaVersion, err := parseSchemaVersion(r, false)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	snapshot, next, err := s.svc.ListReleaseSubscriberProjection(r.Context(), principalFrom(r.Context()),
		domain.ReleaseFilter{Namespace: nsRefFromQuery(r), Name: r.URL.Query().Get("name"), SchemaVersion: schemaVersion}, listPage(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := toSubscriberStreamSnapshotDTO(snapshot)
	writeJSON(w, http.StatusOK, map[string]any{"subscribers": out.Subscribers, "instances": out.Instances,
		"summary": out.Summary, "projection_revision": out.ProjectionRevision,
		"next_page_token": next, "current_revision": out.CurrentRevision, "server_time_unix_ms": out.ServerTimeUnixMS})
}

// Historical lifecycle rows are deliberately separate from effective health.
func (s *server) handleReleaseSubscriberHistory(w http.ResponseWriter, r *http.Request) {
	schemaVersion, err := parseSchemaVersion(r, false)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items, next, revision, err := s.svc.ListReleaseSubscribers(r.Context(), principalFrom(r.Context()),
		domain.ReleaseFilter{Namespace: nsRefFromQuery(r), Name: r.URL.Query().Get("name"), SchemaVersion: schemaVersion}, listPage(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	rows := make([]releaseSubscriberDTO, 0, len(items))
	for _, item := range items {
		rows = append(rows, toReleaseSubscriberDTO(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscribers": rows, "next_page_token": next, "current_revision": revision})
}

func (s *server) handleRollbackRelease(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Env                    string  `json:"env"`
		App                    string  `json:"app"`
		Name                   string  `json:"name"`
		SchemaVersion          *uint64 `json:"schema_version"`
		ExpectedCurrentVersion *uint64 `json:"expected_current_version"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	if body.SchemaVersion == nil {
		s.writeError(w, r, invalidArg("schema_version is required"))
		return
	}
	result, err := s.svc.RollbackConfigurationRelease(r.Context(), principalFrom(r.Context()),
		domain.ReleaseTrack{Namespace: domain.NamespaceRef{Env: body.Env, App: body.App}, Name: body.Name, SchemaVersion: *body.SchemaVersion}, body.ExpectedCurrentVersion)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"release": toReleaseDTO(result.Active.Release), "activation_revision": result.Active.ActivationRevision,
		"previous_version": result.Active.PreviousVersion, "rolled_back_from": result.RolledBackFrom, "changed": result.Changed,
	})
}

// parseReleaseDiffSelector accepts a positive version number or one of the
// movable labels; the value is required.
func parseReleaseDiffSelector(name, raw string) (domain.ReleaseDiffSelector, error) {
	switch raw {
	case "":
		return domain.ReleaseDiffSelector{}, invalidArg(name + " is required")
	case domain.LabelCurrent, domain.LabelPrevious:
		return domain.ReleaseDiffSelector{Label: raw}, nil
	}
	version, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || version == 0 {
		return domain.ReleaseDiffSelector{}, invalidArg(name + " must be a positive version number, current, or previous")
	}
	return domain.ReleaseDiffSelector{Version: version}, nil
}

func (s *server) handleDiffReleases(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	schemaVersion, err := parseSchemaVersion(r, true)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	from, err := parseReleaseDiffSelector("from", q.Get("from"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	to, err := parseReleaseDiffSelector("to", q.Get("to"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	includeValues := true
	switch q.Get("values") {
	case "", "1", "true":
	case "0", "false":
		includeValues = false
	default:
		s.writeError(w, r, invalidArg("values must be 0 or 1"))
		return
	}
	fromTrack := domain.ReleaseTrack{Namespace: nsRefFromQuery(r), Name: q.Get("name"), SchemaVersion: *schemaVersion}
	toTrack := fromTrack
	if env := q.Get("to_env"); env != "" {
		toTrack.Namespace.Env = env
	}
	if raw := q.Get("to_schema_version"); raw != "" {
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			s.writeError(w, r, invalidArg("to_schema_version must be a non-negative integer"))
			return
		}
		toTrack.SchemaVersion = value
	}
	diff, err := s.svc.DiffConfigurationReleases(r.Context(), principalFrom(r.Context()), domain.ReleaseDiffInput{
		From: fromTrack, To: toTrack, FromSelector: from, ToSelector: to, IncludeValues: includeValues,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toReleaseDiffDTO(diff))
}
