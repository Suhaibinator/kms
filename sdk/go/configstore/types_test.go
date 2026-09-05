package configstore

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

func TestCandidateErrorClassifiesUnwrapsAndRedacts(t *testing.T) {
	const canary = "SECRET-CANDIDATE-CAUSE"
	cause := errors.New(canary)
	err := Reject(RejectConfigValidationFailed, cause)

	var candidateErr *CandidateError
	if !errors.As(err, &candidateErr) {
		t.Fatalf("errors.As(*CandidateError) = false: %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatal("CandidateError did not unwrap cause")
	}
	if got := candidateErr.ReleaseRejectionCategory(); got != string(RejectConfigValidationFailed) {
		t.Fatalf("ReleaseRejectionCategory() = %q", got)
	}
	formats := []string{
		fmt.Sprintf("%s", err),
		fmt.Sprintf("%v", err),
		fmt.Sprintf("%+v", err),
		fmt.Sprintf("%#v", err),
		fmt.Sprintf("%q", err),
	}
	encoded, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatalf("json.Marshal() error = %v", marshalErr)
	}
	formats = append(formats, string(encoded))
	for _, output := range formats {
		if strings.Contains(output, canary) {
			t.Fatalf("formatted CandidateError leaked cause: %q", output)
		}
	}
}

func TestOptionsAndManagerFormattingRedactBindingKeys(t *testing.T) {
	const canary = "configstore-binding-key-format-canary"
	options := Options{
		Release: "runtime", BindingKeys: map[string]kmsclient.BindingKey{"password": kmsclient.NewBindingKey(canary)},
		SecretTokenProvider: func(string, string) (string, bool) { return canary, true },
	}
	manager := unitManager(options, func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) {
		return PreparedCandidate{}, nil
	})
	for label, value := range map[string]any{"options": options, "manager": manager} {
		for _, rendered := range []string{
			fmt.Sprintf("%v", value), fmt.Sprintf("%+v", value), fmt.Sprintf("%#v", value),
			fmt.Sprintf("%s", value), fmt.Sprintf("%q", value),
		} {
			if strings.Contains(rendered, canary) {
				t.Fatalf("%s formatting leaked binding key: %q", label, rendered)
			}
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s: %v", label, err)
		}
		if strings.Contains(string(encoded), canary) {
			t.Fatalf("%s JSON leaked binding key: %s", label, encoded)
		}
	}
}

func TestRejectNormalizesUnknownCategory(t *testing.T) {
	err := Reject(RejectionCategory("UNBOUNDED-SECRET-CATEGORY"), errors.New("cause"))
	var candidateErr *CandidateError
	if !errors.As(err, &candidateErr) {
		t.Fatal("errors.As(*CandidateError) = false")
	}
	if got := candidateErr.ReleaseRejectionCategory(); got != string(RejectInternal) {
		t.Fatalf("category = %q, want %q", got, RejectInternal)
	}
}

func TestDefaultMismatchReportIsDeeplyImmutableAndSecretFree(t *testing.T) {
	expected := map[string][]int{"values": {1, 2}}
	actual := struct {
		Secret kmsclient.Secret
	}{Secret: kmsclient.NewSecret([]byte("plaintext-canary"))}
	report := newDefaultMismatchReport(
		MismatchStartup,
		MismatchError,
		ReleaseIdentity{namespace: "prod/app", name: "runtime", version: 3, activationRevision: 9},
		[]FieldDifference{{Path: "group.field", Expected: expected, Actual: actual}},
	)

	expected["values"][0] = 99
	first := report.Fields()
	first[0].Expected.(map[string][]int)["values"][0] = 88
	first[0].Path = "changed"
	second := report.Fields()
	if second[0].Path != "group.field" || second[0].Expected.(map[string][]int)["values"][0] != 1 {
		t.Fatalf("report was mutable: %#v", second)
	}
	if second[0].Actual != "[REDACTED]" {
		t.Fatalf("secret-bearing report value was retained: %#v", second[0].Actual)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), "plaintext-canary") {
		t.Fatalf("report JSON leaked secret: %s", encoded)
	}
}

