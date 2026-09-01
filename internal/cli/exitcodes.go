package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Suhaibinator/kms/internal/domain"
)

// Process exit codes. Scripts branch on these; keep the numbers stable and
// documented in docs/operations.md. Codes 3–9 mirror the gRPC status the
// server returned (or the equivalent domain sentinel for offline commands),
// so `parameter-store get-secret` failing with 5 means "not found" whether
// the caller reads the message or not.
const (
	exitOK                 = 0
	exitError              = 1 // any error not classified below
	exitUsage              = 2 // bad flags or arguments, refused confirmation
	exitUnauthenticated    = 3
	exitPermissionDenied   = 4
	exitNotFound           = 5
	exitConflict           = 6 // already exists, or a compare-and-swap lost
	exitFailedPrecondition = 7 // state forbids the operation (incl. release validation)
	exitUnavailable        = 8 // server unreachable, not ready, or timed out
	exitResourceExhausted  = 9 // rate limited
)

// exitCodeFor classifies err. gRPC statuses are consulted first (the server's
// mapError is the source of truth for online commands), then the domain
// sentinels offline commands surface directly, then transport failures.
func exitCodeFor(err error) int {
	if err == nil {
		return exitOK
	}
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.OK:
			return exitOK
		case codes.Unauthenticated:
			return exitUnauthenticated
		case codes.PermissionDenied:
			return exitPermissionDenied
		case codes.NotFound:
			return exitNotFound
		case codes.AlreadyExists, codes.Aborted:
			return exitConflict
		case codes.FailedPrecondition:
			return exitFailedPrecondition
		case codes.Unavailable, codes.DeadlineExceeded:
			return exitUnavailable
		case codes.ResourceExhausted:
			return exitResourceExhausted
		default:
			return exitError
		}
	}
	var usage usageError
	if errors.As(err, &usage) {
		return exitUsage
	}
	switch {
	case errors.Is(err, domain.ErrUnauthenticated):
		return exitUnauthenticated
	case errors.Is(err, domain.ErrPermissionDenied):
		return exitPermissionDenied
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, os.ErrNotExist):
		return exitNotFound
	case errors.Is(err, domain.ErrAlreadyExists), errors.Is(err, domain.ErrAborted), errors.Is(err, os.ErrExist):
		return exitConflict
	case errors.Is(err, domain.ErrFailedPrecondition):
		return exitFailedPrecondition
	case errors.Is(err, domain.ErrResourceExhausted):
		return exitResourceExhausted
	case errors.Is(err, domain.ErrNotReady), errors.Is(err, context.DeadlineExceeded):
		return exitUnavailable
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return exitUnavailable
	}
	return exitError
}

// failErr prints "error: <prefix>: <err>" to stderr and returns the exit code
// for err. Use it wherever the error came from an RPC, the store, or the
// filesystem; keep fail for messages the CLI itself composes.
func (c *CLI) failErr(prefix string, err error) int {
	if prefix == "" {
		_, _ = fmt.Fprintf(c.Stderr, "error: %v\n", err)
	} else {
		_, _ = fmt.Fprintf(c.Stderr, "error: %s: %v\n", prefix, err)
	}
	return exitCodeFor(err)
}
