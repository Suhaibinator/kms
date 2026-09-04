package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
)

func TestCLISecretReadsWithLargeVersionHistory(t *testing.T) {
	binary := buildParameterStoreBinary(t)
	e := newLoopbackTLSEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	actx := networkAuthContext(ctx, e.adminToken)
	admin := kmsv1.NewAdminServiceClient(e.adminConn)
	secrets := kmsv1.NewSecretServiceClient(e.adminConn)
	_, err := admin.CreateNamespace(actx, &kmsv1.CreateNamespaceRequest{Ref: networkNS("prod", "review"), AllowedAuthMethods: []string{string(domain.AuthMethodToken)}})
	if err != nil {
		t.Fatal(err)
	}
	ref := networkRef("prod", "review", "history")
	// Legal per-version metadata accumulates beyond the default 4 MiB gRPC limit.
	for i := 0; i < 300; i++ {
		_, err = secrets.PutSecret(actx, &kmsv1.PutSecretRequest{Ref: ref, Value: []byte(fmt.Sprintf("value-%d", i+1)), ContentType: "text/plain", MetadataJson: `{"description":"` + strings.Repeat("a", 16350) + `"}`})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := secrets.GetSecret(actx, &kmsv1.GetSecretRequest{Ref: ref, Version: 7}); err != nil {
		t.Fatalf("direct exact read: %v", err)
	}

	releases := kmsv1.NewConfigurationReleaseServiceClient(e.adminConn)
	created, err := releases.CreateRelease(actx, &kmsv1.CreateReleaseRequest{
		Namespace: ref.GetNamespace(), Name: "runtime",
		Entries: []*kmsv1.ReleaseEntrySelector{{Alias: "history", Kind: "secret", Ref: ref, Version: 7}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := releases.ActivateRelease(actx, &kmsv1.ActivateReleaseRequest{
		Namespace: ref.GetNamespace(), Name: "runtime", Version: created.GetRelease().GetVersion(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"exact", []string{"get-secret", "/prod/review/history", "--version", "7"}, "value-7"},
		{"default", []string{"get-secret", "/prod/review/history"}, "value-300"},
		{"current", []string{"get-secret", "/prod/review/history", "--label", "current"}, "value-300"},
		{"previous", []string{"get-secret", "/prod/review/history", "--label", "previous"}, "value-299"},
		{"release", []string{"env", "prod/review", "--release", "runtime"}, "HISTORY=value-7"},
		{"bind", []string{"secret", "bind", "/prod/review/history"}, "Bound"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.CommandContext(ctx, binary, tc.args...)
			cmd.Env = append(os.Environ(), "KMS_ENDPOINT="+e.endpoint(), "KMS_CA_FILE="+e.caFile(t), "KMS_TOKEN="+e.adminToken, "KMS_BINDING_KEY=01234567890123456789012345678901")
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("CLI read failed: %v: %s", err, output)
			}
			if !strings.Contains(string(output), tc.want) {
				t.Fatalf("output = %q, want %q", output, tc.want)
			}
		})
	}
}
