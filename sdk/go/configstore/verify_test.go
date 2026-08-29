package configstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

type fakeVerifyClient struct {
	calls    []kmsclient.VerifyReleaseDefaultsOptions
	response kmsclient.VerifyReleaseDefaultsResult
	err      error
}

func (c *fakeVerifyClient) VerifyReleaseDefaults(_ context.Context, options kmsclient.VerifyReleaseDefaultsOptions) (kmsclient.VerifyReleaseDefaultsResult, error) {
	c.calls = append(c.calls, options)
	return c.response, c.err
}

func verifyTestInput() VerifyInput {
	return VerifyInput{
		SchemaSHA256: strings.Repeat("a", 64),
		Contract: []ContractEntry{
			{Alias: "limits", Kind: ContractKindParameter, ContentType: "json"},
			{Alias: "database", Kind: ContractKindParameter, ContentType: "json"},
			{Alias: "banner", Kind: ContractKindParameter, ContentType: "string"},
			{Alias: "db_password", Kind: ContractKindSecret},
		},
		Groups: map[string]json.RawMessage{
			"limits":   json.RawMessage(`{ "rate": 10, "burst": 20 }`),
			"database": json.RawMessage(`{"host":"db.internal","port":5432}`),
			"banner":   json.RawMessage(`hello   world`),
			// A group for a secret alias must be ignored, never hashed or sent.
			"db_password": json.RawMessage(`"hunter2-secret-canary"`),
		},
	}
}

