package kmsclient

import (
	"context"
	"errors"
	"strings"
	"testing"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	testHashA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testHashB = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	testHashS = "1111111111111111111111111111111111111111111111111111111111111111"
)

func TestVerifyReleaseDefaultsSendsHashesOnlyAndParsesVerdicts(t *testing.T) {
	client, server := newUnboundTestClient(t, Config{Token: "verify-token"})
	server.QueueVerifyReleaseDefaultsResponse(&kmsv1.VerifyReleaseDefaultsResponse{
		Name: "runtime", Version: 7, ActivationRevision: 42, SchemaMatches: true,
		Entries: []*kmsv1.VerifyEntryVerdict{
			{Alias: "database", Verdict: VerifyVerdictMatch},
			{Alias: "limits", Verdict: VerifyVerdictDiffers},
		},
		MatchCount: 1, DiffersCount: 1, UnverifiedCount: 2,
	}, nil)

	result, err := client.VerifyReleaseDefaults(context.Background(), VerifyReleaseDefaultsOptions{
		Namespace:    "prod/app",
		Release:      "runtime",
		Profile:      "prod",
		SchemaSHA256: testHashS,
		Entries: []VerifyDefaultsEntry{
			{Alias: "database", ContentType: "json", SHA256: testHashA},
			{Alias: "limits", ContentType: "json", SHA256: testHashB},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ReleaseName != "runtime" || result.ReleaseVersion != 7 || result.ActivationRevision != 42 || !result.SchemaMatches ||
		result.MatchCount != 1 || result.DiffersCount != 1 || result.UnverifiedCount != 2 || result.Passed() {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Entries) != 2 || result.Entries[0].Alias != "database" || result.Entries[0].Verdict != VerifyVerdictMatch ||
		result.Entries[1].Alias != "limits" || result.Entries[1].Verdict != VerifyVerdictDiffers {
		t.Fatalf("entries = %+v", result.Entries)
	}

	calls := server.VerifyReleaseDefaultsCalls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	request := calls[0]
	if request.GetNamespace().GetEnv() != "prod" || request.GetNamespace().GetApp() != "app" ||
		request.GetName() != "runtime" || request.GetProfile() != "prod" || request.GetSchemaSha256() != testHashS {
		t.Fatalf("request = %+v", request)
	}
	if len(request.GetEntries()) != 2 ||
		request.GetEntries()[0].GetAlias() != "database" || request.GetEntries()[0].GetContentType() != "json" || request.GetEntries()[0].GetSha256() != testHashA ||
		request.GetEntries()[1].GetAlias() != "limits" || request.GetEntries()[1].GetSha256() != testHashB {
		t.Fatalf("request entries = %+v", request.GetEntries())
	}
	if got := server.LastMetadata("VerifyReleaseDefaults").Get("authorization"); len(got) != 1 || got[0] != "Bearer verify-token" {
		t.Fatalf("authorization metadata = %v", got)
	}
}

func TestVerifyReleaseDefaultsPassedRequiresSchemaAndAllMatch(t *testing.T) {
	client, server := newUnboundTestClient(t, Config{})
	server.QueueVerifyReleaseDefaultsResponse(&kmsv1.VerifyReleaseDefaultsResponse{
		Name: "runtime", SchemaMatches: true,
		Entries: []*kmsv1.VerifyEntryVerdict{{Alias: "database", Verdict: VerifyVerdictMatch}},
	}, nil)
	server.QueueVerifyReleaseDefaultsResponse(&kmsv1.VerifyReleaseDefaultsResponse{
		Name: "runtime", SchemaMatches: false,
		Entries: []*kmsv1.VerifyEntryVerdict{{Alias: "database", Verdict: VerifyVerdictMatch}},
	}, nil)
	options := VerifyReleaseDefaultsOptions{
		Namespace: "prod/app",
		Entries:   []VerifyDefaultsEntry{{Alias: "database", ContentType: "json", SHA256: testHashA}},
	}
	passed, err := client.VerifyReleaseDefaults(context.Background(), options)
	if err != nil || !passed.Passed() {
		t.Fatalf("matching result = (%+v, %v)", passed, err)
	}
	failed, err := client.VerifyReleaseDefaults(context.Background(), options)
	if err != nil || failed.Passed() {
		t.Fatalf("schema-mismatch result = (%+v, %v)", failed, err)
	}
}

func TestVerifyReleaseDefaultsValidatesRequests(t *testing.T) {
	client, server := newUnboundTestClient(t, Config{})
	tests := []struct {
		name    string
		options VerifyReleaseDefaultsOptions
		want    string
	}{
		{name: "namespace", options: VerifyReleaseDefaultsOptions{Namespace: "invalid"}, want: "namespace"},
		{
			name:    "empty alias",
			options: VerifyReleaseDefaultsOptions{Namespace: "prod/app", Entries: []VerifyDefaultsEntry{{Alias: " ", SHA256: testHashA}}},
			want:    "empty alias",
		},
		{
			name: "duplicate alias",
			options: VerifyReleaseDefaultsOptions{Namespace: "prod/app", Entries: []VerifyDefaultsEntry{
				{Alias: "database", SHA256: testHashA}, {Alias: "database", SHA256: testHashB},
			}},
			want: "duplicated",
		},
		{
			name:    "uppercase hash",
			options: VerifyReleaseDefaultsOptions{Namespace: "prod/app", Entries: []VerifyDefaultsEntry{{Alias: "database", SHA256: strings.ToUpper(testHashA)}}},
			want:    "invalid sha256",
		},
		{
			name:    "short hash",
			options: VerifyReleaseDefaultsOptions{Namespace: "prod/app", Entries: []VerifyDefaultsEntry{{Alias: "database", SHA256: "abc"}}},
			want:    "invalid sha256",
		},
		{
			name:    "schema hash",
			options: VerifyReleaseDefaultsOptions{Namespace: "prod/app", SchemaSHA256: "not-hex"},
			want:    "schema sha256",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.VerifyReleaseDefaults(context.Background(), tt.options)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
		})
	}
	if calls := server.VerifyReleaseDefaultsCalls(); len(calls) != 0 {
		t.Fatalf("invalid requests reached the server: %d", len(calls))
	}
}

func TestVerifyReleaseDefaultsValidatesResponses(t *testing.T) {
	client, server := newUnboundTestClient(t, Config{})
	options := VerifyReleaseDefaultsOptions{
		Namespace: "prod/app",
		Entries: []VerifyDefaultsEntry{
			{Alias: "database", ContentType: "json", SHA256: testHashA},
			{Alias: "limits", ContentType: "json", SHA256: testHashB},
		},
	}
	tests := []struct {
		name     string
		response *kmsv1.VerifyReleaseDefaultsResponse
		want     string
	}{
		// A nil scripted response crosses the wire as an empty message.
		{name: "empty response", response: nil, want: "0 verdicts for 2 entries"},
		{
			name:     "verdict count mismatch",
			response: &kmsv1.VerifyReleaseDefaultsResponse{Entries: []*kmsv1.VerifyEntryVerdict{{Alias: "database", Verdict: VerifyVerdictMatch}}},
			want:     "1 verdicts for 2 entries",
		},
		{
			// A nil repeated element crosses the wire as an empty verdict.
			name: "empty verdict",
			response: &kmsv1.VerifyReleaseDefaultsResponse{Entries: []*kmsv1.VerifyEntryVerdict{
				{Alias: "database", Verdict: VerifyVerdictMatch}, nil,
			}},
			want: `unknown alias ""`,
		},
		{
			name: "unknown alias echoed",
			response: &kmsv1.VerifyReleaseDefaultsResponse{Entries: []*kmsv1.VerifyEntryVerdict{
				{Alias: "database", Verdict: VerifyVerdictMatch}, {Alias: "other", Verdict: VerifyVerdictMatch},
			}},
			want: `unknown alias "other"`,
		},
		{
			name: "unset verdict",
			response: &kmsv1.VerifyReleaseDefaultsResponse{Entries: []*kmsv1.VerifyEntryVerdict{
				{Alias: "database", Verdict: VerifyVerdictMatch}, {Alias: "limits"},
			}},
			want: "invalid verdict",
		},
		{
			name: "unbounded verdict",
			response: &kmsv1.VerifyReleaseDefaultsResponse{Entries: []*kmsv1.VerifyEntryVerdict{
				{Alias: "database", Verdict: VerifyVerdictMatch}, {Alias: "limits", Verdict: "value=hunter2"},
			}},
			want: "invalid verdict",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server.QueueVerifyReleaseDefaultsResponse(tt.response, nil)
			_, err := client.VerifyReleaseDefaults(context.Background(), options)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Fatalf("error echoed an unbounded server verdict: %v", err)
			}
		})
	}
}

func TestVerifyReleaseDefaultsMapsResourceExhaustedToRateLimited(t *testing.T) {
	client, server := newUnboundTestClient(t, Config{})
	server.QueueVerifyReleaseDefaultsResponse(nil, status.Error(codes.ResourceExhausted, "verify budget exhausted"))
	_, err := client.VerifyReleaseDefaults(context.Background(), VerifyReleaseDefaultsOptions{
		Namespace: "prod/app",
		Entries:   []VerifyDefaultsEntry{{Alias: "database", SHA256: testHashA}},
	})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
	server.QueueVerifyReleaseDefaultsResponse(nil, status.Error(codes.PermissionDenied, "verify-defaults not granted"))
	_, err = client.VerifyReleaseDefaults(context.Background(), VerifyReleaseDefaultsOptions{Namespace: "prod/app"})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("error = %v, want ErrPermissionDenied", err)
	}
}
