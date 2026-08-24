package httpserver

import (
	"net/http"
	"strings"

	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/domain"
)

// Console application endpoints (plan §3.2). Handlers stay thin: parse, call
// one service method, render.

func (s *server) handleGetApplication(w http.ResponseWriter, r *http.Request) {
	app, err := s.svc.GetApplication(r.Context(), principalFrom(r.Context()), r.URL.Query().Get("name"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"application": toApplicationDTO(app)})
}

// insecureListener reports whether this request reached a cleartext listener,
// which the overview surfaces as the insecure_listener finding.
func (s *server) insecureListener(r *http.Request) bool {
	return !s.cfg.TLSEnabled && r.TLS == nil
}

// environmentsFromQuery accepts both repeated `env=a&env=b` and comma-joined
// `env=a,b`, ignoring empty items.
func environmentsFromQuery(values []string) []string {
	var out []string
	for _, value := range values {
		for _, env := range strings.Split(value, ",") {
			if env = strings.TrimSpace(env); env != "" {
				out = append(out, env)
			}
		}
	}
	return out
}

// handleApplicationOverview serves both forms: with name= the full
// ApplicationOverview (optionally narrowed by env=), without it the fleet
// summary.
func (s *server) handleApplicationOverview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	opts := core.OverviewOptions{Environments: environmentsFromQuery(q["env"]), InsecureListener: s.insecureListener(r)}
	if name := q.Get("name"); name != "" {
		overview, err := s.svc.GetApplicationOverview(r.Context(), principalFrom(r.Context()), name, opts)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, toApplicationOverviewDTO(overview))
		return
	}
	fleet, err := s.svc.GetFleetOverview(r.Context(), principalFrom(r.Context()), opts)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toFleetDTO(fleet))
}

func (s *server) handleShipApplication(w http.ResponseWriter, r *http.Request) {
	var body shipRequestDTO
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	result, err := s.svc.ShipApplicationChange(r.Context(), principalFrom(r.Context()), body.toDomain())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toShipResultDTO(result))
}

func (s *server) handleCloneEnvironment(w http.ResponseWriter, r *http.Request) {
	var body cloneEnvironmentRequestDTO
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	result, err := s.svc.CloneApplicationEnvironment(r.Context(), principalFrom(r.Context()), domain.CloneEnvironmentInput{
		Application: body.Application, SourceEnv: body.SourceEnv, TargetEnv: body.TargetEnv, CopyValues: body.CopyValues,
		AuthMethods: authMethodsFromStrings(body.AuthMethods), Description: body.Description,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toCloneEnvironmentDTO(result))
}
