package configstore

import (
	"context"
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

func TestManagerStrictStartupMismatchIsTypedFatalAndNeverPublishes(t *testing.T) {
	var reports []DefaultMismatchReport
	published := 0
	aborted := 0
	manager := unitManager(Options{
		OnDefaultMismatch: func(report DefaultMismatchReport) { reports = append(reports, report) },
	}, func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) {
		return PreparedCandidate{
			Publish: func() { published++ },
			Abort:   func() { aborted++ },
			DefaultDifferences: []FieldDifference{{
				Path: "group.limit", Expected: 10, Actual: 20,
			}},
		}, nil
	})

	prepared, err := manager.prepareWithIdentity(context.Background(), kmsclient.ReleaseSnapshot{}, testIdentity(1, 1))
	if prepared != nil || err == nil {
		t.Fatalf("prepareWithIdentity() = (%v, %v), want rejection", prepared, err)
	}
	var mismatch *DefaultMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("errors.As(*DefaultMismatchError) = false: %v", err)
	}
	var candidateErr *CandidateError
	if !errors.As(err, &candidateErr) || candidateErr.ReleaseRejectionCategory() != string(RejectDefaultMismatch) {
		t.Fatalf("candidate category = %#v", candidateErr)
	}
	if mismatch.Phase() != MismatchStartup || mismatch.Severity() != MismatchFatal {
		t.Fatalf("mismatch phase/severity = %s/%s", mismatch.Phase(), mismatch.Severity())
	}
	if published != 0 || aborted != 1 || len(reports) != 1 {
		t.Fatalf("published=%d aborted=%d reports=%d", published, aborted, len(reports))
	}
	if manager.ready || manager.startupErr != mismatch {
		t.Fatalf("manager startup state = ready:%v error:%p, want error:%p", manager.ready, manager.startupErr, mismatch)
	}
}

func TestManagerBypassRuntimeDivergenceRestorationAndDeduplication(t *testing.T) {
	var reports []DefaultMismatchReport
	var next PreparedCandidate
	manager := unitManager(Options{
		AllowDefaultMismatch: true,
		OnDefaultMismatch:    func(report DefaultMismatchReport) { reports = append(reports, report) },
	}, func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) {
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
		t.Fatalf("bypassed startup prepare error = %v", err)
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
	manager := unitManager(Options{
		AllowDefaultMismatch: true,
		OnDefaultMismatch:    func(DefaultMismatchReport) { reports++ },
	}, func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) { return next, nil })

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
	manager := unitManager(Options{
		AllowDefaultMismatch: true,
		OnDefaultMismatch: func(DefaultMismatchReport) {
			callbacks++
			panic("secret callback panic")
		},
	}, func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) {
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
	manager := unitManager(Options{OnDefaultMismatch: func(DefaultMismatchReport) {}},
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
	manager := unitManager(Options{
		AllowDefaultMismatch: true,
		OnDefaultMismatch:    func(DefaultMismatchReport) {},
		OnCandidateRejected:  func(report CandidateRejectionReport) { reports = append(reports, report) },
	}, func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) {
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
	manager := unitManager(Options{
		OnDefaultMismatch: func(DefaultMismatchReport) {},
		OnCandidateRejected: func(report CandidateRejectionReport) {
			reports = append(reports, report)
			panic("callback panic must be isolated")
		},
	}, func(context.Context, kmsclient.ReleaseSnapshot) (PreparedCandidate, error) {
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
