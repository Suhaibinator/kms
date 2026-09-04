package grpcserver

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

func TestMapError_SentinelCodes(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		code    codes.Code
		wantMsg string // "" means don't assert the exact message
	}{
		{"nil", nil, codes.OK, ""},
		{"invalid", domain.Errorf(domain.ErrInvalidArgument, "bad path"), codes.InvalidArgument, ""},
		{"notfound", domain.Errorf(domain.ErrNotFound, "missing"), codes.NotFound, ""},
		{"exists", domain.Errorf(domain.ErrAlreadyExists, "dup"), codes.AlreadyExists, ""},
		{"unauth", domain.Errorf(domain.ErrUnauthenticated, "bad creds"), codes.Unauthenticated, "unauthenticated"},
		{"denied", domain.Errorf(domain.ErrPermissionDenied, "no"), codes.PermissionDenied, ""},
		{"precond", domain.Errorf(domain.ErrFailedPrecondition, "state"), codes.FailedPrecondition, ""},
		{"aborted", domain.Errorf(domain.ErrAborted, "cas"), codes.Aborted, ""},
		{"exhausted", domain.Errorf(domain.ErrResourceExhausted, "budget"), codes.ResourceExhausted, ""},
		{"purge-cleanup-pending", errors.Join(errors.New("cleanup detail"), storage.ErrPurgeCleanupPending), codes.Unavailable, storage.ErrPurgeCleanupPending.Error()},
		{"notready", domain.Errorf(domain.ErrNotReady, "starting"), codes.Unavailable, "service not ready"},
		{"decrypt", domain.ErrDecryptFailed, codes.Internal, "internal error"},
		{"wrapped-decrypt", domain.Errorf(domain.ErrDecryptFailed, "secret /a v1"), codes.Internal, "internal error"},
		{"unknown", errors.New("boom raw internal detail"), codes.Internal, "internal error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapError(nil, context.Background(), tc.err)
			if tc.err == nil {
				if got != nil {
					t.Fatalf("mapError(nil) = %v, want nil", got)
				}
				return
			}
			st, _ := status.FromError(got)
			if st.Code() != tc.code {
				t.Fatalf("code = %v, want %v", st.Code(), tc.code)
			}
			if tc.wantMsg != "" && st.Message() != tc.wantMsg {
				t.Fatalf("message = %q, want %q", st.Message(), tc.wantMsg)
			}
		})
	}
}

// TestMapError_NeverLeaksInternalDetail ensures decrypt and unknown errors are
// scrubbed to exactly "internal error" with no wrapped context.
func TestMapError_NeverLeaksInternalDetail(t *testing.T) {
	secretish := errors.New("kek_id=kek-7 ciphertext corrupt for /prod/db")
	got := mapError(nil, context.Background(), secretish)
	st, _ := status.FromError(got)
	if st.Message() != "internal error" {
		t.Fatalf("internal error leaked detail: %q", st.Message())
	}
}
