package configstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

func unitManager(options Options, prepare PrepareFunc) *Manager {
	return &Manager{
		options: options,
		prepare: prepare,
		readyCh: make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func testIdentity(version, revision uint64) ReleaseIdentity {
	return ReleaseIdentity{
		namespace:          "prod/app",
		name:               "runtime",
		version:            version,
		activationRevision: revision,
		digest:             "digest",
	}
}

func TestManagerStartupMismatchIsAppliedAndReportedAtErrorSeverity(t *testing.T) {
	var reports []DefaultMismatchReport
	var applied []AppliedReport
	published := 0
	aborted := 0
	manager := unitManager(Options{Callbacks: Callbacks{
		OnDefaultMismatch: func(report DefaultMismatchReport) { reports = append(reports, report) },
		OnApplied:         func(report AppliedReport) { applied = append(applied, report) },
	}}, func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) {
		return PreparedCandidate{
			Publish: func() { published++ },
			Abort:   func() { aborted++ },
			DefaultDifferences: []FieldDifference{{
				Path: "group.limit", Expected: 10, Actual: 20,
			}},
			// Startup has no previously applied generation, so supplied changes
			// must be ignored rather than reported as a reload.
			Changed: []FieldChange{{Path: "group.limit", Previous: 10, Current: 20}},
		}, nil
	})

	prepared, err := manager.prepareWithIdentity(context.Background(), kmsclient.ReleaseSnapshot{}, testIdentity(1, 1))
	if err != nil {
		t.Fatalf("startup mismatch prepare error = %v, want candidate admitted", err)
	}
	if len(reports) != 1 || reports[0].Phase() != PhaseStartup || reports[0].Severity() != MismatchError {
		t.Fatalf("startup mismatch reports = %d %v", len(reports), reports)
	}
	if published != 0 || manager.ready {
		t.Fatalf("candidate became visible before Commit: published=%d ready=%v", published, manager.ready)
	}
	prepared.Commit()
	if published != 1 || aborted != 0 {
		t.Fatalf("published=%d aborted=%d", published, aborted)
	}
	select {
	case <-manager.readyCh:
	default:
		t.Fatal("Commit did not close readyCh")
	}
	if !manager.ready || !manager.divergent || manager.applied.Version() != 1 {
		t.Fatalf("manager state ready=%v divergent=%v applied=%s", manager.ready, manager.divergent, manager.applied)
	}
	if len(applied) != 1 {
		t.Fatalf("OnApplied calls = %d, want 1", len(applied))
	}
	report := applied[0]
	if report.Phase() != PhaseStartup || !report.DefaultDivergent() || report.Release().Version() != 1 {
		t.Fatalf("applied report = %v", report)
	}
	if changes := report.Changed(); len(changes) != 0 {
		t.Fatalf("startup applied report Changed() = %#v, want empty", changes)
	}
}

func TestManagerRuntimeAppliedReportCarriesRedactedChanges(t *testing.T) {
	var applied []AppliedReport
	var next PreparedCandidate
	manager := unitManager(Options{Callbacks: Callbacks{
		OnDefaultMismatch: func(DefaultMismatchReport) {},
		OnApplied:         func(report AppliedReport) { applied = append(applied, report) },
	}}, func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) { return next, nil })

	next = PreparedCandidate{Publish: func() {}}
	initial, err := manager.prepareWithIdentity(context.Background(), kmsclient.ReleaseSnapshot{}, testIdentity(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	initial.Commit()

	secret := kmsclient.NewSecret([]byte("plaintext-canary"))
	groups := map[string]json.RawMessage{"group": json.RawMessage(`{"limit":30}`)}
	next = PreparedCandidate{
		Publish: func() {},
		Changed: []FieldChange{
			{Path: "group.limit", Previous: 10, Current: 30},
			{Path: "group.token", Previous: secret, Current: struct{ S kmsclient.Secret }{secret}},
			{Path: "not a path\n", Previous: 1, Current: 2},
		},
		Groups: func() (map[string]json.RawMessage, error) { return groups, nil },
	}
	reload, err := manager.prepareWithIdentity(context.Background(), kmsclient.ReleaseSnapshot{}, testIdentity(2, 2))
	if err != nil {
		t.Fatal(err)
	}
	reload.Commit()
	if len(applied) != 2 || applied[1].Phase() != PhaseRuntime || applied[1].DefaultDivergent() {
		t.Fatalf("applied reports = %v", applied)
	}
	changes := applied[1].Changed()
	want := []FieldChange{
		{Path: "group.limit", Previous: 10, Current: 30},
		{Path: "group.token", Previous: "[REDACTED]", Current: "[REDACTED]"},
		{Path: "invalid_path", Previous: 1, Current: 2},
	}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("Changed() = %#v, want %#v", changes, want)
	}
	changes[0].Path = "mutated"
	if applied[1].Changed()[0].Path != "group.limit" {
		t.Fatal("applied report changes were mutable")
	}
	for _, rendered := range []string{fmt.Sprint(applied[1]), fmt.Sprintf("%+v", applied[1]), fmt.Sprintf("%#v", applied[1])} {
		if strings.Contains(rendered, "plaintext-canary") || strings.Contains(rendered, "30") {
			t.Fatalf("applied report formatting leaked a value: %q", rendered)
		}
	}

	got, err := applied[1].Groups()
	if err != nil {
		t.Fatal(err)
	}
	if string(got["group"]) != `{"limit":30}` {
		t.Fatalf("Groups() = %s", got["group"])
	}
	got["group"][0] = 'X'
	again, _ := applied[1].Groups()
	if string(again["group"]) != `{"limit":30}` || string(groups["group"]) != `{"limit":30}` {
		t.Fatal("Groups() shared its buffers with the caller")
	}
}

func TestManagerAppliedReportGroupsIsEmptyWhenUnsupplied(t *testing.T) {
	var applied []AppliedReport
	manager := unitManager(Options{Callbacks: Callbacks{
		OnDefaultMismatch: func(DefaultMismatchReport) {},
		OnApplied:         func(report AppliedReport) { applied = append(applied, report) },
	}}, func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) {
		return PreparedCandidate{Publish: func() {}}, nil
	})
	prepared, err := manager.prepareWithIdentity(context.Background(), kmsclient.ReleaseSnapshot{}, testIdentity(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	prepared.Commit()
	groups, err := applied[0].Groups()
	if err != nil || groups == nil || len(groups) != 0 {
		t.Fatalf("Groups() = (%#v, %v), want empty non-nil map", groups, err)
	}
}

func TestManagerAppliedCallbackPanicDoesNotBlockReadiness(t *testing.T) {
	calls := 0
	manager := unitManager(Options{Callbacks: Callbacks{
		OnDefaultMismatch: func(DefaultMismatchReport) {},
		OnApplied: func(AppliedReport) {
			calls++
			panic("applied callback panic")
		},
	}}, func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) {
		return PreparedCandidate{Publish: func() {}}, nil
	})
	prepared, err := manager.prepareWithIdentity(context.Background(), kmsclient.ReleaseSnapshot{}, testIdentity(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	prepared.Commit()
	if calls != 1 || !manager.ready || manager.applied.Version() != 1 {
		t.Fatalf("calls=%d ready=%v applied=%s", calls, manager.ready, manager.applied)
	}
	select {
	case <-manager.readyCh:
	default:
		t.Fatal("readyCh not closed after panicking OnApplied")
	}
}

func TestManagedPreparedReportsDivergenceFieldCount(t *testing.T) {
	var next PreparedCandidate
	manager := unitManager(Options{Callbacks: Callbacks{OnDefaultMismatch: func(DefaultMismatchReport) {}}},
		func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) { return next, nil })

	next = PreparedCandidate{
		Publish: func() {},
		DefaultDifferences: []FieldDifference{
			{Path: "group.a", Expected: 1, Actual: 2},
			{Path: "group.b", Expected: 1, Actual: 3},
		},
	}
	prepared, err := manager.prepareWithIdentity(context.Background(), kmsclient.ReleaseSnapshot{}, testIdentity(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	reporter, ok := prepared.(kmsclient.ReleaseDivergenceReporter)
	if !ok {
		t.Fatalf("%T does not implement ReleaseDivergenceReporter", prepared)
	}
	if divergent, count := reporter.ReleaseDivergence(); !divergent || count != 2 {
		t.Fatalf("ReleaseDivergence() = (%v, %d), want (true, 2)", divergent, count)
	}
	prepared.Commit()

	next = PreparedCandidate{Publish: func() {}}
	prepared, err = manager.prepareWithIdentity(context.Background(), kmsclient.ReleaseSnapshot{}, testIdentity(2, 2))
	if err != nil {
		t.Fatal(err)
	}
	if divergent, count := prepared.(kmsclient.ReleaseDivergenceReporter).ReleaseDivergence(); divergent || count != 0 {
		t.Fatalf("ReleaseDivergence() = (%v, %d), want (false, 0)", divergent, count)
	}
	prepared.Abort()
}

func TestManagerRuntimeDivergenceRestorationAndDeduplication(t *testing.T) {
	var reports []DefaultMismatchReport
	var next PreparedCandidate
	manager := unitManager(Options{Callbacks: Callbacks{
		OnDefaultMismatch: func(report DefaultMismatchReport) { reports = append(reports, report) },
	}}, func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) {
		return next, nil
	})

	published := 0
	next = PreparedCandidate{
		Publish: func() { published++ },
		DefaultDifferences: []FieldDifference{{
			Path: "group.limit", Expected: 10, Actual: 20,
		}},
	}
	prepared, err := manager.prepareWithIdentity(context.Background(), kmsclient.ReleaseSnapshot{}, testIdentity(1, 1))
	if err != nil {
		t.Fatalf("startup prepare error = %v", err)
	}
	prepared.Commit()
	if !manager.ready || !manager.divergent || published != 1 || len(reports) != 1 {
		t.Fatalf("startup state ready=%v divergent=%v published=%d reports=%d", manager.ready, manager.divergent, published, len(reports))
	}
	if reports[0].Phase() != MismatchStartup || reports[0].Severity() != MismatchError {
		t.Fatalf("startup report = %s/%s", reports[0].Phase(), reports[0].Severity())
	}

	next = PreparedCandidate{
		Publish: func() { published++ },
		DefaultDifferences: []FieldDifference{{
			Path: "group.limit", Expected: 10, Actual: 30,
		}},
	}
	prepared, err = manager.prepareWithIdentity(context.Background(), kmsclient.ReleaseSnapshot{}, testIdentity(2, 2))
	if err != nil {
		t.Fatalf("runtime mismatch prepare error = %v", err)
	}
	prepared.Commit()
	if !manager.divergent || len(reports) != 2 || reports[1].Phase() != MismatchRuntime || reports[1].Severity() != MismatchError {
		t.Fatalf("runtime report/state = divergent:%v reports:%d", manager.divergent, len(reports))
	}

	// A reconciliation of the identical release candidate must not report a
	// second time.
	prepared, err = manager.prepareWithIdentity(context.Background(), kmsclient.ReleaseSnapshot{}, testIdentity(2, 2))
	if err != nil {
		t.Fatalf("repeat prepare error = %v", err)
	}
	prepared.Abort()
	if len(reports) != 2 {
		t.Fatalf("repeat callback count = %d", len(reports))
	}

	next = PreparedCandidate{Publish: func() { published++ }}
	prepared, err = manager.prepareWithIdentity(context.Background(), kmsclient.ReleaseSnapshot{}, testIdentity(3, 3))
	if err != nil {
		t.Fatalf("restoration prepare error = %v", err)
	}
	prepared.Commit()
	if manager.divergent || published != 3 || len(reports) != 2 {
		t.Fatalf("restoration state divergent=%v published=%d reports=%d", manager.divergent, published, len(reports))
	}
}

func TestManagerRejectsWholeRuntimeCandidateForRestartChange(t *testing.T) {
	published := 0
	aborted := 0
	reports := 0
	var next PreparedCandidate
	manager := unitManager(Options{Callbacks: Callbacks{
		OnDefaultMismatch: func(DefaultMismatchReport) { reports++ },
	}}, func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) { return next, nil })

	next = PreparedCandidate{Publish: func() { published++ }}
	initial, err := manager.prepareWithIdentity(context.Background(), kmsclient.ReleaseSnapshot{}, testIdentity(1, 1))
	if err != nil {
		t.Fatalf("initial prepare error = %v", err)
	}
	initial.Commit()

	next = PreparedCandidate{
		Publish: func() { published++ },
		Abort:   func() { aborted++ },
		DefaultDifferences: []FieldDifference{{
			Path: "group.hot", Expected: 1, Actual: 2,
		}},
		RestartRequiredFields: []string{"group.restart"},
	}
	prepared, err := manager.prepareWithIdentity(context.Background(), kmsclient.ReleaseSnapshot{}, testIdentity(2, 2))
	if prepared != nil || err == nil {
		t.Fatalf("runtime restart prepare = (%v, %v)", prepared, err)
	}
	var candidateErr *CandidateError
	if !errors.As(err, &candidateErr) || candidateErr.ReleaseRejectionCategory() != string(RejectRestartRequired) {
		t.Fatalf("candidate error = %#v", candidateErr)
	}
	if published != 1 || aborted != 1 || reports != 0 || manager.applied.version != 1 {
		t.Fatalf("LKG changed: published=%d aborted=%d reports=%d applied=%d", published, aborted, reports, manager.applied.version)
	}
}

func TestManagerRecoversMismatchCallbackPanicAndAborts(t *testing.T) {
	aborted := 0
	callbacks := 0
	manager := unitManager(Options{Callbacks: Callbacks{
		OnDefaultMismatch: func(DefaultMismatchReport) {
			callbacks++
			panic("secret callback panic")
		},
	}}, func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) {
		return PreparedCandidate{
			Publish:            func() { t.Fatal("candidate was published") },
			Abort:              func() { aborted++ },
			DefaultDifferences: []FieldDifference{{Path: "group.field", Expected: 1, Actual: 2}},
		}, nil
	})

	prepared, err := manager.prepareWithIdentity(context.Background(), kmsclient.ReleaseSnapshot{}, testIdentity(1, 1))
	if prepared != nil || err == nil {
		t.Fatalf("callback panic prepare = (%v, %v)", prepared, err)
	}
	var candidateErr *CandidateError
	if !errors.As(err, &candidateErr) || candidateErr.ReleaseRejectionCategory() != string(RejectInternal) {
		t.Fatalf("callback panic category = %#v", candidateErr)
	}
	if aborted != 1 {
		t.Fatalf("Abort count = %d", aborted)
	}
	prepared, err = manager.prepareWithIdentity(context.Background(), kmsclient.ReleaseSnapshot{}, testIdentity(1, 1))
	if prepared != nil || err == nil {
		t.Fatalf("reconciled callback panic prepare = (%v, %v)", prepared, err)
	}
	if callbacks != 1 || aborted != 2 {
		t.Fatalf("reconciliation callbacks=%d aborted=%d", callbacks, aborted)
	}
}

func TestManagerRequiresPublishAndAbortIsIdempotent(t *testing.T) {
	aborted := 0
	manager := unitManager(Options{Callbacks: Callbacks{OnDefaultMismatch: func(DefaultMismatchReport) {}}},
		func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) {
			return PreparedCandidate{Abort: func() { aborted++ }}, nil
		})
	prepared, err := manager.prepareWithIdentity(context.Background(), kmsclient.ReleaseSnapshot{}, testIdentity(1, 1))
	if prepared != nil || err == nil || aborted != 1 {
		t.Fatalf("missing Publish result = (%v, %v), aborted=%d", prepared, err, aborted)
	}

	manager.prepare = func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) {
		return PreparedCandidate{Publish: func() {}, Abort: func() { aborted++ }}, nil
	}
	prepared, err = manager.prepareWithIdentity(context.Background(), kmsclient.ReleaseSnapshot{}, testIdentity(2, 2))
	if err != nil {
		t.Fatalf("prepare error = %v", err)
	}
	prepared.Abort()
	prepared.Abort()
	if aborted != 2 {
		t.Fatalf("Abort count after duplicate calls = %d", aborted)
	}
}

