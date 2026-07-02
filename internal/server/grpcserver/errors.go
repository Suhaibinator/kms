package grpcserver

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Suhaibinator/kms/internal/domain"
)

// mapError translates a domain error into a gRPC status. Sentinel errors map to
// their corresponding codes; decryption failures and any unrecognized error map
// to a bare "internal error" so no key metadata, ciphertext detail, or panic
// value ever leaks to the caller. Unexpected errors are logged server-side with
// the request ID for correlation.
func mapError(log *slog.Logger, ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, domain.ErrInvalidArgument):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, domain.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, domain.ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, domain.ErrUnauthenticated):
		// Keep authentication failures generic.
		return status.Error(codes.Unauthenticated, "unauthenticated")
	case errors.Is(err, domain.ErrPermissionDenied):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, domain.ErrFailedPrecondition):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, domain.ErrNotReady):
		return status.Error(codes.Unavailable, "service not ready")
	case errors.Is(err, domain.ErrDecryptFailed):
		// Never reveal whether key, ciphertext, or authz caused the failure.
		return status.Error(codes.Internal, "internal error")
	default:
		// Log the real error server-side; return an opaque message.
		if log != nil {
			log.Error("unhandled internal error", "request_id", requestIDFrom(ctx), "error", err)
		}
		return status.Error(codes.Internal, "internal error")
	}
}

// mapErr is the Server-scoped convenience wrapper.
func (s *Server) mapErr(ctx context.Context, err error) error {
	return mapError(s.log, ctx, err)
}
