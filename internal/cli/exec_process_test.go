package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

// The three-process dance this file runs: the test starts a real gRPC server,
// then a helper process that runs the real `exec` command against it, which in
// turn replaces itself with (or, on Windows, runs) a child that reports the
// environment it was given. Only a real process can show that the injected
// variables survive the platform's exec and that the child's status is the
// wrapper's.
const (
	execHelperEnv   = "KMS_EXEC_HELPER"   // marks the middle process
	execEndpointEnv = "KMS_EXEC_ENDPOINT" // where the middle process should dial
	execMarkerVar   = "KMS_TEST_MARKER"   // an injected parameter; marks the child
	execExitVar     = "KMS_TEST_EXIT"     // an injected parameter; the child's status
	execHostVar     = "KMS_TEST_HOST"     // an injected parameter
	execSecretVar   = "KMS_TEST_SECRET"   // an injected secret
	execLeakVar     = "KMS_SECRET_TOKEN_LEAKED"
	execIdentityVar = "KMS_TOKEN"

	execMarkerValue = "child-ready"
	execHostValue   = "db.internal"
	execSecretValue = "session-plaintext"
	execChildExit   = 7
)

// startTCPStubGRPC serves the registered stubs on a real loopback port, which
// is what a separate process can dial. startStubGRPC's in-memory listener
// cannot cross a process boundary.
func startTCPStubGRPC(t *testing.T, register func(*grpc.Server)) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	register(server)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return listener.Addr().String()
}

// execProcessTestEnv builds the helper's environment: the parent's, with every
// KMS_* setting removed so a developer's shell cannot steer the CLI, plus the
// variables this test controls.
func execProcessTestEnv(endpoint string) []string {
	env := make([]string, 0, 16)
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || strings.HasPrefix(name, "KMS_") {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		execHelperEnv+"=1",
		execEndpointEnv+"="+endpoint,
		// Inherited by the child: the identity credential is the child's to
		// reuse. The per-secret token beside it must not be.
		execIdentityVar+"=identity-token",
		execLeakVar+"=must-not-reach-the-child",
	)
}

// TestExecLaunchesARealProcess is the end-to-end check on every platform: the
// values reach a real child, the per-secret token does not, and the child's
// exit status is what the caller sees.
func TestExecLaunchesARealProcess(t *testing.T) {
	t.Parallel()
	if os.Getenv(execHelperEnv) != "" {
		t.Skip("this process is the exec helper")
	}
	rec := &envRecorder{}
	params := &envParameterStub{
		rec: rec,
		list: []*kmsv1.Parameter{
			{Ref: envTestRef("prod", "app", "kms-test/marker"), Value: execMarkerValue, ContentType: "string"},
			{Ref: envTestRef("prod", "app", "kms-test/exit"), Value: strconv.Itoa(execChildExit), ContentType: "integer"},
			{Ref: envTestRef("prod", "app", "kms-test/host"), Value: execHostValue, ContentType: "string"},
		},
		get:    map[string]*kmsv1.Parameter{},
		getErr: map[string]error{},
	}
	secrets := &envSecretStub{
		rec: rec,
		list: []*kmsv1.SecretMetadata{{
			Ref:    envTestRef("prod", "app", "kms-test/secret"),
			Labels: map[string]uint64{"current": 1},
			Versions: []*kmsv1.SecretVersionInfo{{
				Version: 1,
				State:   "enabled",
			}},
		}},
		get: map[string]*kmsv1.GetSecretResponse{
			"/prod/app/kms-test/secret": {
				Ref: envTestRef("prod", "app", "kms-test/secret"), Version: 1, Value: []byte(execSecretValue),
			},
		},
		getErr:       map[string]error{},
		requireToken: map[string]string{},
	}
	endpoint := startTCPStubGRPC(t, func(s *grpc.Server) {
		kmsv1.RegisterParameterServiceServer(s, params)
		kmsv1.RegisterSecretServiceServer(s, secrets)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestExecHelperProcess$")
	cmd.Env = execProcessTestEnv(endpoint)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("running the helper: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
		}
		code = exitErr.ExitCode()
	}
	if code != execChildExit {
		t.Fatalf("exit = %d, want the child's %d\nstdout=%s\nstderr=%s", code, execChildExit, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		execMarkerVar + "=" + execMarkerValue,
		execHostVar + "=" + execHostValue,
		execSecretVar + "=" + execSecretValue,
		// The identity credential is inherited; the per-secret token is not.
		execIdentityVar + "=identity-token",
		execLeakVar + "=<unset>",
	} {
		if !strings.Contains(stdout.String(), want+"\n") {
			t.Fatalf("child stdout missing %q:\nstdout=%s\nstderr=%s", want, stdout.String(), stderr.String())
		}
	}
}

// TestExecHelperProcess is the middle process: it runs the real CLI, which
// resolves the environment and hands it to the platform's launcher. It is a
// no-op unless the test above started it.
func TestExecHelperProcess(t *testing.T) {
	if os.Getenv(execHelperEnv) == "" {
		t.Skip("not the exec helper")
	}
	code := New().Run([]string{
		"exec", "prod/app",
		"--endpoint", os.Getenv(execEndpointEnv),
		"--insecure",
		"--", os.Args[0], "-test.run=^TestExecChildProcess$",
	})
	// On Unix the CLI has already replaced this process; reaching here means
	// the launch failed or the platform runs the command as a child.
	os.Exit(code)
}

// TestExecChildProcess is the launched command. It reports the environment it
// received and exits with the status the store supplied, so the test above can
// check both the injection and the status passthrough.
func TestExecChildProcess(t *testing.T) {
	if os.Getenv(execMarkerVar) == "" {
		t.Skip("not the exec child")
	}
	for _, name := range []string{execMarkerVar, execHostVar, execSecretVar, execIdentityVar, execLeakVar} {
		value, ok := os.LookupEnv(name)
		if !ok {
			value = "<unset>"
		}
		fmt.Printf("%s=%s\n", name, value)
	}
	code, err := strconv.Atoi(os.Getenv(execExitVar))
	if err != nil {
		fmt.Printf("bad %s: %v\n", execExitVar, err)
		os.Exit(90)
	}
	os.Exit(code)
}