func TestStartRejectsNilMismatchCallbackBeforeLoaderRuns(t *testing.T) {
	_, err := Start(context.Background(), &kmsclient.Client{}, Options{
		Release:  "runtime",
		Contract: []ContractEntry{{Alias: "group", Kind: ContractKindParameter, ContentType: "json"}},
	}, func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) {
		return PreparedCandidate{}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "OnDefaultMismatch") {
		t.Fatalf("Start() error = %v", err)
	}
}

func TestManagerReportsSafeRestartPathsOncePerCandidate(t *testing.T) {
	var reports []CandidateRejectionReport
	var next PreparedCandidate
	manager := unitManager(Options{Callbacks: Callbacks{
		OnDefaultMismatch:   func(DefaultMismatchReport) {},
		OnCandidateRejected: func(report CandidateRejectionReport) { reports = append(reports, report) },
	}}, func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) {
		return next, nil
	})

	next = PreparedCandidate{Publish: func() {}}
	initial, err := manager.prepareWithIdentity(context.Background(), kmsclient.ReleaseSnapshot{}, testIdentity(1, 1))
	if err != nil {
		t.Fatal(err)
	}
	initial.Commit()

	next = PreparedCandidate{
		Publish:               func() { t.Fatal("restart candidate published") },
		RestartRequiredFields: []string{"database.endpoint", "secret_alias", "unsafe\npath"},
	}
	for range 2 {
		prepared, prepareErr := manager.prepareWithIdentity(context.Background(), kmsclient.ReleaseSnapshot{}, testIdentity(2, 2))
		if prepared != nil || prepareErr == nil {
			t.Fatalf("restart prepare = (%v, %v), want rejection", prepared, prepareErr)
		}
	}
	if len(reports) != 1 {
		t.Fatalf("candidate rejection callback count = %d, want 1", len(reports))
	}
	if reports[0].Category() != RejectRestartRequired || reports[0].Release().Version() != 2 {
		t.Fatalf("restart report = %s/%s", reports[0].Category(), reports[0].Release())
	}
	if got := reports[0].Paths(); !reflect.DeepEqual(got, []string{"database.endpoint", "secret_alias"}) {
		t.Fatalf("restart paths = %#v", got)
	}
	paths := reports[0].Paths()
	paths[0] = "mutated"
	if reports[0].Paths()[0] != "database.endpoint" {
		t.Fatal("candidate rejection callback report was mutable")
	}
}

