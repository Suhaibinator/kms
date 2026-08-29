package kmsclient

import (
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Sentinel errors returned by the SDK. Callers should test with errors.Is.
// None of these values, nor any error wrapping them, ever contains secret
// plaintext.
var (
	// ErrNotFound is returned when a parameter or secret (or the requested
	// version/label) does not exist.
	ErrNotFound = errors.New("kmsclient: not found")

	// ErrPermissionDenied is returned when the caller is authenticated but not
	// authorized for the requested path or operation.
	ErrPermissionDenied = errors.New("kmsclient: permission denied")

	// ErrUnauthenticated is returned when the client identity token is missing,
	// invalid, or expired.
	ErrUnauthenticated = errors.New("kmsclient: unauthenticated")

	// ErrFailedPrecondition is returned when the request is well-formed but the
	// server state does not allow it (e.g. mode mismatch on a client-bound
	// secret).
	ErrFailedPrecondition = errors.New("kmsclient: failed precondition")

	// ErrAborted is returned when an optimistic concurrency guard fails. The
	// caller should obtain a fresh preview before retrying the operation.
	ErrAborted = errors.New("kmsclient: aborted")

	// ErrNotInitialized is reserved for compatibility. Declarative values do not
	// currently return it: SecretValue.Value panics with a descriptive message,
	// while ParameterValue.Get returns the empty string before Init/Resolve.
	ErrNotInitialized = errors.New("kmsclient: value not initialized")

	// ErrNoNamespace is returned when a relative key must be resolved but no
	// namespace is available: Config.Namespace is empty and the identity is
	// unbound (WhoAmI reports no namespace). Set Config.Namespace, bind the
	// identity to a namespace, or use an absolute "/env/app/key" display path.
	ErrNoNamespace = errors.New("kmsclient: no namespace")

	// ErrRateLimited is returned when the server has exhausted a per-identity
	// budget for the requested operation (for example VerifyReleaseDefaults).
	// Retry after the window resets; do not retry in a tight loop.
	ErrRateLimited = errors.New("kmsclient: rate limited")
)

// mapError translates a gRPC status error into one of the exported sentinel
// errors, preserving the server's (non-secret) message for context. Non-status
// errors are returned unchanged.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	switch st.Code() {
	case codes.OK:
		return nil
	case codes.NotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, st.Message())
	case codes.PermissionDenied:
		return fmt.Errorf("%w: %s", ErrPermissionDenied, st.Message())
	case codes.Unauthenticated:
		return fmt.Errorf("%w: %s", ErrUnauthenticated, st.Message())
	case codes.FailedPrecondition:
		return fmt.Errorf("%w: %s", ErrFailedPrecondition, st.Message())
	case codes.Aborted:
		return fmt.Errorf("%w: %s", ErrAborted, st.Message())
	case codes.ResourceExhausted:
		return fmt.Errorf("%w: %s", ErrRateLimited, st.Message())
	default:
		// Preserve the original status error (code + message) for everything
		// else (Unavailable, DeadlineExceeded, Internal, ...).
		return err
	}
}
