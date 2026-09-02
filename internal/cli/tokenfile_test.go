package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"

	"github.com/Suhaibinator/kms/internal/fileutil"
)

// writeTokenFile writes contents at mode inside a fresh private directory and
// returns the path. The file is created through fileutil.OpenPrivateExclusive
// rather than os.WriteFile so that it is owned by the current user on every
// platform (an elevated Windows session otherwise creates files owned by the
// Administrators group, which ReadPrivateFile rightly refuses). The mode is
// then applied with an explicit Chmod because the test environment's umask
// would otherwise decide it.
func writeTokenFile(t *testing.T, name, contents string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	f, err := fileutil.OpenPrivateExclusive(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if _, err := f.WriteString(contents); err != nil {
		_ = f.Close()
		t.Fatalf("write %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	return path
}

// TestReadTokenFileAcceptsOneToken: editors add a trailing newline, so one is
// tolerated (in either line ending), but nothing else is.
func TestReadTokenFileAcceptsOneToken(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		contents string
	}{
		{name: "trailing newline", contents: "tok\n"},
		{name: "crlf", contents: "tok\r\n"},
		{name: "no newline", contents: "tok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := readTokenFile(writeTokenFile(t, "token", tc.contents, 0o600))
			if err != nil {
				t.Fatalf("readTokenFile(%q) = %v", tc.contents, err)
			}
			if got != "tok" {
				t.Fatalf("readTokenFile(%q) = %q, want %q", tc.contents, got, "tok")
			}
		})
	}
}

// TestReadTokenFileRejectsAmbiguousContent: a truncated or misnamed file must
// not silently turn an authenticated call into an anonymous one, and a file
// holding two tokens (or a stray space) is a mistake worth reporting.
func TestReadTokenFileRejectsAmbiguousContent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		contents string
		want     string
	}{
		{name: "empty", contents: "", want: "is empty"},
		{name: "newline only", contents: "\n", want: "is empty"},
		{name: "two lines", contents: "one\ntwo\n", want: "exactly one token"},
		{name: "interior space", contents: "one two\n", want: "exactly one token"},
		{name: "interior tab", contents: "one\ttwo", want: "exactly one token"},
		{name: "leading newline", contents: "\ntok\n", want: "exactly one token"},
		{name: "two trailing newlines", contents: "tok\n\n", want: "exactly one token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := writeTokenFile(t, "token", tc.contents, 0o600)
			got, err := readTokenFile(path)
			if err == nil {
				t.Fatalf("readTokenFile(%q) = %q, want an error", tc.contents, got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("readTokenFile(%q) = %v, want it to mention %q", tc.contents, err, tc.want)
			}
			// The message names the file so the operator can find it.
			if !strings.Contains(err.Error(), path) {
				t.Fatalf("error %v does not name %s", err, path)
			}
		})
	}
}

// TestReadTokenFileRejectsUnsafeFiles: a bearer token readable by other local
// accounts, or reached through a symlink whose target can be swapped, is
// refused rather than repaired — relaxing the check would not revoke a handle
// another account may already hold.
func TestReadTokenFileRejectsUnsafeFiles(t *testing.T) {
	t.Parallel()

	t.Run("group and other readable", func(t *testing.T) {
		t.Parallel()
		path := writeTokenFile(t, "token", "tok\n", 0o644)
		if got, err := readTokenFile(path); err == nil {
			t.Fatalf("readTokenFile of a 0644 file = %q, want an error", got)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		t.Parallel()
		target := writeTokenFile(t, "token", "tok\n", 0o600)
		link := filepath.Join(filepath.Dir(target), "link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if got, err := readTokenFile(link); err == nil {
			t.Fatalf("readTokenFile of a symlink = %q, want an error", got)
		}
	})

	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "absent")
		got, err := readTokenFile(path)
		if err == nil {
			t.Fatalf("readTokenFile of a missing file = %q, want an error", got)
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("readTokenFile of a missing file = %v, want os.ErrNotExist", err)
		}
		// A missing credential file is a plain error: 5 means the server said
		// "not found", and a script's `5) echo "no such secret"` must not
		// fire on a mistyped --token-file path.
		if code := exitCodeFor(err); code != exitError {
			t.Fatalf("exitCodeFor = %d, want %d", code, exitError)
		}
	})

	t.Run("directory", func(t *testing.T) {
		t.Parallel()
		if got, err := readTokenFile(t.TempDir()); err == nil {
			t.Fatalf("readTokenFile of a directory = %q, want an error", got)
		}
	})
}