func TestDefaultMismatchReportFormattingNeverIncludesValues(t *testing.T) {
	const canary = "NONSECRET-VALUE-CANARY"
	report := newDefaultMismatchReport(
		MismatchStartup,
		MismatchError,
		ReleaseIdentity{name: "runtime", version: 2},
		[]FieldDifference{{Path: "group.field", Expected: canary, Actual: "other"}},
	)
	formats := []string{
		fmt.Sprintf("%s", report),
		fmt.Sprintf("%v", report),
		fmt.Sprintf("%+v", report),
		fmt.Sprintf("%#v", report),
		fmt.Sprintf("%q", report),
		fmt.Sprintf("%#v", any(report)),
	}
	for _, output := range formats {
		if strings.Contains(output, canary) {
			t.Fatalf("formatted DefaultMismatchReport leaked a value: %q", output)
		}
	}
	if got := report.Fields(); got[0].Expected != canary {
		t.Fatalf("Fields() expected = %#v", got)
	}
}

func TestAppliedReportFormattingNeverIncludesValues(t *testing.T) {
	const canary = "NONSECRET-CHANGE-CANARY"
	const secretCanary = "plaintext-secret-canary"
	groups := map[string]jsontext.Value{"group": jsontext.Value(`{"field":"` + canary + `"}`)}
	report := newAppliedReport(
		PhaseRuntime,
		ReleaseIdentity{namespace: "prod/app", name: "runtime", version: 5, activationRevision: 12},
		true,
		[]FieldChange{
			{Path: "group.field", Previous: "old", Current: canary},
			{Path: "group.secret", Previous: nil, Current: kmsclient.NewSecret([]byte(secretCanary))},
			{Path: "bad path\n", Previous: 1, Current: 2},
		},
		func() (map[string]jsontext.Value, error) { return groups, nil },
	)
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	formats := []string{
		fmt.Sprintf("%s", report),
		fmt.Sprintf("%v", report),
		fmt.Sprintf("%+v", report),
		fmt.Sprintf("%#v", report),
		fmt.Sprintf("%q", report),
		fmt.Sprintf("%#v", any(report)),
	}
	for _, output := range formats {
		if strings.Contains(output, canary) || strings.Contains(output, secretCanary) || strings.Contains(output, "\n") {
			t.Fatalf("formatted AppliedReport leaked a value: %q", output)
		}
		if !strings.Contains(output, "prod/app/runtime@5#12") || !strings.Contains(output, "group.field") {
			t.Fatalf("formatted AppliedReport lacks identity or paths: %q", output)
		}
	}
	// JSON is the explicit structured form: non-secret values are present,
	// secret values are redacted, unsafe paths are normalized.
	if strings.Contains(string(encoded), secretCanary) || strings.Contains(string(encoded), "bad path") {
		t.Fatalf("AppliedReport JSON leaked unsafe data: %s", encoded)
	}
	if !strings.Contains(string(encoded), canary) || !strings.Contains(string(encoded), `"invalid_path"`) {
		t.Fatalf("AppliedReport JSON = %s", encoded)
	}
	changes := report.Changed()
	if len(changes) != 3 || changes[0].Current != canary || changes[1].Current != "[REDACTED]" || changes[2].Path != "invalid_path" {
		t.Fatalf("Changed() = %#v", changes)
	}
	if report.Phase() != PhaseRuntime || !report.DefaultDivergent() || report.Release().Version() != 5 {
		t.Fatalf("report accessors = %s/%v/%s", report.Phase(), report.DefaultDivergent(), report.Release())
	}
}

func TestDefaultMismatchReportNormalizesUnsafeCallerPath(t *testing.T) {
	const unsafe = "field\nLOG-INJECTION"
	report := newDefaultMismatchReport(
		MismatchRuntime,
		MismatchError,
		ReleaseIdentity{name: "runtime", version: 2},
		[]FieldDifference{{Path: unsafe, Expected: 1, Actual: 2}},
	)
	if got := report.Fields()[0].Path; got != "invalid_path" {
		t.Fatalf("unsafe path = %q, want normalized placeholder", got)
	}
	for _, rendered := range []string{fmt.Sprint(report), fmt.Sprintf("%+v", report), fmt.Sprintf("%#v", report)} {
		if strings.Contains(rendered, "LOG-INJECTION") || strings.Contains(rendered, "\n") {
			t.Fatalf("unsafe mismatch path leaked through formatting: %q", rendered)
		}
	}
}

func TestReleaseIdentitySafeZeroRepresentation(t *testing.T) {
	identity := ReleaseIdentityFromSnapshot(kmsclient.ReleaseSnapshot{})
	if !identity.IsZero() || strings.Contains(identity.String(), "[REDACTED]") {
		t.Fatalf("unexpected zero identity: %s", identity.String())
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), "entries") || strings.Contains(string(encoded), "metadata") {
		t.Fatalf("identity JSON contains candidate data: %s", encoded)
	}
}