func TestManagerCandidateRejectionCallbackPanicCannotChangeAdmissionOrRepeat(t *testing.T) {
	const canary = "application-validation-canary"
	var reports []CandidateRejectionReport
	manager := unitManager(Options{Callbacks: Callbacks{
		OnDefaultMismatch: func(DefaultMismatchReport) {},
		OnCandidateRejected: func(report CandidateRejectionReport) {
			reports = append(reports, report)
			panic("callback panic must be isolated")
		},
	}}, func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) {
		return PreparedCandidate{}, Reject(RejectConfigValidationFailed, errors.New(canary))
	})

	for range 2 {
		prepared, err := manager.prepareWithIdentity(context.Background(), kmsclient.ReleaseSnapshot{}, testIdentity(7, 11))
		if prepared != nil || err == nil {
			t.Fatalf("validation prepare = (%v, %v), want rejection", prepared, err)
		}
		var candidateErr *CandidateError
		if !errors.As(err, &candidateErr) || candidateErr.ReleaseRejectionCategory() != string(RejectConfigValidationFailed) {
			t.Fatalf("callback panic changed original rejection: %#v", candidateErr)
		}
	}
	if len(reports) != 1 {
		t.Fatalf("panicking callback count = %d, want 1", len(reports))
	}
	if reports[0].Category() != RejectConfigValidationFailed || len(reports[0].Paths()) != 0 {
		t.Fatalf("validation report = category:%s paths:%#v", reports[0].Category(), reports[0].Paths())
	}
	for _, rendered := range []string{fmt.Sprint(reports[0]), fmt.Sprintf("%+v", reports[0]), fmt.Sprintf("%#v", reports[0])} {
		if strings.Contains(rendered, canary) {
			t.Fatalf("candidate report leaked validation text: %q", rendered)
		}
	}
}
