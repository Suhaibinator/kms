package configkms

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fixtureconfig "github.com/Suhaibinator/kms/internal/configstorefixture/config"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient/kmsclienttest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type faultStartOptions struct {
	reconcileInterval   time.Duration
	onDefaultMismatch   func(configstore.DefaultMismatchReport)
	onCandidateRejected func(configstore.CandidateRejectionReport)
	secretTokenProvider kmsclient.SecretTokenProvider
}

// startFaultFixture enters through the generated public API so callback wiring,
// preparation, and immutable publication all remain in the exercised path.
func startFaultFixture(t *testing.T, initial releaseData, options faultStartOptions) *runningFixture {
	t.Helper()
	server, err := kmsclienttest.New()
	if err != nil {
		t.Fatal(err)
	}
	installInitial(t, server, initial)
	client := newFixtureClient(t, server)
	ctx, cancel := context.WithCancel(context.Background())
	if options.reconcileInterval == 0 {
		options.reconcileInterval = time.Hour
	}
	if options.onDefaultMismatch == nil {
		options.onDefaultMismatch = func(configstore.DefaultMismatchReport) {}
	}
	store, err := Start(ctx, client, Options{
		Release:  fixtureReleaseName,
		Defaults: fixtureconfig.Defaults,
		Callbacks: configstore.Callbacks{
			OnDefaultMismatch:   options.onDefaultMismatch,
			OnCandidateRejected: options.onCandidateRejected,
		},
		SecretTokenProvider:  options.secretTokenProvider,
		ReconcileInterval:    options.reconcileInterval,
		MaxConcurrentFetches: 4,
		InstanceID:           "managed-fault-instance",
	})
	if err != nil {
		cancel()
		_ = client.Close()
		server.Close()
		t.Fatal(err)
	}
	sub, err := server.WaitForReleaseSubscribe(testOperationTimeout)
	if err != nil {
		cancel()
		_ = store.Wait()
		_ = client.Close()
		server.Close()
		t.Fatal(err)
	}
	fixture := &runningFixture{server: server, client: client, store: store, sub: sub, cancel: cancel}
	waitAcknowledgement(t, sub, initial.releaseVersion, kmsclient.ReleaseStateApplied)
	t.Cleanup(func() {
		cancel()
		if err := store.Wait(); err != nil {
			t.Errorf("managed fault store shutdown: %v", err)
		}
		if err := client.Close(); err != nil {
			t.Errorf("managed fault client close: %v", err)
		}
		server.Close()
	})
	return fixture
}

func waitFaultRejectionReport(t *testing.T, reports <-chan configstore.CandidateRejectionReport) configstore.CandidateRejectionReport {
	t.Helper()
	select {
	case report := <-reports:
		return report
	case <-time.After(testOperationTimeout):
		t.Fatal("timed out waiting for candidate rejection report")
		return nil
	}
}