func TestCandidateRejectionReportIsImmutableBoundedAndValueFree(t *testing.T) {
	const canary = "SECRET-REJECTION-CAUSE"
	err := rejectWithPaths(RejectRestartRequired, errors.New(canary), []string{
		"database.endpoint.host",
		"runtime.items[][ *]",
		"runtime.value\nINJECTED",
		"database.endpoint.host",
	})
	var candidateErr *CandidateError
	if !errors.As(err, &candidateErr) {
		t.Fatal("errors.As(*CandidateError) = false")
	}
	report := newCandidateRejectionReport(
		RejectRestartRequired,
		ReleaseIdentity{namespace: "prod/app", name: "runtime", version: 4, activationRevision: 8},
		candidateErr.pathsCopy(),
	)
	paths := report.Paths()
	if !reflect.DeepEqual(paths, []string{"database.endpoint.host"}) {
		t.Fatalf("sanitized paths = %#v", paths)
	}
	paths[0] = "mutated"
	if got := report.Paths(); !reflect.DeepEqual(got, []string{"database.endpoint.host"}) {
		t.Fatalf("report paths were mutable: %#v", got)
	}

	encoded, marshalErr := json.Marshal(report)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	for _, rendered := range []string{
		fmt.Sprint(report), fmt.Sprintf("%+v", report), fmt.Sprintf("%#v", report), string(encoded),
	} {
		if strings.Contains(rendered, canary) || strings.Contains(rendered, "INJECTED") {
			t.Fatalf("candidate rejection report leaked unsafe data: %q", rendered)
		}
	}
	if report.Category() != RejectRestartRequired || report.Release().Version() != 4 {
		t.Fatalf("report identity/category = %s/%s", report.Category(), report.Release())
	}
}

func TestReportBufferedAndStreamingJSONPathsAgree(t *testing.T) {
	release := ReleaseIdentity{namespace: "prod/app", name: "runtime", version: 3, activationRevision: 5}
	tests := []struct {
		name     string
		value    any
		buffered func() ([]byte, error)
	}{
		{
			name:  "applied",
			value: newAppliedReport(PhaseRuntime, release, true, []FieldChange{{Path: "runtime.limit", Previous: 1, Current: 2}}, nil),
			buffered: func() ([]byte, error) {
				return newAppliedReport(PhaseRuntime, release, true, []FieldChange{{Path: "runtime.limit", Previous: 1, Current: 2}}, nil).MarshalJSON()
			},
		},
		{
			name:  "default mismatch",
			value: newDefaultMismatchReport(PhaseStartup, MismatchError, release, []FieldDifference{{Path: "runtime.limit", Expected: 1, Actual: 2}}),
			buffered: func() ([]byte, error) {
				return newDefaultMismatchReport(PhaseStartup, MismatchError, release, []FieldDifference{{Path: "runtime.limit", Expected: 1, Actual: 2}}).MarshalJSON()
			},
		},
		{
			name:  "candidate error",
			value: &CandidateError{category: RejectInternal, cause: errors.New("private diagnostic")},
			buffered: func() ([]byte, error) {
				return (&CandidateError{category: RejectInternal, cause: errors.New("private diagnostic")}).MarshalJSON()
			},
		},
		{
			name:  "candidate rejection",
			value: newCandidateRejectionReport(RejectRestartRequired, release, []string{"runtime.limit"}),
			buffered: func() ([]byte, error) {
				return newCandidateRejectionReport(RejectRestartRequired, release, []string{"runtime.limit"}).MarshalJSON()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			streamed, err := json.Marshal(test.value)
			if err != nil {
				t.Fatalf("streaming marshal: %v", err)
			}
			buffered, err := test.buffered()
			if err != nil {
				t.Fatalf("buffered marshal: %v", err)
			}
			if string(buffered) != string(streamed) {
				t.Fatalf("buffered = %s, streaming = %s", buffered, streamed)
			}
			if strings.Contains(string(streamed), "private diagnostic") {
				t.Fatalf("JSON leaked private diagnostic: %s", streamed)
			}
		})
	}
}

func BenchmarkAppliedReportMarshalJSONV2(b *testing.B) {
	report := newAppliedReport(
		PhaseRuntime,
		ReleaseIdentity{namespace: "prod/app", name: "runtime", version: 5, activationRevision: 12},
		true,
		[]FieldChange{
			{Path: "runtime.limit", Previous: 10, Current: 20},
			{Path: "runtime.endpoint", Previous: "old.internal", Current: "new.internal"},
		},
		nil,
	)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := json.Marshal(report); err != nil {
			b.Fatal(err)
		}
	}
}
