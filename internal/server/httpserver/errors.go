package httpserver

import (
	"encoding/json/v2"
	"errors"
	"net/http"

	"go.uber.org/zap"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// errorEnvelope is the JSON error body documented in docs/http-api.md.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code             string                      `json:"code"`
	Message          string                      `json:"message"`
	ValidationErrors []releaseValidationErrorDTO `json:"validation_errors,omitempty"`
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
	case errors.Is(err, domain.ErrAborted):
		return http.StatusConflict, "aborted", err.Error()
	case errors.Is(err, domain.ErrFailedPrecondition):
		return http.StatusPreconditionFailed, "failed_precondition", err.Error()
	case errors.Is(err, domain.ErrResourceExhausted):
		return http.StatusTooManyRequests, "rate_limited", err.Error()
	case errors.Is(err, storage.ErrPurgeCleanupPending):
		// The logical purge committed. This distinct fixed response must not
		// imply rollback or invite the caller to resubmit the compromised key.
		return http.StatusServiceUnavailable, "purge_cleanup_pending", storage.ErrPurgeCleanupPending.Error()
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
			zap.String("method", r.Method), zap.String("path", r.URL.Path), zap.Int("status", status),
			zap.String("request_id", requestIDFrom(r.Context())), zap.String("error", err.Error()))
	}
	body := errorBody{Code: code, Message: message}
	if validationFailed, ok := errors.AsType[*domain.ReleaseValidationFailedError](err); ok {
		body.ValidationErrors = releaseValidationErrorDTOs(validationFailed.Violations())
	}
	writeJSON(w, status, errorEnvelope{Error: body})
}

func releaseValidationErrorDTOs(validationErrors []domain.ReleaseValidationError) []releaseValidationErrorDTO {
	out := make([]releaseValidationErrorDTO, len(validationErrors))
	for i, validationErr := range validationErrors {
		out[i] = releaseValidationErrorDTO{
			Alias: validationErr.Alias, Code: validationErr.Code,
			SchemaPointer: validationErr.SchemaPointer, Message: validationErr.Message,
		}
	}
	return out
}

// writeErrorCode renders an explicit code/message pair (used for rate limiting,
// which is not a domain error).
func writeErrorCode(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorEnvelope{Error: errorBody{Code: code, Message: message}})
}

// writeJSON serializes v as the response body with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, v)
}
