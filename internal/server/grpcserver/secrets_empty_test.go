package grpcserver

import (
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/internal/domain"
)

// TestEmptySecretValueOverGRPC stores a zero-byte secret (unbound and bound),
// reads it back empty, and rejects an empty value under a JSON content type.
func TestEmptySecretValueOverGRPC(t *testing.T) {
	env := newTestEnv(t, true)
	env.store.addNamespace(domain.NamespaceRef{Env: "prod", App: "svc"})
	client := env.secret()

	plain := pRef("prod", "svc", "empty")
	if _, err := client.PutSecretV03(adminCtx(), &kmsv1.PutSecretRequest{Ref: plain}); err != nil {
		t.Fatalf("put empty: %v", err)
	}
	got, err := client.GetSecret(adminCtx(), &kmsv1.GetSecretRequest{Ref: plain})
	if err != nil {
		t.Fatalf("get empty: %v", err)
	}
	if len(got.GetValue()) != 0 || got.GetVersion() != 1 || got.GetContentType() != "application/octet-stream" {
		t.Fatalf("empty secret = %+v", got)
	}

	bound := pRef("prod", "svc", "empty-bound")
	if _, err := client.PutSecretV03(adminCtx(), &kmsv1.PutSecretRequest{Ref: bound, Value: []byte{}, ContentType: "text/plain", BindingKey: grpcBindingKeyA}); err != nil {
		t.Fatalf("put bound empty: %v", err)
	}
	got, err = client.GetSecret(adminCtx(), &kmsv1.GetSecretRequest{Ref: bound, BindingKey: grpcBindingKeyA})
	if err != nil || len(got.GetValue()) != 0 {
		t.Fatalf("get bound empty = %+v, %v", got, err)
	}

	for _, contentType := range []string{"application/json", "application/json; charset=utf-8", "application/vnd.foo+json"} {
		_, err := client.PutSecretV03(adminCtx(), &kmsv1.PutSecretRequest{Ref: pRef("prod", "svc", "empty-json"), ContentType: contentType})
		if codeOf(err) != codes.InvalidArgument || !strings.Contains(status.Convert(err).Message(), "an empty value is not valid JSON") {
			t.Fatalf("empty %s put = %v, want InvalidArgument explaining JSON", contentType, err)
		}
	}
}
