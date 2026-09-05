package domain

import (
	"errors"
	"fmt"
)

// Sentinel errors. Transport layers map these onto gRPC codes / HTTP status
// codes. Wrap them with %w so errors.Is keeps working:
//
//	fmt.Errorf("parameter %s: %w", path, domain.ErrNotFound)
var (
	// ErrNotFound: the resource (or requested version/label) does not exist.
	ErrNotFound = errors.New("not found")

	// ErrAlreadyExists: creation conflicts with an existing resource.
	ErrAlreadyExists = errors.New("already exists")

	// ErrInvalidArgument: request validation failed (bad path, bad value...).
	ErrInvalidArgument = errors.New("invalid argument")

	// ErrUnauthenticated: missing or invalid credentials. Messages must stay
	// generic and never reveal whether a resource or identity exists.
	ErrUnauthenticated = errors.New("unauthenticated")

	// ErrPermissionDenied: authenticated but not authorized.
	ErrPermissionDenied = errors.New("permission denied")

	// ErrFailedPrecondition: state forbids the operation (disabled version,
	// destroyed version, binding-state mismatch, missing token...).
	ErrFailedPrecondition = errors.New("failed precondition")

	// ErrAborted: a compare-and-swap or other concurrency guard conflicted.
	ErrAborted = errors.New("aborted")

	// ErrResourceExhausted is returned when a per-identity budget (request or
	// mismatch) for a rate-limited operation has been spent.
	ErrResourceExhausted = errors.New("resource exhausted")

	// ErrDecryptFailed: a value could not be decrypted. Callers other than
	// admins receive it without further detail so a wrong binding key and a
	// corrupted ciphertext are indistinguishable.
	ErrDecryptFailed = errors.New("decryption failed")

	// ErrNotReady: the service has not acquired/verified the master key yet.
	ErrNotReady = errors.New("service not ready")
)

// Errorf wraps a sentinel with formatted context.
func Errorf(sentinel error, format string, args ...any) error {
	// Build a fresh argument slice rather than append(args, sentinel): appending
	// to the caller's variadic slice could clobber its backing array if it had
	// spare capacity.
	all := make([]any, 0, len(args)+1)
	all = append(all, args...)
	all = append(all, sentinel)
	return fmt.Errorf(format+": %w", all...)
}
