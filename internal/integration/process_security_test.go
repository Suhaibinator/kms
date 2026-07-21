package integration

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/crypto"
	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/storage"
)

// TestCLIProcessBackupRestoreSecurity builds and executes the actual command
// binary. The source database stays open behind a live TLS server during the
// backup, so this covers the online database path plus process argument parsing,
// secure staging/publication, no-replace restore, and restored-key readability.
func TestCLIProcessBackupRestoreSecurity(t *testing.T) {
	e := newLoopbackTLSEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	rootCtx := networkAuthContext(ctx, e.adminToken)
	admin := kmsv1.NewAdminServiceClient(e.adminConn)
	if _, err := admin.CreateNamespace(rootCtx, &kmsv1.CreateNamespaceRequest{
		Ref: networkNS("prod", "process-backup"), AllowedAuthMethods: []string{string(domain.AuthMethodToken)},
	}); err != nil {
		t.Fatalf("create backup namespace: %v", err)
	}
	if _, err := kmsv1.NewSecretServiceClient(e.adminConn).PutSecret(rootCtx, &kmsv1.PutSecretRequest{
		Ref:   networkRef("prod", "process-backup", "database-password"),
		Value: []byte("process-backup-secret-canary"), ContentType: "text/plain",
	}); err != nil {
		t.Fatalf("seed backup secret over TLS: %v", err)
	}

	binary := buildParameterStoreBinary(t, ctx)
	backupDir := t.TempDir()
	backupPath := filepath.Join(backupDir, "online-backup.db")
	if output, err := runParameterStoreProcess(ctx, binary, "backup", "--db", e.dbPath, "--out", backupPath); err != nil {
		t.Fatalf("CLI process online backup: %v\n%s", err, output)
	}
	assertOwnerOnlyFileMode(t, backupPath)

	restoreDir := t.TempDir()
	restoredPath := filepath.Join(restoreDir, "restored.db")
	if output, err := runParameterStoreProcess(ctx, binary, "restore", "--in", backupPath, "--db", restoredPath); err != nil {
		t.Fatalf("CLI process restore: %v\n%s", err, output)
	}
	assertOwnerOnlyFileMode(t, restoredPath)

	restored, err := storage.Open(restoredPath)
	if err != nil {
		t.Fatalf("open restored process database: %v", err)
	}
	defer func() { _ = restored.Close() }()
	keyring, err := crypto.Unseal(ctx, restored, crypto.UnsealOptions{KeyFilePath: e.keyPath})
	if err != nil {
		t.Fatalf("unseal restored process database: %v", err)
	}
	restoredService := core.New(restored, nil, "process-restore-check")
	restoredService.SetKeyring(keyring)
	value, err := restoredService.GetSecret(ctx, core.Principal{
		Identity: domain.Identity{Name: "network-root", Kind: domain.IdentityKindAdmin},
		Method:   domain.AuthMethodToken,
	}, domain.Ref{NS: domain.NamespaceRef{Env: "prod", App: "process-backup"}, Key: "database-password"}, 0, "")
	if err != nil {
		t.Fatalf("read secret from restored process database: %v", err)
	}
	if string(value.Value) != "process-backup-secret-canary" {
		t.Fatalf("restored secret = %q", value.Value)
	}

	t.Run("existing destination is never replaced without force", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "existing.db")
		const canary = "pre-existing-destination-canary"
		if err := os.WriteFile(destination, []byte(canary), 0o600); err != nil {
			t.Fatal(err)
		}
		output, err := runParameterStoreProcess(ctx, binary, "restore", "--in", backupPath, "--db", destination)
		if err == nil {
			t.Fatalf("restore replaced an existing destination without --force:\n%s", output)
		}
		got, readErr := os.ReadFile(destination)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(got) != canary {
			t.Fatalf("existing destination changed to %q; output:\n%s", got, output)
		}
	})

	t.Run("symlink destination and target are preserved", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("ordinary users cannot create symlinks on all Windows CI runners")
		}
		dir := t.TempDir()
		victim := filepath.Join(dir, "victim")
		const canary = "symlink-target-canary"
		if err := os.WriteFile(victim, []byte(canary), 0o600); err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(dir, "restore-link")
		if err := os.Symlink(victim, destination); err != nil {
			t.Fatal(err)
		}
		output, err := runParameterStoreProcess(ctx, binary, "restore", "--in", backupPath, "--db", destination)
		if err == nil {
			t.Fatalf("restore followed/replaced symlink destination without --force:\n%s", output)
		}
		if target, linkErr := os.Readlink(destination); linkErr != nil || target != victim {
			t.Fatalf("destination symlink = %q err=%v, want %q", target, linkErr, victim)
		}
		got, readErr := os.ReadFile(victim)
		if readErr != nil || string(got) != canary {
			t.Fatalf("symlink target = %q err=%v; output:\n%s", got, readErr, output)
		}
	})

	t.Run("shared writable parent fails closed", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX mode-boundary case")
		}
		parent := filepath.Join(t.TempDir(), "shared")
		if err := os.Mkdir(parent, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(parent, 0o777); err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(parent, "blocked.db")
		output, err := runParameterStoreProcess(ctx, binary, "restore", "--in", backupPath, "--db", destination)
		if err == nil {
			t.Fatalf("restore accepted shared writable parent:\n%s", output)
		}
		if _, statErr := os.Lstat(destination); !os.IsNotExist(statErr) {
			t.Fatalf("failed restore left destination behind: %v; output:\n%s", statErr, output)
		}
	})
}

func buildParameterStoreBinary(t *testing.T, ctx context.Context) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate integration test source")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	binary := filepath.Join(t.TempDir(), "parameter-store")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/parameter-store")
	cmd.Dir = repoRoot
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		t.Fatalf("build parameter-store integration binary: %v\n%s", err, output.String())
	}
	return binary
}

func runParameterStoreProcess(ctx context.Context, binary string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	return output.String(), err
}

func assertOwnerOnlyFileMode(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return // Windows DACL invariants have native build-tagged unit coverage.
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("%s mode = %04o, want 0600", path, got)
	}
}
