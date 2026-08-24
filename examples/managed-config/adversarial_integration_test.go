package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appconfig "github.com/Suhaibinator/kms/examples/managed-config/config"
	"github.com/Suhaibinator/kms/examples/managed-config/configkms"
	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient/kmsclienttest"
)

const (
	adversarialTestTimeout = 15 * time.Second
	adversarialInstanceID  = "managed-config-adversarial-1"
	adversarialReaderCount = 24
)

// TestManagedConfigAdversarialAtomicLifecycle crosses the in-process KMS,
// exact release pins, release watch and acknowledgements, generated decoder,
// managed admission policy, and atomic typed views. It intentionally drives a
// mixed restart/hot candidate so a partial publication cannot masquerade as a
// successful hot reload.
func TestManagedConfigAdversarialAtomicLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), adversarialTestTimeout)
	defer cancel()

	initial := initialReleaseValues()
	demo, err := newDemoKMS(initial)
	if err != nil {
		t.Fatal(err)
	}
	var closeDemoOnce sync.Once
	var closeDemoErr error
	closeDemo := func() {
		closeDemoOnce.Do(func() {
			closeDemoErr = demo.client.Close()
			demo.server.Close()
		})
	}
	t.Cleanup(closeDemo)

	mismatches := make(chan configstore.DefaultMismatchReport, 8)
	rejections := make(chan configstore.CandidateRejectionReport, 8)
	storeCtx, stopStore := context.WithCancel(ctx)
	store, err := configkms.Start(storeCtx, demo.client, configkms.Options{
		Release:  exampleRelease,
		Defaults: appconfig.Defaults,
		OnDefaultMismatch: func(report configstore.DefaultMismatchReport) {
			mismatches <- report
		},
		OnCandidateRejected: func(report configstore.CandidateRejectionReport) {
			rejections <- report
		},
		// Keep counter and callback assertions deterministic. A short interval
		// would deliberately retry the same rejected active release.
		ReconcileInterval: time.Hour,
		InstanceID:        adversarialInstanceID,
	})
	if err != nil {
		stopStore()
		t.Fatalf("start generated store: %v", err)
	}
	var shutdownStoreOnce sync.Once
	var shutdownStoreErr error
	shutdownStore := func() {
		shutdownStoreOnce.Do(func() {
			stopStore()
			shutdownStoreErr = waitForStoreShutdown(store, operationTimeout)
		})
	}
	t.Cleanup(shutdownStore)

	subscription, err := demo.server.WaitForReleaseSubscribe(timeoutFrom(ctx))
	if err != nil {
		t.Fatalf("wait for initial release subscription: %v", err)
	}
	demo.subscription = subscription
	assertRegistration(t, subscription, initial)
	initialAck := waitForFinalAcknowledgement(t, ctx, subscription, initial, kmsclient.ReleaseStateApplied)
	assertAcknowledgement(t, initialAck, initial, kmsclient.ReleaseStateApplied, "")

	oldSnapshot := store.Current()
	assertGeneration(t, oldSnapshot, initial)
	if status := store.Status(); !status.Ready || status.Applied.Version() != initial.releaseVersion || status.DefaultDivergent {
		t.Fatalf("initial status = %+v, want ready matching release 1", status)
	}
	assertNoMismatch(t, mismatches, "matching initial release")
	assertNoRejection(t, rejections, "matching initial release")

	// Secret getters must clone their byte buffer. Mutating one returned pin
	// must not mutate either the captured generation or a future getter.
	mutatedPin := oldSnapshot.RequestHandler().APIKey()
	mutatedValue := mutatedPin.Value()
	mutatedValue[0] ^= 0xff
	if got := oldSnapshot.RequestHandler().APIKey().StringValue(); got != initial.apiKeyPlaintext {
		t.Fatal("mutating a returned secret changed the captured generation")
	}

	hot := hotOverrideValues(initial)
	stopReaders := make(chan struct{})
	readerFailures := make(chan error, 1)
	var readerWG sync.WaitGroup
	oldSeen := make([]atomic.Bool, adversarialReaderCount)
	hotSeen := make([]atomic.Bool, adversarialReaderCount)
	for readerIndex := range adversarialReaderCount {
		readerWG.Go(func() {
			for {
				select {
				case <-stopReaders:
					return
				default:
				}

				snapshot := store.Current()
				version := snapshot.Release().Version()
				switch version {
				case initial.releaseVersion:
					oldSeen[readerIndex].Store(true)
					if err := generationError(snapshot, initial); err != nil {
						recordReaderFailure(readerFailures, err)
						return
					}
				case hot.releaseVersion:
					hotSeen[readerIndex].Store(true)
					if err := generationError(snapshot, hot); err != nil {
						recordReaderFailure(readerFailures, err)
						return
					}
				default:
					recordReaderFailure(readerFailures, fmt.Errorf("reader observed unexpected release %d", version))
					return
				}
			}
		})
	}
	var stopReadersOnce sync.Once
	stopReadersAndWait := func() {
		stopReadersOnce.Do(func() {
			close(stopReaders)
			if waitErr := waitForReaders(&readerWG, operationTimeout); waitErr != nil {
				t.Errorf("stop concurrent readers: %v", waitErr)
			}
		})
	}
	t.Cleanup(stopReadersAndWait)

	waitForConditionOrReaderFailure(t, ctx, readerFailures, "all readers to observe release 1", func() bool {
		return allReadersObserved(oldSeen)
	})
	hotSpec, err := scriptRelease(demo.server, hot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := demo.server.ActivateConfigurationRelease(hotSpec, hot.activationRevision); err != nil {
		t.Fatalf("activate hot release: %v", err)
	}
	hotAck := waitForFinalAcknowledgement(t, ctx, subscription, hot, kmsclient.ReleaseStateApplied)
	assertAcknowledgement(t, hotAck, hot, kmsclient.ReleaseStateApplied, "")
	waitForConditionOrReaderFailure(t, ctx, readerFailures, "all readers to observe release 2", func() bool {
		return allReadersObserved(hotSeen)
	})
	stopReadersAndWait()
	select {
	case readerErr := <-readerFailures:
		t.Fatal(readerErr)
	default:
	}

	// Previously captured snapshots and secret pins must retain the old exact
	// release even after the hot generation has been published.
	assertGeneration(t, oldSnapshot, initial)
	hotSnapshot := store.Current()
	assertGeneration(t, hotSnapshot, hot)
	hotReport := receiveMismatchForTest(t, ctx, mismatches)
	assertMismatchReport(t, hotReport, hot, []string{"runtime.greeting", "runtime.request_limit"})
	waitForCondition(t, ctx, "hot release status and counters", func() bool {
		status := store.Status()
		stats := store.Stats()
		return status.Observed.Version() == hot.releaseVersion &&
			status.Applied.Version() == hot.releaseVersion &&
			status.DefaultDivergent &&
			stats.Applied == 2 &&
			stats.DefaultDivergent
	})
	assertNoMismatch(t, mismatches, "one hot release")
	assertNoRejection(t, rejections, "accepted hot release")

	// Release 3 changes every hot value and a restart-bound value. Admission
	// must reject the complete candidate, including its newly pinned secret.
	restart := restartRequiredValues()
	restartSpec, err := scriptRelease(demo.server, restart)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := demo.server.ActivateConfigurationRelease(restartSpec, restart.activationRevision); err != nil {
		t.Fatalf("activate mixed restart/hot release: %v", err)
	}
	restartAck := waitForFinalAcknowledgement(t, ctx, subscription, restart, kmsclient.ReleaseStateRejected)
	assertAcknowledgement(t, restartAck, restart, kmsclient.ReleaseStateRejected, kmsclient.ReleaseRejectRestartRequired)
	rejection := receiveRejectionForTest(t, ctx, rejections)
	assertRejectionReport(t, rejection, restart)
	waitForCondition(t, ctx, "restart rejection status and counters", func() bool {
		status := store.Status()
		stats := store.Stats()
		return status.Observed.Version() == restart.releaseVersion &&
			status.Observed.ActivationRevision() == restart.activationRevision &&
			status.Applied.Version() == hot.releaseVersion &&
			status.Applied.ActivationRevision() == hot.activationRevision &&
			status.LastRejectionCategory == configstore.RejectRestartRequired &&
			stats.Applied == 2 &&
			stats.Rejected[configstore.RejectRestartRequired] == 1
	})
	assertGeneration(t, store.Current(), hot)
	assertNoMismatch(t, mismatches, "rejected mixed restart/hot release")
	assertNoRejection(t, rejections, "one rejected release")

	// A later release uses the restart pin from the last-known-good generation,
	// not the rejected active KMS release, and restores all source defaults.
	restored := restoredDefaultValues(initial)
	restoredSpec, err := scriptRelease(demo.server, restored)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := demo.server.ActivateConfigurationRelease(restoredSpec, restored.activationRevision); err != nil {
		t.Fatalf("activate restored release: %v", err)
	}
	restoredAck := waitForFinalAcknowledgement(t, ctx, subscription, restored, kmsclient.ReleaseStateApplied)
	assertAcknowledgement(t, restoredAck, restored, kmsclient.ReleaseStateApplied, "")
	waitForCondition(t, ctx, "restored status and counters", func() bool {
		status := store.Status()
		stats := store.Stats()
		return status.Observed.Version() == restored.releaseVersion &&
			status.Applied.Version() == restored.releaseVersion &&
			!status.DefaultDivergent &&
			stats.Applied == 3 &&
			stats.Rejected[configstore.RejectRestartRequired] == 1 &&
			!stats.DefaultDivergent
	})
	assertGeneration(t, store.Current(), restored)
	assertGeneration(t, oldSnapshot, initial)
	assertNoMismatch(t, mismatches, "default restoration")
	assertNoRejection(t, rejections, "default restoration")

	shutdownStore()
	if shutdownStoreErr != nil {
		t.Fatalf("store shutdown: %v", shutdownStoreErr)
	}
	waitForCondition(t, ctx, "release subscription cleanup", func() bool {
		return demo.server.ReleaseSubscribeCount() == 0
	})
	closeDemo()
	if closeDemoErr != nil {
		t.Fatalf("close KMS client: %v", closeDemoErr)
	}
}