func TestFaultReconciliationRetriesTransientResolutionAndDeduplicatesManagedRejection(t *testing.T) {
	const resolutionCanary = "TRANSIENT-RESOLUTION-SECRET-CANARY"
	reports := make(chan configstore.CandidateRejectionReport, 8)
	var reportCalls atomic.Int32
	fixture := startFaultFixture(t, matchingRelease(1, 101), faultStartOptions{
		reconcileInterval: 25 * time.Millisecond,
		onCandidateRejected: func(report configstore.CandidateRejectionReport) {
			reportCalls.Add(1)
			reports <- report
		},
	})

	transient := matchingRelease(2, 102)
	scriptResources(fixture.server, transient)
	fixture.server.SetParameterError(fixtureNamespace, databasePath, status.Error(codes.Unavailable, resolutionCanary))
	if _, err := fixture.server.ActivateConfigurationRelease(releaseSpec(transient), transient.activationRevision); err != nil {
		t.Fatal(err)
	}
	ack := waitAcknowledgement(t, fixture.sub, 2, kmsclient.ReleaseStateRejected)
	if ack.GetRejectionCategory() != kmsclient.ReleaseRejectResolutionFailed || ack.GetDiagnostic() != "" {
		t.Fatalf("transient rejection ack = category %q diagnostic %q", ack.GetRejectionCategory(), ack.GetDiagnostic())
	}
	if got := fixture.store.Current().Release().Version(); got != 1 {
		t.Fatalf("transient resolution failure displaced LKG with release %d", got)
	}
	if reportCalls.Load() != 0 {
		t.Fatalf("preparation-only callback ran for lower-level resolution failure %d times", reportCalls.Load())
	}
	for _, rendered := range []string{fmt.Sprint(ack), fmt.Sprintf("%+v", fixture.store.Status())} {
		if strings.Contains(rendered, resolutionCanary) {
			t.Fatalf("resolution failure leaked through acknowledgement/status: %q", rendered)
		}
	}

	// No new activation is sent. A later reconciliation of the same active
	// identity must retry it after the transient transport failure clears.
	fixture.server.SetParameterError(fixtureNamespace, databasePath, nil)
	waitAcknowledgement(t, fixture.sub, 2, kmsclient.ReleaseStateApplied)
	if got := waitAppliedVersion(t, fixture.store, 2).Release().ActivationRevision(); got != 102 {
		t.Fatalf("reconciled release activation revision = %d, want 102", got)
	}

	invalid := matchingRelease(3, 103)
	invalid.databaseDocument = databaseDocument("db.internal", "0s", 20)
	activate(t, fixture, invalid)
	ack = waitAcknowledgement(t, fixture.sub, 3, kmsclient.ReleaseStateRejected)
	if ack.GetRejectionCategory() != string(configstore.RejectConfigValidationFailed) || ack.GetDiagnostic() != "" {
		t.Fatalf("validation rejection ack = category %q diagnostic %q", ack.GetRejectionCategory(), ack.GetDiagnostic())
	}
	report := waitFaultRejectionReport(t, reports)
	if report.Category() != configstore.RejectConfigValidationFailed || report.Release().Version() != 3 || len(report.Paths()) != 0 {
		t.Fatalf("validation report = category:%s release:%s paths:%#v", report.Category(), report.Release(), report.Paths())
	}
	// Reconciliation should retry a permanently invalid active candidate, but
	// local diagnostics remain once-per-candidate rather than once-per poll.
	waitRejectedCount(t, fixture.store, configstore.RejectConfigValidationFailed, 2)
	if got := reportCalls.Load(); got != 1 {
		t.Fatalf("candidate rejection callback calls = %d, want 1", got)
	}
	select {
	case duplicate := <-reports:
		t.Fatalf("duplicate reconciliation callback: %v", duplicate)
	default:
	}
	if got := fixture.store.Current().Release().Version(); got != 2 {
		t.Fatalf("invalid reconciled candidate displaced release 2 with %d", got)
	}

	recovered := matchingRelease(4, 104)
	activate(t, fixture, recovered)
	waitAcknowledgement(t, fixture.sub, 4, kmsclient.ReleaseStateApplied)
	waitAppliedVersion(t, fixture.store, 4)
}

