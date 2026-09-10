package httpserver

import (
	"net/http"

	"github.com/Suhaibinator/kms/internal/domain"
)

func (s *server) handleSetReleasePin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Namespace           namespaceRefDTO `json:"namespace"`
		Name                string          `json:"name"`
		SchemaVersion       *uint64         `json:"schema_version"`
		Identity            string          `json:"identity"`
		ClientName          string          `json:"client_name"`
		InstanceID          string          `json:"instance_id"`
		SessionID           string          `json:"session_id"`
		Version             uint64          `json:"version"`
		ExpectedPinRevision *uint64         `json:"expected_pin_revision"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	if body.SchemaVersion == nil || body.ExpectedPinRevision == nil {
		s.writeError(w, r, invalidArg("schema_version and expected_pin_revision are required"))
		return
	}
	ref := domain.ReleaseSessionRef{Track: domain.ReleaseTrack{Namespace: domain.NamespaceRef{Env: body.Namespace.Env, App: body.Namespace.App}, Name: body.Name, SchemaVersion: *body.SchemaVersion}, Identity: body.Identity, ClientName: body.ClientName, InstanceID: body.InstanceID, SessionID: body.SessionID}
	target, err := s.svc.SetReleasePin(r.Context(), principalFrom(r.Context()), ref, body.Version, *body.ExpectedPinRevision)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": ref.Track.SchemaVersion, "session_id": ref.SessionID, "target_version": target.Release.Version, "target_revision": target.TargetRevision, "activation_revision": target.ActivationRevision, "pinned": target.Pinned, "pin_revision": target.PinRevision, "pinned_by": target.PinnedBy, "pinned_at_unix_ms": unixMS(target.PinnedAt)})
}
