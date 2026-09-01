package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Suhaibinator/kms/internal/domain"
)

// fakeNetError is a transport failure that is not a gRPC status and not a
// domain sentinel: the last branch exitCodeFor consults. A hand-built type
// keeps the test free of a real socket (and of the sandbox's network policy).
type fakeNetError struct{ timeout bool }

func (e fakeNetError) Error() string   { return "dial tcp 127.0.0.1:1: connect: connection refused" }
func (e fakeNetError) Timeout() bool   { return e.timeout }
func (e fakeNetError) Temporary() bool { return true }

// TestExitCodeForGRPCStatus pins the exit code for every status the server's
// mapError can return. Scripts branch on these numbers, so a code moving is a
// breaking change and must fail here first.
func TestExitCodeForGRPCStatus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		code codes.Code
		want int
	}{
		{codes.OK, exitOK},
		{codes.Unauthenticated, exitUnauthenticated},
		{codes.PermissionDenied, exitPermissionDenied},
		{codes.NotFound, exitNotFound},
		{codes.AlreadyExists, exitConflict},
		{codes.Aborted, exitConflict},
		{codes.FailedPrecondition, exitFailedPrecondition},
		{codes.Unavailable, exitUnavailable},
		{codes.DeadlineExceeded, exitUnavailable},
		{codes.ResourceExhausted, exitResourceExhausted},
		{codes.InvalidArgument, exitError},
		{codes.Internal, exitError},
		{codes.Unknown, exitError},
		{codes.Canceled, exitError},
	} {
		t.Run(tc.code.String(), func(t *testing.T) {
			t.Parallel()
			// codes.OK cannot be carried by a non-nil status error, so assert
			// on the status value's Err() path the same way callers do.
			err := status.Error(tc.code, "boom")
			if tc.code == codes.OK {
				if err != nil {
					t.Fatalf("status.Error(OK) = %v, want nil", err)
				}
				if got := exitCodeFor(status.New(codes.OK, "").Err()); got != exitOK {
					t.Fatalf("exitCodeFor(OK status) = %d, want %d", got, exitOK)
				}
				return
			}
			if got := exitCodeFor(err); got != tc.want {
				t.Fatalf("exitCodeFor(%s) = %d, want %d", tc.code, got, tc.want)
			}
			// Commands add context with %w before failErr sees the error; the
			// classification must survive the wrap.
			wrapped := fmt.Errorf("listing parameters: %w", err)
			if got := exitCodeFor(wrapped); got != tc.want {
				t.Fatalf("exitCodeFor(wrapped %s) = %d, want %d", tc.code, got, tc.want)
			}
		})
	}
}

