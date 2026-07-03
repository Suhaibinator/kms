package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Suhaibinator/kms/internal/domain"
)

// errorEnvelope is the JSON error body documented in docs/http-api.md.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// mapError translates a domain sentinel error into an HTTP status, a stable
// code string, and a client-safe message. Internal and decryption failures are
// collapsed to a generic message so nothing about key metadata, ciphertext, or
// storage internals leaks to the caller.
func mapError(err error) (status int, code, message string) {
	switch {
	case errors.Is(err, domain.ErrInvalidArgument):
		return http.StatusBadRequest, "invalid_argument", err.Error()
	case errors.Is(err, domain.ErrUnauthenticated):
		// Fixed generic message, mirroring the gRPC boundary: an authentication
		// failure must never reveal whether a resource or identity exists
		// (domain.ErrUnauthenticated's invariant). Enforce it here rather than
		// trusting every construction site to stay generic.
		return http.StatusUnauthorized, "unauthenticated", "unauthenticated"
	case errors.Is(err, domain.ErrPermissionDenied):
		return http.StatusForbidden, "permission_denied", err.Error()
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, "not_found", err.Error()
	case errors.Is(err, domain.ErrAlreadyExists):
		return http.StatusConflict, "already_exists", err.Error()
	case errors.Is(err, domain.ErrFailedPrecondition):
		return http.StatusPreconditionFailed, "failed_precondition", err.Error()
	case errors.Is(err, domain.ErrNotReady):
		return http.StatusServiceUnavailable, "unavailable", err.Error()
	case errors.Is(err, domain.ErrDecryptFailed):
		// Never reveal whether the token, ciphertext, or key was at fault.
		return http.StatusInternalServerError, "internal", "internal error"
	default:
		return http.StatusInternalServerError, "internal", "internal error"
	}
}

// writeError renders err as the standard error envelope. Server-side 500s are
// logged with the underlying error; the client only ever sees "internal error".
func (s *server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := mapError(err)
	if status >= 500 {
		s.log.Error("request failed",
			"method", r.Method, "path", r.URL.Path, "status", status,
			"request_id", requestIDFrom(r.Context()), "error", err.Error())
	}
	writeJSON(w, status, errorEnvelope{Error: errorBody{Code: code, Message: message}})
}

// writeErrorCode renders an explicit code/message pair (used for rate limiting,
// which is not a domain error).
func writeErrorCode(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorEnvelope{Error: errorBody{Code: code, Message: message}})
}

// writeJSON serializes v as the response body with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
