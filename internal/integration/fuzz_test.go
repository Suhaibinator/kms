package integration

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Suhaibinator/kms/internal/domain"
	"github.com/Suhaibinator/kms/internal/policy"
)

// FuzzValidateRules feeds arbitrary operation/path pairs through the policy
// validator (which transitively exercises the path/prefix parser) and then
// through evaluation. It must never panic; it must only ever return a normalized
// policy or an ErrInvalidArgument, and a normalized policy must re-validate
// unchanged. Run with:
//
//	go test ./internal/integration -run x -fuzz FuzzValidateRules
//
// Under `go test` (no -fuzz) the seed corpus runs as an ordinary test. (§25.4.2)
func FuzzValidateRules(f *testing.F) {
	seeds := []struct{ op, path string }{
		{"secret:read", "/prod/app/*"},
		{"parameter:*", "/prod"},
		{"*", "/*"},
		{"admin:key:rotate", "/prod/a/b/c"},
		{"", ""},
		{"bogus:op", "not-a-path"},
		{"secret:read", "/../../etc/passwd"},
		{"secret:read", "//double//slash"},
		{"parameter:read", "/a/b/*/c"},
		{"secret:*", "/x/*"},
	}
	for _, s := range seeds {
		f.Add(s.op, s.path)
	}

	f.Fuzz(func(t *testing.T, op, path string) {
		p := domain.Policy{
			Name:    "fuzz",
			Subject: "*",
			Allow:   []domain.PolicyRule{{Operation: op, Path: path}},
		}
		normalized, err := policy.ValidateRules(p)
		if err != nil {
			if !errors.Is(err, domain.ErrInvalidArgument) {
				t.Fatalf("ValidateRules returned non-ErrInvalidArgument error: %v", err)
			}
			return
		}
		// A normalized policy must be idempotent under re-validation.
		reNormalized, err := policy.ValidateRules(normalized)
		if err != nil {
			t.Fatalf("re-validating a normalized policy failed: %v", err)
		}
		if len(reNormalized.Allow) != len(normalized.Allow) ||
			(len(normalized.Allow) == 1 && reNormalized.Allow[0] != normalized.Allow[0]) {
			t.Fatalf("re-validation changed the policy: %+v -> %+v", normalized.Allow, reNormalized.Allow)
		}
		// A normalized policy must be safe to evaluate against arbitrary paths.
		_ = policy.Evaluate([]domain.Policy{normalized}, op, "/prod/app/thing")
		_ = policy.Evaluate([]domain.Policy{normalized}, "secret:read", path)
		_ = policy.MayListUnder([]domain.Policy{normalized}, op, "/prod")
	})
}

// FuzzMetadataJSON drives arbitrary metadata blobs through the real PutParameter
// path, exercising the metadata JSON parser through an exported core method. It
// must never panic, and must only ever accept the write or reject it with
// ErrInvalidArgument. (§25.4.3)
func FuzzMetadataJSON(f *testing.F) {
	h := newHarness(f)
	var mu sync.Mutex // serialize the shared store across parallel fuzz workers
	ctx := context.Background()

	for _, s := range []string{
		`{}`, `{"team":"payments"}`, ``, `not json`, `[1,2,3]`,
		`{"nested":{"a":1}}`, `"string"`, `null`, `123`, `{"unterminated":`,
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, metadata string) {
		mu.Lock()
		defer mu.Unlock()
		_, _, err := h.svc.PutParameter(ctx, h.admin, "/fuzz/metadata", "value", "string", metadata)
		if err != nil && !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("PutParameter with metadata %q returned unexpected error: %v", metadata, err)
		}
	})
}
