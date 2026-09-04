package integration

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
)

func TestBindingCLIExplicitGuardNeedsNoSecretReadPermission(t *testing.T) {
	e := newLoopbackTLSEnv(t)
	binary := buildParameterStoreBinary(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rootCtx := networkAuthContext(ctx, e.adminToken)
	admin := kmsv1.NewAdminServiceClient(e.adminConn)
	secrets := kmsv1.NewSecretServiceClient(e.adminConn)

	if _, err := admin.CreateNamespace(rootCtx, &kmsv1.CreateNamespaceRequest{
		Ref: networkNS("prod", "binding-cli"), AllowedAuthMethods: []string{string(domain.AuthMethodToken)},
	}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	identity, err := admin.CreateIdentity(rootCtx, &kmsv1.CreateIdentityRequest{
		Name: "binding-manager", Kind: domain.IdentityKindClient,
		AuthMethods: []string{string(domain.AuthMethodToken)},
	})
	if err != nil {
		t.Fatalf("create identity: %v", err)
	}
	if _, err := admin.CreatePolicy(rootCtx, &kmsv1.CreatePolicyRequest{Policy: &kmsv1.Policy{
		Name: "binding-manager-only", Subject: "binding-manager",
		Allow: []*kmsv1.PolicyRule{{Operation: domain.OpSecretBindingManage, Env: "prod", App: "binding-cli"}},
	}}); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	ref := networkRef("prod", "binding-cli", "credential")
	if _, err := secrets.PutSecret(rootCtx, &kmsv1.PutSecretRequest{Ref: ref, Value: []byte("value")}); err != nil {
		t.Fatalf("seed secret: %v", err)
	}

	baseEnv := append(os.Environ(),
		"KMS_ENDPOINT="+e.endpoint(),
		"KMS_CA_FILE="+e.caFile(t),
		"KMS_TOKEN="+identity.GetToken(),
	)
	run := func(env []string, args ...string) (string, error) {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = append(baseEnv, env...)
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		err := cmd.Run()
		return output.String(), err
	}
	assertReadDenied := func(env []string, args ...string) {
		t.Helper()
		output, err := run(env, args...)
		if err == nil || !strings.Contains(output, "PermissionDenied") {
			t.Fatalf("default metadata discovery = %v, output=%q; want permission denied", err, output)
		}
	}

	oldKey := []string{"KMS_BINDING_KEY=" + integrationBindingKeyA}
	assertReadDenied(oldKey, "secret", "bind", "/prod/binding-cli/credential")
	if output, err := run(oldKey, "secret", "bind", "/prod/binding-cli/credential", "--expected-current-version", "1"); err != nil {
		t.Fatalf("explicit bind: %v, output=%q", err, output)
	}

	rotateKeys := []string{"KMS_BINDING_KEY=" + integrationBindingKeyA, "KMS_NEW_BINDING_KEY=" + integrationBindingKeyB}
	assertReadDenied(rotateKeys, "binding-key", "rotate", "/prod/binding-cli/credential")
	if output, err := run(rotateKeys, "binding-key", "rotate", "/prod/binding-cli/credential", "--expected-current-version", "2"); err != nil {
		t.Fatalf("explicit rotate: %v, output=%q", err, output)
	}

	newKey := []string{"KMS_BINDING_KEY=" + integrationBindingKeyB}
	assertReadDenied(newKey, "secret", "unbind", "/prod/binding-cli/credential")
	if output, err := run(newKey, "secret", "unbind", "/prod/binding-cli/credential", "--expected-current-version", "2"); err == nil || !strings.Contains(output, "Aborted") {
		t.Fatalf("stale explicit unbind = %v, output=%q; want CAS rejection", err, output)
	}
	metadata, err := secrets.GetSecretMetadata(rootCtx, &kmsv1.GetSecretMetadataRequest{Ref: ref})
	if err != nil || metadata.GetSecret().GetLabels()["current"] != 3 || !metadata.GetSecret().GetBound() {
		t.Fatalf("stale guard mutated secret: metadata=%+v err=%v", metadata.GetSecret(), err)
	}
	if output, err := run(newKey, "secret", "unbind", "/prod/binding-cli/credential", "--expected-current-version", "3"); err != nil {
		t.Fatalf("explicit unbind: %v, output=%q", err, output)
	}
}
