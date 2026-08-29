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

	// Channel-backed callbacks keep the transcript deterministic. A real
	// service normally passes configstore.SlogCallbacks(sink, ...) instead and
	// installs its logger on the sink once configuration has built it.
	mismatches := make(chan configstore.DefaultMismatchReport, 4)
	rejections := make(chan configstore.CandidateRejectionReport, 4)
	applied := make(chan configstore.AppliedReport, 8)
	storeCtx, stopStore := context.WithCancel(ctx)
	store, err := configkms.Start(storeCtx, demo.client, configkms.Options{
		Release:  exampleRelease,
		Defaults: appconfig.Defaults,
		Callbacks: configstore.Callbacks{
			OnDefaultMismatch: func(report configstore.DefaultMismatchReport) {
				mismatches <- report
			},
			OnApplied: func(report configstore.AppliedReport) {
				applied <- report
			},
			OnCandidateRejected: func(report configstore.CandidateRejectionReport) {
				rejections <- report
			},
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
	if err := printApplied(ctx, output, applied); err != nil {
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
	if err := printApplied(ctx, output, applied); err != nil {
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
	if err := printApplied(ctx, output, applied); err != nil {
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

// printApplied consumes the OnApplied report for the generation that was just
// published. The report names canonical paths and, for non-secret fields,
// previous and current values; the transcript prints paths only. Secret
// rotations appear path-only (nil previous and current) by construction.
func printApplied(ctx context.Context, output io.Writer, reports <-chan configstore.AppliedReport) error {
	var report configstore.AppliedReport
	select {
	case report = <-reports:
	case <-ctx.Done():
		return fmt.Errorf("wait for applied report: %w", ctx.Err())
	}
	changed := "(none)"
	if paths := changePaths(report); len(paths) != 0 {
		changed = strings.Join(paths, ",")
	}
	_, err := fmt.Fprintf(output, "applied: phase=%s release=%s divergent=%t changed=%s\n",
		report.Phase(), report.Release(), report.DefaultDivergent(), changed)
	return err
}

func changePaths(report configstore.AppliedReport) []string {
	changes := report.Changed()
	paths := make([]string, len(changes))
	for index := range changes {
		paths[index] = changes[index].Path
	}
	return sortedStrings(paths)
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