// newConnFlags builds a parsed connFlags bound to c, the way every command
// does, including the per-secret token flags.
func newConnFlags(t *testing.T, c *testCLI, args ...string) *connFlags {
	t.Helper()
	fs := c.newFlags("test")
	cf := addConnFlags(&c.CLI, fs)
	addSecretTokenFlags(fs, cf, "per-secret `token`")
	if !c.parseFlags(fs, args) {
		t.Fatalf("parseFlags(%v) failed: %s", args, c.stderr())
	}
	return cf
}

// TestFinalizeLoadsTokenFileFromEnv: KMS_TOKEN_FILE is the recommended way to
// hand a token to the CLI (a mounted credential file, never the process list),
// so finalize must read it into the token the RPC actually sends.
func TestFinalizeLoadsTokenFileFromEnv(t *testing.T) {
	t.Parallel()
	path := writeTokenFile(t, "token", "env-file-token\n", 0o600)
	c := newTestCLI()
	c.lookupEnv = mapLookup(map[string]string{"KMS_TOKEN_FILE": path})
	cf := newConnFlags(t, c)
	if err := cf.finalize(); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if cf.token != "env-file-token" {
		t.Fatalf("token = %q, want %q", cf.token, "env-file-token")
	}
}

// TestFinalizeLoadsSecretTokenFileFromEnv covers the per-secret credential.
func TestFinalizeLoadsSecretTokenFileFromEnv(t *testing.T) {
	t.Parallel()
	path := writeTokenFile(t, "secret-token", "per-secret\n", 0o600)
	c := newTestCLI()
	c.lookupEnv = mapLookup(map[string]string{"KMS_SECRET_TOKEN_FILE": path})
	cf := newConnFlags(t, c)
	if err := cf.finalize(); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if cf.secretToken != "per-secret" {
		t.Fatalf("secretToken = %q, want %q", cf.secretToken, "per-secret")
	}
}

// TestFinalizeRejectsTwoTokenSources: an inline token and a token file come
// from different places (a CI variable versus a mounted file), and silently
// preferring one would let a stale value shadow a rotated credential. It is a
// usage error, so the process exits 2.
func TestFinalizeRejectsTwoTokenSources(t *testing.T) {
	t.Parallel()
	tokenPath := writeTokenFile(t, "token", "from-file\n", 0o600)
	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{
			name: "--token with KMS_TOKEN_FILE",
			env:  map[string]string{"KMS_TOKEN_FILE": tokenPath},
			args: []string{"--token", "inline"},
			want: "--token and --token-file",
		},
		{
			name: "--token with --token-file",
			args: []string{"--token", "inline", "--token-file", tokenPath},
			want: "--token and --token-file",
		},
		{
			name: "--secret-token with --secret-token-file",
			args: []string{"--secret-token", "inline", "--secret-token-file", tokenPath},
			want: "--secret-token and --secret-token-file",
		},
		{
			name: "--secret-token with KMS_SECRET_TOKEN_FILE",
			env:  map[string]string{"KMS_SECRET_TOKEN_FILE": tokenPath},
			args: []string{"--secret-token", "inline"},
			want: "--secret-token and --secret-token-file",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newTestCLI()
			c.lookupEnv = mapLookup(tc.env)
			cf := newConnFlags(t, c, tc.args...)
			err := cf.finalize()
			if err == nil {
				t.Fatal("finalize accepted two token sources")
			}
			var usage usageError
			if !errors.As(err, &usage) {
				t.Fatalf("finalize error = %T (%v), want usageError", err, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("finalize error = %v, want it to mention %q", err, tc.want)
			}
			if code := exitCodeFor(err); code != exitUsage {
				t.Fatalf("exitCodeFor = %d, want %d", code, exitUsage)
			}
			// The conflict is reported instead of a credential being chosen.
			if cf.token == "from-file" {
				t.Fatal("finalize loaded the token file despite the conflict")
			}
		})
	}
}