func TestFaultPrefetchContractFailureReportsIdentityBeforeAnyResourceRead(t *testing.T) {
	const secretCanary = "PREFETCH-MUST-NOT-READ-SECRET-CANARY"
	reports := make(chan configstore.CandidateRejectionReport, 1)
	var fetches atomic.Int64
	var tokenLookups atomic.Int64
	fixture := startFaultFixture(t, matchingRelease(1, 101), faultStartOptions{
		onCandidateRejected: func(report configstore.CandidateRejectionReport) { reports <- report },
		secretTokenProvider: func(string, string) (string, bool) {
			tokenLookups.Add(1)
			return "unused-token", true
		},
	})
	fixture.server.SetGetParameterHook(func(string) { fetches.Add(1) })

	bad := matchingRelease(2, 102)
	bad.passwordValue = []byte(secretCanary)
	bad.runtimeTokenValue = []byte(secretCanary)
	bad.databaseContentType = "text/plain"
	scriptResources(fixture.server, bad)
	spec := releaseSpec(bad)
	for i := range spec.Entries {
		if spec.Entries[i].Kind == "secret" {
			spec.Entries[i].HasAccessToken = true
		}
	}
	if _, err := fixture.server.ActivateConfigurationRelease(spec, bad.activationRevision); err != nil {
		t.Fatal(err)
	}
	ack := waitAcknowledgement(t, fixture.sub, 2, kmsclient.ReleaseStateRejected)
	if ack.GetRejectionCategory() != string(configstore.RejectConfigContractMismatch) || ack.GetDiagnostic() != "" {
		t.Fatalf("contract rejection ack = category %q diagnostic %q", ack.GetRejectionCategory(), ack.GetDiagnostic())
	}
	report := waitFaultRejectionReport(t, reports)
	identity := report.Release()
	if report.Category() != configstore.RejectConfigContractMismatch || len(report.Paths()) != 0 ||
		identity.Namespace() != fixtureNamespace || identity.Name() != fixtureReleaseName ||
		identity.Version() != 2 || identity.ActivationRevision() != 102 ||
		identity.SchemaVersion() != 1 || identity.Digest() == "" {
		t.Fatalf("prefetch report = category:%s identity:%s paths:%#v", report.Category(), identity, report.Paths())
	}
	if fetches.Load() != 0 || tokenLookups.Load() != 0 {
		t.Fatalf("contract rejection performed resource work: fetches=%d tokens=%d", fetches.Load(), tokenLookups.Load())
	}
	statusSnapshot := fixture.store.Status()
	if statusSnapshot.Observed.Version() != 2 || statusSnapshot.Observed.Digest() != identity.Digest() || statusSnapshot.Applied.Version() != 1 {
		t.Fatalf("contract rejection status = %+v", statusSnapshot)
	}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	statusJSON, err := json.Marshal(statusSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, rendered := range []string{
		fmt.Sprint(report), fmt.Sprintf("%+v", report), fmt.Sprintf("%#v", report), string(reportJSON),
		fmt.Sprint(ack), fmt.Sprintf("%+v", statusSnapshot), string(statusJSON),
	} {
		if strings.Contains(rendered, secretCanary) {
			t.Fatalf("prefetch diagnostic leaked an unread secret: %q", rendered)
		}
	}
	if got := fixture.store.Current().Release().Version(); got != 1 {
		t.Fatalf("prefetch rejection displaced release 1 with %d", got)
	}
}

func TestFaultGeneratedDecodeRejectionReportsOnlyCanonicalPathsAndNoInputData(t *testing.T) {
	reports := make(chan configstore.CandidateRejectionReport, 4)
	fixture := startFaultFixture(t, matchingRelease(1, 101), faultStartOptions{
		onCandidateRejected: func(report configstore.CandidateRejectionReport) { reports <- report },
	})

	tests := []struct {
		name      string
		canary    string
		mutate    func(*releaseData, string)
		wantPaths []string
	}{
		{
			name:   "known field decode failure",
			canary: "INVALID-DURATION-INPUT-CANARY",
			mutate: func(candidate *releaseData, canary string) {
				candidate.databaseDocument = databaseDocument("db.internal", canary, 20)
			},
			wantPaths: []string{"database.timeout"},
		},
		{
			name:   "unknown input property",
			canary: "UNKNOWN-INPUT-NAME-CANARY",
			mutate: func(candidate *releaseData, canary string) {
				candidate.runtimeDocument = strings.TrimSuffix(candidate.runtimeDocument, "}") + fmt.Sprintf(",%q:%q}", canary, canary)
			},
			wantPaths: []string{"runtime"},
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			version := uint64(index + 2)
			candidate := matchingRelease(version, 101+version)
			test.mutate(&candidate, test.canary)
			activate(t, fixture, candidate)
			ack := waitAcknowledgement(t, fixture.sub, version, kmsclient.ReleaseStateRejected)
			if ack.GetRejectionCategory() != string(configstore.RejectConfigDecodeFailed) || ack.GetDiagnostic() != "" {
				t.Fatalf("decode rejection ack = category %q diagnostic %q", ack.GetRejectionCategory(), ack.GetDiagnostic())
			}

			var report configstore.CandidateRejectionReport
			select {
			case report = <-reports:
			case <-time.After(testOperationTimeout):
				t.Fatal("timed out waiting for local candidate rejection report")
			}
			if report.Category() != configstore.RejectConfigDecodeFailed || report.Release().Version() != version ||
				report.Release().ActivationRevision() != candidate.activationRevision || !reflect.DeepEqual(report.Paths(), test.wantPaths) {
				t.Fatalf("decode report = category:%s release:%s paths:%#v", report.Category(), report.Release(), report.Paths())
			}
			paths := report.Paths()
			paths[0] = "mutated"
			if !reflect.DeepEqual(report.Paths(), test.wantPaths) {
				t.Fatalf("decode report paths were mutable: %#v", report.Paths())
			}

			reportJSON, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			statusSnapshot := fixture.store.Status()
			statusJSON, err := json.Marshal(statusSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			statsJSON, err := json.Marshal(fixture.store.Stats())
			if err != nil {
				t.Fatal(err)
			}
			for _, rendered := range []string{
				fmt.Sprint(report), fmt.Sprintf("%+v", report), fmt.Sprintf("%#v", report), string(reportJSON),
				fmt.Sprint(ack), fmt.Sprintf("%+v", statusSnapshot), string(statusJSON), string(statsJSON),
			} {
				if strings.Contains(rendered, test.canary) {
					t.Fatalf("decode rejection exposed input data: %q", rendered)
				}
			}
			if got := fixture.store.Current().Release().Version(); got != 1 {
				t.Fatalf("decode rejection displaced release 1 with %d", got)
			}
		})
	}
}

func TestFaultPanickingRejectionCallbackCannotPublishMixedRestartCandidate(t *testing.T) {
	const (
		hotCanary    = "MIXED-HOT-MUST-NOT-PUBLISH-CANARY"
		secretCanary = "MIXED-SECRET-MUST-NOT-PUBLISH-CANARY"
	)
	reports := make(chan configstore.CandidateRejectionReport, 1)
	var rejectionCalls atomic.Int32
	var mismatchCalls atomic.Int32
	fixture := startFaultFixture(t, matchingRelease(1, 101), faultStartOptions{
		reconcileInterval: 25 * time.Millisecond,
		onDefaultMismatch: func(configstore.DefaultMismatchReport) { mismatchCalls.Add(1) },
		onCandidateRejected: func(report configstore.CandidateRejectionReport) {
			if rejectionCalls.Add(1) == 1 {
				reports <- report
			}
			panic(secretCanary)
		},
	})

	stopReaders := make(chan struct{})
	readerFailures := make(chan string, 1)
	var readers sync.WaitGroup
	var stopReadersOnce sync.Once
	stopReadersAndWait := func() {
		stopReadersOnce.Do(func() { close(stopReaders) })
		readers.Wait()
	}
	t.Cleanup(stopReadersAndWait)
	for range 8 {
		readers.Go(func() {
			for {
				select {
				case <-stopReaders:
					return
				default:
				}
				snapshot := fixture.store.Current()
				if snapshot.Release().Version() != 1 || snapshot.ApiHandler().Features()[0] != "search" ||
					snapshot.PersistenceHandler().Password().StringValue() != passwordPlaintext {
					select {
					case readerFailures <- fmt.Sprintf("mixed candidate became visible at release %d", snapshot.Release().Version()):
					default:
					}
					return
				}
			}
		})
	}

	mixed := matchingRelease(2, 102)
	mixed.runtimeDocument = runtimeDocument([]string{hotCanary}, []byte(hotCanary), map[string]uint64{"hot": 999}, [2]float64{9, 9})
	mixed.passwordVersion = 2
	mixed.passwordValue = []byte(secretCanary)
	activate(t, fixture, mixed)
	ack := waitAcknowledgement(t, fixture.sub, 2, kmsclient.ReleaseStateRejected)
	if ack.GetRejectionCategory() != string(configstore.RejectRestartRequired) || ack.GetDiagnostic() != "" {
		t.Fatalf("mixed rejection ack = category %q diagnostic %q", ack.GetRejectionCategory(), ack.GetDiagnostic())
	}
	waitRejectedCount(t, fixture.store, configstore.RejectRestartRequired, 3)
	stopReadersAndWait()
	select {
	case failure := <-readerFailures:
		t.Fatal(failure)
	default:
	}
	if mismatchCalls.Load() != 0 {
		t.Fatalf("restart-rejected candidate emitted %d default-mismatch callbacks", mismatchCalls.Load())
	}
	if rejectionCalls.Load() != 1 {
		t.Fatalf("panicking rejection callback calls = %d, want 1", rejectionCalls.Load())
	}
	report := waitFaultRejectionReport(t, reports)
	if report.Category() != configstore.RejectRestartRequired || report.Release().Version() != 2 ||
		!reflect.DeepEqual(report.Paths(), []string{"database_password"}) {
		t.Fatalf("mixed restart report = category:%s release:%s paths:%#v", report.Category(), report.Release(), report.Paths())
	}
	statusSnapshot := fixture.store.Status()
	statsSnapshot := fixture.store.Stats()
	reportJSON, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	statusJSON, err := json.Marshal(statusSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	statsJSON, err := json.Marshal(statsSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, rendered := range []string{
		fmt.Sprint(report), fmt.Sprintf("%+v", report), fmt.Sprintf("%#v", report), string(reportJSON),
		fmt.Sprint(ack), fmt.Sprintf("%+v", statusSnapshot), string(statusJSON), string(statsJSON),
	} {
		if strings.Contains(rendered, hotCanary) || strings.Contains(rendered, secretCanary) {
			t.Fatalf("mixed rejection leaked candidate material: %q", rendered)
		}
	}
	current := fixture.store.Current()
	if current.Release().Version() != 1 || current.ApiHandler().Features()[0] != "search" ||
		current.PersistenceHandler().Password().Version() != 1 || current.PersistenceHandler().Password().StringValue() != passwordPlaintext {
		t.Fatal("mixed restart rejection changed the active generation")
	}

	// A panic in application diagnostic code is isolated from both the rejected
	// candidate and the loader's ability to publish a later valid generation.
	recovered := matchingRelease(3, 103)
	activate(t, fixture, recovered)
	waitAcknowledgement(t, fixture.sub, 3, kmsclient.ReleaseStateApplied)
	waitAppliedVersion(t, fixture.store, 3)
}

func TestFaultSameRevisionSameDigestAndStaleEventsRespectAuthoritativeFence(t *testing.T) {
	fixture := startFaultFixture(t, matchingRelease(1, 101), faultStartOptions{})
	initial := matchingRelease(1, 101)
	initialRelease, err := fixture.server.SetActiveRelease(releaseSpec(initial), initial.activationRevision)
	if err != nil {
		t.Fatal(err)
	}
	var fetches atomic.Int64
	fixture.server.SetGetParameterHook(func(string) { fetches.Add(1) })
	baselineCandidates := fixture.store.Stats().Candidates

	// Exact duplicates from either stream shape and an older revision are all
	// ignored without resolution work.
	fixture.sub.PushActivation(initialRelease, 101)
	fixture.sub.PushSnapshot(initialRelease, 101)
	fixture.sub.PushActivation(initialRelease, 100)

	// Allocated release version is excluded from the deterministic release
	// digest. A same-revision event with that same digest but a different version
	// must still fail the final active identity fence.
	sameDigestSpec := releaseSpec(initial)
	sameDigestSpec.Version = 2
	sameDigestRelease, err := fixture.server.SetActiveRelease(sameDigestSpec, 101)
	if err != nil {
		t.Fatal(err)
	}
	if sameDigestRelease.GetDigest() != initialRelease.GetDigest() {
		t.Fatalf("test precondition: allocated version unexpectedly changed digest: %q != %q", sameDigestRelease.GetDigest(), initialRelease.GetDigest())
	}
	if _, err := fixture.server.SetActiveRelease(releaseSpec(initial), 101); err != nil {
		t.Fatal(err)
	}
	fixture.sub.PushActivation(sameDigestRelease, 101)
	ack := waitAcknowledgement(t, fixture.sub, 2, kmsclient.ReleaseStateRejected)
	if ack.GetRejectionCategory() != kmsclient.ReleaseRejectSuperseded || ack.GetDiagnostic() != "" {
		t.Fatalf("same-revision fence ack = category %q diagnostic %q", ack.GetRejectionCategory(), ack.GetDiagnostic())
	}
	if got := fixture.store.Current().Release().Version(); got != 1 {
		t.Fatalf("same-revision candidate displaced authoritative release with %d", got)
	}
	// The fenced event is a FIFO barrier for the duplicate and stale events
	// pushed before it. Exactly that one event may reach resolution, which reads
	// the two parameter entries once each.
	if got := fixture.store.Stats().Candidates; got != baselineCandidates+1 || fetches.Load() != 2 {
		t.Fatalf("duplicate/stale events were admitted: candidates=%d want=%d fetches=%d want=2", got, baselineCandidates+1, fetches.Load())
	}
	afterFenceCandidates := fixture.store.Stats().Candidates
	afterFenceFetches := fetches.Load()
	fixture.sub.PushActivation(sameDigestRelease, 101)
	fixture.sub.PushActivation(initialRelease, 100)

	next := matchingRelease(3, 102)
	activate(t, fixture, next)
	waitAcknowledgement(t, fixture.sub, 3, kmsclient.ReleaseStateApplied)
	waitAppliedVersion(t, fixture.store, 3)
	// The applied event is the second FIFO barrier. Only it may add a candidate
	// and perform the two parameter reads after the fenced baseline.
	if got := fixture.store.Stats().Candidates; got != afterFenceCandidates+1 || fetches.Load() != afterFenceFetches+2 {
		t.Fatalf("duplicate fenced/stale events retriggered work: candidates=%d want=%d fetches=%d want=%d", got, afterFenceCandidates+1, fetches.Load(), afterFenceFetches+2)
	}
}
