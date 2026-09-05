package cli

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	secretRPCFailureMessage        = "secret operation failed"
	purgeCleanupPendingWireMessage = "secret purge committed; database artifact cleanup is pending"
)

// redactSecretRPCError preserves the remote status code used for CLI exit-code
// classification while discarding its untrusted description. Secret-bearing
// requests contain plaintext or operator credentials, and a buggy or hostile
// peer must not be able to reflect those values into stderr.
func redactSecretRPCError(err error) error {
	return redactSecretRPCErrorForPurge(err, false)
}

// redactPurgeSecretRPCError additionally preserves the one fixed wire message
// that tells an operator the logical purge committed but artifact cleanup did
// not. It is recognized only on the purge mutation, with an exact code/message
// match; near matches and the same text on another RPC remain redacted.
func redactPurgeSecretRPCError(err error) error {
	return redactSecretRPCErrorForPurge(err, true)
}

func redactSecretRPCErrorForPurge(err error, allowCleanupPending bool) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, secretRPCFailureMessage)
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, secretRPCFailureMessage)
	}
	st, ok := status.FromError(err)
	if !ok {
		return errors.New(secretRPCFailureMessage)
	}
	if allowCleanupPending && st.Code() == codes.Unavailable && st.Message() == purgeCleanupPendingWireMessage {
		return status.Error(codes.Unavailable, purgeCleanupPendingWireMessage)
	}
	return status.Error(st.Code(), secretRPCFailureMessage)
}

func (c *CLI) failSecretRPC(prefix string, err error) int {
	return c.failErr(prefix, redactSecretRPCError(err))
}

func (c *CLI) failPurgeSecretRPC(prefix string, err error) int {
	return c.failErr(prefix, redactPurgeSecretRPCError(err))
}