// TestFinalizeReplaysError: finalize runs once (dial and authCtx both call
// it), so the second caller must see the same failure rather than a nil error
// and a half-resolved credential.
func TestFinalizeReplaysError(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	cf := newConnFlags(t, c, "--token", "inline", "--token-file", writeTokenFile(t, "token", "from-file\n", 0o600))
	first := cf.finalize()
	if first == nil {
		t.Fatal("finalize accepted two token sources")
	}
	second := cf.finalize()
	if second == nil || second.Error() != first.Error() {
		t.Fatalf("second finalize = %v, want the replayed %v", second, first)
	}
	// Repairing the fields afterwards does not re-run the body: the process
	// must not proceed on a credential the first call already rejected.
	cf.tokenFile = ""
	if third := cf.finalize(); third == nil || third.Error() != first.Error() {
		t.Fatalf("third finalize = %v, want the replayed %v", third, first)
	}
}

// TestFinalizeIsIdempotentOnSuccess: repeated calls neither re-read the token
// file nor change the resolved values.
func TestFinalizeIsIdempotentOnSuccess(t *testing.T) {
	t.Parallel()
	path := writeTokenFile(t, "token", "tok\n", 0o600)
	c := newTestCLI()
	cf := newConnFlags(t, c, "--token-file", path)
	if err := cf.finalize(); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	// Removing the file proves the second call does not touch the filesystem.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := cf.finalize(); err != nil {
		t.Fatalf("second finalize: %v", err)
	}
	if cf.token != "tok" {
		t.Fatalf("token = %q, want %q", cf.token, "tok")
	}
}

// TestDialConnRejectsBeforeDialing: dialConn resolves credentials first, so a
// bad token configuration fails without opening a connection — even in tests,
// where the transport is an in-memory override.
func TestDialConnRejectsBeforeDialing(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	dialed := false
	c.dialOverride = func(*connFlags) (*grpc.ClientConn, error) {
		dialed = true
		return nil, nil
	}
	cf := newConnFlags(t, c, "--token", "inline", "--token-file", writeTokenFile(t, "token", "from-file\n", 0o600))

	conn, err := c.dialConn(cf)
	if err == nil {
		t.Fatal("dialConn succeeded with two token sources")
	}
	if conn != nil {
		t.Fatal("dialConn returned a connection alongside an error")
	}
	if dialed {
		t.Fatal("dialConn invoked the transport before resolving credentials")
	}
	if code := exitCodeFor(err); code != exitUsage {
		t.Fatalf("exitCodeFor = %d, want %d", code, exitUsage)
	}
}

// TestDialConnUsesOverrideAfterFinalize is the companion: a well-formed
// configuration reaches the override with its credentials already resolved.
func TestDialConnUsesOverrideAfterFinalize(t *testing.T) {
	t.Parallel()
	c := newTestCLI()
	var seen *connFlags
	c.dialOverride = func(cf *connFlags) (*grpc.ClientConn, error) {
		seen = cf
		return nil, nil
	}
	cf := newConnFlags(t, c, "--token-file", writeTokenFile(t, "token", "tok\n", 0o600))
	if _, err := c.dialConn(cf); err != nil {
		t.Fatalf("dialConn: %v", err)
	}
	if seen == nil {
		t.Fatal("dialConn did not use the override")
	}
	if seen.token != "tok" {
		t.Fatalf("override saw token %q, want %q", seen.token, "tok")
	}
	if seen.endpoint != defaultEndpoint {
		t.Fatalf("override saw endpoint %q, want %q", seen.endpoint, defaultEndpoint)
	}
}