func assertRegistration(t *testing.T, subscription *kmsclienttest.ReleaseSubscription, values releaseValues) {
	t.Helper()
	registration := subscription.Registration
	if registration.GetNamespace().GetEnv() != "dev" || registration.GetNamespace().GetApp() != "managed-config-example" ||
		registration.GetName() != exampleRelease || registration.GetClientName() != "managed-config-example" ||
		registration.GetInstanceId() != adversarialInstanceID || registration.GetLastSeenRevision() != values.activationRevision {
		t.Fatalf("unexpected release registration: %+v", registration)
	}
}

func waitForFinalAcknowledgement(
	t *testing.T,
	ctx context.Context,
	subscription *kmsclienttest.ReleaseSubscription,
	values releaseValues,
	state string,
) *kmsv1.ReleaseAcknowledgement {
	t.Helper()
	for {
		acknowledgement, err := subscription.WaitAcknowledgement(timeoutFrom(ctx))
		if err != nil {
			t.Fatalf("wait for release %d state %s: %v", values.releaseVersion, state, err)
		}
		if acknowledgement.GetVersion() == values.releaseVersion {
			if acknowledgement.GetState() == state {
				return acknowledgement
			}
			if acknowledgement.GetState() == kmsclient.ReleaseStateApplied || acknowledgement.GetState() == kmsclient.ReleaseStateRejected {
				t.Fatalf("release %d reached final state %s, want %s", values.releaseVersion, acknowledgement.GetState(), state)
			}
		}
	}
}