// TestExitCodeForDomainSentinel covers the offline commands, which surface the
// store's sentinels directly instead of a gRPC status. The numbers must match
// the online ones so a script sees the same code either way.
func TestExitCodeForDomainSentinel(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"ErrUnauthenticated", domain.ErrUnauthenticated, exitUnauthenticated},
		{"ErrPermissionDenied", domain.ErrPermissionDenied, exitPermissionDenied},
		{"ErrNotFound", domain.ErrNotFound, exitNotFound},
		{"ErrAlreadyExists", domain.ErrAlreadyExists, exitConflict},
		{"ErrAborted", domain.ErrAborted, exitConflict},
		{"ErrFailedPrecondition", domain.ErrFailedPrecondition, exitFailedPrecondition},
		{"ErrResourceExhausted", domain.ErrResourceExhausted, exitResourceExhausted},
		{"ErrNotReady", domain.ErrNotReady, exitUnavailable},
		// Not classified: an invalid argument or a failed decryption is a plain
		// error, code 1.
		{"ErrInvalidArgument", domain.ErrInvalidArgument, exitError},
		{"ErrDecryptFailed", domain.ErrDecryptFailed, exitError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := exitCodeFor(tc.err); got != tc.want {
				t.Fatalf("exitCodeFor(%v) = %d, want %d", tc.err, got, tc.want)
			}
			wrapped := fmt.Errorf("parameter /prod/api/x: %w", tc.err)
			if got := exitCodeFor(wrapped); got != tc.want {
				t.Fatalf("exitCodeFor(wrapped %v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// TestExitCodeForStdlibAndTransport covers the errors the filesystem and the
// transport produce, which have no gRPC status: a missing file is "not found"
// just like a missing parameter, and an unreachable server is "unavailable"
// whether the failure came from gRPC or from the dialer.
func TestExitCodeForStdlibAndTransport(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, exitOK},
		{"plain", errors.New("plain"), exitError},
		{"usageError", usageError("--token and --token-file are mutually exclusive"), exitUsage},
		{"wrapped usageError", fmt.Errorf("--token-file: %w", usageError("bad")), exitUsage},
		{"os.ErrNotExist", os.ErrNotExist, exitNotFound},
		{"PathError not exist", &os.PathError{Op: "open", Path: "/nope", Err: os.ErrNotExist}, exitNotFound},
		{"os.ErrExist", os.ErrExist, exitConflict},
		{"wrapped os.ErrExist", fmt.Errorf("backup file: %w", os.ErrExist), exitConflict},
		{"context.DeadlineExceeded", context.DeadlineExceeded, exitUnavailable},
		{"wrapped deadline", fmt.Errorf("rpc: %w", context.DeadlineExceeded), exitUnavailable},
		{"net.Error", fakeNetError{}, exitUnavailable},
		{"wrapped net.Error", fmt.Errorf("dialing kms:8443: %w", fakeNetError{}), exitUnavailable},
		{"timeout net.Error", fakeNetError{timeout: true}, exitUnavailable},
		{"net.OpError", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, exitUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := exitCodeFor(tc.err); got != tc.want {
				t.Fatalf("exitCodeFor(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// TestExitCodesAreDistinct guards the constants themselves: two kinds sharing
// a number (other than the deliberate AlreadyExists/Aborted pair, which share
// exitConflict) would make a script's branch ambiguous.
func TestExitCodesAreDistinct(t *testing.T) {
	t.Parallel()
	want := map[string]int{
		"ok": 0, "error": 1, "usage": 2, "unauthenticated": 3, "permission denied": 4,
		"not found": 5, "conflict": 6, "failed precondition": 7, "unavailable": 8,
		"resource exhausted": 9,
	}
	got := map[string]int{
		"ok": exitOK, "error": exitError, "usage": exitUsage,
		"unauthenticated": exitUnauthenticated, "permission denied": exitPermissionDenied,
		"not found": exitNotFound, "conflict": exitConflict,
		"failed precondition": exitFailedPrecondition, "unavailable": exitUnavailable,
		"resource exhausted": exitResourceExhausted,
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("exit code for %s = %d, want %d (documented in docs/operations.md)", name, got[name], w)
		}
	}
}

// TestFailErr checks both the message shape and that the returned code is the
// classified one: a command that writes "error: …" but returns 1 for a
// NotFound would defeat the whole scheme.
func TestFailErr(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		prefix   string
		err      error
		wantOut  string
		wantCode int
	}{
		{
			name:     "prefix",
			prefix:   "fetching secret",
			err:      status.Error(codes.NotFound, "no such secret"),
			wantOut:  "error: fetching secret: rpc error: code = NotFound desc = no such secret\n",
			wantCode: exitNotFound,
		},
		{
			name:     "empty prefix",
			prefix:   "",
			err:      errors.New("msg"),
			wantOut:  "error: msg\n",
			wantCode: exitError,
		},
		{
			name:     "empty prefix keeps the classified code",
			prefix:   "",
			err:      domain.ErrNotReady,
			wantOut:  "error: service not ready\n",
			wantCode: exitUnavailable,
		},
		{
			name:     "usage error",
			prefix:   "--token-file",
			err:      usageError("is empty"),
			wantOut:  "error: --token-file: is empty\n",
			wantCode: exitUsage,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newTestCLI()
			if code := c.failErr(tc.prefix, tc.err); code != tc.wantCode {
				t.Fatalf("failErr code = %d, want %d", code, tc.wantCode)
			}
			if got := c.stderr(); got != tc.wantOut {
				t.Fatalf("failErr stderr = %q, want %q", got, tc.wantOut)
			}
			// Errors never touch stdout: in JSON mode it carries exactly one
			// document and nothing else.
			if c.stdout() != "" {
				t.Fatalf("failErr wrote to stdout: %q", c.stdout())
			}
		})
	}
}

// TestFailKeepsGenericCode: fail is for messages the CLI composes itself, and
// those stay exit 1 even when the text mentions a resource that is missing.
func TestFailKeepsGenericCode(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	if code := c.fail("missing %s", "--env"); code != exitError {
		t.Fatalf("fail code = %d, want %d", code, exitError)
	}
	if got, want := c.stderr(), "error: missing --env\n"; got != want {
		t.Fatalf("fail stderr = %q, want %q", got, want)
	}
}

// TestFailErrIsNotSilencedByQuiet: --quiet suppresses progress lines only.
func TestFailErrIsNotSilencedByQuiet(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	c.quiet = true
	if code := c.failErr("", os.ErrNotExist); code != exitNotFound {
		t.Fatalf("failErr code = %d, want %d", code, exitNotFound)
	}
	if c.stderr() == "" {
		t.Fatal("--quiet silenced an error")
	}
}

// TestInvocationErrorsExitUsage pins exit 2 for the ways a command line can be
// wrong before anything is dialed or opened: a missing action or positional,
// an unknown action, a malformed namespace or version. None of these need a
// server, so an error other than 2 would mean the CLI tried to do work.
func TestInvocationErrorsExitUsage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args    []string
		wantErr string
	}{
		{[]string{"admin", "namespace"}, "requires an action"},
		{[]string{"admin", "namespace", "frobnicate"}, "unknown namespace action"},
		{[]string{"admin", "namespace", "create", "--env", "prod"}, "both --env and --app are required"},
		{[]string{"admin", "namespace", "create", "--env", "prod", "--app", "api", "--auth-methods", "carrier-pigeon"}, "auth method"},
		{[]string{"admin", "namespace", "delete", "--app", "api", "--yes"}, "both --env and --app are required"},
		{[]string{"admin", "identity"}, "requires an action"},
		{[]string{"admin", "identity", "frobnicate"}, "unknown identity action"},
		{[]string{"admin", "ca"}, "admin ca supports only: ca show"},
		{[]string{"release", "show", "prod/app", "rel"}, "requires ENV/APP NAME VERSION"},
		{[]string{"release", "validate", "prod/app", "rel", "v1"}, "invalid VERSION"},
		{[]string{"release", "activate", "prod:app", "rel", "1"}, "invalid namespace"},
		{[]string{"admin", "ca", "rotate"}, "admin ca supports only: ca show"},
		{[]string{"admin", "identity", "create"}, "requires a NAME argument"},
		{[]string{"admin", "identity", "create", "svc", "--namespace", "prod"}, "invalid --namespace"},
		{[]string{"admin", "identity", "issue-cert"}, "requires a NAME argument"},
		{[]string{"admin", "identity", "revoke-cert"}, "requires a NAME argument"},
		{[]string{"admin", "identity", "rotate"}, "requires a NAME argument"},
		{[]string{"admin", "identity", "revoke"}, "requires a NAME argument"},
		{[]string{"put-secret"}, "requires a /env/app/key argument"},
		{[]string{"get-secret"}, "requires a /env/app/key argument"},
		{[]string{"put-parameter", "/prod/api/key"}, "requires /env/app/key and VALUE"},
		{[]string{"list"}, "requires an env/app namespace argument"},
		{[]string{"release", "frobnicate"}, "unknown release command"},
		{[]string{"release", "create"}, "requires FILE or --file"},
		{[]string{"release", "list"}, "requires ENV/APP"},
		{[]string{"release", "diff", "prod/api", "rel"}, "requires ENV/APP NAME FROM_VERSION TO_VERSION"},
		{[]string{"release", "diff", "prod/api", "rel", "x", "2"}, "invalid FROM_VERSION"},
		{[]string{"release", "diff", "prod/api", "rel", "1", "y"}, "invalid TO_VERSION"},
		{[]string{"release", "rollback"}, "requires ENV/APP NAME"},
		{[]string{"release", "subscribers", "prod/api"}, "requires ENV/APP NAME"},
		{[]string{"release", "schema"}, "requires create, show, or list"},
		{[]string{"release", "schema", "frobnicate"}, "unknown release schema command"},
		{[]string{"release", "schema", "create", "id"}, "requires ID FILE"},
		{[]string{"release", "schema", "show", "id"}, "requires ID VERSION"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Parallel()
			c := newTestCLI()
			if code := c.Run(tc.args); code != exitUsage {
				t.Fatalf("Run(%v) = %d, want %d; stderr=%s", tc.args, code, exitUsage, c.stderr())
			}
			if !strings.Contains(c.stderr(), tc.wantErr) {
				t.Fatalf("Run(%v) stderr = %q, want it to mention %q", tc.args, c.stderr(), tc.wantErr)
			}
			if c.stdout() != "" {
				t.Fatalf("Run(%v) wrote stdout: %q", tc.args, c.stdout())
			}
		})
	}
}
