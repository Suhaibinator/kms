// Command managed-config demonstrates generated, typed, atomically reloaded
// application configuration with a hermetic in-process KMS fake.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	appconfig "github.com/Suhaibinator/kms/examples/managed-config/config"
	"github.com/Suhaibinator/kms/examples/managed-config/configkms"
	"github.com/Suhaibinator/kms/sdk/go/configstore"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "managed-config example: %v\n", err)
		os.Exit(1)
	}
}

func run(parent context.Context, output io.Writer) error {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()

	initial := initialReleaseValues()
	demo, err := newDemoKMS(initial)
	if err != nil {
		return err
	}
	defer demo.close()

	mismatches := make(chan configstore.DefaultMismatchReport, 4)
	rejections := make(chan configstore.CandidateRejectionReport, 4)
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
		ReconcileInterval: time.Hour,
		InstanceID:        "managed-config-example-1",
	})
	if err != nil {
		stopStore()
		return fmt.Errorf("start generated store: %w", err)
	}
	defer func() {
		stopStore()
		_ = store.Wait()
	}()

	if err := demo.waitForInitial(ctx, initial.releaseVersion); err != nil {
		return err
	}

	oldSnapshot := store.Current()
	oldServer := oldSnapshot.HttpServer()
	oldHandler := oldSnapshot.RequestHandler()
	oldAPIKey := oldHandler.APIKey()
	if _, err := fmt.Fprintf(output,
		"initial typed snapshot: release=%d address=%q greeting=%q request_limit=%d api_key=%s api_key_version=%d\n",
		oldSnapshot.Release().Version(), oldServer.ListenAddress(), oldHandler.Greeting(), oldHandler.RequestLimit(), oldAPIKey, oldAPIKey.Version()); err != nil {
		return err
	}

	hot := hotOverrideValues(initial)
	if _, err := demo.activate(ctx, hot, kmsclient.ReleaseStateApplied); err != nil {
		return err
	}
	mismatch, err := receiveMismatch(ctx, mismatches)
	if err != nil {
		return err
	}
	hotSnapshot := store.Current()
	hotHandler := hotSnapshot.RequestHandler()
	hotAPIKey := hotHandler.APIKey()
	if _, err := fmt.Fprintf(output,
		"atomic hot override: release=%d greeting=%q request_limit=%d api_key=%s api_key_version=%d divergent=%t\n",
		hotSnapshot.Release().Version(), hotHandler.Greeting(), hotHandler.RequestLimit(), hotAPIKey, hotAPIKey.Version(), store.Status().DefaultDivergent); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output,
		"default divergence: phase=%s severity=%s fields=%s\n",
		mismatch.Phase(), mismatch.Severity(), strings.Join(differencePaths(mismatch), ",")); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output,
		"old snapshot is immutable: release=%d greeting=%q request_limit=%d api_key_version=%d\n",
		oldSnapshot.Release().Version(), oldHandler.Greeting(), oldHandler.RequestLimit(), oldAPIKey.Version()); err != nil {
		return err
	}

	// This candidate mixes a restart-bound address change with hot values. The
	// whole generation must be rejected; the hot subset must not leak through.
	restart := restartRequiredValues()
	rejectionCategory, err := demo.activate(ctx, restart, kmsclient.ReleaseStateRejected)
	if err != nil {
		return err
	}
	if rejectionCategory != kmsclient.ReleaseRejectRestartRequired {
		return fmt.Errorf("release %d rejection category = %q, want %q",
			restart.releaseVersion, rejectionCategory, kmsclient.ReleaseRejectRestartRequired)
	}
	rejection, err := receiveRejection(ctx, rejections)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output,
		"restart-required candidate: release=%d category=%s fields=%s\n",
		rejection.Release().Version(), rejection.Category(), strings.Join(sortedStrings(rejection.Paths()), ",")); err != nil {
		return err
	}

	lastKnownGood := store.Current()
	lastKnownGoodServer := lastKnownGood.HttpServer()
	lastKnownGoodHandler := lastKnownGood.RequestHandler()
	lastKnownGoodAPIKey := lastKnownGoodHandler.APIKey()
	if _, err := fmt.Fprintf(output,
		"last-known-good after whole-candidate rejection: release=%d address=%q greeting=%q request_limit=%d api_key_version=%d\n",
		lastKnownGood.Release().Version(), lastKnownGoodServer.ListenAddress(), lastKnownGoodHandler.Greeting(), lastKnownGoodHandler.RequestLimit(), lastKnownGoodAPIKey.Version()); err != nil {
		return err
	}

	restored := restoredDefaultValues(initial)
	if _, err := demo.activate(ctx, restored, kmsclient.ReleaseStateApplied); err != nil {
		return err
	}
	restoredSnapshot := store.Current()
	restoredHandler := restoredSnapshot.RequestHandler()
	restoredAPIKey := restoredHandler.APIKey()
	if _, err := fmt.Fprintf(output,
		"restored application defaults: release=%d greeting=%q request_limit=%d api_key=%s api_key_version=%d divergent=%t\n",
		restoredSnapshot.Release().Version(), restoredHandler.Greeting(), restoredHandler.RequestLimit(), restoredAPIKey, restoredAPIKey.Version(), store.Status().DefaultDivergent); err != nil {
		return err
	}

	return nil
}

func receiveMismatch(
	ctx context.Context,
	reports <-chan configstore.DefaultMismatchReport,
) (configstore.DefaultMismatchReport, error) {
	select {
	case report := <-reports:
		return report, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for default mismatch report: %w", ctx.Err())
	}
}

func receiveRejection(
	ctx context.Context,
	reports <-chan configstore.CandidateRejectionReport,
) (configstore.CandidateRejectionReport, error) {
	select {
	case report := <-reports:
		return report, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for candidate rejection report: %w", ctx.Err())
	}
}

func differencePaths(report configstore.DefaultMismatchReport) []string {
	fields := report.Fields()
	paths := make([]string, len(fields))
	for index := range fields {
		paths[index] = fields[index].Path
	}
	return sortedStrings(paths)
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