func assertAcknowledgement(
	t *testing.T,
	acknowledgement *kmsv1.ReleaseAcknowledgement,
	values releaseValues,
	state string,
	category string,
) {
	t.Helper()
	if acknowledgement.GetNamespace().GetEnv() != "dev" || acknowledgement.GetNamespace().GetApp() != "managed-config-example" ||
		acknowledgement.GetName() != exampleRelease || acknowledgement.GetVersion() != values.releaseVersion ||
		acknowledgement.GetActivationRevision() != values.activationRevision ||
		acknowledgement.GetClientName() != "managed-config-example" || acknowledgement.GetInstanceId() != adversarialInstanceID ||
		acknowledgement.GetState() != state || acknowledgement.GetRejectionCategory() != category ||
		acknowledgement.GetDiagnostic() != "" || acknowledgement.GetTimestampUnixMs() <= 0 {
		t.Fatalf("unexpected release acknowledgement: %+v", acknowledgement)
	}
}

func assertGeneration(t *testing.T, snapshot configkms.Snapshot, values releaseValues) {
	t.Helper()
	if err := generationError(snapshot, values); err != nil {
		t.Fatal(err)
	}
}

func generationError(snapshot configkms.Snapshot, values releaseValues) error {
	release := snapshot.Release()
	server := snapshot.HttpServer()
	handler := snapshot.RequestHandler()
	apiKey := handler.APIKey()
	if release.Version() != values.releaseVersion || release.ActivationRevision() != values.activationRevision {
		return fmt.Errorf("snapshot release = %d#%d, want %d#%d", release.Version(), release.ActivationRevision(), values.releaseVersion, values.activationRevision)
	}
	if server.ListenAddress() != values.listenAddress {
		return fmt.Errorf("release %d has the wrong restart-bound address", release.Version())
	}
	if handler.Greeting() != values.greeting || handler.RequestLimit() != values.requestLimit {
		return fmt.Errorf("release %d exposes a mixed hot parameter generation", release.Version())
	}
	if apiKey.Version() != values.apiKeyVersion || apiKey.StringValue() != values.apiKeyPlaintext ||
		apiKey.Path() != "/dev/managed-config-example/"+apiKeyPath || apiKey.ContentType() != "text/plain" {
		return fmt.Errorf("release %d exposes the wrong exact secret pin", release.Version())
	}
	return nil
}