func TestVerifyDefaultsSendsCanonicalHashesWithoutSecrets(t *testing.T) {
	client := &fakeVerifyClient{response: kmsclient.VerifyReleaseDefaultsResult{
		ReleaseName: "runtime", ReleaseVersion: 3, ActivationRevision: 9, SchemaMatches: true,
		Entries: []kmsclient.VerifyDefaultsVerdict{
			{Alias: "limits", Verdict: kmsclient.VerifyVerdictMatch},
			{Alias: "database", Verdict: kmsclient.VerifyVerdictMatch},
			{Alias: "banner", Verdict: kmsclient.VerifyVerdictMatch},
		},
		UnverifiedCount: 1,
	}}
	in := verifyTestInput()
	result, err := VerifyDefaults(context.Background(), client, in, VerifyOptions{Namespace: "prod/app", Release: "runtime", Profile: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("calls = %d", len(client.calls))
	}
	call := client.calls[0]
	if call.Namespace != "prod/app" || call.Release != "runtime" || call.Profile != "prod" || call.SchemaSHA256 != in.SchemaSHA256 {
		t.Fatalf("request = %+v", call)
	}
	if len(call.Entries) != 3 {
		t.Fatalf("entries = %+v, want three parameter aliases", call.Entries)
	}
	for _, entry := range call.Entries {
		contentType := ""
		for _, contract := range in.Contract {
			if contract.Alias == entry.Alias {
				contentType = contract.ContentType
			}
		}
		want, hashErr := ParameterHash(contentType, in.Groups[entry.Alias])
		if hashErr != nil {
			t.Fatal(hashErr)
		}
		if entry.SHA256 != want || entry.ContentType != contentType {
			t.Fatalf("entry %s = %+v, want hash %s", entry.Alias, entry, want)
		}
		if entry.Alias == "db_password" {
			t.Fatal("secret alias was sent")
		}
	}
	// Canonicalization is what makes the hash meaningful: whitespace and key
	// order must not change it for JSON, but bytes are exact for strings.
	compactHash, _ := ParameterHash("json", []byte(`{"burst":20,"rate":10}`))
	if call.Entries[0].SHA256 != compactHash {
		t.Fatalf("limits hash %s is not canonical (%s)", call.Entries[0].SHA256, compactHash)
	}
	rendered := fmt.Sprintf("%+v", call)
	if strings.Contains(rendered, "hunter2") || strings.Contains(rendered, "db.internal") {
		t.Fatalf("request carried a value: %s", rendered)
	}

	if !result.Passed() || result.Namespace != "prod/app" || result.ReleaseName != "runtime" || result.ReleaseVersion != 3 ||
		result.ActivationRevision != 9 || result.Unverified != 1 || len(result.Entries) != 3 {
		t.Fatalf("result = %+v", result)
	}
	if result.Entries[2].Alias != "banner" || result.Entries[2].ContentType != "string" {
		t.Fatalf("entry content types were not restored: %+v", result.Entries)
	}
}

func TestVerifyDefaultsRejectsMissingGroupAndBadInputs(t *testing.T) {
	client := &fakeVerifyClient{}
	in := verifyTestInput()
	delete(in.Groups, "database")
	_, err := VerifyDefaults(context.Background(), client, in, VerifyOptions{Namespace: "prod/app"})
	if err == nil || !strings.Contains(err.Error(), "missing encoded parameter group database") {
		t.Fatalf("missing group error = %v", err)
	}

	in = verifyTestInput()
	in.Groups["limits"] = json.RawMessage(`{"rate": 1,}`)
	_, err = VerifyDefaults(context.Background(), client, in, VerifyOptions{Namespace: "prod/app"})
	if err == nil || !strings.Contains(err.Error(), "hash parameter group limits") {
		t.Fatalf("invalid json error = %v", err)
	}

	if _, err := VerifyDefaults(context.Background(), nil, verifyTestInput(), VerifyOptions{Namespace: "prod/app"}); err == nil || !strings.Contains(err.Error(), "requires a client") {
		t.Fatalf("nil client error = %v", err)
	}
	if _, err := VerifyDefaults(context.Background(), client, verifyTestInput(), VerifyOptions{Namespace: "  "}); err == nil || !strings.Contains(err.Error(), "Namespace") {
		t.Fatalf("empty namespace error = %v", err)
	}
	if len(client.calls) != 0 {
		t.Fatalf("invalid inputs reached the client: %d calls", len(client.calls))
	}
}

func TestVerifyDefaultsWrapsRateLimitWithGuidance(t *testing.T) {
	client := &fakeVerifyClient{err: fmt.Errorf("%w: budget spent", kmsclient.ErrRateLimited)}
	_, err := VerifyDefaults(context.Background(), client, verifyTestInput(), VerifyOptions{Namespace: "prod/app"})
	if !errors.Is(err, kmsclient.ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
	if !strings.Contains(err.Error(), "wait for the window to reset") {
		t.Fatalf("rate limit error lacks guidance: %v", err)
	}

	other := errors.New("transport down")
	client = &fakeVerifyClient{err: other}
	_, err = VerifyDefaults(context.Background(), client, verifyTestInput(), VerifyOptions{Namespace: "prod/app"})
	if !errors.Is(err, other) || strings.Contains(err.Error(), "budget") {
		t.Fatalf("other error = %v", err)
	}
}

func TestVerifyResultVerdictsDrivePassedFailuresAndReport(t *testing.T) {
	verdicts := []string{
		kmsclient.VerifyVerdictMatch,
		kmsclient.VerifyVerdictDiffers,
		kmsclient.VerifyVerdictMissingInRelease,
		kmsclient.VerifyVerdictUnknownAlias,
		kmsclient.VerifyVerdictSecretAlias,
		kmsclient.VerifyVerdictUnsupportedContentType,
	}
	for _, verdict := range verdicts {
		t.Run(verdict, func(t *testing.T) {
			client := &fakeVerifyClient{response: kmsclient.VerifyReleaseDefaultsResult{
				ReleaseName: "runtime", ReleaseVersion: 2, ActivationRevision: 5, SchemaMatches: true,
				Entries: []kmsclient.VerifyDefaultsVerdict{
					{Alias: "limits", Verdict: kmsclient.VerifyVerdictMatch},
					{Alias: "database", Verdict: verdict},
					{Alias: "banner", Verdict: kmsclient.VerifyVerdictMatch},
				},
			}}
			result, err := VerifyDefaults(context.Background(), client, verifyTestInput(), VerifyOptions{Namespace: "prod/app"})
			if err != nil {
				t.Fatal(err)
			}
			wantPass := verdict == kmsclient.VerifyVerdictMatch
			if result.Passed() != wantPass {
				t.Fatalf("Passed() = %v for %s", result.Passed(), verdict)
			}
			failures := result.Failures()
			if wantPass {
				if len(failures) != 0 {
					t.Fatalf("Failures() = %+v", failures)
				}
			} else if len(failures) != 1 || failures[0].Alias != "database" || failures[0].Verdict != verdict || failures[0].ContentType != "json" {
				t.Fatalf("Failures() = %+v", failures)
			}

			report := result.Report()
			for _, value := range []string{"hunter2", "db.internal", "5432", "hello"} {
				if strings.Contains(report, value) {
					t.Fatalf("report contains a value %q:\n%s", value, report)
				}
			}
			if !strings.Contains(report, "prod/app runtime@2#5  schema: match") {
				t.Fatalf("report lacks identity line:\n%s", report)
			}
			lines := strings.Split(strings.TrimSpace(report), "\n")
			var aliasOrder []string
			for _, line := range lines {
				fields := strings.Fields(line)
				if len(fields) >= 2 && (fields[1] == "banner" || fields[1] == "database" || fields[1] == "limits") {
					aliasOrder = append(aliasOrder, fields[1])
				}
			}
			if strings.Join(aliasOrder, ",") != "banner,database,limits" {
				t.Fatalf("report aliases are not sorted: %v\n%s", aliasOrder, report)
			}
			summary := "summary: match=3 differs=0 missing_in_release=0 unknown_alias=0 secret_alias=0 unsupported_content_type=0 unverified=0"
			if !wantPass {
				summary = strings.Replace(strings.Replace(summary, "match=3", "match=2", 1), verdict+"=0", verdict+"=1", 1)
			}
			if !strings.Contains(report, summary) {
				t.Fatalf("report lacks summary %q:\n%s", summary, report)
			}
			wantResult := "result: active release differs from source defaults"
			if wantPass {
				wantResult = "result: active release matches source defaults"
			}
			if !strings.HasSuffix(strings.TrimSpace(report), wantResult) {
				t.Fatalf("report result line mismatch:\n%s", report)
			}
		})
	}
}

func TestVerifyResultSchemaMismatchFailsWithoutEntryFailures(t *testing.T) {
	client := &fakeVerifyClient{response: kmsclient.VerifyReleaseDefaultsResult{
		ReleaseName: "runtime", SchemaMatches: false,
		Entries: []kmsclient.VerifyDefaultsVerdict{
			{Alias: "limits", Verdict: kmsclient.VerifyVerdictMatch},
			{Alias: "database", Verdict: kmsclient.VerifyVerdictMatch},
			{Alias: "banner", Verdict: kmsclient.VerifyVerdictMatch},
		},
		UnverifiedCount: 4,
	}}
	result, err := VerifyDefaults(context.Background(), client, verifyTestInput(), VerifyOptions{Namespace: "prod/app"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed() || len(result.Failures()) != 0 {
		t.Fatalf("schema mismatch: passed=%v failures=%+v", result.Passed(), result.Failures())
	}
	report := result.Report()
	if !strings.Contains(report, "schema: differs") || !strings.Contains(report, "unverified=4") ||
		!strings.Contains(report, "result: active release differs from source defaults") {
		t.Fatalf("report:\n%s", report)
	}
}