func recordReaderFailure(failures chan<- error, err error) {
	select {
	case failures <- err:
	default:
	}
}

func allReadersObserved(observed []atomic.Bool) bool {
	for index := range observed {
		if !observed[index].Load() {
			return false
		}
	}
	return true
}

func waitForConditionOrReaderFailure(
	t *testing.T,
	ctx context.Context,
	failures <-chan error,
	description string,
	predicate func() bool,
) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if predicate() {
			return
		}
		select {
		case err := <-failures:
			t.Fatal(err)
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s: %v", description, ctx.Err())
		}
	}
}

func waitForCondition(t *testing.T, ctx context.Context, description string, predicate func() bool) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if predicate() {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s: %v", description, ctx.Err())
		}
	}
}

func waitForReaders(readers *sync.WaitGroup, timeout time.Duration) error {
	done := make(chan struct{})
	go func() {
		readers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return errors.New("timed out waiting for concurrent readers")
	}
}

func waitForStoreShutdown(store *configkms.Store, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- store.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return errors.New("timed out waiting for managed store shutdown")
	}
}

func receiveMismatchForTest(
	t *testing.T,
	ctx context.Context,
	reports <-chan configstore.DefaultMismatchReport,
) configstore.DefaultMismatchReport {
	t.Helper()
	select {
	case report := <-reports:
		return report
	case <-ctx.Done():
		t.Fatalf("wait for default mismatch report: %v", ctx.Err())
		return nil
	}
}

func assertMismatchReport(
	t *testing.T,
	report configstore.DefaultMismatchReport,
	values releaseValues,
	wantPaths []string,
) {
	t.Helper()
	if report.Phase() != configstore.MismatchRuntime || report.Severity() != configstore.MismatchError ||
		report.Release().Version() != values.releaseVersion || report.Release().ActivationRevision() != values.activationRevision {
		t.Fatalf("unexpected default mismatch report: %s", report)
	}
	if paths := differencePaths(report); !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("default mismatch paths = %v, want %v", paths, wantPaths)
	}
	fields := report.Fields()
	for index := range fields {
		fields[index].Path = "mutated"
	}
	if paths := differencePaths(report); !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("default mismatch report exposed mutable paths: %v", paths)
	}
}

func receiveRejectionForTest(
	t *testing.T,
	ctx context.Context,
	reports <-chan configstore.CandidateRejectionReport,
) configstore.CandidateRejectionReport {
	t.Helper()
	select {
	case report := <-reports:
		return report
	case <-ctx.Done():
		t.Fatalf("wait for candidate rejection report: %v", ctx.Err())
		return nil
	}
}

func assertRejectionReport(t *testing.T, report configstore.CandidateRejectionReport, values releaseValues) {
	t.Helper()
	wantPaths := []string{"server.listen_address"}
	if report.Category() != configstore.RejectRestartRequired || report.Release().Version() != values.releaseVersion ||
		report.Release().ActivationRevision() != values.activationRevision || !reflect.DeepEqual(report.Paths(), wantPaths) {
		t.Fatalf("unexpected candidate rejection report: category=%s release=%s paths=%v", report.Category(), report.Release(), report.Paths())
	}
	paths := report.Paths()
	paths[0] = "mutated"
	if !reflect.DeepEqual(report.Paths(), wantPaths) {
		t.Fatalf("candidate rejection report exposed mutable paths: %v", report.Paths())
	}
}

func assertNoMismatch(t *testing.T, reports <-chan configstore.DefaultMismatchReport, phase string) {
	t.Helper()
	select {
	case report := <-reports:
		t.Fatalf("unexpected default mismatch during %s: %s", phase, report)
	default:
	}
}

func assertNoRejection(t *testing.T, reports <-chan configstore.CandidateRejectionReport, phase string) {
	t.Helper()
	select {
	case report := <-reports:
		t.Fatalf("unexpected candidate rejection during %s: category=%s release=%s paths=%v", phase, report.Category(), report.Release(), report.Paths())
	default:
	}
}
